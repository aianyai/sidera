package daemon_test

import (
	"bytes"
	"context"
	"errors"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/node/clock/clocktest"
	nodeconfig "github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/daemon"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/ipc"
	"github.com/aianyai/sidera/internal/node/keys"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/txn"
)

// init makes the data directory: the root only the owner can reach, the layout
// every owner declares, the keys, the certificate, the self-revocation, the
// clock record, the configuration with its default preset, and the marker that
// says it finished. What it tells the caller is what has to be shown to the owner.
func TestInitMakesTheDataDirectoryWithItsLayoutKeysAndConfiguration(t *testing.T) {
	clk := clocktest.NewFake(t0)
	home := newHome(t)
	rep, err := daemon.Init(context.Background(), config(home, clk))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if rep.Home != home || rep.Node.IsZero() {
		t.Errorf("report %+v: want the home and the node", rep)
	}
	if want := filepath.Join(home, "keys", "self-revocation.rec"); rep.SelfRevocationPath != want {
		t.Errorf("the self-revocation is said to be at %q, want %q", rep.SelfRevocationPath, want)
	}
	for _, kind := range []ipc.Kind{ipc.KindAgent, ipc.KindManage} {
		addr, err := ipc.Locate(home, kind)
		if err != nil {
			t.Fatal(err)
		}
		if got := rep.Addresses[kind]; got != addr.Name() {
			t.Errorf("address of the %s interface %q, want %q", kind, got, addr.Name())
		}
	}

	dirs := append([]string{"keys", "config", "run", "manage", "logs"}, store.StateDirs()...)
	for _, d := range dirs {
		info, err := os.Stat(filepath.Join(home, d))
		if err != nil || !info.IsDir() {
			t.Errorf("%s: %v", d, err)
			continue
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
			t.Errorf("%s has mode %v, want 0700", d, info.Mode().Perm())
		}
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(home); err != nil || info.Mode().Perm() != 0o700 {
			t.Errorf("the data directory: %v, %v; want 0700", info, err)
		}
		for _, f := range []string{"keys/identity.key", "keys/network.key", "keys/network.cert", "keys/network-serial",
			"keys/self-revocation.rec", "keys/clock.rec", "keys/init.rec", "config/node.json", "config/policy.json", "config/presets.json", "config/instances.json"} {
			info, err := os.Stat(filepath.Join(home, f))
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Errorf("%s: %v, %v; want a file of mode 0600", f, info, err)
			}
		}
	}

	// the node can start over what init made: the layers load, and the preset
	// that init writes says that local sources may use it
	fsys, err := fsx.OpenOS(home, bytes.NewReader(bytes.Repeat([]byte{1}, 4096)))
	if err != nil {
		t.Fatal(err)
	}
	if done, err := keys.Initialized(fsys); err != nil || !done {
		t.Errorf("Initialized = %v, %v", done, err)
	}
	b, err := fsys.ReadFile("config/presets.json")
	if err != nil {
		t.Fatal(err)
	}
	p, err := nodeconfig.DecodePresets(b)
	if err != nil {
		t.Fatalf("the presets init wrote do not decode: %v", err)
	}
	if got, ok := p.Items[nodeconfig.DefaultPresetName]; !ok || got.LocalSources != authz.PresetLocalAllow || len(p.Items) != 1 {
		t.Errorf("presets %+v, want the one default preset open to local sources", p.Items)
	}
}

// init does not touch a data directory that init finished: it refuses, says so,
// and not one file changes: not a byte, not a permission.
func TestInitRefusesAnInitializedHomeAndChangesNothing(t *testing.T) {
	c, _ := initialized(t)
	before := snapshot(t, c.Home)
	_, err := daemon.Init(context.Background(), c)
	if !errors.Is(err, daemon.ErrAlreadyInitialized) {
		t.Fatalf("a second init: %v; want ErrAlreadyInitialized", err)
	}
	if after := snapshot(t, c.Home); !reflect.DeepEqual(before, after) {
		t.Errorf("a refused init changed the data directory:\n%s", diff(before, after))
	}
}

func diff(a, b map[string]string) string {
	var out []string
	for k, v := range a {
		if w, ok := b[k]; !ok {
			out = append(out, "removed "+k)
		} else if v != w {
			out = append(out, "changed "+k)
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			out = append(out, "added "+k)
		}
	}
	return strings.Join(out, "\n")
}

