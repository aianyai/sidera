package fsxtest

import (
	"bytes"
	"errors"
	"io/fs"
	"reflect"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
)

// RunConformance runs the behaviour every fsx.FS must share, so that the same
// composite operations and the code above them behave the same on Mem and on
// the real file system. newFS returns an empty FS for each case.
//
// The cases state requirements (what must fail, what must be visible), not
// snapshots of one platform's error codes: errors are compared with the
// io/fs sentinels.
func RunConformance(t *testing.T, newFS func(t *testing.T) fsx.FS) {
	tag := fsx.Tag{Tx: "conform", Role: "f"}
	cases := []struct {
		name string
		run  func(t *testing.T, fsys fsx.FS)
	}{
		{"temporary-file-written-synced-and-renamed-over-a-target", func(t *testing.T, fsys fsx.FS) {
			for _, content := range []string{"first", "second, longer"} {
				f := mustTemp(t, fsys, tag, ".")
				if !strings.HasPrefix(f.Name(), fsx.TempPrefix) {
					t.Fatalf("temporary name %q lacks the %q prefix", f.Name(), fsx.TempPrefix)
				}
				mustWrite(t, f, content)
				if err := f.Sync(); err != nil {
					t.Fatalf("Sync: %v", err)
				}
				if err := f.Close(); err != nil {
					t.Fatalf("Close: %v", err)
				}
				if err := fsys.Rename(tag, f.Name(), "target"); err != nil {
					t.Fatalf("Rename over the target: %v", err)
				}
				assertContent(t, fsys, "target", content)
			}
			assertNames(t, fsys, ".", "target")
		}},
		{"temporary-names-in-one-directory-are-distinct", func(t *testing.T, fsys fsx.FS) {
			seen := map[string]bool{}
			for range 5 {
				f := mustTemp(t, fsys, tag, ".")
				if seen[f.Name()] {
					t.Fatalf("temporary name %q handed out twice", f.Name())
				}
				seen[f.Name()] = true
				f.Close()
			}
		}},
		{"temporary-file-in-a-subdirectory-has-a-relative-name", func(t *testing.T, fsys fsx.FS) {
			mustMkdir(t, fsys, tag, "d")
			f := mustTemp(t, fsys, tag, "d")
			defer f.Close()
			if !strings.HasPrefix(f.Name(), "d/"+fsx.TempPrefix) {
				t.Errorf("Name() = %q, want a path relative to the root under d/", f.Name())
			}
		}},
		{"write-or-sync-after-close-fails", func(t *testing.T, fsys fsx.FS) {
			f := mustTemp(t, fsys, tag, ".")
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := f.Write([]byte("x")); err == nil {
				t.Error("Write after Close succeeded")
			}
			if err := f.Sync(); err == nil {
				t.Error("Sync after Close succeeded")
			}
		}},
		{"rename-within-a-directory-moves-the-file", func(t *testing.T, fsys fsx.FS) {
			mustPut(t, fsys, tag, "a", "x")
			if err := fsys.Rename(tag, "a", "b"); err != nil {
				t.Fatal(err)
			}
			assertNames(t, fsys, ".", "b")
			assertContent(t, fsys, "b", "x")
		}},
		{"rename-of-a-missing-file-fails", func(t *testing.T, fsys fsx.FS) {
			if err := fsys.Rename(tag, "missing", "b"); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("Rename of a missing file = %v, want fs.ErrNotExist", err)
			}
		}},
		{"rename-across-directories-fails", func(t *testing.T, fsys fsx.FS) {
			mustMkdir(t, fsys, tag, "d")
			mustPut(t, fsys, tag, "a", "x")
			if err := fsys.Rename(tag, "a", "d/a"); !errors.Is(err, fs.ErrInvalid) {
				t.Errorf("Rename across directories = %v, want fs.ErrInvalid", err)
			}
			assertContent(t, fsys, "a", "x")
		}},
		{"rename-never-moves-or-replaces-a-directory", func(t *testing.T, fsys fsx.FS) {
			mustMkdir(t, fsys, tag, "d")
			mustPut(t, fsys, tag, "a", "x")
			if err := fsys.Rename(tag, "a", "d"); err == nil {
				t.Error("Rename onto a directory succeeded")
			}
			if err := fsys.Rename(tag, "d", "e"); err == nil {
				t.Error("Rename of a directory succeeded; RenameDir is for directories")
			}
			assertNames(t, fsys, ".", "a", "d")
		}},
		{"remove-deletes-a-file-and-reports-a-missing-one", func(t *testing.T, fsys fsx.FS) {
			mustPut(t, fsys, tag, "a", "x")
			if err := fsys.Remove(tag, "a"); err != nil {
				t.Fatal(err)
			}
			if _, err := fsys.ReadFile("a"); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("ReadFile after Remove = %v, want fs.ErrNotExist", err)
			}
			if err := fsys.Remove(tag, "a"); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("Remove of a missing file = %v, want fs.ErrNotExist", err)
			}
		}},
		{"remove-does-not-remove-a-directory", func(t *testing.T, fsys fsx.FS) {
			mustMkdir(t, fsys, tag, "d")
			if err := fsys.Remove(tag, "d"); err == nil {
				t.Error("Remove of a directory succeeded")
			}
			assertNames(t, fsys, ".", "d")
		}},
		{"remove-all-deletes-a-tree-and-accepts-a-missing-path", func(t *testing.T, fsys fsx.FS) {
			mustMkdir(t, fsys, tag, "d")
			mustMkdir(t, fsys, tag, "d/e")
			mustPut(t, fsys, tag, "d/f", "x")
			mustPut(t, fsys, tag, "d/e/g", "y")
			if err := fsys.RemoveAll(tag, "d"); err != nil {
				t.Fatal(err)
			}
			assertNames(t, fsys, ".")
			if err := fsys.RemoveAll(tag, "d"); err != nil {
				t.Errorf("RemoveAll of a missing path = %v, want nil", err)
			}
		}},
		{"mkdir-fails-when-the-name-exists-or-the-parent-is-missing", func(t *testing.T, fsys fsx.FS) {
			mustMkdir(t, fsys, tag, "d")
			if err := fsys.Mkdir(tag, "d"); !errors.Is(err, fs.ErrExist) {
				t.Errorf("Mkdir of an existing directory = %v, want fs.ErrExist", err)
			}
			mustPut(t, fsys, tag, "f", "x")
			if err := fsys.Mkdir(tag, "f"); !errors.Is(err, fs.ErrExist) {
				t.Errorf("Mkdir over a file = %v, want fs.ErrExist", err)
			}
			if err := fsys.Mkdir(tag, "missing/d"); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("Mkdir without a parent = %v, want fs.ErrNotExist", err)
			}
		}},
		{"rename-dir-moves-a-directory-with-its-content", func(t *testing.T, fsys fsx.FS) {
			mustMkdir(t, fsys, tag, "stage")
			mustPut(t, fsys, tag, "stage/f", "x")
			if err := fsys.RenameDir(tag, "stage", "pkg"); err != nil {
				t.Fatal(err)
			}
			assertNames(t, fsys, ".", "pkg")
			assertContent(t, fsys, "pkg/f", "x")
		}},
		{"rename-dir-refuses-an-existing-target", func(t *testing.T, fsys fsx.FS) {
			mustMkdir(t, fsys, tag, "stage")
			mustMkdir(t, fsys, tag, "pkg")
			mustPut(t, fsys, tag, "stage/new", "new")
			mustPut(t, fsys, tag, "pkg/old", "old")
			if err := fsys.RenameDir(tag, "stage", "pkg"); !errors.Is(err, fs.ErrExist) {
				t.Errorf("RenameDir onto an existing directory = %v, want fs.ErrExist", err)
			}
			mustPut(t, fsys, tag, "file", "x")
			if err := fsys.RenameDir(tag, "stage", "file"); !errors.Is(err, fs.ErrExist) {
				t.Errorf("RenameDir onto a file = %v, want fs.ErrExist", err)
			}
			assertContent(t, fsys, "stage/new", "new")
			assertContent(t, fsys, "pkg/old", "old")
		}},
		{"rename-dir-fails-for-a-missing-source-or-a-file", func(t *testing.T, fsys fsx.FS) {
			mustPut(t, fsys, tag, "f", "x")
			if err := fsys.RenameDir(tag, "missing", "x"); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("RenameDir of a missing directory = %v, want fs.ErrNotExist", err)
			}
			if err := fsys.RenameDir(tag, "f", "x"); err == nil {
				t.Error("RenameDir of a file succeeded")
			}
		}},
		{"read-dir-lists-sorted-names-with-their-kind", func(t *testing.T, fsys fsx.FS) {
			mustMkdir(t, fsys, tag, "m")
			mustPut(t, fsys, tag, "z", "x")
			mustPut(t, fsys, tag, "a", "x")
			mustMkdir(t, fsys, tag, "b")
			got, err := fsys.ReadDir(".")
			if err != nil {
				t.Fatal(err)
			}
			want := []fsx.Entry{{Name: "a"}, {Name: "b", IsDir: true}, {Name: "m", IsDir: true}, {Name: "z"}}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("ReadDir = %v, want %v", got, want)
			}
			if _, err := fsys.ReadDir("missing"); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("ReadDir of a missing directory = %v, want fs.ErrNotExist", err)
			}
		}},
		{"stat-reports-kind-and-size-and-does-not-invent-files", func(t *testing.T, fsys fsx.FS) {
			mustPut(t, fsys, tag, "f", "12345")
			mustMkdir(t, fsys, tag, "d")
			if info, err := fsys.Stat("f"); err != nil || info.IsDir || info.Size != 5 || info.Name != "f" {
				t.Errorf("Stat(f) = %+v, %v; want a 5-byte file named f", info, err)
			}
			if info, err := fsys.Stat("d"); err != nil || !info.IsDir || info.Name != "d" {
				t.Errorf("Stat(d) = %+v, %v; want a directory named d", info, err)
			}
			if info, err := fsys.Stat("."); err != nil || !info.IsDir {
				t.Errorf("Stat(root) = %+v, %v; want a directory", info, err)
			}
			if _, err := fsys.Stat("missing"); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("Stat of a missing path = %v, want fs.ErrNotExist", err)
			}
		}},
		{"append-creates-the-file-and-always-writes-at-the-end", func(t *testing.T, fsys fsx.FS) {
			for _, chunk := range []string{"one;", "two;"} {
				f, err := fsys.OpenAppend(tag, "log")
				if err != nil {
					t.Fatal(err)
				}
				mustWrite(t, f, chunk)
				if err := f.Sync(); err != nil {
					t.Fatalf("Sync of an append handle: %v", err)
				}
				if err := f.Close(); err != nil {
					t.Fatal(err)
				}
			}
			f, err := fsys.OpenAppend(tag, "log")
			if err != nil {
				t.Fatal(err)
			}
			mustWrite(t, f, "three;")
			mustWrite(t, f, "four;")
			f.Close()
			assertContent(t, fsys, "log", "one;two;three;four;")
		}},
		{"open-append-of-a-directory-fails", func(t *testing.T, fsys fsx.FS) {
			mustMkdir(t, fsys, tag, "d")
			if f, err := fsys.OpenAppend(tag, "d"); err == nil {
				f.Close()
				t.Error("OpenAppend of a directory succeeded")
			}
		}},
		{"read-file-of-a-directory-or-a-missing-file-fails", func(t *testing.T, fsys fsx.FS) {
			mustMkdir(t, fsys, tag, "d")
			if _, err := fsys.ReadFile("d"); err == nil {
				t.Error("ReadFile of a directory succeeded")
			}
			if _, err := fsys.ReadFile("missing"); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("ReadFile of a missing file = %v, want fs.ErrNotExist", err)
			}
		}},
		{"sync-dir-needs-an-existing-directory", func(t *testing.T, fsys fsx.FS) {
			mustMkdir(t, fsys, tag, "d")
			for _, dir := range []string{".", "d"} {
				if err := fsys.SyncDir(tag, dir); err != nil {
					t.Errorf("SyncDir(%q) = %v", dir, err)
				}
			}
			if err := fsys.SyncDir(tag, "missing"); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("SyncDir of a missing directory = %v, want fs.ErrNotExist", err)
			}
		}},
		{"second-lock-conflicts-until-the-first-is-released", func(t *testing.T, fsys fsx.FS) {
			unlock, err := fsys.Lock("daemon.lock")
			if err != nil {
				t.Fatalf("first Lock: %v", err)
			}
			if _, err := fsys.Lock("daemon.lock"); !errors.Is(err, fsx.ErrLocked) {
				t.Errorf("second Lock = %v, want fsx.ErrLocked", err)
			}
			other, err := fsys.Lock("other.lock")
			if err != nil {
				t.Errorf("Lock of a different file: %v", err)
			} else {
				other()
			}
			if err := unlock(); err != nil {
				t.Fatalf("unlock: %v", err)
			}
			if err := unlock(); err != nil {
				t.Errorf("second unlock = %v, want it to be harmless", err)
			}
			again, err := fsys.Lock("daemon.lock")
			if err != nil {
				t.Fatalf("Lock after release: %v", err)
			}
			again()
		}},
		{"available-reports-space-for-an-existing-directory", func(t *testing.T, fsys fsx.FS) {
			if n, err := fsys.Available("."); err != nil || n == 0 {
				t.Errorf("Available(root) = %d, %v; want a positive number", n, err)
			}
			if _, err := fsys.Available("missing"); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("Available of a missing directory = %v, want fs.ErrNotExist", err)
			}
		}},
		{"invalid-paths-are-refused-by-every-method", func(t *testing.T, fsys fsx.FS) {
			assertRefusedByEveryMethod(t, fsys, tag, "", "/abs", "../up", "a/../b", "a//b", "a/./b", "a/")
		}},
		{"names-with-a-backslash-colon-or-nul-are-refused-by-every-method", func(t *testing.T, fsys fsx.FS) {
			// On Windows a backslash is a separator and a colon names a stream or a
			// drive, so these would reach outside the root or a different file.
			// Every FS refuses them, so the node behaves alike everywhere.
			assertRefusedByEveryMethod(t, fsys, tag, `a\b`, `..\x`, `a:b`, "a/b:c", "a\x00b")
		}},
		{"the-root-cannot-be-removed-renamed-or-created", func(t *testing.T, fsys fsx.FS) {
			if err := fsys.Remove(tag, "."); err == nil {
				t.Error("Remove(root) succeeded")
			}
			if err := fsys.RemoveAll(tag, "."); err == nil {
				t.Error("RemoveAll(root) succeeded")
			}
			if err := fsys.Mkdir(tag, "."); err == nil {
				t.Error("Mkdir(root) succeeded")
			}
			if err := fsys.RenameDir(tag, ".", "x"); err == nil {
				t.Error("RenameDir(root) succeeded")
			}
			if _, err := fsys.Stat("."); err != nil {
				t.Errorf("the root is gone: %v", err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { tc.run(t, newFS(t)) })
	}
}

func second[T any](_ T, err error) error { return err }

func mustTemp(t *testing.T, fsys fsx.FS, tag fsx.Tag, dir string) fsx.File {
	t.Helper()
	f, err := fsys.CreateTemp(tag, dir, fsx.ModePrivate)
	if err != nil {
		t.Fatalf("CreateTemp(%q): %v", dir, err)
	}
	return f
}

func mustWrite(t *testing.T, f fsx.File, s string) {
	t.Helper()
	if n, err := f.Write([]byte(s)); err != nil || n != len(s) {
		t.Fatalf("Write(%q) = %d, %v", s, n, err)
	}
}

func mustMkdir(t *testing.T, fsys fsx.FS, tag fsx.Tag, dir string) {
	t.Helper()
	if err := fsys.Mkdir(tag, dir); err != nil {
		t.Fatalf("Mkdir(%q): %v", dir, err)
	}
}

// mustPut creates a file through the composite operation.
func mustPut(t *testing.T, fsys fsx.FS, tag fsx.Tag, p, content string) {
	t.Helper()
	if err := fsx.Replace(fsys, tag, p, []byte(content), fsx.ModePrivate); err != nil {
		t.Fatalf("Replace(%q): %v", p, err)
	}
}

func assertContent(t *testing.T, fsys fsx.Reader, p, want string) {
	t.Helper()
	got, err := fsys.ReadFile(p)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", p, err)
	}
	if !bytes.Equal(got, []byte(want)) {
		t.Errorf("%s = %q, want %q", p, got, want)
	}
}

