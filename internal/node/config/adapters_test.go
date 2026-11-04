package config_test

import (
	"bytes"
	"errors"
	"io/fs"
	"math"
	"path"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/txn"
	"github.com/aianyai/sidera/sdk/go/manifest"
)

// srcPackage is a package directory as the owner hands it over: a file system
// of its own whose root is the package.
func srcPackage(t testing.TB, id, version string, extra map[string]string) *fsxtest.Mem {
	t.Helper()
	mem := fsxtest.NewMem()
	files := map[string]string{
		"sidera-adapter.json":        manifestOf(id, version),
		"bin/sidera-adapter-example": "#!program " + version + "\n",
		"lib/a/b.txt":                "deep\n",
		"README":                     "read me\n",
	}
	for p, c := range extra {
		files[p] = c
	}
	for p, c := range files {
		mkdirAll(t, mem, path.Dir(p))
		if err := fsx.Replace(mem, setupTag, p, []byte(c), fsx.ModePrivate); err != nil {
			t.Fatal(err)
		}
	}
	return mem
}

func suffixReader() *bytes.Reader {
	return bytes.NewReader([]byte{0xab, 0xcd, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06})
}

const stageSuffix = "abcd010203040506"

// otherSuffixReader draws a staging suffix that suffixReader does not.
func otherSuffixReader() *bytes.Reader {
	return bytes.NewReader([]byte{0xef, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07})
}

func ensureState(t testing.TB, mem *fsxtest.Mem) { mkdirAll(t, mem, "state") }

// install runs the installation of a staged package as the engine composes it.
func install(t testing.TB, rg *rig, c *config.Config, staged config.Staged) error {
	t.Helper()
	return installAsking(t, rg, c, staged, nil)
}

// installAsking is install with the answer to whether the process of a disabled
// instance is still ending.
func installAsking(t testing.TB, rg *rig, c *config.Config, staged config.Staged, ending config.StillEnding) error {
	t.Helper()
	ensureState(t, rg.mem)
	plan, err := c.PlanInstall(staged, ending)
	if err != nil || plan.AlreadyCurrent {
		return err
	}
	tx := rg.w.Begin(testAdapterInstall)
	defer tx.Close()
	if err := plan.Promote(tx); err != nil {
		return err
	}
	done, err := plan.Point(tx)
	if err != nil {
		return err
	}
	if err := tx.Replace("result", "state/install-result", []byte("installed"), fsx.ModePrivate); err != nil {
		return err
	}
	if err := plan.DiscardOld(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	done()
	return nil
}

func TestAdapterLayoutPaths(t *testing.T) {
	for got, want := range map[string]string{
		config.AdapterDir("example"):                      "config/adapters/example",
		config.AdapterCurrentPath("example"):              "config/adapters/example/current",
		config.AdapterVersionsDir("example"):              "config/adapters/example/versions",
		config.AdapterVersionDir("example", "1.2.3-rc.1"): "config/adapters/example/versions/1.2.3-rc.1",
		config.AdapterManifestPath("example", "1.2.3"):    "config/adapters/example/versions/1.2.3/sidera-adapter.json",
		config.AdapterStageDir("example", "ab12"):         "config/adapters/example/versions/.stage-ab12",
		config.EndpointIDsPath("example"):                 "config/endpoint-ids/example.json",
	} {
		if got != want {
			t.Errorf("path %q, want %q", got, want)
		}
	}
	_, digest, _ := selfRevocation(t, 7)
	if got, want := config.RevocationPath(digest), "config/revocations/"+digest.String()+".rec"; got != want {
		t.Errorf("revocation path %q, want %q", got, want)
	}
}

// A package is copied whole, with its subdirectories, next to the versions. It
// is parsed there; nothing is installed.
func TestStageAdapterCopiesThePackageNextToTheVersions(t *testing.T) {
	rg, c := loaded(t)
	src := srcPackage(t, "example", "2.0.0", nil)
	staged, err := c.StageAdapter(src, suffixReader())
	if err != nil {
		t.Fatalf("StageAdapter: %v", err)
	}
	wantDir := "config/adapters/example/versions/.stage-" + stageSuffix
	if staged.ID != "example" || staged.Version != "2.0.0" || staged.Dir != wantDir || staged.Manifest.Version != "2.0.0" {
		t.Errorf("staged = %+v, want example 2.0.0 in %s", staged, wantDir)
	}
	for p, want := range snapshot(t, src) {
		if strings.HasSuffix(p, "/") {
			if _, err := rg.mem.Stat(path.Join(wantDir, strings.TrimSuffix(p, "/"))); err != nil {
				t.Errorf("directory %s was not copied: %v", p, err)
			}
			continue
		}
		got, err := rg.mem.ReadFile(path.Join(wantDir, p))
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, %v, want %q", p, got, err, want)
		}
	}
	if ads := c.Adapters(); len(ads) != 1 || ads[0].Version != "1.0.0" {
		t.Errorf("adapters = %+v: staging installs nothing", ads)
	}
	if data, _ := rg.mem.ReadFile(config.AdapterCurrentPath("example")); string(data) != goldenCurrent {
		t.Error("the current pointer moved")
	}
}

// A first installation creates the adapter's directories, none of which is an
// installation until the pointer exists.
func TestStageOfANewAdapterCreatesItsDirectories(t *testing.T) {
	rg, c := loaded(t)
	src := srcPackage(t, "fresh", "0.1.0", nil)
	staged, err := c.StageAdapter(src, suffixReader())
	if err != nil {
		t.Fatalf("StageAdapter: %v", err)
	}
	if !exists(rg.mem, config.AdapterStageDir("fresh", stageSuffix)) {
		t.Error("the staging directory is missing")
	}
	work, err := config.AdapterGCWork(rg.mem)
	if err != nil {
		t.Fatal(err)
	}
	want := []config.GCItem{{Path: config.AdapterDir("fresh"), Reason: config.GCNotInstalled}}
	if !reflect.DeepEqual(work, want) {
		t.Errorf("gc work = %+v, want %+v: an adapter directory without a pointer is an installation that did not finish", work, want)
	}
	if err := c.Unstage(staged); err != nil {
		t.Fatal(err)
	}
}

