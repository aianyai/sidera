package txn

import (
	"errors"
	"fmt"
	"path"

	"github.com/aianyai/sidera/internal/node/fsx"
)

// Tx is one open transaction. It is used by one goroutine. A caller
// acknowledges nothing, and updates nothing in memory, before Commit returns
// nil, and does not read back a path it wrote in the same Tx.
//
// Every call is checked against the declaration, and a call the declaration
// does not allow panics: it is a programming error, found the first time it
// runs. The first failed write is sticky: from then on every call, Commit
// included, returns that error and runs no step.
type Tx struct {
	w *Writer
	d *compiled

	cur       int // index of the declared write made last, -1 before the first
	err       error
	committed bool // the commit step of a write that is not preparatory has completed
	ended     bool

	// A group of ReplaceDefer is pending, in this directory, until the SyncDir
	// of it.
	pending    string
	hasPending bool

	files map[string]*appendFile // paths appended in this Tx, open
}

// appendFile is a file appended in this Tx: the handle every append and the
// sync go through, and the role its steps are attributed to.
type appendFile struct {
	f    fsx.File
	role string
}

func (t *Tx) panicf(format string, args ...any) {
	panic(fmt.Sprintf("txn: %s: ", t.d.name) + fmt.Sprintf(format, args...))
}

// next matches a call against the declaration and moves past the write it
// belongs to. A call belongs to the write made last again when that one is
// Many, or to a later write when everything declared between is Optional.
func (t *Tx) next(role string, op Op) {
	ws := t.d.writes
	if t.cur >= 0 && ws[t.cur].Many && ws[t.cur].Role == role && ws[t.cur].Op == op {
		return
	}
	for j := t.cur + 1; j < len(ws); j++ {
		if ws[j].Role == role && ws[j].Op == op {
			t.cur = j
			return
		}
		if !ws[j].Optional {
			break
		}
	}
	declared, other := false, false
	for _, w := range ws {
		if w.Role == role {
			declared = true
			other = other || w.Op != op
		}
	}
	switch {
	case !declared:
		t.panicf("role %q is not declared", role)
	case other && !t.matchesAnywhere(role, op):
		t.panicf("role %q is declared as another operation than %s", role, opName(op))
	}
	t.panicf("%s %q is out of order: the declaration allows it neither again nor next (last made: %s)", opName(op), role, t.lastMade())
}

func (t *Tx) matchesAnywhere(role string, op Op) bool {
	for _, w := range t.d.writes {
		if w.Role == role && w.Op == op {
			return true
		}
	}
	return false
}

func (t *Tx) lastMade() string {
	if t.cur < 0 {
		return "nothing yet"
	}
	return fmt.Sprintf("%q", t.d.writes[t.cur].Role)
}

func opName(op Op) string {
	names := [...]string{"", "Replace", "ReplaceDefer", "Append", "SyncFile", "RenameFile", "Remove", "SyncDir", "MakeDir", "RenameDir", "RemoveAll", "Discard"}
	if int(op) < len(names) {
		return names[op]
	}
	return fmt.Sprintf("Op(%d)", op)
}

// checkPending enforces that the files of a deferred group are made durable
// before anything else is written: the group is one directory and the next
// write is the SyncDir of it. Were another write to come first, a power loss
// could keep that write and lose the group it was meant to follow.
func (t *Tx) checkPending(op Op, p string) {
	switch {
	case op == OpReplaceDefer:
		if dir := path.Dir(p); t.hasPending && dir != t.pending {
			t.panicf("a deferred group may use one directory only, a second directory %q follows %q", dir, t.pending)
		}
		t.pending, t.hasPending = path.Dir(p), true
	case op == OpSyncDir && t.hasPending:
		if p != t.pending {
			t.panicf("SyncDir of directory %q while the deferred group of directory %q is not synced: the next write after a deferred group must be the SyncDir of its directory", p, t.pending)
		}
		t.hasPending = false
	case t.hasPending:
		t.panicf("%s while the deferred group of directory %q is not synced: the next write after a deferred group must be the SyncDir of its directory", opName(op), t.pending)
	}
}

