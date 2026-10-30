package inproc

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path"
	"testing"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/keys"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/txn"
)

func transactionBenchmarkEngineCases() []transactionBenchmarkCase {
	return []transactionBenchmarkCase{
		{name: "direct-register", prepare: prepareTransactionDirectRegister},
		{name: "runtime-admit", prepare: prepareTransactionRuntimeAdmit},
		{name: "clock-clear", prepare: prepareTransactionClockClear},
		{name: "backup-confirm", prepare: prepareTransactionBackupConfirm},
		{name: "identity-retire", prepare: prepareTransactionIdentityRetire},
		{name: "adapter-install", prepare: func(b *testing.B, o txn.Observer) transactionBenchmarkFixture {
			return prepareTransactionEngineInstall(b, o, false)
		}},
		{name: "adapter-install-current", prepare: func(b *testing.B, o txn.Observer) transactionBenchmarkFixture {
			return prepareTransactionEngineInstall(b, o, true)
		}},
	}
}

func prepareTransactionDirectRegister(b *testing.B, observer txn.Observer) transactionBenchmarkFixture {
	h := harness.New(b, harness.Options{FS: harness.RealFS, Observer: observer})
	var address string
	return transactionBenchmarkFixture{
		invoke: func() error {
			var err error
			address, err = h.Direct("benchmark")
			return err
		},
		verify: func() error {
			addr, err := ident.ParseAddress(address)
			if err != nil {
				return err
			}
			ids, err := transactionBenchmarkEngineIDs(h)
			if err != nil {
				return err
			}
			if ids.Direct["benchmark"] != addr.Endpoint {
				return fmt.Errorf("registered endpoint has no matching persistent stable id")
			}
			return transactionBenchmarkEngineEndpoint(h, addr.Endpoint, adapterreg.KindDirect)
		},
		cleanup: h.Stop,
	}
}

func prepareTransactionRuntimeAdmit(b *testing.B, observer txn.Observer) transactionBenchmarkFixture {
	// The actual host admits one runtime-capable adapter during the first boot.
	h := harness.NewStopped(b, harness.Options{FS: harness.RealFS, Observer: observer, Adapter: &harness.AdapterOptions{Runtime: true}})
	return transactionBenchmarkFixture{
		invoke: func() error {
			_, err := h.TryStart(h.Ctx(), harness.StartOptions{})
			return err
		},
		verify: func() error {
			ids, err := transactionBenchmarkEngineIDs(h)
			if err != nil {
				return err
			}
			if ids.Runtime.IsZero() {
				return fmt.Errorf("runtime admission persisted no stable id")
			}
			return transactionBenchmarkEngineEndpoint(h, ids.Runtime, adapterreg.KindRuntime)
		},
		cleanup: h.Stop,
	}
}

func transactionBenchmarkEngineIDs(h *harness.H) (config.EndpointIDs, error) {
	raw, err := h.Faults().ReadFile(config.EndpointIDsPath(harness.Instance))
	if err != nil {
		return config.EndpointIDs{}, err
	}
	return config.DecodeEndpointIDs(harness.Instance, raw)
}

func transactionBenchmarkEngineEndpoint(h *harness.H, id ident.EndpointID, kind adapterreg.EndpointKind) error {
	if err := h.Quiesce(); err != nil {
		return err
	}
	scan, err := h.Records()
	if err != nil {
		return err
	}
	if len(scan.Endpoints) != 1 {
		return fmt.Errorf("registration persisted %d endpoints, want one", len(scan.Endpoints))
	}
	ep := scan.Endpoints[0]
	if ep.ID != id || ep.Kind != kind || ep.Instance != harness.Instance || ep.Status != store.EndpointOpen || len(ep.Token) != store.TokenLen {
		return fmt.Errorf("registration persisted an unexpected endpoint record")
	}
	return nil
}

func transactionBenchmarkEngineCommand(id byte, name string, op engine.Operation) engine.ManagementCommand {
	return engine.ManagementCommand{
		Key:         admin.KeyFor(admin.LocalOwner(), admin.OperationID{231, id}),
		Fingerprint: sha256.Sum256([]byte(name)), Name: name, Operation: op,
	}
}

// The result is checked by scanning the actual record file, independently of
// the engine's in-memory replay cache. Each operation also checks its own effect.
func transactionBenchmarkEngineOperation(h *harness.H, cmd engine.ManagementCommand, effect func() error) transactionBenchmarkFixture {
	var result engine.StoredResult
	return transactionBenchmarkFixture{
		invoke: func() error {
			var err error
			result, err = h.Engine().Execute(h.Ctx(), cmd)
			return err
		},
		verify: func() error {
			if result.Replayed || len(result.Result) == 0 {
				return fmt.Errorf("management operation returned no new result")
			}
			scan, err := h.Records()
			if err != nil {
				return err
			}
			for _, rec := range scan.Admin {
				if rec.Key == cmd.Key && rec.Fingerprint == cmd.Fingerprint && rec.Op == cmd.Name && bytes.Equal(rec.Result, result.Result) {
					return effect()
				}
			}
			return fmt.Errorf("management operation has no matching persistent result")
		},
		cleanup: h.Stop,
	}
}

