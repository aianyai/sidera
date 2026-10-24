package fsx_test

import (
	"errors"
	"io/fs"
	"reflect"
	"syscall"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
)

var tx = fsx.Tag{Tx: "tx", Role: "f"}

// recorded wraps a fresh Mem so that every step is listed in the order it
// reached the file system.
type recorded struct {
	mem   *fsxtest.Mem
	fs    fsx.FS
	steps *[]string
}

func newRecorded() recorded {
	steps := new([]string)
	mem := fsxtest.NewMem()
	fs := fsx.Observe(mem, new(fsx.Counter), func(p fsx.Point) error {
		*steps = append(*steps, p.String())
		return nil
	})
	return recorded{mem: mem, fs: fs, steps: steps}
}

func (r recorded) take() []string {
	got := *r.steps
	*r.steps = nil
	return got
}

func mustWrite(t *testing.T, fs fsx.FS, p, content string) {
	t.Helper()
	if err := fsx.Replace(fs, fsx.Tag{Tx: "setup", Role: "f"}, p, []byte(content), fsx.ModePrivate); err != nil {
		t.Fatalf("setup Replace(%q): %v", p, err)
	}
}

func mustMkdir(t *testing.T, fs fsx.FS, p string) {
	t.Helper()
	if err := fsx.MakeDir(fs, fsx.Tag{Tx: "setup", Role: "d"}, p); err != nil {
		t.Fatalf("setup MakeDir(%q): %v", p, err)
	}
}

func assertSteps(t *testing.T, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("steps = %v\nwant    %v", got, want)
	}
}

// The step sequence of each composite operation is fixed: the crash points
// declared for a transaction are expanded from it.
func TestCompositeOperationsRunTheirFixedSteps(t *testing.T) {
	t.Run("replace", func(t *testing.T) {
		r := newRecorded()
		if err := fsx.Replace(r.fs, tx, "rec", []byte("x"), fsx.ModePrivate); err != nil {
			t.Fatal(err)
		}
		assertSteps(t, r.take(), []string{"tx/f.create#1", "tx/f.write#1", "tx/f.sync#1", "tx/f.rename#1", "tx/f.sync-dir#1"})
	})
	t.Run("append-then-sync-file", func(t *testing.T) {
		r := newRecorded()
		f, err := fsx.OpenAppend(r.fs, tx, "log")
		if err != nil {
			t.Fatal(err)
		}
		for _, rec := range []string{"a\n", "b\n"} {
			if err := fsx.Append(f, tx, []byte(rec)); err != nil {
				t.Fatal(err)
			}
		}
		if err := fsx.SyncFile(r.fs, tx, f); err != nil {
			t.Fatal(err)
		}
		f.Close()
		assertSteps(t, r.take(), []string{"tx/f.open#1", "tx/f.write#1", "tx/f.write#2", "tx/f.sync#1", "tx/f.sync-dir#1"})
	})
	t.Run("rename-file", func(t *testing.T) {
		r := newRecorded()
		mustWrite(t, r.mem, "a", "x")
		if err := fsx.RenameFile(r.fs, tx, "a", "b"); err != nil {
			t.Fatal(err)
		}
		assertSteps(t, r.take(), []string{"tx/f.rename#1", "tx/f.sync-dir#1"})
	})
	t.Run("remove-has-no-directory-sync", func(t *testing.T) {
		r := newRecorded()
		mustWrite(t, r.mem, "a", "x")
		if err := fsx.Remove(r.fs, tx, "a"); err != nil {
			t.Fatal(err)
		}
		assertSteps(t, r.take(), []string{"tx/f.remove#1"})
	})
	t.Run("sync-dir", func(t *testing.T) {
		r := newRecorded()
		if err := fsx.SyncDir(r.fs, tx, "."); err != nil {
			t.Fatal(err)
		}
		assertSteps(t, r.take(), []string{"tx/f.sync-dir#1"})
	})
	t.Run("make-dir", func(t *testing.T) {
		r := newRecorded()
		if err := fsx.MakeDir(r.fs, tx, "d"); err != nil {
			t.Fatal(err)
		}
		assertSteps(t, r.take(), []string{"tx/f.mkdir#1", "tx/f.sync-dir#1"})
	})
	t.Run("rename-dir", func(t *testing.T) {
		r := newRecorded()
		mustMkdir(t, r.mem, "stage")
		if err := fsx.RenameDir(r.fs, tx, "stage", "pkg"); err != nil {
			t.Fatal(err)
		}
		assertSteps(t, r.take(), []string{"tx/f.rename-dir#1", "tx/f.sync-dir#1"})
	})
	t.Run("remove-all", func(t *testing.T) {
		r := newRecorded()
		mustMkdir(t, r.mem, "d")
		if err := fsx.RemoveAll(r.fs, tx, "d"); err != nil {
			t.Fatal(err)
		}
		assertSteps(t, r.take(), []string{"tx/f.remove-all#1"})
	})
	t.Run("discard", func(t *testing.T) {
		r := newRecorded()
		mustMkdir(t, r.mem, "d")
		if err := fsx.Discard(r.fs, tx, "d"); err != nil {
			t.Fatal(err)
		}
		assertSteps(t, r.take(), []string{"tx/f.rename-dir#1", "tx/f.sync-dir#1", "tx/f.remove-all#1"})
	})
}