func (t *Tx) mustBeOpen() {
	if t.ended {
		t.panicf("the Tx has ended: Commit or Close was already called")
	}
}

// run is the shape of every write: check, run the operation, classify a
// failure, remember a commit.
func (t *Tx) run(role string, op Op, p string, do func(tag fsx.Tag) error) error {
	t.mustBeOpen()
	if t.err != nil {
		return t.err
	}
	t.next(role, op)
	t.checkPending(op, p)
	// A preparatory write never commits: what it leaves is garbage until a
	// write that is not preparatory has done its part.
	prepare := t.d.writes[t.cur].Prepare
	if err := do(fsx.Tag{Tx: t.d.name, Role: role}); err != nil {
		return t.fail(op, prepare, err)
	}
	if !prepare && commitStepOf(op) != 0 {
		t.committed = true
	}
	return nil
}

// fail records the first error. The class is the file system layer's, raised
// to Fatal once a commit step has completed: from then on the disk is in one
// of the intermediate states that replay is designed for, while memory still
// shows the state before the transaction, and carrying on would write on a
// view that is wrong. This is the one place that rule is applied.
func (t *Tx) fail(op Op, prepare bool, err error) error {
	e := &Error{Tx: t.d.name, Class: fsx.Fatal, Err: err}
	var fe *fsx.Error
	if errors.As(err, &fe) {
		e.Point, e.Path, e.Err, e.Class = fe.Point, fe.Path, fe.Err, fe.Class
		if t.committed || (!prepare && commitStepCompleted(op, fe.Point.Step)) {
			e.Class = fsx.Fatal
		}
	}
	t.err = e
	return e
}

// Replace atomically replaces the file at path.
func (t *Tx) Replace(role, path string, data []byte, mode fsx.Mode) error {
	return t.run(role, OpReplace, path, func(tag fsx.Tag) error {
		return fsx.Replace(t.w.fs, tag, path, data, mode)
	})
}

// ReplaceDefer replaces the file at path like Replace, without syncing its
// directory. The files of a group are all in one directory, and the next write
// is the SyncDir of it.
func (t *Tx) ReplaceDefer(role, path string, data []byte, mode fsx.Mode) error {
	return t.run(role, OpReplaceDefer, path, func(tag fsx.Tag) error {
		return fsx.ReplaceDeferred(t.w.fs, tag, path, data, mode)
	})
}

// Append appends to the file at path, creating it. The Tx opens the file at the
// first append to a path and keeps it open until Commit or Close, because the
// sync must go through the handle that appended. A failed append closes the
// handle at once: it may have left a partial record, the Tx is failed and
// writes nothing more, and the next Tx opens the file afresh and so starts its
// first record after whatever is there.
func (t *Tx) Append(role, p string, data []byte) error {
	return t.run(role, OpAppend, p, func(tag fsx.Tag) error {
		af, ok := t.files[p]
		if ok && af.role != role {
			t.panicf("%q is appended under role %q, so it cannot be appended under role %q: the steps of a file are attributed to the role it was opened under", p, af.role, role)
		}
		if !ok {
			f, err := fsx.OpenAppend(t.w.fs, tag, p)
			if err != nil {
				return err
			}
			af = &appendFile{f: f, role: role}
			t.files[p] = af
		}
		if err := fsx.Append(af.f, tag, data); err != nil {
			t.drop(p)
			return err
		}
		return nil
	})
}

// SyncFile makes what was appended to path in this Tx durable, through the
// handle that appended it. The role is the role of the Append.
func (t *Tx) SyncFile(role, p string) error {
	return t.run(role, OpSyncFile, p, func(tag fsx.Tag) error {
		af, ok := t.files[p]
		if !ok {
			t.panicf("SyncFile of %q, which was not appended in this transaction", p)
		}
		if af.role != role {
			t.panicf("%q was appended under role %q, so SyncFile must use that role, not %q", p, af.role, role)
		}
		if err := fsx.SyncFile(t.w.fs, tag, af.f); err != nil {
			t.drop(p)
			return err
		}
		return nil
	})
}

