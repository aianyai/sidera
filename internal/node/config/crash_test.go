package config_test

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"path"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/txn"
)

// walker is an Observer that, before every step of a transaction, takes the
// disks a crash at that instant can leave, and hands each to check: the process
// dying, the worst power loss, and some seeded ones.
type walker struct {
	mem    *fsxtest.Mem
	rng    *rand.Rand
	check  func(where string, clone *fsxtest.Mem, processCrash bool)
	points []string
	paused bool
}

func newWalker(mem *fsxtest.Mem, seed uint64, check func(where string, clone *fsxtest.Mem, processCrash bool)) *walker {
	return &walker{mem: mem, rng: rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)), check: check}
}

// The kinds of crash a walker takes at each point.
const (
	modelProcessCrash = "process crash"
	modelWorstPower   = "worst power loss"
	modelPowerLoss    = "power loss"
)

var crashModels = []string{modelProcessCrash, modelWorstPower, modelPowerLoss}

// crashed counts, for each crash point and each kind of crash, the crash states
// the walkers took there and judged: reaching a point is not crashing at it.
var crashed = struct {
	sync.Mutex
	taken map[fsx.Key]map[string]int
}{taken: map[fsx.Key]map[string]int{}}

func recordCrash(p fsx.Point, model string) {
	crashed.Lock()
	defer crashed.Unlock()
	if crashed.taken[p.Key] == nil {
		crashed.taken[p.Key] = map[string]int{}
	}
	crashed.taken[p.Key][model]++
}

func (w *walker) Reached(p fsx.Point, _ txn.Kind) {
	if w.paused {
		return
	}
	w.points = append(w.points, p.String())
	where := "before " + p.String()
	w.check(where+" (process crash)", w.mem.Clone(), true)
	recordCrash(p, modelProcessCrash)
	w.check(where+" (worst power loss)", w.mem.CrashCloneWorst(), false)
	recordCrash(p, modelWorstPower)
	for i := 0; i < 3; i++ {
		w.check(fmt.Sprintf("%s (power loss %d)", where, i), w.mem.CrashClone(w.rng), false)
		recordCrash(p, modelPowerLoss)
	}
}

// restart is what the next start does on a disk: remove the temporary files,
// then load, which repairs the adapter directory.
func restart(t testing.TB, disk *fsxtest.Mem) (*config.Config, error) {
	t.Helper()
	w := txn.New(disk, newTable(t), nil)
	if err := w.Recover(); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	return config.Load(w)
}

// crashCase is one transaction of config, run on a disk, with how to tell the
// state before it from the state after it.
type crashCase struct {
	name string
	// disk builds the disk before the transaction, with the Config loaded on it.
	disk func(t *testing.T) *fsxtest.Mem
	// prepare runs after the configuration is loaded and before the walk
	// starts: what an earlier request of the same process did.
	prepare func(t *testing.T, rg *rig, c *config.Config)
	// run performs the transaction. It runs on a Writer whose observer is the
	// walker.
	run func(t *testing.T, rg *rig, c *config.Config) error
	// state says "before" or "after" for a configuration read after a restart,
	// and fails the test for a state that is neither.
	state func(t *testing.T, where string, c *config.Config) string
	// invisible is true for a transaction that changes nothing a restart can
	// read: staging and unstaging only prepare or clean, so the state is always
	// "before".
	invisible bool
	// done checks the disk after the transaction ran to its end, without a crash.
	done func(t *testing.T, rg *rig, c *config.Config)
}

func versionState(read func(c *config.Config) admin.Version, before admin.Version) func(*testing.T, string, *config.Config) string {
	return func(t *testing.T, where string, c *config.Config) string {
		switch got := read(c); got {
		case before:
			return "before"
		case before + 1:
			return "after"
		default:
			t.Errorf("%s: version %d is neither %d nor %d", where, got, before, before+1)
			return "?"
		}
	}
}