// init takes the instance lock before it looks at anything, so it cannot run
// beside a daemon, or beside another init: while the lock is held elsewhere it is
// refused and has written nothing; once the lock is free it works, and it lets the
// lock go when it is done, so that the daemon can start.
func TestInitTakesTheInstanceLockAndLetsItGo(t *testing.T) {
	clk := clocktest.NewFake(t0)
	home := newHome(t)
	c := config(home, clk)
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	fsys, err := fsx.OpenOS(home, bytes.NewReader(bytes.Repeat([]byte{2}, 4096)))
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := fsys.Lock("daemon.lock")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := daemon.Init(context.Background(), c); !errors.Is(err, daemon.ErrAlreadyRunning) {
		t.Fatalf("init while the lock is held: %v; want ErrAlreadyRunning", err)
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "daemon.lock" {
			t.Errorf("a refused init wrote %s", e.Name())
		}
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	if _, err := daemon.Init(context.Background(), c); err != nil {
		t.Fatalf("init once the lock was free: %v", err)
	}
	// init let go of the lock: the daemon, which takes it first thing, can start
	d := started(t, config(home, clk))
	if d == nil {
		t.Fatal("no daemon")
	}
}

// init makes nothing when the sockets it would make a path for cannot be bound on
// this platform: the length of the path is judged by the one function that owns
// it, before the first directory is made.
func TestInitRefusesAPathTheSocketsCannotUseBeforeMakingAnything(t *testing.T) {
	requireUnix(t)
	home := filepath.Join(os.TempDir(), "no-such-parent-"+strings.Repeat("x", 120), "home")
	_, err := daemon.Init(context.Background(), config(home, clocktest.NewFake(t0)))
	var tooLong *ipc.PathTooLongError
	if !errors.As(err, &tooLong) {
		t.Fatalf("init on a home with a long path: %v; want a *ipc.PathTooLongError", err)
	}
	if _, err := os.Stat(filepath.Dir(home)); err == nil {
		t.Error("init made a directory before it judged the path")
	}
}

// The data directory is made by init and by nothing else: a home that is not there
// is not made by a start, and a home init never finished is not started.
func TestStartRefusesAHomeThatInitDidNotMake(t *testing.T) {
	clk := clocktest.NewFake(t0)
	home := newHome(t)
	if _, err := daemon.Start(context.Background(), config(home, clk)); !errors.Is(err, daemon.ErrNotInitialized) {
		t.Errorf("a start on a home that is not there: %v; want ErrNotInitialized", err)
	}
	if _, err := os.Stat(home); err == nil {
		t.Error("a start made the data directory")
	}
	requireUnix(t)
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, home)
	if _, err := daemon.Start(context.Background(), config(home, clk)); !errors.Is(err, daemon.ErrNotInitialized) {
		t.Errorf("a start on an empty home: %v; want ErrNotInitialized", err)
	}
	if after := snapshot(t, home); !reflect.DeepEqual(before, after) {
		t.Errorf("a refused start changed the home:\n%s", diff(before, after))
	}
}

// ---------------------------------------------------------------------------
// Crashes

// crashRun is init run over a disk in memory, with the disk a crash would leave
// before each of its write steps kept: the process dying, the worst power loss
// and some seeded ones.
type crashRun struct {
	states []crashState
	keys   map[fsx.Key]bool
}

type crashState struct {
	name string
	mem  *fsxtest.Mem
}

func newMemHome(t testing.TB) (*fsxtest.Mem, string) {
	t.Helper()
	return fsxtest.NewMem(), filepath.Join(os.TempDir(), "sidera-mem-home")
}

func runInitCapturing(t testing.TB, mem *fsxtest.Mem, home string, rng *rand.Rand, seed byte) (crashRun, error) {
	t.Helper()
	run := crashRun{keys: map[fsx.Key]bool{}}
	c := memConfig(home, clocktest.NewFake(t0), mem)
	c.Observer = observerFunc(func(p fsx.Point, _ txn.Kind) {
		run.keys[p.Key] = true
		run.states = append(run.states,
			crashState{"process-crash before " + p.String(), mem.Clone()},
			crashState{"worst-power-loss before " + p.String(), mem.CrashCloneWorst()},
			crashState{"seeded-power-loss before " + p.String(), mem.CrashClone(rng)},
		)
	})
	c.Rand = seeded(seed)
	_, err := daemon.Init(context.Background(), c)
	return run, err
}

// observerFunc is a txn.Observer made of a function.
type observerFunc func(fsx.Point, txn.Kind)

func (f observerFunc) Reached(observation fsx.Observation, k txn.Kind) {
	p := observation.Point
	f(p, k)
}

func initRerun(t testing.TB, mem *fsxtest.Mem, home string, seed byte) error {
	t.Helper()
	c := memConfig(home, clocktest.NewFake(t0.Add(time.Minute)), mem)
	c.Rand = seeded(seed)
	_, err := daemon.Init(context.Background(), c)
	return err
}