// The failure class of every step of every composite operation, written down
// independently of the classification table: a failed directory sync and a
// failed sync of an appended file are fatal, every other step is a failure of
// the operation.
func TestFailureClassOfEveryStep(t *testing.T) {
	type operation struct {
		name  string
		steps []fsx.Step
		setup func(t *testing.T, mem *fsxtest.Mem) // state before the operation, built on the bare Mem
		run   func(fs fsx.FS) error
		// appendSync marks the operation whose Sync step is fatal.
		appendSync bool
	}
	ops := []operation{
		{
			name:  "Replace",
			steps: []fsx.Step{fsx.StepCreate, fsx.StepWrite, fsx.StepSync, fsx.StepRename, fsx.StepSyncDir},
			run:   func(fs fsx.FS) error { return fsx.Replace(fs, tx, "rec", []byte("new"), fsx.ModePrivate) },
		},
		{
			name:  "OpenAppend and Append",
			steps: []fsx.Step{fsx.StepOpen, fsx.StepWrite},
			run: func(fs fsx.FS) error {
				f, err := fsx.OpenAppend(fs, tx, "log")
				if err != nil {
					return err
				}
				defer f.Close()
				return fsx.Append(f, tx, []byte("record\n"))
			},
		},
		{
			name:       "SyncFile",
			steps:      []fsx.Step{fsx.StepSync, fsx.StepSyncDir},
			appendSync: true,
			run: func(fs fsx.FS) error {
				f, err := fsx.OpenAppend(fs, tx, "log")
				if err != nil {
					return err
				}
				defer f.Close()
				if err := fsx.Append(f, tx, []byte("record\n")); err != nil {
					return err
				}
				return fsx.SyncFile(fs, tx, f)
			},
		},
		{
			name:  "RenameFile",
			steps: []fsx.Step{fsx.StepRename, fsx.StepSyncDir},
			setup: func(t *testing.T, mem *fsxtest.Mem) { mustWrite(t, mem, "a", "x") },
			run:   func(fs fsx.FS) error { return fsx.RenameFile(fs, tx, "a", "b") },
		},
		{
			name:  "Remove",
			steps: []fsx.Step{fsx.StepRemove},
			setup: func(t *testing.T, mem *fsxtest.Mem) { mustWrite(t, mem, "a", "x") },
			run:   func(fs fsx.FS) error { return fsx.Remove(fs, tx, "a") },
		},
		{
			name:  "SyncDir",
			steps: []fsx.Step{fsx.StepSyncDir},
			run:   func(fs fsx.FS) error { return fsx.SyncDir(fs, tx, ".") },
		},
		{
			name:  "MakeDir",
			steps: []fsx.Step{fsx.StepMkdir, fsx.StepSyncDir},
			run:   func(fs fsx.FS) error { return fsx.MakeDir(fs, tx, "d") },
		},
		{
			name:  "RenameDir",
			steps: []fsx.Step{fsx.StepRenameDir, fsx.StepSyncDir},
			setup: func(t *testing.T, mem *fsxtest.Mem) { mustMkdir(t, mem, "stage") },
			run:   func(fs fsx.FS) error { return fsx.RenameDir(fs, tx, "stage", "pkg") },
		},
		{
			name:  "RemoveAll",
			steps: []fsx.Step{fsx.StepRemoveAll},
			setup: func(t *testing.T, mem *fsxtest.Mem) { mustMkdir(t, mem, "d") },
			run:   func(fs fsx.FS) error { return fsx.RemoveAll(fs, tx, "d") },
		},
		{
			name:  "Discard",
			steps: []fsx.Step{fsx.StepRenameDir, fsx.StepSyncDir, fsx.StepRemoveAll},
			setup: func(t *testing.T, mem *fsxtest.Mem) { mustMkdir(t, mem, "d") },
			run:   func(fs fsx.FS) error { return fsx.Discard(fs, tx, "d") },
		},
	}
	for _, op := range ops {
		for _, step := range op.steps {
			t.Run(op.name+"/"+step.String(), func(t *testing.T) {
				mem := fsxtest.NewMem()
				if op.setup != nil {
					op.setup(t, mem)
				}
				faulty := fsxtest.NewFaulty(t, mem)
				rule := faulty.Fail(fsxtest.Match{Tx: tx.Tx, Role: tx.Role, Step: step, Hit: 1}, syscall.EIO)

				err := op.run(faulty)

				var fe *fsx.Error
				if !errors.As(err, &fe) {
					t.Fatalf("error = %v, want *fsx.Error", err)
				}
				want := fsx.Failed
				if step == fsx.StepSyncDir || (op.appendSync && step == fsx.StepSync) {
					want = fsx.Fatal
				}
				if fe.Class != want {
					t.Errorf("class = %v, want %v", fe.Class, want)
				}
				if fsx.IsFatal(err) != (want == fsx.Fatal) {
					t.Errorf("IsFatal = %v, want %v", fsx.IsFatal(err), want == fsx.Fatal)
				}
				if fe.Point.Step != step || fe.Point.Tag != tx {
					t.Errorf("failed at %v of %v, want %v of %v", fe.Point.Step, fe.Point.Tag, step, tx)
				}
				if !errors.Is(err, syscall.EIO) {
					t.Errorf("error %v does not wrap EIO", err)
				}
				if rule.Hits() != 1 {
					t.Errorf("rule fired %d times, want 1", rule.Hits())
				}
			})
		}
	}
}