func treeDisk(t *testing.T) *fsxtest.Mem {
	mem := fsxtest.NewMem()
	putTree(t, mem, validTree(t))
	ensureState(t, mem)
	return mem
}

func crashCases(t *testing.T) []crashCase {
	_, _, extraRec := selfRevocation(t, 9)
	// staged is the package that the disk of the case holds, staged by the
	// install worker of the process that died before the transaction.
	var staged config.Staged
	stagedFresh := func(t *testing.T, c *config.Config, id, version string) config.Staged {
		s, err := c.StageAdapter(srcPackage(t, id, version, nil), suffixReader())
		if err != nil {
			t.Fatalf("StageAdapter: %v", err)
		}
		return s
	}
	disabledTree := func(t *testing.T) *fsxtest.Mem {
		mem := treeDisk(t)
		doc := strings.Replace(goldenInstances, `"enabled": true`, `"enabled": false`, 1)
		if err := fsx.Replace(mem, setupTag, config.InstancesPath, []byte(doc), fsx.ModePrivate); err != nil {
			t.Fatal(err)
		}
		return mem
	}
	prepStage := func(id, version string) func(*testing.T, *rig, *config.Config) {
		return func(t *testing.T, rg *rig, c *config.Config) { staged = stagedFresh(t, c, id, version) }
	}

	return []crashCase{
		{
			name: "node-set",
			disk: treeDisk,
			run: func(t *testing.T, rg *rig, c *config.Config) error {
				_, err := c.SetNodeSettings(request(1, 3), []byte(`{"reply_permission": false}`), result(new([]config.Node), `{"ok":true}`))
				return err
			},
			state: versionState(func(c *config.Config) admin.Version { return c.Node().Version }, 3),
		},
		{
			name: "presets-change/add",
			disk: treeDisk,
			run: func(t *testing.T, rg *rig, c *config.Config) error {
				_, err := c.AddPreset(request(1, 2), "extra", []byte(`{"local_sources": "deny"}`), result(new([]config.Presets), `{"ok":true}`))
				return err
			},
			state: versionState(func(c *config.Config) admin.Version { return c.Presets().Version }, 2),
		},
		{
			name: "presets-change/remove",
			disk: treeDisk,
			run: func(t *testing.T, rg *rig, c *config.Config) error {
				_, err := c.RemovePreset(request(1, 2), "sandboxed", result(new([]config.Presets), `{"ok":true}`))
				return err
			},
			state: versionState(func(c *config.Config) admin.Version { return c.Presets().Version }, 2),
		},
		{
			name: "endpoint-set",
			disk: treeDisk,
			run: func(t *testing.T, rg *rig, c *config.Config) error {
				_, err := c.SetEndpointSettings(request(1, 4), "example", mustEndpoint(t, ep1Text), []byte(`{"backlog_limit": 9}`), isOpen(t), result(new([]config.Instances), `{"ok":true}`))
				return err
			},
			state: versionState(func(c *config.Config) admin.Version { return c.Instances().Version }, 4),
		},
		{
			name: "adapter-enable",
			disk: treeDisk,
			run: func(t *testing.T, rg *rig, c *config.Config) error {
				_, err := c.EnableAdapter(request(1, 4), "second", "example", nil, isOpen(t), result(new([]config.Instances), `{"ok":true}`))
				return err
			},
			state: versionState(func(c *config.Config) admin.Version { return c.Instances().Version }, 4),
		},
		{
			name: "adapter-disable",
			disk: treeDisk,
			run: func(t *testing.T, rg *rig, c *config.Config) error {
				_, err := c.DisableAdapter(request(1, 4), "example", isOpen(t), result(new([]config.Instances), `{"ok":true}`))
				return err
			},
			state: versionState(func(c *config.Config) admin.Version { return c.Instances().Version }, 4),
		},
		{
			name:      "adapter-stage/upgrade",
			invisible: true,
			disk:      treeDisk,
			run: func(t *testing.T, rg *rig, c *config.Config) error {
				_, err := c.StageAdapter(srcPackage(t, "example", "2.0.0", nil), suffixReader())
				return err
			},
			// Staging changes nothing a restart can see: the installed version is
			// still 1.0.0 whatever the crash.
			state: func(t *testing.T, where string, c *config.Config) string {
				if ads := c.Adapters(); len(ads) != 1 || ads[0].Version != "1.0.0" {
					t.Errorf("%s: adapters = %+v, want example 1.0.0 only", where, ads)
				}
				return "before"
			},
			done: func(t *testing.T, rg *rig, c *config.Config) {
				if !exists(rg.mem, config.AdapterStageDir("example", stageSuffix)) {
					t.Error("the package was not staged")
				}
			},
		},
		{
			name:      "adapter-stage/first installation",
			invisible: true,
			disk:      treeDisk,
			run: func(t *testing.T, rg *rig, c *config.Config) error {
				_, err := c.StageAdapter(srcPackage(t, "fresh", "0.1.0", nil), suffixReader())
				return err
			},
			state: func(t *testing.T, where string, c *config.Config) string {
				if ads := c.Adapters(); len(ads) != 1 {
					t.Errorf("%s: adapters = %+v, want only example", where, ads)
				}
				return "before"
			},
			done: func(t *testing.T, rg *rig, c *config.Config) {
				if !exists(rg.mem, config.AdapterStageDir("fresh", stageSuffix)) {
					t.Error("the package was not staged")
				}
			},
		},
		{
			name:      "adapter-unstage/upgrade",
			invisible: true,
			disk:      treeDisk, prepare: prepStage("example", "2.0.0"),
			run: func(t *testing.T, rg *rig, c *config.Config) error {
				return c.Unstage(staged)
			},
			state: func(t *testing.T, where string, c *config.Config) string {
				if ads := c.Adapters(); len(ads) != 1 || ads[0].Version != "1.0.0" {
					t.Errorf("%s: adapters = %+v", where, ads)
				}
				return "before"
			},
			done: func(t *testing.T, rg *rig, c *config.Config) {
				if exists(rg.mem, config.AdapterStageDir("example", stageSuffix)) {
					t.Error("the staging directory survived Unstage")
				}
			},
		},
		{
			name:      "adapter-unstage/first installation",
			invisible: true,
			disk:      treeDisk, prepare: prepStage("fresh", "0.1.0"),
			run: func(t *testing.T, rg *rig, c *config.Config) error {
				return c.Unstage(staged)
			},
			state: func(t *testing.T, where string, c *config.Config) string {
				if ads := c.Adapters(); len(ads) != 1 {
					t.Errorf("%s: adapters = %+v", where, ads)
				}
				return "before"
			},
			done: func(t *testing.T, rg *rig, c *config.Config) {
				if exists(rg.mem, config.AdapterDir("fresh")) {
					t.Error("the adapter directory survived Unstage")
				}
			},
		},
		{
			// An upgrade whose version directory was renamed in and never made
			// current: the unstage discards it as well as the staging directory.
			name:      "adapter-unstage/version renamed in but not current",
			invisible: true,
			disk:      disabledTree,
			prepare: func(t *testing.T, rg *rig, c *config.Config) {
				staged = stagedFresh(t, c, "example", "2.0.0")
				plan, err := c.PlanInstall(staged)
				if err != nil {
					t.Fatal(err)
				}
				tx := rg.w.Begin(testAdapterInstall)
				if err := plan.Promote(tx); err != nil {
					t.Fatal(err)
				}
				tx.Close() // the rename is a preparatory write: the installation never committed
			},
			run: func(t *testing.T, rg *rig, c *config.Config) error {
				return c.Unstage(staged)
			},
			state: func(t *testing.T, where string, c *config.Config) string {
				if ads := c.Adapters(); len(ads) != 1 || ads[0].Version != "1.0.0" {
					t.Errorf("%s: adapters = %+v", where, ads)
				}
				return "before"
			},
			done: func(t *testing.T, rg *rig, c *config.Config) {
				if exists(rg.mem, config.AdapterVersionDir("example", "2.0.0")) {
					t.Error("the version directory that never became current survived Unstage")
				}
				if !exists(rg.mem, config.AdapterVersionDir("example", "1.0.0")) {
					t.Error("Unstage removed the installed version")
				}
			},
		},
		{
			name: "adapter-install/upgrade",
			disk: disabledTree, prepare: prepStage("example", "2.0.0"),
			run: func(t *testing.T, rg *rig, c *config.Config) error {
				return install(t, rg, c, staged)
			},
			state: func(t *testing.T, where string, c *config.Config) string {
				ads := c.Adapters()
				if len(ads) != 1 {
					t.Errorf("%s: adapters = %+v", where, ads)
					return "?"
				}
				switch ads[0].Version {
				case "1.0.0":
					return "before"
				case "2.0.0":
					return "after"
				}
				t.Errorf("%s: installed version %q", where, ads[0].Version)
				return "?"
			},
			done: func(t *testing.T, rg *rig, c *config.Config) {
				if exists(rg.mem, config.AdapterVersionDir("example", "1.0.0")) {
					t.Error("the previous version survived")
				}
			},
		},
		{
			name: "adapter-install/first installation",
			disk: treeDisk, prepare: prepStage("fresh", "0.1.0"),
			run: func(t *testing.T, rg *rig, c *config.Config) error {
				return install(t, rg, c, staged)
			},
			state: func(t *testing.T, where string, c *config.Config) string {
				switch len(c.Adapters()) {
				case 1:
					return "before"
				case 2:
					return "after"
				}
				t.Errorf("%s: adapters = %+v", where, c.Adapters())
				return "?"
			},
		},
		{
			name: "direct-register",
			disk: treeDisk,
			run: func(t *testing.T, rg *rig, c *config.Config) error {
				ch, err := c.DirectID("example", "fresh", bytes.NewReader(bytes.Repeat([]byte{0x61}, 16)))
				if err != nil {
					return err
				}
				tx := rg.w.Begin(testDirectRegister)
				defer tx.Close()
				done, err := c.PutEndpointIDs(tx, ch)
				if err != nil {
					return err
				}
				if err := tx.Replace("endpoint", "state/endpoint-fresh", []byte("rec"), fsx.ModePrivate); err != nil {
					return err
				}
				if err := tx.Commit(); err != nil {
					return err
				}
				done()
				return nil
			},
			state: func(t *testing.T, where string, c *config.Config) string {
				if c.EndpointIDs("example").Direct["fresh"].IsZero() {
					return "before"
				}
				if c.EndpointIDs("example").Direct["notes"] != mustEndpoint(t, ep2Text) {
					t.Errorf("%s: an existing id was lost", where)
				}
				return "after"
			},
		},
		{
			name: "runtime-admit",
			disk: func(t *testing.T) *fsxtest.Mem {
				mem := treeDisk(t)
				// An instance with no runtime endpoint id yet.
				doc := strings.Replace(goldenEndpointIDs, `  "runtime": "`+ep1Text+`",`+"\n", "", 1)
				if err := fsx.Replace(mem, setupTag, config.EndpointIDsPath("example"), []byte(doc), fsx.ModePrivate); err != nil {
					t.Fatal(err)
				}
				return mem
			},
			run: func(t *testing.T, rg *rig, c *config.Config) error {
				ch, err := c.RuntimeID("example", bytes.NewReader(bytes.Repeat([]byte{0x62}, 16)))
				if err != nil {
					return err
				}
				tx := rg.w.Begin(testRuntimeAdmit)
				defer tx.Close()
				done, err := c.PutEndpointIDs(tx, ch)
				if err != nil {
					return err
				}
				if err := tx.Replace("endpoint", "state/endpoint-runtime", []byte("rec"), fsx.ModePrivate); err != nil {
					return err
				}
				if err := tx.Commit(); err != nil {
					return err
				}
				done()
				return nil
			},
			state: func(t *testing.T, where string, c *config.Config) string {
				if c.EndpointIDs("example").Runtime.IsZero() {
					return "before"
				}
				return "after"
			},
		},
		{
			name: "backup-confirm",
			disk: treeDisk,
			run: func(t *testing.T, rg *rig, c *config.Config) error {
				b, _ := c.Backup()
				b.ConfirmedAt = b.ConfirmedAt.AddDate(0, 0, 1)
				tx := rg.w.Begin(testBackupConfirm)
				defer tx.Close()
				done, err := c.PutBackup(tx, b)
				if err != nil {
					return err
				}
				if err := tx.Replace("result", "state/backup-result", []byte("ok"), fsx.ModePrivate); err != nil {
					return err
				}
				if err := tx.Commit(); err != nil {
					return err
				}
				done()
				return nil
			},
			state: func(t *testing.T, where string, c *config.Config) string {
				b, ok := c.Backup()
				if !ok {
					t.Errorf("%s: the confirmation is gone", where)
					return "?"
				}
				switch b.ConfirmedAt.Day() {
				case 24:
					return "before"
				case 25:
					return "after"
				}
				t.Errorf("%s: confirmed at %v", where, b.ConfirmedAt)
				return "?"
			},
		},
		{
			name: "identity-retire",
			disk: treeDisk,
			run: func(t *testing.T, rg *rig, c *config.Config) error {
				tx := rg.w.Begin(testIdentityRetire)
				defer tx.Close()
				done, err := c.PutRevocation(tx, extraRec)
				if err != nil {
					return err
				}
				if err := tx.Replace("result", "state/retire-result", []byte("ok"), fsx.ModePrivate); err != nil {
					return err
				}
				if err := tx.Commit(); err != nil {
					return err
				}
				done()
				return nil
			},
			state: func(t *testing.T, where string, c *config.Config) string {
				switch len(c.Revocations()) {
				case 1:
					return "before"
				case 2:
					return "after"
				}
				t.Errorf("%s: %d revocations", where, len(c.Revocations()))
				return "?"
			},
		},
	}
}