func prepareTransactionClockClear(b *testing.B, observer txn.Observer) transactionBenchmarkFixture {
	h := harness.New(b, harness.Options{FS: harness.RealFS, Observer: observer, RollbackPaused: true})
	cmd := transactionBenchmarkEngineCommand(1, "clock.clear-rollback", engine.ClearClockRollbackOp(func(v engine.ClockClear) ([]byte, error) { return json.Marshal(v) }))
	return transactionBenchmarkEngineOperation(h, cmd, func() error {
		clock, err := keys.ReadClock(h.Faults())
		if err == nil && (clock.Paused() || !clock.HighWater.Equal(h.Clock().Now())) {
			return fmt.Errorf("clock clear did not persist the unpaused current time")
		}
		return err
	})
}

func prepareTransactionBackupConfirm(b *testing.B, observer txn.Observer) transactionBenchmarkFixture {
	h := harness.New(b, harness.Options{FS: harness.RealFS, Observer: observer})
	exported, err := h.Engine().ExportRevocation(h.Ctx())
	if err != nil {
		b.Fatal(err)
	}
	cmd := transactionBenchmarkEngineCommand(1, "identity.confirm-backup", engine.ConfirmRevocationBackupOp(exported.Digest, func(v engine.BackupConfirmed) ([]byte, error) { return json.Marshal(v) }))
	return transactionBenchmarkEngineOperation(h, cmd, func() error {
		raw, err := h.Faults().ReadFile(config.BackupPath)
		if err != nil {
			return err
		}
		backup, err := config.DecodeBackup(raw)
		if err == nil && (!backup.Confirms(exported.Digest) || !backup.ConfirmedAt.Equal(h.Clock().Now())) {
			return fmt.Errorf("backup confirmation persisted a different statement or time")
		}
		return err
	})
}

func prepareTransactionIdentityRetire(b *testing.B, observer txn.Observer) transactionBenchmarkFixture {
	h := harness.New(b, harness.Options{FS: harness.RealFS, Observer: observer})
	exported, err := h.Engine().ExportRevocation(h.Ctx())
	if err != nil {
		b.Fatal(err)
	}
	cmd := transactionBenchmarkEngineCommand(1, "identity.retire", engine.RetireIdentityOp(harness.NodeID(), func() ([]byte, error) { return []byte(`{"retired":true}`), nil }))
	return transactionBenchmarkEngineOperation(h, cmd, func() error {
		name := config.RevocationPath(exported.Digest)
		raw, err := h.Faults().ReadFile(name)
		if err != nil {
			return err
		}
		rev, rec, err := config.DecodeRevocation(path.Base(name), raw)
		if err == nil && (rev.Node() != harness.NodeID() || rec.Digest() != exported.Digest) {
			return fmt.Errorf("retirement persisted a different self-revocation")
		}
		return err
	})
}

func prepareTransactionEngineInstall(b *testing.B, observer txn.Observer, current bool) transactionBenchmarkFixture {
	const version = "0.1.0"
	source := transactionBenchmarkPackage(b)
	h := harness.New(b, harness.Options{FS: harness.RealFS, Observer: observer, OpenPackage: func(string) (fsx.Reader, error) { return source, nil }})
	var installed engine.AdapterInstalled
	command := func(id byte) engine.ManagementCommand {
		return transactionBenchmarkEngineCommand(id, "adapters.install", engine.InstallAdapterOp("benchmark-package", func(v engine.AdapterInstalled) ([]byte, error) {
			installed = v
			return json.Marshal(v)
		}))
	}
	if current {
		if _, err := h.Engine().Execute(h.Ctx(), command(1)); err != nil {
			b.Fatal(err)
		}
	}
	return transactionBenchmarkEngineOperation(h, command(2), func() error {
		if installed.ID != "benchmark" || installed.Version != version || installed.AlreadyCurrent != current {
			return fmt.Errorf("installation returned an unexpected package or current state: %+v", installed)
		}
		raw, err := h.Faults().ReadFile(config.AdapterCurrentPath("benchmark"))
		if err != nil {
			return err
		}
		gotVersion, err := config.DecodeAdapterCurrent("benchmark", raw)
		if err != nil {
			return err
		}
		if gotVersion != version {
			return fmt.Errorf("installed version = %s, want %s", gotVersion, version)
		}
		program, err := h.Faults().ReadFile(path.Join(config.AdapterVersionDir("benchmark", version), "adapter"))
		if err == nil && !bytes.Equal(program, bytes.Repeat([]byte{'x'}, 128)) {
			return fmt.Errorf("installed program differs from its 128-byte source")
		}
		return err
	})
}
