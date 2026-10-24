package config_test

import (
	"bytes"
	"errors"
	"io/fs"
	"path"
	"reflect"
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

func ensureState(t testing.TB, mem *fsxtest.Mem) { mkdirAll(t, mem, "state") }

// install runs the installation of a staged package as the engine composes it.
func install(t testing.TB, rg *rig, c *config.Config, staged config.Staged) error {
	t.Helper()
	ensureState(t, rg.mem)
	plan, err := c.PlanInstall(staged)
	if err != nil || plan.AlreadyCurrent {
		return err
	}
	tx := rg.w.Begin(testAdapterInstall)
	defer tx.Close()
	if err := plan.DiscardStale(tx); err != nil {
		return err
	}
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
	if p == f.path {
		f.mu.Lock()
		f.reads++
		n := f.reads
		f.mu.Unlock()
		if n > 1 {
			return []byte(f.second), nil
		}
	}
	return f.Reader.ReadFile(p)
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
	t.Run("a version directory that was renamed in and did not become current", func(t *testing.T) {
		rg, c := loaded(t)
		if _, err := c.DisableAdapter(request(1, 4), "example", nil, result(new([]config.Instances), `{}`)); err != nil {
			t.Fatal(err)
		}
		staged, err := c.StageAdapter(srcPackage(t, "example", "2.0.0", nil), suffixReader())
		if err != nil {
			t.Fatal(err)
		}
		plan, err := c.PlanInstall(staged)
		if err != nil {
			t.Fatal(err)
		}
		tx := rg.w.Begin(testAdapterInstall)
		if err := plan.Promote(tx); err != nil {
			t.Fatal(err)
		}
		tx.Close() // the commit failed or was refused after the rename
		if !exists(rg.mem, config.AdapterVersionDir("example", "2.0.0")) {
			t.Fatal("the version directory is not there to be dropped")
		}
		if err := c.Unstage(staged); err != nil {
			t.Fatalf("Unstage: %v", err)
		}
		if exists(rg.mem, config.AdapterVersionDir("example", "2.0.0")) {
			t.Error("the version directory that never became current is still there")
		}
		if !exists(rg.mem, config.AdapterVersionDir("example", "1.0.0")) {
			t.Error("the installed version was removed")
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
		// The instance is disabled first: an adapter with an enabled instance is
		// not upgraded.
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
	t.Run("a leftover directory of the version is replaced", func(t *testing.T) {
		rg, c := loaded(t)
		if _, err := c.DisableAdapter(request(1, 4), "example", nil, result(new([]config.Instances), `{}`)); err != nil {
			t.Fatal(err)
		}
		mkdirAll(t, rg.mem, config.AdapterVersionDir("example", "2.0.0"))
		if err := fsx.Replace(rg.mem, setupTag, path.Join(config.AdapterVersionDir("example", "2.0.0"), "old-leftover"), []byte("x"), fsx.ModePrivate); err != nil {
			t.Fatal(err)
		}
		staged, err := c.StageAdapter(srcPackage(t, "example", "2.0.0", nil), suffixReader())
		if err != nil {
			t.Fatal(err)
		}
		if err := install(t, rg, c, staged); err != nil {
			t.Fatalf("install: %v", err)
		}
		if exists(rg.mem, path.Join(config.AdapterVersionDir("example", "2.0.0"), "old-leftover")) {
			t.Error("the leftover directory's content is part of the new version")
		}
		if got, err := rg.mem.ReadFile(path.Join(config.AdapterVersionDir("example", "2.0.0"), "README")); err != nil || string(got) != "read me\n" {
			t.Errorf("the new version's README = %q, %v", got, err)
		}
	})
	t.Run("the version that is current is already installed", func(t *testing.T) {
		_, c := loaded(t)
		staged, err := c.StageAdapter(srcPackage(t, "example", "1.0.0", nil), suffixReader())
		if err != nil {
			t.Fatal(err)
		}
		plan, err := c.PlanInstall(staged)
		if err != nil || !plan.AlreadyCurrent {
			t.Errorf("PlanInstall = %+v, %v, want AlreadyCurrent", plan, err)
		}
	})
	t.Run("an adapter with an enabled instance is not upgraded", func(t *testing.T) {
		_, c := loaded(t)
		staged, err := c.StageAdapter(srcPackage(t, "example", "2.0.0", nil), suffixReader())
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.PlanInstall(staged)
		if ce := changeReason(t, err); ce.Reason != config.ChangeAdapterInUse {
			t.Errorf("reason = %v, want adapter_in_use", ce.Reason)
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
		if _, err := c.PlanInstall(staged); err != nil {
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
	if p == b.file {
		b.once.Do(func() { close(b.entered) })
		<-b.release
	}
	return b.Reader.ReadFile(p)
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
	`"tested":[{"runtime":"claude","min":"1.0.0","max":"2.0.0"}],` +
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
	m.Tested[0].Min = "x"
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