// Every transaction of config, run from the disk it starts on, is stopped
// before each of its steps, by the process dying and by power loss in several
// fates. On the disk each leaves, the next start loads a configuration that is
// the one before the transaction or the one after it, nothing in between; the
// recovery has nothing left to do afterwards; and with a process crash the
// transaction, once visible, stays visible at every later point. The run
// itself ends in the "after" state and every injection point was hit.
func TestEveryConfigTransactionSurvivesACrashAtEveryPoint(t *testing.T) {
	for _, tc := range crashCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			mem := tc.disk(t)
			var rg *rig
			var c *config.Config

			seen := ""
			visible := false
			checks := 0
			w := newWalker(mem, 11, func(where string, clone *fsxtest.Mem, processCrash bool) {
				checks++
				c2, err := restart(t, clone)
				if err != nil {
					t.Errorf("%s: the next start refuses the tree: %v", where, err)
					return
				}
				got := tc.state(t, where, c2)
				if processCrash {
					if seen == "after" && got == "before" {
						t.Errorf("%s: the transaction was visible at an earlier point and is gone", where)
					}
					seen = got
					visible = visible || got == "after"
				}
				if work := mustCleanup(t, clone); len(work) != 0 {
					t.Errorf("%s: recovery work left after a start: %v", where, work)
				}
				if work, err := config.AdapterGCWork(clone); err != nil || len(work) != 0 {
					t.Errorf("%s: adapter gc work left after a start: %v, %v", where, work, err)
				}
				// Starting again changes nothing.
				c3, err := config.Load(txn.New(clone, newTable(t), nil))
				if err != nil || tc.state(t, where, c3) != got {
					t.Errorf("%s: a second start reads another state: %v", where, err)
				}
			})
			w.paused = true
			rg = onDisk(t, mem, w)
			c = rg.load()
			if tc.prepare != nil {
				tc.prepare(t, rg, c)
			}
			w.paused = false

			if err := tc.run(t, rg, c); err != nil {
				t.Fatalf("the transaction failed without a crash: %v", err)
			}
			if len(w.points) == 0 || checks == 0 {
				t.Fatal("no step of the transaction was reached: the crash injection did not happen")
			}
			t.Logf("%d steps, %d crash states examined", len(w.points), checks)
			if !tc.invisible && !visible {
				t.Error("the transaction never became visible at any point")
			}
			final, err := restart(t, mem.Clone())
			if err != nil {
				t.Fatalf("restart after the run: %v", err)
			}
			want := "after"
			if tc.invisible {
				want = "before"
			}
			if got := tc.state(t, "after the run", final); got != want {
				t.Errorf("the run ended in state %q, want %s", got, want)
			}
			if tc.done != nil {
				tc.done(t, rg, c)
			}
		})
	}
}

