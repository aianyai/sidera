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

// Internal returns the declarations that belong to this package. The
// composition root puts them in front of the declarations of every owner when
// it builds the table, so Writer.Recover has its transaction.
func Internal() []*Decl {
	return []*Decl{{
		Name: cleanupName,
		Kind: Recovery,
		Writes: []Write{
			{Role: "tmp", Op: OpRemove, Many: true, Optional: true},
			{Role: "trash", Op: OpRemoveAll, Many: true, Optional: true},
			{Role: "dir", Op: OpSyncDir, Many: true, Optional: true},
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

// Recover removes every leftover of an interrupted operation: `.tmp-*` files
// and `.trash-*` directories, anywhere under the data directory. A node calls it
// at startup, right after it takes the instance lock and before it loads
// anything, so nothing is writing. It does not replay anything: what a half
// finished transaction left behind is for the package that owns those files to
// repair.
//
// The removal is one Recovery transaction, all removals first and the
// directory syncs after them. When there is nothing to remove it does nothing
// and reaches no point, so a start on a clean disk is not a recovery. The table
// must hold the cleanup declaration even then: a composition root that left out
// Internal() fails at its first start, not at the first crash.
func (w *Writer) Recover() error {
	c, ok := w.table.byName[cleanupName]
	if !ok {
		panic("txn: the table has no " + cleanupName + " transaction; build it from txn.Internal() and the declarations of the owners")
	}
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