func TestReplaceLeavesNoTemporaryAndKeepsTheTargetWhenAnyStepBeforeTheRenameFails(t *testing.T) {
	for _, step := range []fsx.Step{fsx.StepWrite, fsx.StepSync, fsx.StepRename} {
		t.Run(step.String(), func(t *testing.T) {
			mem := fsxtest.NewMem()
			mustWrite(t, mem, "rec", "old")
			faulty := fsxtest.NewFaulty(t, mem)
			faulty.Fail(fsxtest.Match{Tx: tx.Tx, Step: step}, syscall.ENOSPC)

			err := fsx.Replace(faulty, tx, "rec", []byte("new"), fsx.ModePrivate)

			if err == nil || fsx.IsFatal(err) {
				t.Fatalf("error = %v, want a non-fatal failure", err)
			}
			if got, _ := mem.ReadFile("rec"); string(got) != "old" {
				t.Errorf("target = %q, want it unchanged", got)
			}
			if left := listTemporary(t, mem, "."); len(left) != 0 {
				t.Errorf("temporary files left: %v", left)
			}
		})
	}
	t.Run("short-write", func(t *testing.T) {
		mem := fsxtest.NewMem()
		faulty := fsxtest.NewFaulty(t, mem)
		faulty.FailShortWrite(fsxtest.Match{Tx: tx.Tx, Step: fsx.StepWrite}, 3, syscall.ENOSPC)
		err := fsx.Replace(faulty, tx, "rec", []byte("0123456789"), fsx.ModePrivate)
		if err == nil || fsx.IsFatal(err) || !errors.Is(err, syscall.ENOSPC) {
			t.Fatalf("error = %v, want a non-fatal ENOSPC failure", err)
		}
		if _, err := mem.Stat("rec"); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("target exists after a failed first write: %v", err)
		}
		if left := listTemporary(t, mem, "."); len(left) != 0 {
			t.Errorf("temporary files left: %v", left)
		}
	})
}

