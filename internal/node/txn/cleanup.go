package txn

import (
	"fmt"
	"path"
	"sort"

	"github.com/aianyai/sidera/internal/node/fsx"
)

// cleanupName is the transaction that removes the debris of interrupted
// operations. Its roles: tmp removes leftover files, trash removes leftover
// directories, dir syncs each directory that held one.
const cleanupName = "txn-cleanup"

// settleName is the transaction that makes the directory tree durable at
// every start. Its one role, dir, syncs a directory.
const settleName = "txn-settle"

// Internal returns the declarations that belong to this package. The
// composition root puts them in front of the declarations of every owner when
// it builds the table, so Writer.Recover has its transactions. The cleanup
// transaction repairs a crash and is of the Recovery kind; the settle
// transaction runs at every start, so it is Normal: a clean start is not a
// recovery.
func Internal() []*Decl {
	return []*Decl{{
		Name: cleanupName,
		Kind: Recovery,
		Writes: []Write{
			{Role: "tmp", Op: OpRemove, Many: true, Optional: true},
			{Role: "trash", Op: OpRemoveAll, Many: true, Optional: true},
			{Role: "dir", Op: OpSyncDir, Many: true, Optional: true},
		},
	}, {
		Name: settleName,
		Kind: Normal,
		Writes: []Write{
			{Role: "dir", Op: OpSyncDir, Many: true},
		},
	}}
}

// CleanupWork is what the cleanup transaction has to do: every temporary file
// and every discarded directory under the data directory, in a stable order.
// It only reads. Recover executes exactly this list, and a test evaluates it on
// a copy of the disk: after a run without a crash, damage or injected fault,
// the list is empty at every quiescent point, because a temporary name exists
// only while a replacement is under way and a garbage name only while a
// directory is being discarded.
func CleanupWork(r fsx.Reader) ([]fsx.Leftover, error) {
	return fsx.Leftovers(r, ".")
}

// Recover is the first thing a node does to its data directory after it takes
// the instance lock and before it loads anything, so nothing is writing. It
// does two things, in this order.
//
// First it removes every leftover of an interrupted operation: `.tmp-*` files
// and `.trash-*` directories, anywhere under the data directory. That is one
// Recovery transaction, all removals first and the directory syncs after them;
// with nothing to remove it does nothing and reaches no point, so a start on a
// clean disk is not a recovery. It does not replay anything: what a half
// finished transaction left behind is for the package that owns those files to
// repair.
//
// Then it makes every directory durable, in one Normal transaction that syncs
// each directory under the data directory, also on a clean disk. After a
// process crash a rename or a removal can be visible and not yet durable, and
// the recovery of an owner reads existence and acts on it durably: it deletes
// what a pointer or a reference no longer names. A power loss after that could
// bring the old pointer or reference back to something that is gone. Making
// what is visible durable first means every owner's recovery predicate runs
// against a state that cannot be taken back. The cost is one directory sync per
// directory at every start.
//
// The table must hold both declarations even on a clean disk: a composition
// root that left out Internal() fails at its first start, not at the first
// crash.
func (w *Writer) Recover() error {
	cleanup := w.internal(cleanupName)
	settle := w.internal(settleName)
	if err := w.cleanup(cleanup); err != nil {
		return err
	}
	return w.settle(settle)
}

func (w *Writer) internal(name string) *compiled {
	c, ok := w.table.byName[name]
	if !ok {
		panic("txn: the table has no " + name + " transaction; build it from txn.Internal() and the declarations of the owners")
	}
	return c
}

// cleanup removes the leftovers CleanupWork lists, files first, then
// directories, then syncs each directory that held one.
func (w *Writer) cleanup(c *compiled) error {
	work, err := CleanupWork(w.base)
	if err != nil {
		return fmt.Errorf("txn: cleanup: %w", err)
	}
	if len(work) == 0 {
		return nil
	}
	tx := w.begin(c)
	defer tx.Close()

	var files, dirs, parents []string
	seen := map[string]bool{}
	for _, l := range work {
		if l.IsDir {
			dirs = append(dirs, l.Path)
		} else {
			files = append(files, l.Path)
		}
		if parent := path.Dir(l.Path); !seen[parent] {
			seen[parent] = true
			parents = append(parents, parent)
		}
	}
	sort.Strings(parents)
	for _, p := range files {
		if err := tx.Remove("tmp", p); err != nil {
			return err
		}
	}
	for _, p := range dirs {
		if err := tx.RemoveAll("trash", p); err != nil {
			return err
		}
	}
	for _, p := range parents {
		if err := tx.SyncDir("dir", p); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// settle syncs every directory under the data directory, as the tree is after
// the cleanup: a directory the cleanup removed is not there to sync.
//
// No order is needed for durability. A directory sync makes durable the entries
// of that directory and no others, and every rename stays inside one directory,
// so no directory's durability depends on another's. The order is parents before
// their children, names sorted, only so that the numbering of crash points does
// not vary between runs.
func (w *Writer) settle(c *compiled) error {
	dirs, err := directories(w.base)
	if err != nil {
		return fmt.Errorf("txn: settle: %w", err)
	}
	tx := w.begin(c)
	defer tx.Close()
	for _, d := range dirs {
		if err := tx.SyncDir("dir", d); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// directories lists the data directory root and every directory under it,
// each directory before what it contains, names sorted.
func directories(r fsx.Reader) ([]string, error) {
	dirs := []string{"."}
	var walk func(dir string) error
	walk = func(dir string) error {
		entries, err := r.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if !e.IsDir {
				continue
			}
			p := path.Join(dir, e.Name)
			dirs = append(dirs, p)
			if err := walk(p); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk("."); err != nil {
		return nil, err
	}
	return dirs, nil
}