// drop closes and forgets the handle of an appended file. A close error is
// ignored: the file is closed because the write or sync on it failed, and that
// failure is the one reported.
func (t *Tx) drop(p string) {
	if af, ok := t.files[p]; ok {
		af.f.Close()
		delete(t.files, p)
	}
}

// RenameFile renames a file within its directory and syncs the directory.
func (t *Tx) RenameFile(role, from, to string) error {
	return t.run(role, OpRenameFile, from, func(tag fsx.Tag) error {
		return fsx.RenameFile(t.w.fs, tag, from, to)
	})
}

// Remove removes a file. A batch of removals is followed by a SyncDir.
func (t *Tx) Remove(role, path string) error {
	return t.run(role, OpRemove, path, func(tag fsx.Tag) error {
		return fsx.Remove(t.w.fs, tag, path)
	})
}

// SyncDir syncs a directory: after a group of ReplaceDefer or a batch of
// Remove.
func (t *Tx) SyncDir(role, dir string) error {
	return t.run(role, OpSyncDir, dir, func(tag fsx.Tag) error {
		return fsx.SyncDir(t.w.fs, tag, dir)
	})
}

// MakeDir creates a directory and syncs its parent.
func (t *Tx) MakeDir(role, dir string) error {
	return t.run(role, OpMakeDir, dir, func(tag fsx.Tag) error {
		return fsx.MakeDir(t.w.fs, tag, dir)
	})
}

// RenameDir moves a directory within its parent, which must not hold the
// target, and syncs the parent.
func (t *Tx) RenameDir(role, from, to string) error {
	return t.run(role, OpRenameDir, from, func(tag fsx.Tag) error {
		return fsx.RenameDir(t.w.fs, tag, from, to)
	})
}

// RemoveAll removes a directory tree that was already renamed to a garbage
// name.
func (t *Tx) RemoveAll(role, dir string) error {
	return t.run(role, OpRemoveAll, dir, func(tag fsx.Tag) error {
		return fsx.RemoveAll(t.w.fs, tag, dir)
	})
}

// Discard deletes a directory tree: rename to a garbage name, sync, remove.
func (t *Tx) Discard(role, dir string) error {
	return t.run(role, OpDiscard, dir, func(tag fsx.Tag) error {
		return fsx.Discard(t.w.fs, tag, dir)
	})
}

// Commit ends the transaction. If a write failed it returns that error;
// otherwise it checks that the declaration is complete, reaches the commit
// point, and returns nil: only now may the caller acknowledge or update memory.
func (t *Tx) Commit() error {
	t.mustBeOpen()
	defer t.end()
	if t.err != nil {
		return t.err
	}
	for _, w := range t.d.writes[t.cur+1:] {
		if !w.Optional {
			t.panicf("Commit of an incomplete transaction: the write %q is required and was not made", w.Role)
		}
	}
	if t.hasPending {
		t.panicf("Commit while the deferred group of directory %q is not synced: the next write after a deferred group must be the SyncDir of its directory", t.pending)
	}
	t.w.commitPoint(t.d.name)
	return nil
}

// Close ends the transaction if it has not ended. It is meant for
// `defer tx.Close()` and does nothing after Commit. A transaction closed
// without a commit leaves what its writes did: the layer has no rollback, and
// the declaration says which writes are preparatory leftovers.
func (t *Tx) Close() {
	if !t.ended {
		t.end()
	}
}

// end releases the handles and the declaration. A close error is ignored: a
// file synced through its handle is already durable, and one that was not is
// not promised to be.
func (t *Tx) end() {
	t.ended = true
	for p := range t.files {
		t.drop(p)
	}
	t.w.release(t.d)
}