// The removal of a temporary file after a failure is not a step: it is not
// numbered, no observer hears about it, and a rule for "any remove" does not
// fire on it. Whether it succeeds changes nothing about the error returned.
func TestCleanupAfterAFailureIsNotAStep(t *testing.T) {
	var steps []string
	mem := fsxtest.NewMem()
	faulty := fsxtest.NewFaulty(t, mem)
	faulty.Fail(fsxtest.Match{Tx: tx.Tx, Step: fsx.StepSync}, syscall.EIO)
	cleanup := faulty.Fail(fsxtest.Match{Tx: fsx.TxCleanup, Step: fsx.StepRemove}, syscall.EIO)
	observed := fsx.Observe(faulty, new(fsx.Counter), func(p fsx.Point) error {
		steps = append(steps, p.String())
		return nil
	})

	err := fsx.Replace(observed, tx, "rec", []byte("new"), fsx.ModePrivate)

	var fe *fsx.Error
	if !errors.As(err, &fe) || fe.Point.Step != fsx.StepSync || fe.Class != fsx.Failed {
		t.Fatalf("error = %v, want the failed sync of the temporary file", err)
	}
	assertSteps(t, steps, []string{"tx/f.create#1", "tx/f.write#1", "tx/f.sync#1"})
	if cleanup.Hits() != 1 {
		t.Errorf("cleanup removal reached the file system %d times, want 1", cleanup.Hits())
	}
	// The removal failed, so the leftover stays for the next start to collect.
	left, err := fsx.Leftovers(mem, ".")
	if err != nil || len(left) != 1 {
		t.Errorf("Leftovers = %v, %v; want the one temporary file", left, err)
	}
}

func TestAppendShortWriteIsAFailureThatLeavesAPartialTail(t *testing.T) {
	mem := fsxtest.NewMem()
	faulty := fsxtest.NewFaulty(t, mem)
	f, err := fsx.OpenAppend(faulty, tx, "log")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := fsx.Append(f, tx, []byte("whole\n")); err != nil {
		t.Fatal(err)
	}
	faulty.FailShortWrite(fsxtest.Match{Tx: tx.Tx, Step: fsx.StepWrite, Hit: 2}, 4, syscall.ENOSPC)

	err = fsx.Append(f, tx, []byte("second record\n"))

	var fe *fsx.Error
	if !errors.As(err, &fe) || fe.Class != fsx.Failed || !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("error = %v, want a non-fatal ENOSPC failure", err)
	}
	if got, _ := mem.ReadFile("log"); string(got) != "whole\nseco" {
		t.Errorf("file = %q, want the first record and four bytes of the second", got)
	}
}

func TestInvalidPathsFailTheOperationBeforeAnyStep(t *testing.T) {
	r := newRecorded()
	paths := []string{"", "/abs", "../up", "a/../b", "a//b", "a/./b", "a/"}
	for _, p := range paths {
		for name, call := range map[string]func() error{
			"Replace":    func() error { return fsx.Replace(r.fs, tx, p, nil, fsx.ModePrivate) },
			"OpenAppend": func() error { _, err := fsx.OpenAppend(r.fs, tx, p); return err },
			"Remove":     func() error { return fsx.Remove(r.fs, tx, p) },
			"SyncDir":    func() error { return fsx.SyncDir(r.fs, tx, p) },
			"MakeDir":    func() error { return fsx.MakeDir(r.fs, tx, p) },
			"RemoveAll":  func() error { return fsx.RemoveAll(r.fs, tx, p) },
			"Discard":    func() error { return fsx.Discard(r.fs, tx, p) },
			"RenameFile": func() error { return fsx.RenameFile(r.fs, tx, p, "x") },
			"RenameDir":  func() error { return fsx.RenameDir(r.fs, tx, "x", p) },
		} {
			err := call()
			var fe *fsx.Error
			if !errors.As(err, &fe) || fe.Class != fsx.Failed || !errors.Is(err, fs.ErrInvalid) {
				t.Errorf("%s(%q) = %v, want a non-fatal failure wrapping fs.ErrInvalid", name, p, err)
			}
		}
	}
	assertSteps(t, r.take(), nil)
}