func assertNames(t *testing.T, fsys fsx.Reader, dir string, want ...string) {
	t.Helper()
	entries, err := fsys.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", dir, err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name)
	}
	if !reflect.DeepEqual(got, want) && !(len(got) == 0 && len(want) == 0) {
		t.Errorf("%s holds %v, want %v", dir, got, want)
	}
}

// assertRefusedByEveryMethod checks that every method of fsys refuses each path
// with fs.ErrInvalid.
func assertRefusedByEveryMethod(t *testing.T, fsys fsx.FS, tag fsx.Tag, paths ...string) {
	t.Helper()
	for _, p := range paths {
		calls := map[string]error{
			"ReadFile":   second(fsys.ReadFile(p)),
			"ReadDir":    second(fsys.ReadDir(p)),
			"Stat":       second(fsys.Stat(p)),
			"Available":  second(fsys.Available(p)),
			"CreateTemp": second(fsys.CreateTemp(tag, p, fsx.ModePrivate)),
			"OpenAppend": second(fsys.OpenAppend(tag, p)),
			"Rename":     fsys.Rename(tag, p, "x"),
			"SyncDir":    fsys.SyncDir(tag, p),
			"Remove":     fsys.Remove(tag, p),
			"RemoveAll":  fsys.RemoveAll(tag, p),
			"Mkdir":      fsys.Mkdir(tag, p),
			"RenameDir":  fsys.RenameDir(tag, "x", p),
			"Lock":       second(fsys.Lock(p)),
		}
		for name, err := range calls {
			if !errors.Is(err, fs.ErrInvalid) {
				t.Errorf("%s(%q) = %v, want fs.ErrInvalid", name, p, err)
			}
		}
	}
}
