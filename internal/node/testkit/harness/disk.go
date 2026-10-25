package harness

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"math/rand/v2"
	"path"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/txn"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/manifest"
)

// DefaultStart is the origin of the test clock unless a test gives another.
var DefaultStart = time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)

// Instance is the adapter instance whose direct endpoints the fixture registers.
// It is installed and enabled when the node is made.
const Instance = "chat"

// NodeID is the identifier of the fixture's node.
func NodeID() ident.NodeID {
	n, err := ident.NodeIDFromBytes(bytes.Repeat([]byte{7}, ident.NodeIDLen))
	if err != nil {
		panic("harness: " + err.Error())
	}
	return n
}

// The building blocks below make the disk of a node and a process over it. The
// fixture uses them, and so does a test of a package that needs the pieces and
// not the whole fixture, so that the way a node is made is written once.

const manifestText = `{"manifest_version":1,"id":"chat","version":"1.0.0","protocol":{"min":1,"max":1},` +
	`"entry":{"program":"bin/sidera-adapter-chat"},"platforms":[{"os":"linux","arch":"amd64"}]}`

// ManifestText is the manifest of the adapter the fixture installs.
func ManifestText() string { return manifestText }

// setupTag tags the writes that lay out a disk before any process mounts it.
var setupTag = fsx.Tag{Tx: "setup", Role: "f"}

// The transactions that the composition root and the install flow own, declared
// here the way they declare them, so that the table of the program is complete.
var (
	initDecl = &txn.Decl{Name: "init", Kind: txn.Normal, Writes: append(config.InitWrites(),
		txn.Write{Role: "marker", Op: txn.OpReplace})}
	installDecl = &txn.Decl{Name: "adapter-install", Kind: txn.Normal, Writes: append(config.InstallWrites(),
		txn.Write{Role: store.RoleResult, Op: txn.OpReplace}, config.InstallOldWrite())}
)

// ProgramTable is the table of every transaction of the program: the internal
// ones, those of the store, the configuration and the engine, and the two that
// the composition root owns.
func ProgramTable(t testing.TB) *txn.Table {
	t.Helper()
	decls := append(txn.Internal(), store.Transactions()...)
	decls = append(decls, config.Transactions()...)
	decls = append(decls, engine.Transactions()...)
	decls = append(decls, initDecl, installDecl)
	tab, err := txn.NewTable(decls...)
	if err != nil {
		t.Fatalf("harness: NewTable: %v", err)
	}
	return tab
}

func mustMkdir(t testing.TB, mem *fsxtest.Mem, dir string) {
	t.Helper()
	if dir == "." {
		return
	}
	if _, err := mem.Stat(dir); err == nil {
		return
	}
	mustMkdir(t, mem, path.Dir(dir))
	if err := fsx.MakeDir(mem, setupTag, dir); err != nil {
		t.Fatalf("harness: setup MakeDir(%q): %v", dir, err)
	}
}

// MakeLayout creates the directories the daemon makes before anything is written:
// the keys directory and the directories of state/.
func MakeLayout(t testing.TB, mem *fsxtest.Mem) {
	t.Helper()
	for _, d := range append([]string{"keys"}, store.StateDirs()...) {
		mustMkdir(t, mem, d)
	}
}

// Mounted is a process over a disk, before the engine: a writer on the fault
// wrapper, its recovery done, and a store. The process's calls reach the disk
// through the fault wrapper and, below it, a cut-off layer that a crash closes.
type Mounted struct {
	Mem    *fsxtest.Mem
	Faulty *fsxtest.Faulty
	Writer *txn.Writer
	Store  *store.Store
	cut    *cutoff
}

// cutOff ends the process's access to its disk: every call after it fails, and
// it returns when the calls in flight have ended.
func (m *Mounted) cutOff() { m.cut.cutOff() }

// Mount starts a process over the disk as a start does before the engine: the
// recovery of the transaction layer, then a store. The observer, which may be
// nil, is told about every write step of the process.
func Mount(t testing.TB, tab *txn.Table, disk *fsxtest.Mem, obs txn.Observer) *Mounted {
	t.Helper()
	return MountWithFaults(t, tab, disk, obs, nil)
}

// MountWithFaults is Mount with a chance to arm the fault wrapper before the
// recovery runs, so that a rule can fail a write of the recovery itself. arm may
// be nil.
func MountWithFaults(t testing.TB, tab *txn.Table, disk *fsxtest.Mem, obs txn.Observer, arm func(*fsxtest.Faulty)) *Mounted {
	t.Helper()
	cut := newCutoff(disk)
	faulty := fsxtest.NewFaulty(t, cut)
	if arm != nil {
		arm(faulty)
	}
	w := txn.New(faulty, tab, obs)
	if err := w.Recover(); err != nil {
		t.Fatalf("harness: Recover: %v", err)
	}
	return &Mounted{Mem: disk, Faulty: faulty, Writer: w, Store: store.New(w), cut: cut}
}