// A backslash separates path elements on Windows, a colon names an alternate
// data stream or a drive, and NUL ends a name: a path holding one would reach a
// different file on Windows than the same path means everywhere else. The rule
// is the same on every platform, so a node's behaviour does not depend on it.
func TestPathElementsNeverHoldABackslashColonOrNul(t *testing.T) {
	for _, p := range []string{`a\b`, `..\x`, `a:b`, "a/b:c", "a\x00b", `C:\x`, `\\host\share`, "file:stream"} {
		if err := fsx.CheckPath(p); !errors.Is(err, fs.ErrInvalid) {
			t.Errorf("CheckPath(%q) = %v, want fs.ErrInvalid", p, err)
		}
		if err := fsx.CheckRename(p, "x"); !errors.Is(err, fs.ErrInvalid) {
			t.Errorf("CheckRename(%q, x) = %v, want fs.ErrInvalid", p, err)
		}
		r := newRecorded()
		if err := fsx.Replace(r.fs, tx, p, nil, fsx.ModePrivate); !errors.Is(err, fs.ErrInvalid) {
			t.Errorf("Replace(%q) = %v, want fs.ErrInvalid", p, err)
		}
		assertSteps(t, r.take(), nil)
	}
	for _, p := range []string{"a", "a.b", "a-b_c/d", ".hidden", "a b"} {
		if err := fsx.CheckPath(p); err != nil {
			t.Errorf("CheckPath(%q) = %v, want it accepted", p, err)
		}
	}
}

func TestTheRootIsNotTheSubjectOfAWriteOperation(t *testing.T) {
	r := newRecorded()
	for name, call := range map[string]func() error{
		"Replace":    func() error { return fsx.Replace(r.fs, tx, ".", nil, fsx.ModePrivate) },
		"Remove":     func() error { return fsx.Remove(r.fs, tx, ".") },
		"MakeDir":    func() error { return fsx.MakeDir(r.fs, tx, ".") },
		"RemoveAll":  func() error { return fsx.RemoveAll(r.fs, tx, ".") },
		"Discard":    func() error { return fsx.Discard(r.fs, tx, ".") },
		"RenameDir":  func() error { return fsx.RenameDir(r.fs, tx, ".", "x") },
		"RenameFile": func() error { return fsx.RenameFile(r.fs, tx, "x", ".") },
	} {
		if err := call(); !errors.Is(err, fs.ErrInvalid) {
			t.Errorf("%s(root) = %v, want fs.ErrInvalid", name, err)
		}
	}
	assertSteps(t, r.take(), nil)
	// Syncing the root is how a new top-level directory becomes durable.
	if err := fsx.SyncDir(r.fs, tx, "."); err != nil {
		t.Errorf("SyncDir(root) = %v", err)
	}
}

func TestRenamesAcrossDirectoriesAreRefused(t *testing.T) {
	r := newRecorded()
	mustMkdir(t, r.mem, "a")
	mustMkdir(t, r.mem, "b")
	mustWrite(t, r.mem, "a/f", "x")
	if err := fsx.RenameFile(r.fs, tx, "a/f", "b/f"); !errors.Is(err, fs.ErrInvalid) {
		t.Errorf("RenameFile across directories = %v, want fs.ErrInvalid", err)
	}
	if err := fsx.RenameDir(r.fs, tx, "a", "b/a"); !errors.Is(err, fs.ErrInvalid) {
		t.Errorf("RenameDir across directories = %v, want fs.ErrInvalid", err)
	}
	assertSteps(t, r.take(), nil)
}

