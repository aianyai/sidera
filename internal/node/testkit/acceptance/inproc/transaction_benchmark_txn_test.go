package inproc

import (
	"fmt"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/txn"
)

func transactionBenchmarkTxnCases() []transactionBenchmarkCase {
	return []transactionBenchmarkCase{
		{name: "txn-settle", prepare: prepareTransactionSettle},
		{name: "txn-cleanup", prepare: prepareTransactionCleanup},
	}
}

func prepareTransactionSettle(b *testing.B, observer txn.Observer) transactionBenchmarkFixture {
	fs, w := newTransactionBenchmarkWriter(b, observer)
	if err := fsx.MakeDir(fs, transactionBenchmarkSetupTag, "child"); err != nil {
		b.Fatal(err)
	}
	return transactionBenchmarkFixture{
		invoke: w.Recover,
		verify: func() error {
			info, err := fs.Stat("child")
			if err == nil && !info.IsDir {
				return fmt.Errorf("settle lost its child directory")
			}
			return err
		},
	}
}

func prepareTransactionCleanup(b *testing.B, observer txn.Observer) transactionBenchmarkFixture {
	fs, w := newTransactionBenchmarkWriter(b, observer)
	if err := fsx.Replace(fs, transactionBenchmarkSetupTag, ".tmp-benchmark", []byte("leftover"), fsx.ModePrivate); err != nil {
		b.Fatal(err)
	}
	if err := fsx.MakeDir(fs, transactionBenchmarkSetupTag, ".trash-benchmark"); err != nil {
		b.Fatal(err)
	}
	if err := fsx.Replace(fs, transactionBenchmarkSetupTag, ".trash-benchmark/body", []byte("leftover"), fsx.ModePrivate); err != nil {
		b.Fatal(err)
	}
	return transactionBenchmarkFixture{
		invoke: w.Recover,
		verify: func() error {
			work, err := txn.CleanupWork(fs)
			if err == nil && len(work) != 0 {
				return fmt.Errorf("cleanup retained %d leftovers", len(work))
			}
			return err
		},
	}
}

var transactionBenchmarkSetupTag = fsx.Tag{Tx: "setup", Role: "fixture"}