// A package that cannot be staged leaves nothing behind: no staging directory
// and no adapter directory without a pointer.
func TestRefusedStageLeavesNothing(t *testing.T) {
	// A source whose manifest changes between the first read and the copy.
	swapped := srcPackage(t, "fresh", "0.1.0", nil)
	rows := []struct {
		name string
		src  func() fsx.Reader
	}{
		{"a reserved temporary name", func() fsx.Reader {
			return srcPackage(t, "fresh", "0.1.0", map[string]string{"lib/.tmp-x": "x"})
		}},
		{"a reserved trash name", func() fsx.Reader {
			return srcPackage(t, "fresh", "0.1.0", map[string]string{".trash-1/x": "x"})
		}},
		{"a reserved staging name", func() fsx.Reader {
			return srcPackage(t, "fresh", "0.1.0", map[string]string{"lib/.stage-q": "x"})
		}},
		{"no manifest", func() fsx.Reader {
			m := srcPackage(t, "fresh", "0.1.0", nil)
			removeFile(t, m, "sidera-adapter.json")
			return m
		}},
		{"a manifest that is not valid", func() fsx.Reader {
			return srcPackage(t, "fresh", "0.1.0", map[string]string{"sidera-adapter.json": `{"manifest_version": 1}`})
		}},
		{"a manifest that changes while the package is copied", func() fsx.Reader {
			return &flipping{Reader: swapped, path: "sidera-adapter.json", second: manifestOf("other", "0.1.0")}
		}},
		{"a manifest that changes while the package is copied and keeps its id and version", func() fsx.Reader {
			// valid, the same package by id and version, and not the same bytes: it
			// could as well have named another entry program or platform
			return &flipping{Reader: swapped, path: "sidera-adapter.json", second: manifestOf("fresh", "0.1.0") + "\n"}
		}},
		{"a package larger than the limit by what its files say", func() fsx.Reader {
			return &sizes{Reader: srcPackage(t, "fresh", "0.1.0", nil), size: map[string]int64{"README": config.MaxPackageBytes + 1}}
		}},
		{"a package larger than the limit by what its files hold", func() fsx.Reader {
			// a file that says it is small and holds more, as a link to a large file does
			return &holds{Reader: srcPackage(t, "fresh", "0.1.0", nil), path: "README", data: bigFile()}
		}},
		{"a program that is not in the package", func() fsx.Reader {
			m := srcPackage(t, "fresh", "0.1.0", nil)
			removeFile(t, m, "bin/sidera-adapter-example")
			return m
		}},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			rg, c := loaded(t)
			before := snapshot(t, rg.mem)
			if _, err := c.StageAdapter(r.src(), suffixReader()); err == nil {
				t.Fatal("StageAdapter accepted the package")
			}
			after := snapshot(t, rg.mem)
			if !reflect.DeepEqual(before, after) {
				t.Errorf("a refused stage left %v", diffKeys(before, after))
			}
			if work, err := config.AdapterGCWork(rg.mem); err != nil || len(work) != 0 {
				t.Errorf("gc work after a refused stage = %+v, %v", work, err)
			}
		})
	}
}

// sizes is a package whose files say they are as large as the map says.
type sizes struct {
	fsx.Reader
	size map[string]int64
}

func (s *sizes) Stat(p string) (fsx.Info, error) {
	info, err := s.Reader.Stat(p)
	if n, ok := s.size[p]; ok {
		info.Size = n
	}
	return info, err
}

// holds is a package one of whose files holds more than it says: Stat shows the
// size of the original, and ReadFile gives data.
type holds struct {
	fsx.Reader
	path string
	data []byte
}

func (h *holds) ReadFile(p string) ([]byte, error) {
	return h.ReadFileLimit(p, math.MaxInt64)
}

func (h *holds) ReadFileLimit(p string, maxBytes int64) ([]byte, error) {
	if p == h.path {
		if int64(len(h.data)) > maxBytes {
			return nil, fsx.ErrTooLarge
		}
		return h.data, nil
	}
	return h.Reader.ReadFileLimit(p, maxBytes)
}

// bigFile is one byte more than a package may hold. It is allocated once and never
// written, so the operating system does not back it with memory.
var bigFile = sync.OnceValue(func() []byte { return make([]byte, config.MaxPackageBytes+1) })

// A package that holds more than the limit is refused as an invalid package, and
// the error names the file that went over and the limit, so that the owner who
// pointed at the wrong directory sees why.
func TestAPackageOverTheSizeLimitIsRefusedAndTheErrorNamesTheLimit(t *testing.T) {
	for name, src := range map[string]func() fsx.Reader{
		"by what its files say": func() fsx.Reader {
			return &sizes{Reader: srcPackage(t, "fresh", "0.1.0", nil), size: map[string]int64{"README": config.MaxPackageBytes + 1}}
		},
		"by what its files hold": func() fsx.Reader {
			return &holds{Reader: srcPackage(t, "fresh", "0.1.0", nil), path: "README", data: bigFile()}
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, c := loaded(t)
			_, err := c.StageAdapter(src(), suffixReader())
			var ce *config.ChangeError
			if !errors.As(err, &ce) || ce.Reason != config.ChangeInvalid || ce.Field != "README" {
				t.Fatalf("StageAdapter = %v, want an invalid change that names the file README", err)
			}
			if want := "256 MiB"; !strings.Contains(err.Error(), want) {
				t.Errorf("the error %q does not state the limit (%s)", err, want)
			}
		})
	}
}

// unreadable is a package some of whose files and directories fail to be read.
type unreadable struct {
	fsx.Reader
	files, dirs []string
}

func (u *unreadable) ReadFile(p string) ([]byte, error) {
	return u.ReadFileLimit(p, math.MaxInt64)
}

func (u *unreadable) ReadFileLimit(p string, maxBytes int64) ([]byte, error) {
	if slices.Contains(u.files, p) {
		return nil, &fs.PathError{Op: "read", Path: p, Err: fs.ErrPermission}
	}
	return u.Reader.ReadFileLimit(p, maxBytes)
}

func (u *unreadable) ReadDir(d string) ([]fsx.Entry, error) {
	if slices.Contains(u.dirs, d) {
		return nil, &fs.PathError{Op: "open", Path: d, Err: fs.ErrPermission}
	}
	return u.Reader.ReadDir(d)
}

