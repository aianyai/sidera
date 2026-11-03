package unpack

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type kind int

const (
	file kind = iota
	dir
	symlink
	hardlink
	device
	fifo
)

// entry is one member of a test archive. For links, data is the link target.
type entry struct {
	name string
	kind kind
	data string
	mode fs.FileMode
}

func f(name, data string, mode fs.FileMode) entry {
	return entry{name: name, kind: file, data: data, mode: mode}
}
func d(name string) entry { return entry{name: name, kind: dir, mode: 0o755} }

type format struct {
	name  string
	build func(t *testing.T, entries []entry) []byte
}

var formats = []format{{"tar.gz", buildTarGz}, {"zip", buildZip}}

func buildTarGz(t *testing.T, entries []entry) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: int64(e.mode.Perm())}
		switch e.kind {
		case file:
			h.Typeflag, h.Size = tar.TypeReg, int64(len(e.data))
		case dir:
			h.Typeflag = tar.TypeDir
		case symlink:
			h.Typeflag, h.Linkname = tar.TypeSymlink, e.data
		case hardlink:
			h.Typeflag, h.Linkname = tar.TypeLink, e.data
		case device:
			h.Typeflag, h.Devmajor, h.Devminor = tar.TypeChar, 1, 3
		case fifo:
			h.Typeflag = tar.TypeFifo
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if e.kind == file {
			if _, err := tw.Write([]byte(e.data)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func buildZip(t *testing.T, entries []entry) []byte {
	t.Helper()
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		switch e.kind {
		case file:
			h.SetMode(e.mode.Perm())
		case dir:
			h.SetMode(fs.ModeDir | e.mode.Perm())
		case symlink:
			h.SetMode(fs.ModeSymlink | 0o777)
		case device:
			h.SetMode(fs.ModeDevice | fs.ModeCharDevice | 0o600)
		case fifo:
			h.SetMode(fs.ModeNamedPipe | 0o600)
		case hardlink:
			t.Fatal("zip has no hard links")
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if e.kind == file || e.kind == symlink {
			if _, err := w.Write([]byte(e.data)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// run writes the archive and extracts it into a new directory inside a scratch
// root, and returns the root, the extraction directory and Extract's error.
func run(t *testing.T, fm format, entries []entry, limits Limits) (root, out string, err error) {
	t.Helper()
	root = t.TempDir()
	archive := filepath.Join(root, "release."+fm.name)
	if err := os.WriteFile(archive, fm.build(t, entries), 0o600); err != nil {
		t.Fatal(err)
	}
	out = filepath.Join(root, "out")
	if err := os.Mkdir(out, 0o755); err != nil {
		t.Fatal(err)
	}
	return root, out, Extract(archive, out, limits)
}

var small = Limits{MaxMember: 16, MaxTotal: 40, MaxEntries: 6}

func TestExtractRefuses(t *testing.T) {
	tests := []struct {
		name    string
		entries []entry
		limits  Limits
		skipZip bool // the kind of member cannot be written into a zip
	}{
		{name: "absolute path", entries: []entry{f("/x", "a", 0o644)}},
		{name: "drive path", entries: []entry{f(`C:\x`, "a", 0o644)}},
		{name: "drive path with slashes", entries: []entry{f("C:/x", "a", 0o644)}},
		{name: "UNC path", entries: []entry{f(`\\host\x`, "a", 0o644)}},
		{name: "backslash", entries: []entry{f(`a\b`, "a", 0o644)}},
		{name: "dot dot first", entries: []entry{f("../escape", "a", 0o644)}},
		{name: "dot dot inside", entries: []entry{f("adapters/../../escape", "a", 0o644)}},
		{name: "dot dot directory", entries: []entry{d("../escape/")}},
		{name: "dot component", entries: []entry{f("./x", "a", 0o644)}},
		{name: "empty component", entries: []entry{f("a//b", "a", 0o644)}},
		{name: "empty name", entries: []entry{f("", "a", 0o644)}},
		{name: "symbolic link", entries: []entry{entry{name: "link", kind: symlink, data: "/etc/passwd"}}},
		{name: "symbolic link inside the tree", entries: []entry{d("a/"), entry{name: "a/link", kind: symlink, data: "b"}}},
		{name: "hard link", entries: []entry{f("a", "a", 0o644), entry{name: "b", kind: hardlink, data: "a"}}, skipZip: true},
		{name: "device file", entries: []entry{entry{name: "null", kind: device}}},
		{name: "named pipe", entries: []entry{entry{name: "pipe", kind: fifo}}},
		{name: "duplicate file", entries: []entry{f("a", "1", 0o644), f("a", "2", 0o644)}},
		{name: "duplicate directory", entries: []entry{d("a/"), d("a/")}},
		{name: "file and directory of one name", entries: []entry{f("a", "1", 0o644), d("a/")}},
		{name: "member over the cap", entries: []entry{f("a", strings.Repeat("x", 17), 0o644)}, limits: small},
		{name: "total over the cap", entries: []entry{
			f("a", strings.Repeat("x", 16), 0o644), f("b", strings.Repeat("x", 16), 0o644), f("c", strings.Repeat("x", 9), 0o644),
		}, limits: small},
		{name: "too many members", entries: []entry{
			f("a", "", 0o644), f("b", "", 0o644), f("c", "", 0o644), f("d", "", 0o644), f("e", "", 0o644), f("f", "", 0o644), f("g", "", 0o644),
		}, limits: small},
		{name: "directories count as members", entries: []entry{
			d("a/"), d("b/"), d("c/"), d("d/"), d("e/"), d("f/"), d("g/"),
		}, limits: small},
	}
	for _, tc := range tests {
		for _, fm := range formats {
			if tc.skipZip && fm.name == "zip" {
				continue
			}
			t.Run(fm.name+"/"+tc.name, func(t *testing.T) {
				limits := tc.limits
				if limits == (Limits{}) {
					limits = small
				}
				root, _, err := run(t, fm, tc.entries, limits)
				if !errors.Is(err, ErrRefused) {
					t.Fatalf("Extract = %v, want ErrRefused", err)
				}
				// Nothing may have been written outside the extraction directory.
				names := dirNames(t, root)
				if want := "out release." + fm.name; strings.Join(names, " ") != want {
					t.Fatalf("scratch root holds %q, want %q", names, want)
				}
			})
		}
	}
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// releaseLayout has the shape of a release archive: no directory around the
// files, the two programs at the top, one package per adapter.
func releaseLayout(withDirs bool) []entry {
	var es []entry
	add := func(e ...entry) { es = append(es, e...) }
	add(f("sidera", "program", 0o755), f("sidera-adapter-console", "console", 0o755))
	if withDirs {
		add(d("adapters/"), d("adapters/claude/"), d("adapters/claude/bin/"), d("adapters/console/"), d("adapters/console/bin/"))
	}
	add(
		f("adapters/claude/sidera-adapter.json", "{}", 0o644),
		f("adapters/claude/bin/sidera-adapter-claude", "claude", 0o755),
		f("adapters/console/sidera-adapter.json", "{}", 0o644),
		f("adapters/console/bin/sidera-adapter-console", "console", 0o755),
		f("LICENSE", "license", 0o644), f("THIRD_PARTY_NOTICES", "notices", 0o644), f("README.txt", "readme", 0o644),
	)
	return es
}

func TestExtractReleaseLayout(t *testing.T) {
	for _, fm := range formats {
		for _, withDirs := range []bool{true, false} {
			name := fm.name + "/with directory entries"
			if !withDirs {
				name = fm.name + "/files only"
			}
			t.Run(name, func(t *testing.T) {
				entries := releaseLayout(withDirs)
				_, out, err := run(t, fm, entries, DefaultLimits())
				if err != nil {
					t.Fatal(err)
				}
				want := map[string]entry{}
				for _, e := range entries {
					if e.kind == file {
						want[e.name] = e
					}
				}
				got := map[string]bool{}
				err = filepath.WalkDir(out, func(path string, de fs.DirEntry, err error) error {
					if err != nil || path == out {
						return err
					}
					rel, _ := filepath.Rel(out, path)
					rel = filepath.ToSlash(rel)
					if de.IsDir() {
						return nil
					}
					got[rel] = true
					e, ok := want[rel]
					if !ok {
						t.Errorf("unexpected file %s", rel)
						return nil
					}
					if !de.Type().IsRegular() {
						t.Errorf("%s is not a regular file", rel)
					}
					body, err := os.ReadFile(path)
					if err != nil {
						return err
					}
					if string(body) != e.data {
						t.Errorf("%s holds %q, want %q", rel, body, e.data)
					}
					if runtime.GOOS != "windows" {
						info, err := de.Info()
						if err != nil {
							return err
						}
						if exec := info.Mode().Perm()&0o111 != 0; exec != (e.mode&0o111 != 0) {
							t.Errorf("%s has mode %v, executable should be %v", rel, info.Mode().Perm(), !exec)
						}
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				for rel := range want {
					if !got[rel] {
						t.Errorf("missing file %s", rel)
					}
				}
			})
		}
	}
}

func TestExtractNeedsAnEmptyDirectoryAndAKnownSuffix(t *testing.T) {
	root := t.TempDir()
	archive := filepath.Join(root, "release.tar.gz")
	if err := os.WriteFile(archive, buildTarGz(t, []entry{f("a", "a", 0o644)}), 0o600); err != nil {
		t.Fatal(err)
	}
	notEmpty := filepath.Join(root, "full")
	if err := os.Mkdir(notEmpty, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(notEmpty, "old"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Extract(archive, notEmpty, DefaultLimits()); err == nil {
		t.Error("a directory with files in it was accepted")
	}
	if err := Extract(archive, filepath.Join(root, "missing"), DefaultLimits()); err == nil {
		t.Error("a directory that does not exist was accepted")
	}
	empty := filepath.Join(root, "empty")
	if err := os.Mkdir(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(root, "release.tar")
	if err := os.Rename(archive, other); err != nil {
		t.Fatal(err)
	}
	if err := Extract(other, empty, DefaultLimits()); err == nil {
		t.Error("an archive that is neither .tar.gz nor .zip was accepted")
	}
}