func mustCleanup(t testing.TB, mem *fsxtest.Mem) []fsx.Leftover {
	t.Helper()
	work, err := txn.CleanupWork(mem)
	if err != nil {
		t.Fatal(err)
	}
	return work
}

// A crash in the middle of the repair itself: the repair removes directories
// the pointer does not name, in a transaction of the Recovery kind, and the
// start after a crash in it finds the work finished or left, and finishes it.
// What the pointer names is never touched.
func TestCrashDuringAdapterGCReplay(t *testing.T) {
	dirty := func(t *testing.T) *fsxtest.Mem {
		mem := treeDisk(t)
		for _, d := range []string{
			config.AdapterStageDir("example", "beef"),
			config.AdapterVersionDir("example", "0.9.0"),
			config.AdapterStageDir("half", "cafe"),
		} {
			mkdirAll(t, mem, d)
			if err := fsx.Replace(mem, setupTag, path.Join(d, "payload"), []byte("p"), fsx.ModePrivate); err != nil {
				t.Fatal(err)
			}
		}
		return mem
	}
	mem := dirty(t)
	want := snapshot(t, treeDisk(t)) // the clean tree: what the repair has to leave

	checks := 0
	w := newWalker(mem, 21, func(where string, clone *fsxtest.Mem, processCrash bool) {
		checks++
		c2, err := restart(t, clone)
		if err != nil {
			t.Errorf("%s: the start after a crash in the repair refuses the tree: %v", where, err)
			return
		}
		if ads := c2.Adapters(); len(ads) != 1 || ads[0].Version != "1.0.0" {
			t.Errorf("%s: adapters = %+v", where, ads)
		}
		if got := snapshot(t, clone); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: the repair left %v", where, diffKeys(want, got))
		}
		if work, _ := config.AdapterGCWork(clone); len(work) != 0 {
			t.Errorf("%s: gc work left: %v", where, work)
		}
		for _, p := range []string{config.AdapterManifestPath("example", "1.0.0"), config.AdapterCurrentPath("example")} {
			if !exists(clone, p) {
				t.Errorf("%s: the repair removed %s, which the pointer names", where, p)
			}
		}
	})
	rg := onDisk(t, mem, w)
	if _, err := config.Load(rg.w); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if checks == 0 {
		t.Fatal("the repair ran no step: nothing was injected")
	}
	for _, p := range w.points {
		if !strings.HasPrefix(p, "adapter-gc/") {
			t.Errorf("loading reached %s: the only write of a load is the repair", p)
		}
	}
	if got := snapshot(t, mem); !reflect.DeepEqual(got, want) {
		t.Errorf("the repair left %v", diffKeys(want, got))
	}
}