// A file or a directory of the package that cannot be read is a SourceError that
// names it, and the refusal leaves nothing behind: the owner can fix the package,
// and it is no failure of this node's own storage.
func TestAPackageThatCannotBeReadIsASourceErrorThatNamesWhatFailed(t *testing.T) {
	rows := []struct {
		name string
		src  func() fsx.Reader
		path string
	}{
		{"a file", func() fsx.Reader {
			return &unreadable{Reader: srcPackage(t, "fresh", "0.1.0", nil), files: []string{"lib/a/b.txt"}}
		}, "lib/a/b.txt"},
		{"the program", func() fsx.Reader {
			return &unreadable{Reader: srcPackage(t, "fresh", "0.1.0", nil), files: []string{"bin/sidera-adapter-example"}}
		}, "bin/sidera-adapter-example"},
		{"a directory", func() fsx.Reader {
			return &unreadable{Reader: srcPackage(t, "fresh", "0.1.0", nil), dirs: []string{"lib/a"}}
		}, "lib/a"},
		{"the package itself", func() fsx.Reader {
			return &unreadable{Reader: srcPackage(t, "fresh", "0.1.0", nil), dirs: []string{"."}}
		}, "."},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			rg, c := loaded(t)
			before := snapshot(t, rg.mem)
			_, err := c.StageAdapter(r.src(), suffixReader())
			var se *config.SourceError
			if !errors.As(err, &se) || se.Path != r.path || !errors.Is(err, fs.ErrPermission) {
				t.Fatalf("StageAdapter = %v, want a SourceError for %q caused by the permission", err, r.path)
			}
			if after := snapshot(t, rg.mem); !reflect.DeepEqual(before, after) {
				t.Errorf("a package that cannot be read left %v", diffKeys(before, after))
			}
		})
	}
}

// flipping serves a different file the second time the manifest is read: the
// package changing under the copy.
type flipping struct {
	fsx.Reader
	path   string
	second string
	mu     sync.Mutex
	reads  int
}

func (f *flipping) ReadFile(p string) ([]byte, error) {
	return f.ReadFileLimit(p, math.MaxInt64)
}

func (f *flipping) ReadFileLimit(p string, maxBytes int64) ([]byte, error) {
	if p == f.path {
		f.mu.Lock()
		f.reads++
		n := f.reads
		f.mu.Unlock()
		if n > 1 {
			if int64(len(f.second)) > maxBytes {
				return nil, fsx.ErrTooLarge
			}
			return []byte(f.second), nil
		}
	}
	return f.Reader.ReadFileLimit(p, maxBytes)
}

func diffKeys(a, b map[string]string) []string {
	var out []string
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			out = append(out, "-"+k)
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			out = append(out, "+"+k)
		}
	}
	return out
}

// Unstage drops what an installation that stopped halfway left, and nothing the
// installed adapter needs.
func TestUnstage(t *testing.T) {
	t.Run("the staging directory of an upgrade", func(t *testing.T) {
		rg, c := loaded(t)
		staged, err := c.StageAdapter(srcPackage(t, "example", "2.0.0", nil), suffixReader())
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Unstage(staged); err != nil {
			t.Fatalf("Unstage: %v", err)
		}
		if exists(rg.mem, staged.Dir) {
			t.Error("the staging directory is still there")
		}
		if !exists(rg.mem, config.AdapterManifestPath("example", "1.0.0")) || !exists(rg.mem, config.AdapterCurrentPath("example")) {
			t.Error("the installed version was touched")
		}
	})
	t.Run("a version directory that was renamed in and did not become current is not its own", func(t *testing.T) {
		rg, c := loaded(t)
		if _, err := c.DisableAdapter(request(1, 4), "example", nil, result(new([]config.Instances), `{}`)); err != nil {
			t.Fatal(err)
		}
		staged, err := c.StageAdapter(srcPackage(t, "example", "2.0.0", nil), suffixReader())
		if err != nil {
			t.Fatal(err)
		}
		plan, err := c.PlanInstall(staged, nil)
		if err != nil {
			t.Fatal(err)
		}
		tx := rg.w.Begin(testAdapterInstall)
		if err := plan.Promote(tx); err != nil {
			t.Fatal(err)
		}
		tx.Close() // the commit failed or was refused after the rename
		if err := c.Unstage(staged); err != nil {
			t.Fatalf("Unstage: %v", err)
		}
		// An installation that committed meanwhile has such a directory too, for a
		// moment before its pointer names it, and Unstage cannot tell the two apart.
		if !exists(rg.mem, config.AdapterVersionDir("example", "2.0.0")) {
			t.Error("Unstage removed a version directory that is not its staging directory")
		}
		if !exists(rg.mem, config.AdapterVersionDir("example", "1.0.0")) {
			t.Error("the installed version was removed")
		}
		// The repair removes it at the next start.
		if work, err := config.AdapterGCWork(rg.mem); err != nil || len(work) != 1 || work[0].Reason != config.GCSuperseded {
			t.Errorf("the garbage the repair removes = %+v, %v; want the version directory", work, err)
		}
	})
	t.Run("a first installation that failed leaves no adapter directory", func(t *testing.T) {
		rg, c := loaded(t)
		staged, err := c.StageAdapter(srcPackage(t, "fresh", "0.1.0", nil), suffixReader())
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Unstage(staged); err != nil {
			t.Fatalf("Unstage: %v", err)
		}
		if exists(rg.mem, config.AdapterDir("fresh")) {
			t.Error("the adapter directory of an adapter that was never installed is still there")
		}
	})
	t.Run("a first installation that is not alone in the adapter directory leaves it", func(t *testing.T) {
		rg, c := loaded(t)
		first, err := c.StageAdapter(srcPackage(t, "fresh", "0.1.0", nil), suffixReader())
		if err != nil {
			t.Fatal(err)
		}
		second, err := c.StageAdapter(srcPackage(t, "fresh", "0.1.0", nil), otherSuffixReader())
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Unstage(first); err != nil {
			t.Fatalf("Unstage: %v", err)
		}
		if exists(rg.mem, first.Dir) || !exists(rg.mem, second.Dir) {
			t.Error("Unstage must drop its own staging directory and only that")
		}
	})
	t.Run("the current version restaged is kept", func(t *testing.T) {
		rg, c := loaded(t)
		staged, err := c.StageAdapter(srcPackage(t, "example", "1.0.0", nil), suffixReader())
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Unstage(staged); err != nil {
			t.Fatal(err)
		}
		if exists(rg.mem, staged.Dir) || !exists(rg.mem, config.AdapterManifestPath("example", "1.0.0")) {
			t.Error("unstaging a copy of the current version must drop the copy and keep the version")
		}
	})
	t.Run("nothing to drop is not an error", func(t *testing.T) {
		_, c := loaded(t)
		staged, err := c.StageAdapter(srcPackage(t, "example", "2.0.0", nil), suffixReader())
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Unstage(staged); err != nil {
			t.Fatal(err)
		}
		if err := c.Unstage(staged); err != nil {
			t.Errorf("a second Unstage: %v", err)
		}
	})
}

