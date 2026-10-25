package daemon

import (
	"slices"

	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/keys"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/txn"
)

// Directories of the layout that no owner declares: where the sockets and the logs
// live. The directories of keys/, config/ and state/ are the owners'.
const (
	runDir    = "run"
	manageDir = "manage"
	logsDir   = "logs"
)

// layoutDirs are the directories that the layout rule makes again when one is
// missing: state/ and the places of the sockets and the logs. A start has no
// right to make keys/ or config/ again: a missing one is the owner removing what
// init made, and refusing the start naming it is what keeps a retired identity
// from coming back.
func layoutDirs() []string {
	return append(slices.Clone(store.StateDirs()), runDir, manageDir, logsDir)
}

// initDirs are the directories init makes that config does not: its own are the
// work of its plan.
func initDirs() []string { return append([]string{keys.Dir}, layoutDirs()...) }

// roleDir is the role under which init and the layout rule make directories, the
// one that config declares for its own.
const roleDir = config.RoleDir

var (
	// declInit is the transaction of init: the directories, the keys, then the
	// configuration, and the marker that says it finished, last. Every file write
	// is optional because init reuses what is valid; the writes of config and keys
	// are the ones those packages declare, composed here because only the composing
	// package can declare a transaction of two owners.
	declInit = txn.Decl{Name: "init", Kind: txn.Normal, Writes: slices.Concat(
		config.InitWrites()[:1], // the directories, under roleDir
		keys.InitWrites(),
		config.InitWrites()[1:],
		[]txn.Write{keys.InitMarkerWrite()},
	)}

	// declLayout makes the directories of the layout that are missing. It is a
	// recovery rule of the start: it has work only where the owner removed state/
	// or a socket or log directory, and after an init or a repair it has none.
	declLayout = txn.Decl{Name: "layout", Kind: txn.Recovery, Writes: []txn.Write{
		{Role: roleDir, Op: txn.OpMakeDir, Many: true, Optional: true},
	}}
)

// Transactions returns the declarations of the transactions of this package.
func Transactions() []*txn.Decl { return []*txn.Decl{&declInit, &declLayout} }

// Declarations are the declarations of the whole program: the transaction layer's
// own and every owner's, in one list, from which the one table is built.
func Declarations() []*txn.Decl {
	var out []*txn.Decl
	out = append(out, txn.Internal()...)
	out = append(out, store.Transactions()...)
	out = append(out, config.Transactions()...)
	out = append(out, keys.Transactions()...)
	out = append(out, engine.Transactions()...)
	out = append(out, Transactions()...)
	return out
}

// Table validates the declarations of the program and builds the table that the
// transaction writer and the crash tests are made from.
func Table() (*txn.Table, error) { return txn.NewTable(Declarations()...) }

// LayoutWork lists the directories of the layout that are missing, a parent
// before its children: the work of the layout rule as a pure function of the
// disk, which a test of a clean run reads too.
func LayoutWork(r fsx.Reader) ([]string, error) {
	var missing []string
	for _, d := range layoutDirs() {
		ok, err := fsx.Exists(r, d)
		if err != nil {
			return nil, err
		}
		if !ok {
			missing = append(missing, d)
		}
	}
	return missing, nil
}

// RepairLayout is the layout rule: it makes the directories that LayoutWork
// finds, in one transaction. With nothing to do it writes nothing and begins no
// transaction.
func RepairLayout(w *txn.Writer) error {
	work, err := LayoutWork(w.Reader())
	if err != nil || len(work) == 0 {
		return err
	}
	tx := w.Begin(&declLayout)
	defer tx.Close()
	for _, d := range work {
		if err := tx.MakeDir(roleDir, d); err != nil {
			return err
		}
	}
	return tx.Commit()
}