// SeedNode makes the node on the writer's disk through the code the program
// uses, writing no file by hand: the configuration `init` writes, then the
// adapter Instance installed and enabled. It returns the configuration loaded
// from what it wrote. The disk's layout must exist (MakeLayout).
func SeedNode(t testing.TB, w *txn.Writer, at time.Time) *config.Config {
	t.Helper()
	cfg := initConfig(t, w)
	installAdapter(t, w, cfg, at)
	enableAdapter(t, cfg)
	return cfg
}

func initConfig(t testing.TB, w *txn.Writer) *config.Config {
	t.Helper()
	plan, err := config.PlanInit(w.Reader())
	if err != nil {
		t.Fatalf("harness: PlanInit: %v", err)
	}
	tx := w.Begin(initDecl)
	defer tx.Close()
	if err := plan.MakeDirs(tx); err != nil {
		t.Fatal(err)
	}
	if err := plan.WriteFiles(tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Replace("marker", "init-marker", []byte("init"), fsx.ModePrivate); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(w)
	if err != nil {
		t.Fatalf("harness: Load: %v", err)
	}
	return cfg
}

func installAdapter(t testing.TB, w *txn.Writer, cfg *config.Config, at time.Time) {
	t.Helper()
	src := fsxtest.NewMem()
	for p, c := range map[string]string{"sidera-adapter.json": manifestText, "bin/sidera-adapter-chat": "#!chat\n"} {
		mustMkdir(t, src, path.Dir(p))
		if err := fsx.Replace(src, setupTag, p, []byte(c), fsx.ModePrivate); err != nil {
			t.Fatal(err)
		}
	}
	staged, err := cfg.StageAdapter(src, bytes.NewReader(bytes.Repeat([]byte{9}, 8)))
	if err != nil {
		t.Fatalf("harness: StageAdapter: %v", err)
	}
	plan, err := cfg.PlanInstall(staged)
	if err != nil {
		t.Fatalf("harness: PlanInstall: %v", err)
	}
	var op admin.OperationID
	op[0] = 1
	res, err := store.PrepareAdminResult(store.AdminRecord{
		Key: admin.RequestKey{ID: op}, Op: "adapter.install", Result: []byte(`{"installed":true}`),
		At: at, RetainUntil: at.Add(store.AdminResultRetention),
	})
	if err != nil {
		t.Fatal(err)
	}
	tx := w.Begin(installDecl)
	defer tx.Close()
	if err := plan.DiscardStale(tx); err != nil {
		t.Fatal(err)
	}
	if err := plan.Promote(tx); err != nil {
		t.Fatal(err)
	}
	done, err := plan.Point(tx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutAdminResult(tx, store.RoleResult, res); err != nil {
		t.Fatal(err)
	}
	if err := plan.DiscardOld(tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	done()
}

func enableAdapter(t testing.TB, cfg *config.Config) {
	t.Helper()
	var op admin.OperationID
	op[0] = 2
	req := config.Request{Key: admin.KeyFor(admin.LocalOwner(), op), Expected: cfg.Instances().Version}
	if _, err := cfg.EnableAdapter(req, Instance, Instance, nil, nil, func(config.Instances) (json.RawMessage, error) {
		return json.RawMessage(`{"enabled":true}`), nil
	}); err != nil {
		t.Fatalf("harness: EnableAdapter: %v", err)
	}
}

// AdapterAdmission is what the engine is told when the adapter Instance is
// admitted: it declares direct endpoints, at most maxDirect of them.
func AdapterAdmission(t testing.TB, maxDirect int) adapterreg.Admission {
	t.Helper()
	caps := adapterproto.Capabilities{Direct: &adapterproto.DirectCapabilities{MaxEndpoints: maxDirect}}
	m, err := manifest.Parse([]byte(manifestText))
	if err != nil {
		t.Fatalf("harness: manifest: %v", err)
	}
	adm, err := adapterreg.Admit(caps, m)
	if err != nil {
		t.Fatalf("harness: Admit: %v", err)
	}
	return adm
}

// RandReader is the deterministic random source of an engine: the same seed
// gives the same stream, and seeds that differ give streams that differ.
func RandReader(seed uint64) io.Reader {
	var key [32]byte
	binary.LittleEndian.PutUint64(key[:8], seed)
	return rand.NewChaCha8(key)
}

// A node made by SeedNode is the same for every test that starts at the same
// time, so it is made once per test binary and each fixture starts from a copy
// of the disk.
var (
	seedMu    sync.Mutex
	seedDisks = map[int64]*fsxtest.Mem{}
)

// seededDisk returns a fresh copy of the disk of an initialised node whose
// records are stamped at.
func seededDisk(t testing.TB, at time.Time) *fsxtest.Mem {
	t.Helper()
	seedMu.Lock()
	defer seedMu.Unlock()
	disk, ok := seedDisks[at.UnixNano()]
	if !ok {
		disk = fsxtest.NewMem()
		MakeLayout(t, disk)
		m := Mount(t, ProgramTable(t), disk, nil)
		SeedNode(t, m.Writer, at)
		seedDisks[at.UnixNano()] = disk
	}
	return disk.Clone()
}