func TestInstallBecomesCurrent(t *testing.T) {
	t.Run("an upgrade", func(t *testing.T) {
		rg, c := loaded(t)
		// The instance is disabled first: no process holds the files of the version
		// that is replaced, so the installation discards it.
		if _, err := c.DisableAdapter(request(1, 4), "example", nil, result(new([]config.Instances), `{}`)); err != nil {
			t.Fatal(err)
		}
		staged, err := c.StageAdapter(srcPackage(t, "example", "2.0.0", nil), suffixReader())
		if err != nil {
			t.Fatal(err)
		}
		if err := install(t, rg, c, staged); err != nil {
			t.Fatalf("install: %v", err)
		}
		if ads := c.Adapters(); len(ads) != 1 || ads[0].Version != "2.0.0" {
			t.Errorf("adapters = %+v, want example 2.0.0", ads)
		}
		if data, _ := rg.mem.ReadFile(config.AdapterCurrentPath("example")); !strings.Contains(string(data), `"2.0.0"`) {
			t.Errorf("current = %q", data)
		}
		if exists(rg.mem, config.AdapterVersionDir("example", "1.0.0")) {
			t.Error("the previous version was not discarded")
		}
		if exists(rg.mem, staged.Dir) {
			t.Error("the staging directory is still there")
		}
		if again := rg.load().Adapters(); len(again) != 1 || again[0].Version != "2.0.0" {
			t.Errorf("a restart reads %+v", again)
		}
		if work, _ := config.AdapterGCWork(rg.mem); len(work) != 0 {
			t.Errorf("gc work after an installation = %+v", work)
		}
	})
	t.Run("a first installation", func(t *testing.T) {
		rg, c := loaded(t)
		staged, err := c.StageAdapter(srcPackage(t, "fresh", "0.1.0", nil), suffixReader())
		if err != nil {
			t.Fatal(err)
		}
		if err := install(t, rg, c, staged); err != nil {
			t.Fatalf("install: %v", err)
		}
		if len(c.Adapters()) != 2 {
			t.Errorf("adapters = %+v", c.Adapters())
		}
		if work, _ := config.AdapterGCWork(rg.mem); len(work) != 0 {
			t.Errorf("gc work after a first installation = %+v", work)
		}
	})
	t.Run("a leftover directory of the version is left for the repair and the package goes to a directory of its own", func(t *testing.T) {
		rg, c := loaded(t)
		if _, err := c.DisableAdapter(request(1, 4), "example", nil, result(new([]config.Instances), `{}`)); err != nil {
			t.Fatal(err)
		}
		leftover := path.Join(config.AdapterVersionDir("example", "2.0.0"), "old-leftover")
		mkdirAll(t, rg.mem, config.AdapterVersionDir("example", "2.0.0"))
		if err := fsx.Replace(rg.mem, setupTag, leftover, []byte("x"), fsx.ModePrivate); err != nil {
			t.Fatal(err)
		}
		staged, err := c.StageAdapter(srcPackage(t, "example", "2.0.0", nil), suffixReader())
		if err != nil {
			t.Fatal(err)
		}
		if err := install(t, rg, c, staged); err != nil {
			t.Fatalf("install: %v", err)
		}
		dir := c.Adapters()[0].Dir()
		if dir == config.AdapterVersionDir("example", "2.0.0") {
			t.Errorf("the package was installed over the leftover, in %s", dir)
		}
		if got, err := rg.mem.ReadFile(path.Join(dir, "README")); err != nil || string(got) != "read me\n" {
			t.Errorf("the new version's README = %q, %v", got, err)
		}
		if !exists(rg.mem, leftover) {
			t.Error("the installation discarded a directory that is not the package it replaces")
		}
		rg.load()
		if exists(rg.mem, leftover) {
			t.Error("the repair at the next start left the leftover directory")
		}
	})
	t.Run("a version that an earlier installation replaced is installed under a directory of its own", func(t *testing.T) {
		// The enabled instance may run any package that was replaced, so none of
		// them is discarded, and installing a version again must not discard the
		// directory a replaced package of that version is still in.
		rg, c := loaded(t)
		mark := func(dir string) string {
			p := path.Join(dir, "marker")
			if err := fsx.Replace(rg.mem, setupTag, p, []byte("held"), fsx.ModePrivate); err != nil {
				t.Fatal(err)
			}
			return p
		}
		held := []string{mark(config.AdapterVersionDir("example", "1.0.0"))}
		for i, step := range []struct{ version, dir string }{
			{"2.0.0", config.AdapterVersionDir("example", "2.0.0")},
			{"1.0.0", config.AdapterRevisionDir("example", "1.0.0", 1)},
			{"2.0.0", config.AdapterRevisionDir("example", "2.0.0", 1)},
			{"1.0.0", config.AdapterRevisionDir("example", "1.0.0", 2)},
		} {
			staged, err := c.StageAdapter(srcPackage(t, "example", step.version, nil), bytes.NewReader([]byte{byte(i + 1), 2, 3, 4, 5, 6, 7, 8}))
			if err != nil {
				t.Fatal(err)
			}
			if err := install(t, rg, c, staged); err != nil {
				t.Fatalf("install %s: %v", step.version, err)
			}
			if got := c.Adapters()[0]; got.Version != step.version || got.Dir() != step.dir {
				t.Fatalf("after installing %s the adapter is %s in %s, want %s", step.version, got.Version, got.Dir(), step.dir)
			}
			if got, err := rg.mem.ReadFile(path.Join(step.dir, "README")); err != nil || string(got) != "read me\n" {
				t.Fatalf("the README of the package in %s = %q, %v", step.dir, got, err)
			}
			for _, m := range held {
				if !exists(rg.mem, m) {
					t.Errorf("installing %s discarded %s", step.version, path.Dir(m))
				}
			}
			held = append(held, mark(step.dir))
		}
		restarted := rg.load()
		if got := restarted.Adapters()[0]; got.Version != "1.0.0" || got.Revision != 2 {
			t.Errorf("after a restart the adapter is %+v, want 1.0.0 at revision 2", got)
		}
		if work, _ := config.AdapterGCWork(rg.mem); len(work) != 0 {
			t.Errorf("gc work after the repair = %+v", work)
		}
		for _, m := range held[:len(held)-1] {
			if exists(rg.mem, m) {
				t.Errorf("the repair left %s", path.Dir(m))
			}
		}
	})
	t.Run("a package of unknown contents is replaced by the same version", func(t *testing.T) {
		_, c := loaded(t)
		staged, err := c.StageAdapter(srcPackage(t, "example", "1.0.0", nil), suffixReader())
		if err != nil {
			t.Fatal(err)
		}
		plan, err := c.PlanInstall(staged, nil)
		if err != nil || plan.AlreadyCurrent || !plan.Replaced {
			t.Errorf("PlanInstall = %+v, %v, want the package replaced", plan, err)
		}
	})
	t.Run("the same version with the same contents is already installed, with other contents it is replaced", func(t *testing.T) {
		rg, c := loaded(t)
		// The golden tree holds a package whose contents were not recorded; the first
		// installation of the version makes them known, as a replacement.
		staged, err := c.StageAdapter(srcPackage(t, "example", "1.0.0", nil), suffixReader())
		if err != nil {
			t.Fatal(err)
		}
		if err := install(t, rg, c, staged); err != nil {
			t.Fatal(err)
		}
		again, err := c.StageAdapter(srcPackage(t, "example", "1.0.0", nil), bytes.NewReader([]byte{1, 1, 1, 1, 1, 1, 1, 1}))
		if err != nil {
			t.Fatal(err)
		}
		if again.Digest != staged.Digest {
			t.Errorf("the same package has the digests %s and %s", staged.Digest, again.Digest)
		}
		if plan, err := c.PlanInstall(again, nil); err != nil || !plan.AlreadyCurrent || plan.Replaced {
			t.Errorf("PlanInstall of the same contents = %+v, %v, want AlreadyCurrent", plan, err)
		}
		if err := c.Unstage(again); err != nil {
			t.Fatal(err)
		}

		changed, err := c.StageAdapter(srcPackage(t, "example", "1.0.0", map[string]string{"README": "changed\n"}), bytes.NewReader([]byte{2, 2, 2, 2, 2, 2, 2, 2}))
		if err != nil {
			t.Fatal(err)
		}
		if changed.Digest == staged.Digest {
			t.Fatal("other contents have the same digest")
		}
		plan, err := c.PlanInstall(changed, nil)
		if err != nil || plan.AlreadyCurrent || !plan.Replaced {
			t.Fatalf("PlanInstall of other contents = %+v, %v, want the package replaced", plan, err)
		}
		if err := install(t, rg, c, changed); err != nil {
			t.Fatal(err)
		}
		if ads := c.Adapters(); len(ads) != 1 || ads[0].Version != "1.0.0" || ads[0].Digest != changed.Digest || ads[0].Revision != 2 {
			t.Errorf("adapters = %+v, want 1.0.0 at revision 2 with the new digest", ads)
		}
		if got, err := rg.mem.ReadFile(path.Join(c.Adapters()[0].Dir(), "README")); err != nil || string(got) != "changed\n" {
			t.Errorf("README of the installed package = %q, %v", got, err)
		}
		// The enabled instance may still run the package that was replaced, so its
		// directory is left for the repair.
		if !exists(rg.mem, config.AdapterRevisionDir("example", "1.0.0", 1)) {
			t.Error("the directory an enabled instance may run from was discarded")
		}
		restarted := rg.load()
		if exists(rg.mem, config.AdapterRevisionDir("example", "1.0.0", 1)) || exists(rg.mem, config.AdapterVersionDir("example", "1.0.0")) {
			t.Error("the repair at the next start left a package the pointer does not name")
		}
		if got := restarted.Adapters(); len(got) != 1 || got[0].Revision != 2 {
			t.Errorf("after a restart adapters = %+v", got)
		}
	})
	t.Run("a package replaced with no instance holding it is discarded", func(t *testing.T) {
		rg, c := loaded(t)
		if _, err := c.DisableAdapter(request(1, 4), "example", nil, result(new([]config.Instances), `{}`)); err != nil {
			t.Fatal(err)
		}
		staged, err := c.StageAdapter(srcPackage(t, "example", "1.0.0", map[string]string{"README": "changed\n"}), suffixReader())
		if err != nil {
			t.Fatal(err)
		}
		if err := install(t, rg, c, staged); err != nil {
			t.Fatal(err)
		}
		if exists(rg.mem, config.AdapterVersionDir("example", "1.0.0")) {
			t.Error("the replaced package was left although no instance runs")
		}
		if !exists(rg.mem, config.AdapterRevisionDir("example", "1.0.0", 1)) {
			t.Error("the new package is not in its directory")
		}
	})
	t.Run("an adapter with an enabled instance is upgraded and the version it runs is left for the repair", func(t *testing.T) {
		rg, c := loaded(t)
		staged, err := c.StageAdapter(srcPackage(t, "example", "2.0.0", nil), suffixReader())
		if err != nil {
			t.Fatal(err)
		}
		plan, err := c.PlanInstall(staged, nil)
		if err != nil || plan.AlreadyCurrent || !plan.Replaced {
			t.Fatalf("PlanInstall = %+v, %v, want the package replaced", plan, err)
		}
		if err := install(t, rg, c, staged); err != nil {
			t.Fatalf("install: %v", err)
		}
		if ads := c.Adapters(); len(ads) != 1 || ads[0].Version != "2.0.0" {
			t.Errorf("adapters = %+v, want example 2.0.0", ads)
		}
		if !exists(rg.mem, config.AdapterVersionDir("example", "1.0.0")) {
			t.Error("the directory an enabled instance may run from was discarded")
		}
		rg.load()
		if exists(rg.mem, config.AdapterVersionDir("example", "1.0.0")) {
			t.Error("the repair at the next start left a package the pointer does not name")
		}
	})
	t.Run("a disabled instance whose process is still ending holds the files it ran from, whatever the package", func(t *testing.T) {
		for _, p := range []struct {
			name    string
			version string
			files   map[string]string
		}{
			{"another version", "2.0.0", nil},
			{"the same version with other contents", "1.0.0", map[string]string{"README": "changed\n"}},
		} {
			for _, e := range []struct {
				name   string
				ending config.StillEnding
				held   bool
			}{
				{"the process is ending", func(string) bool { return true }, true},
				{"the process has ended", func(string) bool { return false }, false},
				{"nothing can say", nil, false},
			} {
				t.Run(p.name+", "+e.name, func(t *testing.T) {
					rg, c := loaded(t)
					doc := strings.Replace(goldenInstances, `"enabled": true`, `"enabled": false`, 1)
					if err := fsx.Replace(rg.mem, setupTag, config.InstancesPath, []byte(doc), fsx.ModePrivate); err != nil {
						t.Fatal(err)
					}
					c = rg.load()
					staged, err := c.StageAdapter(srcPackage(t, "example", p.version, p.files), suffixReader())
					if err != nil {
						t.Fatal(err)
					}
					if err := installAsking(t, rg, c, staged, e.ending); err != nil {
						t.Fatalf("install: %v", err)
					}
					if got := exists(rg.mem, config.AdapterVersionDir("example", "1.0.0")); got != e.held {
						t.Errorf("the package that was replaced is there: %v, want %v", got, e.held)
					}
					rg.load()
					if exists(rg.mem, config.AdapterVersionDir("example", "1.0.0")) {
						t.Error("the repair at the next start left a package the pointer does not name")
					}
				})
			}
		}
	})
	t.Run("a first installation is not an upgrade whatever instances exist", func(t *testing.T) {
		rg, c := loaded(t)
		// An owner wrote an enabled instance of an adapter that is not installed yet.
		doc := strings.Replace(goldenInstances, `"adapter": "example"`, `"adapter": "fresh"`, 1)
		if err := fsx.Replace(rg.mem, setupTag, config.InstancesPath, []byte(doc), fsx.ModePrivate); err != nil {
			t.Fatal(err)
		}
		c = rg.load()
		staged, err := c.StageAdapter(srcPackage(t, "fresh", "0.1.0", nil), suffixReader())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.PlanInstall(staged, nil); err != nil {
			t.Errorf("PlanInstall: %v", err)
		}
	})
}