// Whatever state a crash leaves init in, running init again ends in a complete
// data directory, and what was durable before the crash is kept as it was: a key
// is never replaced, the owner's configuration never rewritten. The rerun is
// crashed at its own steps too, and run a third time.
func TestInitReplaysToACompleteHomeFromEveryCrashState(t *testing.T) {
	rng := rand.New(rand.NewPCG(11, 12))
	mem, home := newMemHome(t)
	run, err := runInitCapturing(t, mem, home, rng, 1)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if len(run.states) < 3*20 {
		t.Fatalf("only %d states captured: the crash points of init were not reached", len(run.states))
	}
	protected := []string{"keys/identity.key", "keys/network.key", "keys/self-revocation.rec", "keys/clock.rec",
		"config/node.json", "config/policy.json", "config/presets.json", "config/instances.json"}
	read := func(m *fsxtest.Mem) map[string][]byte {
		out := map[string][]byte{}
		for _, p := range protected {
			if b, err := m.ReadFile(p); err == nil {
				out[p] = b
			}
		}
		return out
	}
	complete := func(where string, m *fsxtest.Mem, seed byte) {
		t.Helper()
		before := read(m)
		if done, _ := keys.Initialized(m); !done {
			if err := initRerun(t, m, home, seed); err != nil {
				t.Fatalf("%s: init again: %v", where, err)
			}
		}
		if done, err := keys.Initialized(m); err != nil || !done {
			t.Fatalf("%s: init did not finish: %v, %v", where, done, err)
		}
		for p, want := range before {
			if got, err := m.ReadFile(p); err != nil || !bytes.Equal(got, want) {
				t.Errorf("%s: %s was changed by the rerun of init", where, p)
			}
		}
		for _, d := range append([]string{"keys", "config", "run", "manage", "logs"}, store.StateDirs()...) {
			if _, err := m.Stat(d); err != nil {
				t.Errorf("%s: the directory %s is missing: %v", where, d, err)
			}
		}
	}
	nested := 0
	for i, s := range run.states {
		inner := s.mem.Clone()
		complete(s.name, s.mem, 2)
		// crash the rerun at its steps, for some states
		if i%9 != 0 {
			continue
		}
		if done, _ := keys.Initialized(inner); done {
			continue
		}
		rr, err := runInitCapturingOn(t, inner, home, rng, 3)
		if err != nil {
			t.Fatalf("%s: the rerun: %v", s.name, err)
		}
		for _, is := range rr.states {
			nested++
			complete(s.name+", then "+is.name, is.mem, 4)
		}
	}
	if nested < 5000 {
		t.Errorf("only %d crashes during a rerun of init were exercised", nested)
	}
}

func runInitCapturingOn(t testing.TB, mem *fsxtest.Mem, home string, rng *rand.Rand, seed byte) (crashRun, error) {
	t.Helper()
	return runInitCapturing(t, mem, home, rng, seed)
}

// Every crash point that init and the layout rule declare is reached: the table of
// declarations is static, so a write that the run never makes is a point the
// crash tests never walk. The expected keys come from the table, which the run
// did not produce.
func TestEveryDeclaredCrashPointOfInitAndLayoutIsReached(t *testing.T) {
	tab, err := daemon.Table()
	if err != nil {
		t.Fatalf("Table: %v", err)
	}
	want := map[string]map[fsx.Key]bool{}
	for _, k := range tab.Keys() {
		if k.Tag.Tx == "init" || k.Tag.Tx == "layout" {
			if want[k.Tag.Tx] == nil {
				want[k.Tag.Tx] = map[fsx.Key]bool{}
			}
			want[k.Tag.Tx][k.Key] = false
		}
	}
	if len(want["init"]) == 0 || len(want["layout"]) == 0 {
		t.Fatalf("the table holds %d points of init and %d of the layout rule", len(want["init"]), len(want["layout"]))
	}
	rng := rand.New(rand.NewPCG(5, 6))
	// a whole init, on an empty disk
	mem, home := newMemHome(t)
	run, err := runInitCapturing(t, mem, home, rng, 1)
	if err != nil {
		t.Fatal(err)
	}
	for k := range run.keys {
		for tx := range want {
			if _, ok := want[tx][k]; ok {
				want[tx][k] = true
			}
		}
	}
	// the layout rule, on a disk that lost its directories
	for _, d := range []string{"state", "run", "logs"} {
		lost := mem.Clone()
		if err := fsx.RemoveAll(lost, fsx.Tag{Tx: "setup", Role: "r"}, d); err != nil {
			t.Fatal(err)
		}
		hit := map[fsx.Key]bool{}
		recovered := false
		w := txn.New(lost, tab, observerFunc(func(p fsx.Point, _ txn.Kind) {
			if recovered { // what the layout rule alone reaches, not the recovery before it
				hit[p.Key] = true
			}
		}))
		if err := w.Recover(); err != nil {
			t.Fatalf("Recover: %v", err)
		}
		recovered = true
		if err := daemon.RepairLayout(w); err != nil {
			t.Fatalf("RepairLayout after losing %s: %v", d, err)
		}
		for k := range hit {
			if _, ok := want["layout"][k]; ok {
				want["layout"][k] = true
			}
		}
	}
	for tx, keys := range want {
		for k, hit := range keys {
			if !hit {
				t.Errorf("the declared crash point %s of %s was never reached", k, tx)
			}
		}
	}
}

