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
	"github.com/aianyai/sidera/internal/node/keys"
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

// keySeed is the seed of the random source the fixture's keys are drawn from, so
// that the node, whose identifier is derived from its identity key, is the same
// in every fixture.
const keySeed = 7

var (
	nodeOnce sync.Once
	nodeID   ident.NodeID
)

// NodeID is the identifier of the fixture's node: the one its identity key
// derives, as `init` makes it from the seeded random source.
func NodeID() ident.NodeID {
	nodeOnce.Do(func() {
		plan, err := keys.PlanInit(fsxtest.NewMem(), RandReader(keySeed), DefaultStart)
		if err != nil {
			panic("harness: the key plan of the fixture: " + err.Error())
		}
		nodeID = plan.Node()
	})
	return nodeID
}

// The building blocks below make the disk of a node and a process over it. The
// fixture uses them, and so does a test of a package that needs the pieces and
// not the whole fixture, so that the way a node is made is written once.

// manifestText is the manifest of the adapter the fixture installs. It is made from
// the same options as what the fake adapter declares (AdapterManifest), so that the
// installed package and the adapter that runs as it cannot disagree.
var manifestText = func() string {
	raw, err := AdapterManifest(adapterproto.MaxDirectEndpoints).Encode()
	if err != nil {
		panic("harness: the manifest of the adapter: " + err.Error())
	}
	return string(raw)
}()

// runtimeManifestText is the manifest of the same adapter when it also declares a
// runtime, which a fixture with Options.Adapter.Runtime installs instead.
var runtimeManifestText = func() string {
	raw, err := RuntimeAdapterManifest(adapterproto.MaxDirectEndpoints).Encode()
	if err != nil {
		panic("harness: the manifest of the adapter with a runtime: " + err.Error())
	}
	return string(raw)
}()

// ManifestText is the manifest of the adapter the fixture installs.
func ManifestText() string { return manifestText }

// setupTag tags the writes that lay out a disk before any process mounts it.
var setupTag = fsx.Tag{Tx: "setup", Role: "f"}

// The transactions that the composition root owns, declared here the way it
// declares them, so that the table of the program is complete, and the engine's
// installation, which the fixture runs.
var (
	initDecl = &txn.Decl{Name: "init", Kind: txn.Normal, Writes: append(append(keys.InitWrites(), config.InitWrites()...),
		keys.InitMarkerWrite())}
	// installDecl is the engine's own declaration of the installation, which the
	// fixture runs as the engine does when it installs the adapter of the node.
	installDecl = engineDecl("adapter-install")
)

// engineDecl finds a declaration among the engine's by name.
func engineDecl(name string) *txn.Decl {
	for _, d := range engine.Transactions() {
		if d.Name == name {
			return d
		}
	}
	panic("harness: the engine declares no transaction " + name)
}

// ProgramTable is the table of every transaction of the program: the internal
// ones, those of the store, the configuration, the keys and the engine, and the
// two that the composition root owns.
func ProgramTable(t testing.TB) *txn.Table {
	t.Helper()
	decls := append(txn.Internal(), store.Transactions()...)
	decls = append(decls, config.Transactions()...)
	decls = append(decls, keys.Transactions()...)
	decls = append(decls, engine.Transactions()...)
	decls = append(decls, initDecl)
	tab, err := txn.NewTable(decls...)
	if err != nil {
		t.Fatalf("harness: NewTable: %v", err)
	}
	return tab
}