// init is stopped before each of its steps. After every crash the init that
// runs again completes: it reuses what the crashed one wrote, and the
// configuration it leaves loads and is the one init writes on an empty disk.
func TestInitSurvivesACrashAtEveryPoint(t *testing.T) {
	fresh := newRig(t)
	if err := runInit(fresh); err != nil {
		t.Fatal(err)
	}
	want := snapshot(t, fresh.mem)

	mem := fsxtest.NewMem()
	checks := 0
	w := newWalker(mem, 31, func(where string, clone *fsxtest.Mem, processCrash bool) {
		checks++
		w := txn.New(clone, newTable(t), nil)
		if err := w.Recover(); err != nil {
			t.Fatalf("%s: Recover: %v", where, err)
		}
		again := &rig{t: t, mem: clone, tab: newTable(t), w: w}
		if err := runInit(again); err != nil {
			t.Errorf("%s: init does not complete after a crash: %v", where, err)
			return
		}
		if got := snapshot(t, clone); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: init after a crash left %v", where, diffKeys(want, got))
		}
		if _, err := config.Load(w); err != nil {
			t.Errorf("%s: the configuration init left does not load: %v", where, err)
		}
	})
	rg := onDisk(t, mem, w)
	if err := runInit(rg); err != nil {
		t.Fatalf("init: %v", err)
	}
	if checks == 0 {
		t.Fatal("init reached no step")
	}
	if got := snapshot(t, mem); !reflect.DeepEqual(got, want) {
		t.Errorf("init left %v", diffKeys(want, got))
	}
}

