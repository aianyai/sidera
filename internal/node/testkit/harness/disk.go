package harness

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"math/rand/v2"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/daemon"
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

// manifestTextOf is the manifest of the adapter package of an instance, made from
// the same options as what the fake adapter declares (AdapterManifestOf), so that
// the installed package and the adapter that runs as it cannot disagree. With
// runtime it is the manifest of an adapter that also declares a runtime, which a
// fixture with Options.Adapter.Runtime installs.
func manifestTextOf(instance string, runtime bool) string {
	o := AdapterManifestOf(instance, adapterproto.MaxDirectEndpoints)
	if runtime {
		o = RuntimeAdapterManifestOf(instance, adapterproto.MaxDirectEndpoints)
	}
	raw, err := o.Encode()
	if err != nil {
		panic("harness: the manifest of the adapter " + instance + ": " + err.Error())
	}
	return string(raw)
}

// manifestText is the manifest of the adapter Instance the fixture installs.
var manifestText = manifestTextOf(Instance, false)

// adapterPackage is an adapter package the fixture installs and enables under the
// name of its instance.
type adapterPackage struct {
	instance, manifest string
}

// adapterPackages are the packages of a node: Instance first, then the extra
// instances in the order given.
func adapterPackages(runtime bool, extra []string) []adapterPackage {
	pkgs := []adapterPackage{{Instance, manifestTextOf(Instance, runtime)}}
	for _, name := range extra {
		pkgs = append(pkgs, adapterPackage{name, manifestTextOf(name, runtime)})
	}
	return pkgs
}

// ManifestText is the manifest of the adapter the fixture installs.
func ManifestText() string { return manifestText }

// setupTag tags the writes that lay out a disk before any process mounts it.
var setupTag = fsx.Tag{Tx: "setup", Role: "f"}

// installDecl is the engine's own declaration of the installation, which the
// fixture runs as the engine does when it installs the adapter of the node.
var installDecl = declNamed(engine.Transactions(), "adapter-install")

// initDecl is the daemon's own declaration of init, which the fixture runs as the
// daemon does when it lays out a node.
var initDecl = declNamed(daemon.Transactions(), "init")

// declNamed finds a declaration among those of an owner by name.
func declNamed(decls []*txn.Decl, name string) *txn.Decl {
	for _, d := range decls {
		if d.Name == name {
			return d
		}
	}
	panic("harness: no declaration named " + name)
}

// ProgramTable is the table of every transaction of the program, as the daemon
// builds it from the declarations of the transaction layer and of every owner.
func ProgramTable(t testing.TB) *txn.Table {
	t.Helper()
	tab, err := daemon.Table()
	if err != nil {
		t.Fatalf("harness: daemon.Table: %v", err)
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
	m := newMounted(t, tab, base, obs, arm)
	if err := m.Writer.Recover(); err != nil {
		t.Fatalf("harness: Recover: %v", err)
	}
	return m
}

// newMounted gives a generation its disk boundary before recovery can write.
func newMounted(t testing.TB, tab *txn.Table, base fsx.FS, obs txn.Observer, arm func(*fsxtest.Faulty)) *Mounted {
	cut := newCutoff(base)
	faulty := fsxtest.NewFaulty(t, cut)
	if arm != nil {
		arm(faulty)
	}
	w := txn.New(faulty, tab, obs)
	return &Mounted{Faulty: faulty, Writer: w, Store: store.New(w), cut: cut}
}

// SeedNode makes the node on the writer's disk through the code the program
// uses, writing no file by hand: the keys and the configuration `init` writes, then the
// adapter Instance installed and enabled. It returns the configuration loaded
// from what it wrote. The disk's layout must exist (MakeLayout).
func SeedNode(t testing.TB, w *txn.Writer, at time.Time) *config.Config {
	t.Helper()
	return seedNode(t, w, at, adapterPackages(false, nil))
}

// seedNode is SeedNode for the packages given: each is installed and enabled under
// its instance name, by operations of its own, so that no two share a request key.
func seedNode(t testing.TB, w *txn.Writer, at time.Time, pkgs []adapterPackage) *config.Config {
	t.Helper()
	cfg := initConfig(t, w, at)
	for i, p := range pkgs {
		// The first package keeps the operation numbers 1 and 2 of the one-instance node;
		// further ones take 5 and 6, numbered by the second byte.
		install, enable := admin.OperationID{1}, admin.OperationID{2}
		if i > 0 {
			install, enable = admin.OperationID{5, byte(i)}, admin.OperationID{6, byte(i)}
		}
		installAdapter(t, w, cfg, at, p.manifest, install)
		enableAdapter(t, cfg, p.instance, enable)
	}
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
	for _, step := range []func(*txn.Tx) error{plan.MakeDirs, kp.WriteKeys, plan.WriteFiles, kp.WriteMarker} {
		if err := step(tx); err != nil {
			t.Fatal(err)
		}
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

func installAdapter(t testing.TB, w *txn.Writer, cfg *config.Config, at time.Time, manifest string, op admin.OperationID) {
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

func enableAdapter(t testing.TB, cfg *config.Config, instance string, op admin.OperationID) {
	t.Helper()
	req := config.Request{Key: admin.KeyFor(admin.LocalOwner(), op), Expected: cfg.Instances().Version}
	if _, err := cfg.EnableAdapter(req, instance, instance, nil, nil, func(config.Instances) (json.RawMessage, error) {
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
// are stamped at, whether the installed adapter declares a runtime and which
// further instances it has (their names, joined).
type seedKey struct {
	at      int64
	runtime bool
	extra   string
}

// seededDisk returns a fresh copy of the disk of an initialised node whose
// records are stamped at.
func seededDisk(t testing.TB, at time.Time, runtime bool, extra []string) *fsxtest.Mem {
	t.Helper()
	seedMu.Lock()
	defer seedMu.Unlock()
	key := seedKey{at.UnixNano(), runtime, strings.Join(extra, ",")}
	disk, ok := seedDisks[key]
	if !ok {
		disk = fsxtest.NewMem()
		MakeLayout(t, disk)
		m := Mount(t, ProgramTable(t), disk, nil)
		seedNode(t, m.Writer, at, adapterPackages(runtime, extra))
		seedDisks[key] = disk
	}
	return disk.Clone()
}