// The garbage of the adapter directory, as a function of the disk, named: the
// recovery transaction removes exactly these, and a clean disk has none.
func TestAdapterGCWork(t *testing.T) {
	rows := []struct {
		name  string
		build func(t *testing.T, mem *fsxtest.Mem)
		want  []config.GCItem
	}{
		{"a clean installation", func(t *testing.T, mem *fsxtest.Mem) {}, nil},
		{"a staging directory", func(t *testing.T, mem *fsxtest.Mem) {
			mkdirAll(t, mem, config.AdapterStageDir("example", "beef"))
		}, []config.GCItem{{Path: config.AdapterStageDir("example", "beef"), Reason: config.GCStaging}}},
		{"a version the pointer does not name", func(t *testing.T, mem *fsxtest.Mem) {
			mkdirAll(t, mem, config.AdapterVersionDir("example", "0.9.0"))
		}, []config.GCItem{{Path: config.AdapterVersionDir("example", "0.9.0"), Reason: config.GCSuperseded}}},
		{"an adapter directory without a pointer", func(t *testing.T, mem *fsxtest.Mem) {
			mkdirAll(t, mem, config.AdapterVersionsDir("half"))
			mkdirAll(t, mem, config.AdapterStageDir("half", "cafe"))
		}, []config.GCItem{{Path: config.AdapterDir("half"), Reason: config.GCNotInstalled}}},
		{"an empty adapter directory", func(t *testing.T, mem *fsxtest.Mem) {
			mkdirAll(t, mem, config.AdapterDir("empty"))
		}, []config.GCItem{{Path: config.AdapterDir("empty"), Reason: config.GCNotInstalled}}},
		{"all of them, in a stable order", func(t *testing.T, mem *fsxtest.Mem) {
			mkdirAll(t, mem, config.AdapterStageDir("example", "beef"))
			mkdirAll(t, mem, config.AdapterVersionDir("example", "0.9.0"))
			mkdirAll(t, mem, config.AdapterDir("half"))
		}, []config.GCItem{
			{Path: config.AdapterStageDir("example", "beef"), Reason: config.GCStaging},
			{Path: config.AdapterVersionDir("example", "0.9.0"), Reason: config.GCSuperseded},
			{Path: config.AdapterDir("half"), Reason: config.GCNotInstalled},
		}},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			rg := newRig(t)
			putTree(t, rg.mem, validTree(t))
			r.build(t, rg.mem)
			got, err := config.AdapterGCWork(rg.mem)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, r.want) {
				t.Errorf("work = %+v, want %+v", got, r.want)
			}
		})
	}
}

