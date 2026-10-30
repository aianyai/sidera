package inproc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"testing"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/txn"
)

func transactionBenchmarkConfigCases() []transactionBenchmarkCase {
	return []transactionBenchmarkCase{
		{name: "node-set", prepare: func(b *testing.B, observer txn.Observer) transactionBenchmarkFixture {
			_, w, cfg, _ := newTransactionBenchmarkNode(b, observer)
			req := transactionBenchmarkRequest(cfg.Node().Version)
			return transactionBenchmarkFixture{
				invoke: func() error {
					_, err := cfg.SetNodeSettings(req, []byte(`{"reply_permission":false}`), transactionBenchmarkResult[config.Node])
					return err
				},
				verify: func() error {
					loaded, err := config.Load(w)
					if err == nil && (loaded.Node().Settings.ReplyPermission || loaded.Node().Version != req.Expected+1) {
						return fmt.Errorf("node setting was not persisted")
					}
					return err
				},
			}
		}},
		{name: "presets-change", prepare: func(b *testing.B, observer txn.Observer) transactionBenchmarkFixture {
			_, w, cfg, _ := newTransactionBenchmarkNode(b, observer)
			req := transactionBenchmarkRequest(cfg.Presets().Version)
			return transactionBenchmarkFixture{
				invoke: func() error {
					_, err := cfg.AddPreset(req, "bench", []byte(`{"local_sources":"deny","description":"Benchmark preset."}`), transactionBenchmarkResult[config.Presets])
					return err
				},
				verify: func() error {
					loaded, err := config.Load(w)
					if err == nil {
						if _, ok := loaded.Presets().Items["bench"]; !ok {
							return fmt.Errorf("preset was not persisted")
						}
					}
					return err
				},
			}
		}},
		{name: "endpoint-set", prepare: prepareTransactionEndpointSet},
		{name: "adapter-enable", prepare: prepareTransactionAdapterEnable},
		{name: "adapter-disable", prepare: prepareTransactionAdapterDisable},
		{name: "adapter-stage", prepare: prepareTransactionAdapterStage},
		{name: "adapter-unstage", prepare: prepareTransactionAdapterUnstage},
		{name: "adapter-gc", prepare: prepareTransactionAdapterGC},
	}
}

func transactionBenchmarkRequest(version admin.Version) config.Request {
	return config.Request{Key: admin.KeyFor(admin.LocalOwner(), admin.OperationID{42}), Expected: version}
}

func transactionBenchmarkResult[T any](T) (json.RawMessage, error) {
	return json.RawMessage(`{"changed":true}`), nil
}

func prepareTransactionEndpointSet(b *testing.B, observer txn.Observer) transactionBenchmarkFixture {
	_, w, cfg, _ := newTransactionBenchmarkNode(b, observer)
	endpoint, err := ident.NewEndpointID(harness.RandReader(66))
	if err != nil {
		b.Fatal(err)
	}
	// A nil OpenEndpoints is the configuration owner's existing API for a
	// caller without the engine's live-endpoint view.
	req := transactionBenchmarkRequest(cfg.Instances().Version)
	return transactionBenchmarkFixture{
		invoke: func() error {
			_, err := cfg.SetEndpointSettings(req, harness.Instance, endpoint, []byte(`{"backlog_limit":17}`), nil, transactionBenchmarkResult[config.Instances])
			return err
		},
		verify: func() error {
			loaded, err := config.Load(w)
			if err == nil && loaded.Instances().Items[harness.Instance].Endpoints[endpoint].BacklogLimit != 17 {
				return fmt.Errorf("endpoint setting was not persisted")
			}
			return err
		},
	}
}

func prepareTransactionAdapterEnable(b *testing.B, observer txn.Observer) transactionBenchmarkFixture {
	_, w, cfg, _ := newTransactionBenchmarkNode(b, observer)
	if _, err := cfg.DisableAdapter(transactionBenchmarkRequest(cfg.Instances().Version), harness.Instance, nil, transactionBenchmarkResult[config.Instances]); err != nil {
		b.Fatal(err)
	}
	req := transactionBenchmarkRequest(cfg.Instances().Version)
	return transactionBenchmarkFixture{
		invoke: func() error {
			_, err := cfg.EnableAdapter(req, harness.Instance, harness.Instance, nil, nil, transactionBenchmarkResult[config.Instances])
			return err
		},
		verify: func() error { return transactionBenchmarkAdapterEnabled(w, true) },
	}
}

