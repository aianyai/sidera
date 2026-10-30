package inproc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/aianyai/sidera/internal/node/clock/clocktest"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/daemon"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/keys"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/txn"
)

func transactionBenchmarkDaemonCases() []transactionBenchmarkCase {
	return []transactionBenchmarkCase{
		{name: "init", prepare: prepareTransactionDaemonInit},
		{name: "layout", prepare: prepareTransactionDaemonLayout},
	}
}

func prepareTransactionDaemonInit(b *testing.B, observer txn.Observer) transactionBenchmarkFixture {
	// Init validates socket paths even though it starts no listeners. Omit the
	// benchmark name from the temporary path so Unix socket limits still fit.
	parent, err := os.MkdirTemp("", "tb")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { os.RemoveAll(parent) })
	home := filepath.Join(parent, "h")
	if err := fsx.MakeRoot(home); err != nil {
		b.Fatal(err)
	}
	fs, err := fsx.OpenOS(home, harness.RandReader(200))
	if err != nil {
		b.Fatal(err)
	}
	cfg := daemon.Config{Home: home, FS: fs, Observer: observer, Clock: clocktest.NewFake(harness.DefaultStart), Rand: harness.RandReader(201)}
	var report daemon.InitReport
	return transactionBenchmarkFixture{
		invoke: func() error {
			var err error
			report, err = daemon.Init(context.Background(), cfg)
			return err
		},
		verify: func() error {
			done, err := keys.Initialized(fs)
			if err != nil {
				return err
			}
			if !done || report.Node.IsZero() {
				return fmt.Errorf("initialization persisted no completed node identity")
			}
			table, err := daemon.Table()
			if err != nil {
				return err
			}
			w := txn.New(fs, table, nil)
			if err := w.Recover(); err != nil {
				return err
			}
			key, err := keys.Load(w)
			if err != nil {
				return err
			}
			if key.Node() != report.Node {
				return fmt.Errorf("initialization report differs from the persisted identity")
			}
			if _, err := config.Load(w); err != nil {
				return err
			}
			work, err := daemon.LayoutWork(fs)
			if err == nil && len(work) != 0 {
				return fmt.Errorf("initialization left %d missing layout directories", len(work))
			}
			return err
		},
	}
}

func prepareTransactionDaemonLayout(b *testing.B, observer txn.Observer) transactionBenchmarkFixture {
	fs, w, _, _ := newTransactionBenchmarkNode(b, observer)
	if err := fsx.RemoveAll(fs, transactionBenchmarkSetupTag, "logs"); err != nil {
		b.Fatal(err)
	}
	return transactionBenchmarkFixture{
		invoke: func() error { return daemon.RepairLayout(w) },
		verify: func() error {
			work, err := daemon.LayoutWork(fs)
			if err == nil && len(work) != 0 {
				return fmt.Errorf("layout recovery left %d missing directories", len(work))
			}
			return err
		},
	}
}