// A tree with garbage in the adapter directory loads, and the loading removes
// the garbage. An adapter directory without a pointer is never a reason to
// refuse to start.
func TestLoadRepairsTheAdapterDirectory(t *testing.T) {
	rg := newRig(t)
	putTree(t, rg.mem, validTree(t))
	mkdirAll(t, rg.mem, config.AdapterStageDir("example", "beef"))
	mkdirAll(t, rg.mem, config.AdapterVersionDir("example", "0.9.0"))
	mkdirAll(t, rg.mem, config.AdapterStageDir("half", "cafe"))
	if err := fsx.Replace(rg.mem, setupTag, path.Join(config.AdapterStageDir("example", "beef"), "f"), []byte("x"), fsx.ModePrivate); err != nil {
		t.Fatal(err)
	}
	c := rg.load()
	if work, err := config.AdapterGCWork(rg.mem); err != nil || len(work) != 0 {
		t.Errorf("gc work after Load = %+v, %v", work, err)
	}
	if ads := c.Adapters(); len(ads) != 1 || ads[0].ID != "example" || ads[0].Version != "1.0.0" {
		t.Errorf("adapters = %+v", ads)
	}
	if exists(rg.mem, config.AdapterDir("half")) {
		t.Error("the unfinished adapter directory is still there")
	}
	if !exists(rg.mem, config.AdapterManifestPath("example", "1.0.0")) {
		t.Error("the current version was removed")
	}
	if _, err := rg.mem.Stat(config.AdapterDir("half")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Stat = %v", err)
	}
}