func prepareTransactionAdapterDisable(b *testing.B, observer txn.Observer) transactionBenchmarkFixture {
	_, w, cfg, _ := newTransactionBenchmarkNode(b, observer)
	req := transactionBenchmarkRequest(cfg.Instances().Version)
	return transactionBenchmarkFixture{
		invoke: func() error {
			_, err := cfg.DisableAdapter(req, harness.Instance, nil, transactionBenchmarkResult[config.Instances])
			return err
		},
		verify: func() error { return transactionBenchmarkAdapterEnabled(w, false) },
	}
}

func transactionBenchmarkAdapterEnabled(w *txn.Writer, enabled bool) error {
	cfg, err := config.Load(w)
	if err == nil && cfg.Instances().Items[harness.Instance].Enabled != enabled {
		return fmt.Errorf("adapter enabled state was not persisted")
	}
	return err
}

// The source is a real directory with one manifest and one 128-byte program.
// Only the owner's copy to the node disk is timed, never source preparation.
func transactionBenchmarkPackage(b *testing.B) fsx.Reader {
	b.Helper()
	src, _ := newTransactionBenchmarkWriter(b, nil)
	options := fakeadapter.DefaultManifestOptions()
	options.ID, options.Program = "benchmark", "adapter"
	manifest, err := options.Encode()
	if err != nil {
		b.Fatal(err)
	}
	for name, data := range map[string][]byte{"sidera-adapter.json": manifest, "adapter": bytes.Repeat([]byte{'x'}, 128)} {
		if err := fsx.Replace(src, transactionBenchmarkSetupTag, name, data, fsx.ModePrivate); err != nil {
			b.Fatal(err)
		}
	}
	return src
}

func prepareTransactionAdapterStage(b *testing.B, observer txn.Observer) transactionBenchmarkFixture {
	fs, _, cfg, _ := newTransactionBenchmarkNode(b, observer)
	source := transactionBenchmarkPackage(b)
	var staged config.Staged
	return transactionBenchmarkFixture{
		invoke: func() error {
			var err error
			staged, err = cfg.StageAdapter(source, harness.RandReader(99))
			return err
		},
		verify: func() error {
			data, err := fs.ReadFile(path.Join(staged.Dir, "adapter"))
			if err == nil && !bytes.Equal(data, bytes.Repeat([]byte{'x'}, 128)) {
				return fmt.Errorf("staged program differs from source")
			}
			return err
		},
		cleanup: func() {
			if err := cfg.Unstage(staged); err != nil {
				b.Error(err)
			}
		},
	}
}

func transactionBenchmarkStaged(b *testing.B, cfg *config.Config) config.Staged {
	b.Helper()
	staged, err := cfg.StageAdapter(transactionBenchmarkPackage(b), harness.RandReader(99))
	if err != nil {
		b.Fatal(err)
	}
	return staged
}

func prepareTransactionAdapterUnstage(b *testing.B, observer txn.Observer) transactionBenchmarkFixture {
	fs, _, cfg, _ := newTransactionBenchmarkNode(b, observer)
	staged := transactionBenchmarkStaged(b, cfg)
	return transactionBenchmarkFixture{
		invoke: func() error { return cfg.Unstage(staged) },
		verify: func() error {
			exists, err := fsx.Exists(fs, config.AdapterDir(staged.ID))
			if err == nil && exists {
				return fmt.Errorf("unstage retained the uninstalled adapter directory")
			}
			return err
		},
	}
}

func prepareTransactionAdapterGC(b *testing.B, observer txn.Observer) transactionBenchmarkFixture {
	fs, w, cfg, _ := newTransactionBenchmarkNode(b, observer)
	transactionBenchmarkStaged(b, cfg)
	return transactionBenchmarkFixture{
		invoke: func() error { _, err := config.Load(w); return err },
		verify: func() error {
			work, err := config.AdapterGCWork(fs)
			if err == nil && len(work) != 0 {
				return fmt.Errorf("adapter recovery retained %d garbage directories", len(work))
			}
			return err
		},
	}
}
