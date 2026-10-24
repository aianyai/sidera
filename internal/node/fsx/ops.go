package fsx

import (
	"fmt"
	"io"
	"path"
)

// The composite durable operations. Each is a fixed sequence of FS steps, the
// same on every FS, and each failure carries its class from the table in
// classify.go. A call rejected before any step ran (an invalid path, a missing
// directory to look in) is a failure of the operation, reported against the
// step that would have run first.

// cleanupTag is the tag of failure-path cleanup. It is a function so that no
// package-level value can be reassigned to change what cleanup is attributed to.
func cleanupTag() Tag { return Tag{Tx: TxCleanup} }

// Replace atomically replaces the file at path with data: create a temporary
// file next to it, write, sync, rename over the target, sync the directory.
// A failure before the rename removes the temporary file and leaves the target
// as it was. After the rename the outcome is unknown and the error is Fatal.
func Replace(fs FS, tag Tag, p string, data []byte, mode Mode) error {
	if err := checkNotRoot(p); err != nil {
		return reject(tag, StepCreate, p, err)
	}
	dir := path.Dir(p)
	f, err := fs.CreateTemp(tag, dir, mode)
	if err != nil {
		return fail(opReplace, tag, StepCreate, p, err)
	}
	tmp := f.Name()
	abandon := func(step Step, err error) error {
		f.Close()
		// The leftover is removed at the next start in any case; a failure here
		// changes nothing.
		fs.Remove(cleanupTag(), tmp)
		return fail(opReplace, tag, step, p, err)
	}
	if err := writeAll(f, data); err != nil {
		return abandon(StepWrite, err)
	}
	if err := f.Sync(); err != nil {
		return abandon(StepSync, err)
	}
	// Close before the rename: Windows cannot rename an open file. A close error
	// after a successful sync means the descriptor was not released cleanly.
	if err := f.Close(); err != nil {
		fs.Remove(cleanupTag(), tmp)
		return fail(opReplace, tag, StepSync, p, err)
	}
	if err := fs.Rename(tag, tmp, p); err != nil {
		fs.Remove(cleanupTag(), tmp)
		return fail(opReplace, tag, StepRename, p, err)
	}
	if err := fs.SyncDir(tag, dir); err != nil {
		return fail(opReplace, tag, StepSyncDir, p, err)
	}
	return nil
}

// OpenAppend opens a file for appending, creating it if absent. Keep the
// handle: SyncFile must sync the handle that did the appending, because a
// write-back error is reported only to descriptors that were open when it
// happened.
func OpenAppend(fs FS, tag Tag, p string) (File, error) {
	if err := checkNotRoot(p); err != nil {
		return nil, reject(tag, StepOpen, p, err)
	}
	f, err := fs.OpenAppend(tag, p)
	if err != nil {
		return nil, fail(opAppend, tag, StepOpen, p, err)
	}
	return f, nil
}

// Append writes data to a handle from OpenAppend without syncing it. A short
// write can leave half a record at the end of the file, so appended formats
// delimit and checksum their records and readers skip a damaged tail.
func Append(f File, tag Tag, data []byte) error {
	if err := writeAll(f, data); err != nil {
		return fail(opAppend, tag, StepWrite, f.Name(), err)
	}
	return nil
}

// SyncFile makes everything appended through f durable: sync the handle, then
// sync its directory so a newly created file's name is durable too. Both
// failures are fatal: the data is visible to this process, but whether it is
// on disk is unknown.
func SyncFile(fs FS, tag Tag, f File) error {
	if err := f.Sync(); err != nil {
		return fail(opSyncFile, tag, StepSync, f.Name(), err)
	}
	if err := fs.SyncDir(tag, path.Dir(f.Name())); err != nil {
		return fail(opSyncFile, tag, StepSyncDir, f.Name(), err)
	}
	return nil
}

// RenameFile renames a file within its directory, replacing the target, and
// syncs the directory.
func RenameFile(fs FS, tag Tag, from, to string) error {
	if err := CheckRename(from, to); err != nil {
		return reject(tag, StepRename, from, err)
	}
	if err := fs.Rename(tag, from, to); err != nil {
		return fail(opRenameFile, tag, StepRename, from, err)
	}
	if err := fs.SyncDir(tag, path.Dir(to)); err != nil {
		return fail(opRenameFile, tag, StepSyncDir, to, err)
	}
	return nil
}