// blockingSource serves a package and holds the read of one file until released:
// a copy that is stuck halfway, as a slow disk or a network share makes it.
type blockingSource struct {
	fsx.Reader
	file    string
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingSource) ReadFile(p string) ([]byte, error) {
	return b.ReadFileLimit(p, math.MaxInt64)
}

func (b *blockingSource) ReadFileLimit(p string, maxBytes int64) ([]byte, error) {
	if p == b.file {
		b.once.Do(func() { close(b.entered) })
		<-b.release
	}
	return b.Reader.ReadFileLimit(p, maxBytes)
}

// Copying a package can take as long as the package is big, and it touches only
// a staging directory no change reads, so the writer loop must not wait for it:
// while one package is stuck halfway through its copy, a versioned change and
// the dropping of another staged package both complete.
func TestStagingDoesNotHoldUpTheWriterLoop(t *testing.T) {
	rg, c := loaded(t)
	other, err := c.StageAdapter(srcPackage(t, "fresh", "0.1.0", nil), suffixReader())
	if err != nil {
		t.Fatalf("StageAdapter: %v", err)
	}

	src := &blockingSource{Reader: srcPackage(t, "example", "2.0.0", nil), file: "README",
		entered: make(chan struct{}), release: make(chan struct{})}
	staging := make(chan error, 1)
	go func() {
		_, err := c.StageAdapter(src, bytes.NewReader([]byte{9, 9, 9, 9, 9, 9, 9, 9}))
		staging <- err
	}()
	released := false
	release := func() {
		if !released {
			released = true
			close(src.release)
		}
	}
	defer release()
	select {
	case <-src.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the copy never reached the file it was to block on")
	}

	changed := make(chan error, 1)
	go func() {
		_, err := c.SetNodeSettings(request(1, c.Node().Version), []byte(`{"reply_permission": false}`), result(new([]config.Node), `{}`))
		changed <- err
	}()
	unstaged := make(chan error, 1)
	go func() { unstaged <- c.Unstage(other) }()
	for _, step := range []struct {
		name string
		done <-chan error
	}{{"a versioned change", changed}, {"the dropping of another staged package", unstaged}} {
		select {
		case err := <-step.done:
			if err != nil {
				t.Errorf("%s: %v", step.name, err)
			}
		case <-time.After(10 * time.Second):
			t.Errorf("%s waited for a copy that is stuck halfway", step.name)
		}
	}
	if c.Node().Settings.ReplyPermission {
		t.Error("the versioned change did not take effect")
	}

	release()
	if err := <-staging; err != nil {
		t.Errorf("the copy that was held up failed once released: %v", err)
	}
	if !exists(rg.mem, config.AdapterStageDir("example", "0909090909090909")) {
		t.Error("the copy that was held up did not leave its staging directory")
	}
}

// modeFS records the mode of every file a transaction creates, under the path
// the file ends up at: the in-memory file system does not model modes.
type modeFS struct {
	*fsxtest.Mem
	mu    sync.Mutex
	temp  map[string]fsx.Mode
	final map[string]fsx.Mode
}

func (m *modeFS) CreateTemp(tag fsx.Tag, dir string, mode fsx.Mode) (fsx.File, error) {
	f, err := m.Mem.CreateTemp(tag, dir, mode)
	if err == nil {
		m.mu.Lock()
		m.temp[f.Name()] = mode
		m.mu.Unlock()
	}
	return f, err
}

func (m *modeFS) Rename(tag fsx.Tag, from, to string) error {
	err := m.Mem.Rename(tag, from, to)
	if err == nil {
		m.mu.Lock()
		if mode, ok := m.temp[from]; ok {
			delete(m.temp, from)
			m.final[to] = mode
		}
		m.mu.Unlock()
	}
	return err
}

// The entry program of a staged package is the one file that is executable;
// every other file is private to the owner.
func TestStagedFilesKeepTheirModes(t *testing.T) {
	mem := fsxtest.NewMem()
	fsys := &modeFS{Mem: mem, temp: map[string]fsx.Mode{}, final: map[string]fsx.Mode{}}
	rg := &rig{t: t, mem: mem, tab: newTable(t)}
	rg.w = txn.New(fsys, rg.tab, nil)
	recoverWriter(t, rg.w)
	putTree(t, mem, validTree(t))
	c := rg.load()

	staged, err := c.StageAdapter(srcPackage(t, "example", "2.0.0", nil), suffixReader())
	if err != nil {
		t.Fatalf("StageAdapter: %v", err)
	}
	want := map[string]fsx.Mode{
		path.Join(staged.Dir, "bin/sidera-adapter-example"): fsx.ModePrivateExec,
		path.Join(staged.Dir, "sidera-adapter.json"):        fsx.ModePrivate,
		path.Join(staged.Dir, "lib/a/b.txt"):                fsx.ModePrivate,
		path.Join(staged.Dir, "README"):                     fsx.ModePrivate,
	}
	fsys.mu.Lock()
	defer fsys.mu.Unlock()
	for p, mode := range want {
		got, ok := fsys.final[p]
		if !ok || got != mode {
			t.Errorf("mode of %s = %v (recorded %v), want %v", p, got, ok, mode)
		}
	}
	for p, mode := range fsys.final {
		if _, expected := want[p]; !expected && strings.HasPrefix(p, staged.Dir) {
			t.Errorf("a file %s (mode %v) was written that the package does not hold", p, mode)
		}
	}
}

const richManifest = `{"manifest_version":1,"id":"rich","version":"0.1.0","protocol":{"min":1,"max":1},` +
	`"entry":{"program":"bin/sidera-adapter-example","args":["--serve"]},` +
	`"platforms":[{"os":"linux","arch":"amd64"},{"os":"darwin","arch":"arm64"}],` +
	`"requires":[{"program":"git","os":["linux","darwin"]}],` +
	`"config":{"instance":[{"key":"limit","type":"integer","title":"Limit","default":3,"min":1,"max":9}],` +
	`"preset":[{"key":"mode","type":"enum","title":"Mode","options":["a","b"],"default":"a"}]},` +
	`"budgets":[{"frontend":"cli","unit":"chars","limit":1000}],` +
	`"recv_wait_limits":[{"frontend":"cli","max_s":30}],` +
	`"ack_markers":{"replaced":[{"literal":"replaced"}],"truncated":[{"regexp":"cut.*"}]}}`