// The layout rule is a recovery rule: it has work only where a directory of the
// layout is missing, and a data directory that init made, or that a start has
// repaired, leaves it none.
func TestTheLayoutRuleHasNoWorkOnAHomeThatInitMade(t *testing.T) {
	mem, home := newMemHome(t)
	if _, err := runInitCapturing(t, mem, home, rand.New(rand.NewPCG(1, 1)), 1); err != nil {
		t.Fatal(err)
	}
	if work, err := daemon.LayoutWork(mem); err != nil || len(work) != 0 {
		t.Errorf("LayoutWork on a home init made = %v, %v; want nothing", work, err)
	}
	if err := fsx.RemoveAll(mem, fsx.Tag{Tx: "setup", Role: "r"}, "state"); err != nil {
		t.Fatal(err)
	}
	work, err := daemon.LayoutWork(mem)
	if err != nil || len(work) != len(store.StateDirs()) {
		t.Errorf("LayoutWork after the owner removed state/ = %v, %v; want the %d directories of state/", work, err, len(store.StateDirs()))
	}
	// parents before children, as the rule makes them
	seen := map[string]bool{}
	for _, d := range work {
		if p := filepath.ToSlash(filepath.Dir(d)); p != "." && p != "" && !seen[p] && p != "state" {
			t.Errorf("%s comes before its parent %s", d, p)
		}
		seen[d] = true
	}
}

// An owner who deletes keys/ to start the identity afresh and runs init again gets
// a new identity, and keeps everything of the configuration: the settings, the
// presets and the instances are the owner's decisions and are not rewritten.
func TestInitAfterTheOwnerDeletedKeysMakesANewIdentityAndKeepsTheConfiguration(t *testing.T) {
	c, clk := initialized(t)
	first := nodeOf(t, c)
	cfgFiles := []string{"node.json", "policy.json", "presets.json", "instances.json"}
	before := map[string][]byte{}
	for _, f := range cfgFiles {
		b, err := os.ReadFile(filepath.Join(c.Home, "config", f))
		if err != nil {
			t.Fatal(err)
		}
		before[f] = b
	}
	if err := os.RemoveAll(filepath.Join(c.Home, "keys")); err != nil {
		t.Fatal(err)
	}
	again := config(c.Home, clk)
	again.Rand = seeded(77)
	rep, err := daemon.Init(context.Background(), again)
	if err != nil {
		t.Fatalf("init after keys/ was deleted: %v", err)
	}
	if rep.Node == first {
		t.Error("the node has the identity of the keys that were deleted")
	}
	for _, f := range cfgFiles {
		if b, err := os.ReadFile(filepath.Join(c.Home, "config", f)); err != nil || !bytes.Equal(b, before[f]) {
			t.Errorf("config/%s was rewritten by init: %v", f, err)
		}
	}
	started(t, again)
}

// An owner who deletes config/ cannot start the node and cannot run init either: a
// retired identity would come back otherwise, because the record of its
// retirement is in config/revocations/.
func TestAHomeWhoseConfigurationWasDeletedIsNeitherStartedNorInitializedAgain(t *testing.T) {
	c, _ := initialized(t)
	if err := os.RemoveAll(filepath.Join(c.Home, "config")); err != nil {
		t.Fatal(err)
	}
	_, err := daemon.Start(context.Background(), c)
	var fe *nodeconfig.FileError
	if !errors.As(err, &fe) || !strings.HasPrefix(fe.Path, "config") {
		t.Errorf("a start without config/: %v; want an error that names it", err)
	}
	if _, err := daemon.Init(context.Background(), c); !errors.Is(err, daemon.ErrAlreadyInitialized) {
		t.Errorf("init without config/: %v; want ErrAlreadyInitialized", err)
	}
	if _, err := os.Stat(filepath.Join(c.Home, "config")); err == nil {
		t.Error("config/ was made again")
	}
}