func TestRenameDirRefusesAnExistingTargetAndChangesNothing(t *testing.T) {
	mem := fsxtest.NewMem()
	mustMkdir(t, mem, "stage")
	mustMkdir(t, mem, "pkg")
	mustWrite(t, mem, "stage/new", "new")
	mustWrite(t, mem, "pkg/old", "old")

	err := fsx.RenameDir(mem, tx, "stage", "pkg")

	var fe *fsx.Error
	if !errors.As(err, &fe) || fe.Class != fsx.Failed || !errors.Is(err, fs.ErrExist) {
		t.Fatalf("error = %v, want a non-fatal failure wrapping fs.ErrExist", err)
	}
	if got, _ := mem.ReadFile("stage/new"); string(got) != "new" {
		t.Errorf("the staging directory changed: %q", got)
	}
	if got, _ := mem.ReadFile("pkg/old"); string(got) != "old" {
		t.Errorf("the target directory changed: %q", got)
	}
}

func TestDiscardRenamesToAGarbageNameBeforeDeleting(t *testing.T) {
	var cloneAtRemoveAll *fsxtest.Mem
	mem := fsxtest.NewMem()
	mustMkdir(t, mem, "pkg")
	mustWrite(t, mem, "pkg/a", "a")
	mustWrite(t, mem, "pkg/b", "b")
	observed := fsx.Observe(mem, new(fsx.Counter), func(p fsx.Point) error {
		if p.Step == fsx.StepRemoveAll {
			cloneAtRemoveAll = mem.Clone() // a process crash right before the deletion
		}
		return nil
	})

	if err := fsx.Discard(observed, tx, "pkg"); err != nil {
		t.Fatal(err)
	}

	if cloneAtRemoveAll == nil {
		t.Fatal("the remove-all step was never reached")
	}
	entries, _ := cloneAtRemoveAll.ReadDir(".")
	if !reflect.DeepEqual(entries, []fsx.Entry{{Name: ".trash-1", IsDir: true}}) {
		t.Errorf("before the deletion the root holds %v, want only .trash-1", entries)
	}
	if got, _ := cloneAtRemoveAll.ReadFile(".trash-1/a"); string(got) != "a" {
		t.Errorf("the content is not under the garbage name: %q", got)
	}
	if entries, _ := mem.ReadDir("."); len(entries) != 0 {
		t.Errorf("after Discard the root holds %v, want nothing", entries)
	}
}

func TestDiscardNeverReusesALeftoverGarbageName(t *testing.T) {
	mem := fsxtest.NewMem()
	mustMkdir(t, mem, ".trash-1")
	mustMkdir(t, mem, ".trash-2")
	mustMkdir(t, mem, "pkg")
	var renamedTo string
	observed := fsx.Observe(mem, new(fsx.Counter), func(p fsx.Point) error {
		if p.Step == fsx.StepSyncDir {
			entries, _ := mem.ReadDir(".")
			for _, e := range entries {
				if e.Name == ".trash-3" {
					renamedTo = e.Name
				}
			}
		}
		return nil
	})
	if err := fsx.Discard(observed, tx, "pkg"); err != nil {
		t.Fatal(err)
	}
	if renamedTo != ".trash-3" {
		t.Errorf("garbage name = %q, want .trash-3 (the first unused)", renamedTo)
	}
}

func TestDiscardOfAMissingDirectoryFailsWithoutSideEffects(t *testing.T) {
	r := newRecorded()
	err := fsx.Discard(r.fs, tx, "missing")
	var fe *fsx.Error
	if !errors.As(err, &fe) || fe.Class != fsx.Failed || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("error = %v, want a non-fatal failure wrapping fs.ErrNotExist", err)
	}
	if entries, _ := r.mem.ReadDir("."); len(entries) != 0 {
		t.Errorf("the root holds %v, want nothing", entries)
	}
}