// What Adapters returns is the caller's: changing any slice, pointer or raw
// value in it changes nothing that a later call, or the configuration, holds.
func TestAdaptersReturnsCopiesOfTheManifests(t *testing.T) {
	rg, c := loaded(t)
	want, err := manifest.Parse([]byte(richManifest))
	if err != nil {
		t.Fatalf("the manifest of the test is not valid: %v", err)
	}
	staged, err := c.StageAdapter(srcPackage(t, "rich", "0.1.0", map[string]string{"sidera-adapter.json": richManifest}), suffixReader())
	if err != nil {
		t.Fatalf("StageAdapter: %v", err)
	}
	if err := install(t, rg, c, staged); err != nil {
		t.Fatalf("install: %v", err)
	}
	find := func() manifest.Manifest {
		for _, a := range c.Adapters() {
			if a.ID == "rich" {
				return a.Manifest
			}
		}
		t.Fatal("the installed adapter is not listed")
		return manifest.Manifest{}
	}
	if got := find(); !reflect.DeepEqual(got, want) {
		t.Fatalf("manifest = %+v, want %+v", got, want)
	}

	m := find()
	m.Entry.Args[0] = "x"
	m.Platforms[0].OS = "x"
	m.Requires[0].OS[0] = "x"
	m.Config.Instance[0].Title = "x"
	m.Config.Instance[0].Default[0] = '7'
	*m.Config.Instance[0].Max = 99
	m.Config.Preset[0].Options[0] = "x"
	m.Budgets[0].Limit = 1
	m.RecvWaitLimits[0].MaxS = 1
	*m.AckMarkers.Replaced[0].Literal = "x"
	*m.AckMarkers.Truncated[0].Regexp = "x"
	if got := find(); !reflect.DeepEqual(got, want) {
		t.Errorf("a change to a returned manifest reached the next call:\n got %+v\nwant %+v", got, want)
	}

	// The manifest the caller staged is not shared with the configuration either.
	staged.Manifest.Platforms[0].OS = "x"
	staged.Manifest.Budgets[0].Limit = 1
	if got := find(); !reflect.DeepEqual(got, want) {
		t.Errorf("a change to the staged manifest reached the installed one:\n got %+v\nwant %+v", got, want)
	}
}

// boundedSource is a package source that permits reads only within the remaining
// package budget.
type boundedSource struct {
	fsx.Reader
	remaining int64
	first     bool
}

func (b *boundedSource) ReadFile(p string) ([]byte, error) {
	return nil, errors.New("this source requires bounded reads")
}

func (b *boundedSource) ReadFileLimit(p string, maxBytes int64) ([]byte, error) {
	// The manifest is read once for judgement before the copy starts.
	if !b.first {
		b.first = true
		if p != manifest.FileName || maxBytes < 0 || maxBytes > config.MaxPackageBytes {
			return nil, errors.New("manifest read exceeds its budget")
		}
		return b.Reader.ReadFileLimit(p, maxBytes)
	}
	if maxBytes < 0 || maxBytes > b.remaining {
		return nil, errors.New("read exceeds the remaining package budget")
	}
	data, err := b.Reader.ReadFileLimit(p, maxBytes)
	b.remaining -= int64(len(data))
	return data, err
}

func TestStagingReadsTheManifestAndEveryFileWithinThePackageBudget(t *testing.T) {
	_, c := loaded(t)
	src := &boundedSource{Reader: srcPackage(t, "fresh", "0.1.0", nil), remaining: config.MaxPackageBytes}
	staged, err := c.StageAdapter(src, suffixReader())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Unstage(staged); err != nil {
		t.Fatal(err)
	}
}

// Installing a package over an adapter whose package could not be loaded makes the
// adapter good again, and a pointer that names the very package being installed
// does not make that installation a no-op: the manifest it points at is the thing
// that is wrong.
func TestInstallingOverAFaultedAdapterRecoversIt(t *testing.T) {
	rg := newRig(t)
	tree := validTree(t)
	tree[config.AdapterManifestPath("example", "1.0.0")] = `{"manifest_version": 1}`
	// The pointer holds the digest of the package that is about to be installed.
	pkg := srcPackage(t, "example", "1.0.0", nil)
	_, c0 := loaded(t)
	staged0, err := c0.StageAdapter(pkg, suffixReader())
	if err != nil {
		t.Fatal(err)
	}
	tree[config.AdapterCurrentPath("example")] = `{"format": 1, "version": "1.0.0", "digest": "` + staged0.Digest + `"}`
	putTree(t, rg.mem, tree)
	c := rg.load()
	if ads := c.Adapters(); len(ads) != 1 || ads[0].Fault == nil {
		t.Fatalf("adapters = %+v, want example faulted", ads)
	}
	staged, err := c.StageAdapter(srcPackage(t, "example", "1.0.0", nil), otherSuffixReader())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := c.PlanInstall(staged, nil)
	if err != nil {
		t.Fatal(err)
	}
	if plan.AlreadyCurrent || !plan.Replaced {
		t.Fatalf("plan = {AlreadyCurrent %v Replaced %v}, want a replacement", plan.AlreadyCurrent, plan.Replaced)
	}
	if err := install(t, rg, c, staged); err != nil {
		t.Fatalf("install: %v", err)
	}
	if ads := c.Adapters(); len(ads) != 1 || ads[0].Fault != nil || ads[0].Manifest.ID != "example" {
		t.Errorf("adapters after the installation = %+v, want example loaded", ads)
	}
	if ads := rg.load().Adapters(); len(ads) != 1 || ads[0].Fault != nil {
		t.Errorf("a restart reads %+v", ads)
	}
}

// The repair of the adapter directory leaves an adapter whose pointer cannot be
// read alone: what it keeps is not known.
func TestAdapterGCWorkSkipsAnAdapterWhosePointerIsBad(t *testing.T) {
	rg := newRig(t)
	tree := validTree(t)
	tree[config.AdapterCurrentPath("example")] = "not json"
	putTree(t, rg.mem, tree)
	mkdirAll(t, rg.mem, config.AdapterVersionDir("example", "0.9.0"))
	work, err := config.AdapterGCWork(rg.mem)
	if err != nil || len(work) != 0 {
		t.Errorf("work = %+v, %v; want none and no error", work, err)
	}
}