// Remove unlinks a file. It does not sync the directory: a batch of removals
// is followed by one SyncDir.
func Remove(fs FS, tag Tag, p string) error {
	if err := checkNotRoot(p); err != nil {
		return reject(tag, StepRemove, p, err)
	}
	if err := fs.Remove(tag, p); err != nil {
		return fail(opRemove, tag, StepRemove, p, err)
	}
	return nil
}

// SyncDir flushes a directory, typically after a batch of Remove calls.
func SyncDir(fs FS, tag Tag, dir string) error {
	if err := CheckPath(dir); err != nil {
		return reject(tag, StepSyncDir, dir, err)
	}
	if err := fs.SyncDir(tag, dir); err != nil {
		return fail(opSyncDir, tag, StepSyncDir, dir, err)
	}
	return nil
}

// MakeDir creates a directory and syncs its parent, so the new name is durable.
func MakeDir(fs FS, tag Tag, dir string) error {
	if err := checkNotRoot(dir); err != nil {
		return reject(tag, StepMkdir, dir, err)
	}
	if err := fs.Mkdir(tag, dir); err != nil {
		return fail(opMakeDir, tag, StepMkdir, dir, err)
	}
	if err := fs.SyncDir(tag, path.Dir(dir)); err != nil {
		return fail(opMakeDir, tag, StepSyncDir, dir, err)
	}
	return nil
}

// RenameDir moves a directory within its parent so that its whole content
// appears at once, and syncs the parent. The target must not exist.
func RenameDir(fs FS, tag Tag, from, to string) error {
	if err := CheckRename(from, to); err != nil {
		return reject(tag, StepRenameDir, from, err)
	}
	if err := fs.RenameDir(tag, from, to); err != nil {
		return fail(opRenameDir, tag, StepRenameDir, from, err)
	}
	if err := fs.SyncDir(tag, path.Dir(to)); err != nil {
		return fail(opRenameDir, tag, StepSyncDir, to, err)
	}
	return nil
}

// RemoveAll removes a directory tree. A crash can leave any subset removed, so
// it is meant for directories that were first renamed to a garbage name (see
// Discard).
func RemoveAll(fs FS, tag Tag, dir string) error {
	if err := checkNotRoot(dir); err != nil {
		return reject(tag, StepRemoveAll, dir, err)
	}
	if err := fs.RemoveAll(tag, dir); err != nil {
		return fail(opRemoveAll, tag, StepRemoveAll, dir, err)
	}
	return nil
}

// Discard deletes a directory tree: rename it to a ".trash-" name in the same
// parent, sync the parent, remove the tree. A crash while removing leaves a
// half-deleted directory, but only under a garbage name that startup removes.
func Discard(fs FS, tag Tag, dir string) error {
	if err := checkNotRoot(dir); err != nil {
		return reject(tag, StepRenameDir, dir, err)
	}
	trash, err := freeTrashName(fs, dir)
	if err != nil {
		return reject(tag, StepRenameDir, dir, err)
	}
	if err := fs.RenameDir(tag, dir, trash); err != nil {
		return fail(opDiscard, tag, StepRenameDir, dir, err)
	}
	if err := fs.SyncDir(tag, path.Dir(trash)); err != nil {
		return fail(opDiscard, tag, StepSyncDir, trash, err)
	}
	if err := fs.RemoveAll(tag, trash); err != nil {
		return fail(opDiscard, tag, StepRemoveAll, trash, err)
	}
	return nil
}

// freeTrashName picks ".trash-<hex>" in dir's parent: the smallest number
// whose name is unused, so names are deterministic and a leftover from an
// earlier crash is never reused. Callers hold the instance lock, so nothing
// else creates names meanwhile.
func freeTrashName(fs FS, dir string) (string, error) {
	parent := path.Dir(dir)
	entries, err := fs.ReadDir(parent)
	if err != nil {
		return "", err
	}
	used := make(map[string]bool, len(entries))
	for _, e := range entries {
		used[e.Name] = true
	}
	for n := 1; ; n++ {
		name := fmt.Sprintf("%s%x", TrashPrefix, n)
		if !used[name] {
			return path.Join(parent, name), nil
		}
	}
}

func writeAll(w io.Writer, data []byte) error {
	n, err := w.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	return err
}