// What the owner wrote before init is still there after a crash of init at any
// point, in the sense that matters: a valid file the owner wrote is never in
// any crash state replaced.
func TestInitCrashNeverReplacesAnOwnersFile(t *testing.T) {
	handPresets := `{"format":1,"version":7,"presets":{"mine":{"local_sources":"deny"}}}`
	mem := fsxtest.NewMem()
	putTree(t, mem, map[string]string{config.PresetsPath: handPresets})
	checks := 0
	w := newWalker(mem, 41, func(where string, clone *fsxtest.Mem, processCrash bool) {
		checks++
		got, err := clone.ReadFile(config.PresetsPath)
		if err != nil || string(got) != handPresets {
			t.Errorf("%s: presets.json = %q, %v: the owner's file was replaced", where, got, err)
		}
	})
	rg := onDisk(t, mem, w)
	if err := runInit(rg); err != nil {
		t.Fatalf("init: %v", err)
	}
	if checks == 0 {
		t.Fatal("init reached no step")
	}
}

// Reaching a point is not crashing at it: every crash point config declares has
// had a crash state taken at it and judged, of every kind of crash, by one of
// the walks above (a point that only a recovery after another transaction's
// crash reaches needs a case of its own that starts from that state). The
// points of the transaction layer itself, the cleanup and the settle of every
// start, are walked by the tests of txn and store, and the transactions the
// tests compose from config's write functions are declared by the tests.
//
// It runs after the walks, in source order, and says nothing on a run that
// selected none of them.
func TestEveryDeclaredCrashPointIsCrashedAtUnderEveryKindOfCrash(t *testing.T) {
	crashed.Lock()
	defer crashed.Unlock()
	if len(crashed.taken) == 0 {
		t.Skip("no walk ran in this run: nothing to assert about")
	}
	tab, err := txn.NewTable(append(txn.Internal(), config.Transactions()...)...)
	if err != nil {
		t.Fatal(err)
	}
	declared := map[string]bool{}
	for _, d := range config.Transactions() {
		declared[d.Name] = true
	}
	var gaps []string
	checked := 0
	for _, k := range tab.Keys() {
		if !declared[k.Tag.Tx] {
			continue
		}
		checked++
		for _, m := range crashModels {
			if crashed.taken[k.Key][m] == 0 {
				gaps = append(gaps, fmt.Sprintf("%s under %s", k.Key, m))
			}
		}
	}
	if checked == 0 {
		t.Fatal("no key of config was checked")
	}
	sort.Strings(gaps)
	if len(gaps) > 0 {
		t.Errorf("%d crash points of config have no crash state taken at them, for the kind of crash named:\n%s", len(gaps), strings.Join(gaps, "\n"))
	}
}