// Every durable operation ends durable: the worst power loss, which keeps only
// what was synced, still shows its effect.
func TestEachCompositeOperationIsDurableWhenItReturns(t *testing.T) {
	check := func(t *testing.T, mem *fsxtest.Mem, want map[string]string, absent ...string) {
		t.Helper()
		crashed := mem.CrashCloneWorst()
		for p, content := range want {
			if got, err := crashed.ReadFile(p); err != nil || string(got) != content {
				t.Errorf("after power loss %s = %q, %v; want %q", p, got, err, content)
			}
		}
		for _, p := range absent {
			if _, err := crashed.Stat(p); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("after power loss %s exists: %v", p, err)
			}
		}
	}
	t.Run("Replace", func(t *testing.T) {
		mem := fsxtest.NewMem()
		mustWrite(t, mem, "rec", "old")
		if err := fsx.Replace(mem, tx, "rec", []byte("new"), fsx.ModePrivate); err != nil {
			t.Fatal(err)
		}
		check(t, mem, map[string]string{"rec": "new"})
	})
	t.Run("SyncFile", func(t *testing.T) {
		mem := fsxtest.NewMem()
		f, err := fsx.OpenAppend(mem, tx, "log")
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := fsx.Append(f, tx, []byte("a\n")); err != nil {
			t.Fatal(err)
		}
		if err := fsx.SyncFile(mem, tx, f); err != nil {
			t.Fatal(err)
		}
		check(t, mem, map[string]string{"log": "a\n"})
	})
	t.Run("RenameFile", func(t *testing.T) {
		mem := fsxtest.NewMem()
		mustWrite(t, mem, "a", "x")
		if err := fsx.RenameFile(mem, tx, "a", "b"); err != nil {
			t.Fatal(err)
		}
		check(t, mem, map[string]string{"b": "x"}, "a")
	})
	t.Run("MakeDir", func(t *testing.T) {
		mem := fsxtest.NewMem()
		if err := fsx.MakeDir(mem, tx, "d"); err != nil {
			t.Fatal(err)
		}
		if info, err := mem.CrashCloneWorst().Stat("d"); err != nil || !info.IsDir {
			t.Errorf("after power loss d = %+v, %v; want a directory", info, err)
		}
	})
	t.Run("RenameDir", func(t *testing.T) {
		mem := fsxtest.NewMem()
		mustMkdir(t, mem, "stage")
		mustWrite(t, mem, "stage/f", "x")
		if err := fsx.RenameDir(mem, tx, "stage", "pkg"); err != nil {
			t.Fatal(err)
		}
		check(t, mem, map[string]string{"pkg/f": "x"}, "stage")
	})
	t.Run("Remove-then-SyncDir", func(t *testing.T) {
		mem := fsxtest.NewMem()
		mustWrite(t, mem, "a", "x")
		if err := fsx.Remove(mem, tx, "a"); err != nil {
			t.Fatal(err)
		}
		check(t, mem, map[string]string{"a": "x"}) // removal alone is not durable
		if err := fsx.SyncDir(mem, tx, "."); err != nil {
			t.Fatal(err)
		}
		check(t, mem, nil, "a")
	})
	t.Run("Discard", func(t *testing.T) {
		mem := fsxtest.NewMem()
		mustMkdir(t, mem, "pkg")
		mustWrite(t, mem, "pkg/f", "x")
		if err := fsx.Discard(mem, tx, "pkg"); err != nil {
			t.Fatal(err)
		}
		// The rename is durable, so the original name is gone; the deletion of
		// the garbage-named tree is not, and may be incomplete.
		check(t, mem, nil, "pkg")
	})
}

// The file's Write and Sync steps are attributed to the tag the file was opened
// with, and SyncFile's directory sync carries the tag it is given, which the
// transaction layer makes the same tag. Both steps of SyncFile therefore appear
// under the role that appended.
func TestSyncFileStepsCarryTheTagTheFileWasOpenedWith(t *testing.T) {
	r := newRecorded()
	appendTag := fsx.Tag{Tx: "intake", Role: "events"}
	f, err := fsx.OpenAppend(r.fs, appendTag, "log")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := fsx.Append(f, appendTag, []byte("record\n")); err != nil {
		t.Fatal(err)
	}
	r.take()

	if err := fsx.SyncFile(r.fs, appendTag, f); err != nil {
		t.Fatal(err)
	}

	assertSteps(t, r.take(), []string{"intake/events.sync#1", "intake/events.sync-dir#1"})
}