func mustMkdir(t testing.TB, mem fsx.FS, dir string) {
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
func MakeLayout(t testing.TB, mem fsx.FS) {
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
	m := MountOn(t, tab, disk, obs, arm)
	m.Mem = disk
	return m
}

// MountOn is MountWithFaults over any file system, the real one among them. Its
// Mounted has no Mem: a disk that is not in memory cannot be cloned for a crash.
func MountOn(t testing.TB, tab *txn.Table, base fsx.FS, obs txn.Observer, arm func(*fsxtest.Faulty)) *Mounted {
	t.Helper()
	cut := newCutoff(base)
	faulty := fsxtest.NewFaulty(t, cut)
	if arm != nil {
		arm(faulty)
	}
	w := txn.New(faulty, tab, obs)
	if err := w.Recover(); err != nil {
		t.Fatalf("harness: Recover: %v", err)
	}
	return &Mounted{Faulty: faulty, Writer: w, Store: store.New(w), cut: cut}
}

// SeedNode makes the node on the writer's disk through the code the program
// uses, writing no file by hand: the keys and the configuration `init` writes, then the
// adapter Instance installed and enabled. It returns the configuration loaded
// from what it wrote. The disk's layout must exist (MakeLayout).
func SeedNode(t testing.TB, w *txn.Writer, at time.Time) *config.Config {
	t.Helper()
	return seedNode(t, w, at, manifestText)
}

func seedNode(t testing.TB, w *txn.Writer, at time.Time, manifest string) *config.Config {
	t.Helper()
	cfg := initConfig(t, w, at)
	installAdapter(t, w, cfg, at, manifest)
	enableAdapter(t, cfg)
	return cfg
}

func initConfig(t testing.TB, w *txn.Writer, at time.Time) *config.Config {
	t.Helper()
	kp, err := keys.PlanInit(w.Reader(), RandReader(keySeed), at)
	if err != nil {
		t.Fatalf("harness: keys.PlanInit: %v", err)
	}
	plan, err := config.PlanInit(w.Reader())
	if err != nil {
		t.Fatalf("harness: PlanInit: %v", err)
	}
	tx := w.Begin(initDecl)
	defer tx.Close()
	if err := kp.WriteKeys(tx); err != nil {
		t.Fatal(err)
	}
	if err := plan.MakeDirs(tx); err != nil {
		t.Fatal(err)
	}
	if err := plan.WriteFiles(tx); err != nil {
		t.Fatal(err)
	}
	if err := kp.WriteMarker(tx); err != nil {
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

func installAdapter(t testing.TB, w *txn.Writer, cfg *config.Config, at time.Time, manifest string) {
	t.Helper()
	src := fsxtest.NewMem()
	for p, c := range map[string]string{"sidera-adapter.json": manifest, "bin/sidera-adapter-chat": "#!chat\n"} {
		mustMkdir(t, src, path.Dir(p))
		if err := fsx.Replace(src, setupTag, p, []byte(c), fsx.ModePrivate); err != nil {
			t.Fatal(err)
		}
	}
	staged, err := cfg.StageAdapter(src, bytes.NewReader(bytes.Repeat([]byte{9}, 8)))
	if err != nil {
		t.Fatalf("harness: StageAdapter: %v", err)
	}
	plan, err := cfg.PlanInstall(staged, nil)
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
	caps := AdapterManifest(maxDirect).Capabilities()
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
	seedDisks = map[seedKey]*fsxtest.Mem{}
)

// seedKey is what makes one seeded disk differ from another: the time its records
// are stamped at and whether the installed adapter declares a runtime.
type seedKey struct {
	at      int64
	runtime bool
}

// seededDisk returns a fresh copy of the disk of an initialised node whose
// records are stamped at.
func seededDisk(t testing.TB, at time.Time, runtime bool) *fsxtest.Mem {
	t.Helper()
	seedMu.Lock()
	defer seedMu.Unlock()
	key := seedKey{at.UnixNano(), runtime}
	disk, ok := seedDisks[key]
	if !ok {
		disk = fsxtest.NewMem()
		MakeLayout(t, disk)
		m := Mount(t, ProgramTable(t), disk, nil)
		text := manifestText
		if runtime {
			text = runtimeManifestText
		}
		seedNode(t, m.Writer, at, text)
		seedDisks[key] = disk
	}
	return disk.Clone()
}
