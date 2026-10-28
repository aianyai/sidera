package harness_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent/engineagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/internal/node/txn"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

type startupObserverFunc func(fsx.Observation, txn.Kind)

func (f startupObserverFunc) Reached(observation fsx.Observation, kind txn.Kind) {
	f(observation, kind)
}

func TestAControlledStartStopsSelectingWhenItBeginsServing(t *testing.T) {
	var observed atomic.Int64
	h := harness.NewStopped(t, harness.Options{Observer: startupObserverFunc(func(fsx.Observation, txn.Kind) {
		observed.Add(1)
	})})
	ctx, cancel := context.WithCancel(bg())
	defer cancel()
	selected := 0
	crashed, err := h.TryStart(ctx, harness.StartOptions{CrashAt: func(fsx.Point, txn.Kind) (harness.CrashMode, bool) {
		selected++
		return harness.CrashMode{}, false
	}})
	if crashed || err != nil || selected == 0 {
		t.Fatalf("controlled start = (%v, %v), selected %d points", crashed, err, selected)
	}
	calls, raw := selected, observed.Load()
	cancel() // the caller bounds startup, not a successfully serving generation
	h.MustDirect("after")
	if selected != calls {
		t.Error("startup selection continued into serving")
	}
	if observed.Load() <= raw {
		t.Error("disabling startup selection disabled the original program observer")
	}
}

func TestAControlledStartCanCrashTransactionRecoveryAndRestartOnItsSnapshot(t *testing.T) {
	h := harness.NewStopped(t, harness.Options{})
	if err := h.Quiesce(); !errors.Is(err, engineagent.ErrNodeNotRunning) {
		t.Fatalf("a newly stopped fixture is running: %v", err)
	}
	h.Start()
	leaveStartupDebris(t, h)
	old := h.Disk()
	var atCut map[string]string
	calls := 0
	crashed, err := h.TryStart(bg(), harness.StartOptions{CrashAt: func(p fsx.Point, kind txn.Kind) (harness.CrashMode, bool) {
		calls++
		if p.Tag.Tx != "txn-cleanup" || p.Step != fsx.StepRemove {
			return harness.CrashMode{}, false
		}
		if kind != txn.Recovery {
			t.Errorf("cleanup has kind %v, want Recovery", kind)
		}
		atCut = snapshotOf(t, old)
		return harness.ProcessCrash.At(p.String()), true
	}})
	if !crashed || err != nil || atCut == nil {
		t.Fatalf("controlled start = (%v, %v), reached = %v", crashed, err, atCut != nil)
	}
	if got := snapshotOf(t, old); !reflect.DeepEqual(got, atCut) {
		t.Error("transaction recovery changed the abandoned disk after the cutoff")
	}
	if h.Disk() == old || !reflect.DeepEqual(snapshot(t, h), atCut) {
		t.Error("the next generation did not receive the cutoff snapshot")
	}
	if err := h.Quiesce(); !errors.Is(err, engineagent.ErrNodeNotRunning) {
		t.Fatalf("the crashed start was published: %v", err)
	}
	selected := calls
	h.Start()
	got := h.MustDirect("after")
	// Generation two died before an engine existed, but still consumes its seed.
	// A first generation with seed three therefore draws the same identifiers
	// as this fixture's third generation with seed one.
	reference := harness.New(t, harness.Options{Seed: 3})
	if want := reference.MustDirect("after"); got != want {
		t.Errorf("the third generation drew endpoint %s, want %s", got, want)
	}
	if calls != selected {
		t.Error("the startup selector survived its attempt")
	}
	if work, err := txn.CleanupWork(h.Disk()); err != nil || len(work) != 0 {
		t.Errorf("the third generation did not finish recovery: %v, %v", work, err)
	}
	log, err := h.Ledger()
	if err != nil || len(log.Kind(ledger.KindFixtureCrash)) != 1 {
		t.Fatalf("crash ledger = %v, %v", log, err)
	}
}

// These are startup boundaries with different resource ownership: Open runs
// before the engine loop, whereas tick runs with the engine and host started.
// A commit observer has no subsequent filesystem call to report the cutoff.
func TestAControlledStartReclaimsEachRecoveryStageBeforeTheNextGeneration(t *testing.T) {
	for _, tc := range []struct {
		name, tx string
		step     fsx.Step
		prepare  func(*testing.T, *harness.H)
	}{
		{"transaction commit", "txn-cleanup", fsx.StepCommit, leaveStartupDebris},
		{"scan write", "quarantine", fsx.StepWrite, damageStartupEndpoint},
		{"scan commit", "quarantine", fsx.StepCommit, damageStartupEndpoint},
		{"delivery write", "tick", fsx.StepWrite, expireStartupMessage},
		{"delivery commit", "tick", fsx.StepCommit, expireStartupMessage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := harness.New(t, harness.Options{Adapter: &harness.AdapterOptions{}})
			tc.prepare(t, h)
			old := h.Disk()
			var atCut map[string]string
			calls := 0
			crashed, err := h.TryStart(bg(), harness.StartOptions{CrashAt: func(p fsx.Point, _ txn.Kind) (harness.CrashMode, bool) {
				calls++
				if p.Tag.Tx != tc.tx || p.Step != tc.step {
					return harness.CrashMode{}, false
				}
				atCut = snapshotOf(t, old)
				return harness.PowerLoss(7).At(p.String()), true
			}})
			if !crashed || err != nil || atCut == nil {
				t.Fatalf("controlled start = (%v, %v), reached = %v", crashed, err, atCut != nil)
			}
			if got := snapshotOf(t, old); !reflect.DeepEqual(got, atCut) {
				t.Error("the abandoned generation wrote after its cutoff")
			}
			if err := h.Quiesce(); !errors.Is(err, engineagent.ErrNodeNotRunning) {
				t.Fatalf("a killed generation was published: %v", err)
			}
			launches := h.AdapterLaunches(harness.Instance)
			for _, launch := range launches {
				if !launch.Ended() {
					t.Fatal("TryStart returned with a live adapter from the failed generation")
				}
			}
			if tc.tx == "tick" {
				if len(launches) != 2 {
					t.Fatalf("delivery recovery had %d launches, want two generations", len(launches))
				}
				if slices.Contains(launches[1].Methods(), adapterproto.MethodShutdown) {
					t.Error("the failed generation sent shutdown instead of aborting")
				}
			}
			selected := calls
			h.Start()
			if err := h.Quiesce(); err != nil {
				t.Fatalf("the third generation cannot recover: %v", err)
			}
			if calls != selected {
				t.Error("the startup selector survived its attempt")
			}
			latest := h.AdapterLaunches(harness.Instance)
			if latest[len(latest)-1].Ended() {
				t.Error("cleanup of the failed generation cut the next generation's adapter")
			}
			log, err := h.Ledger()
			if err != nil || len(log.Kind(ledger.KindFixtureCrash)) != 1 {
				t.Fatalf("crash ledger = %v, %v", log, err)
			}
		})
	}
}

func leaveStartupDebris(t *testing.T, h *harness.H) {
	t.Helper()
	h.Stop()
	f, err := h.Disk().CreateTemp(fsx.Tag{Tx: "offline", Role: "debris"}, "state", fsx.ModePrivate)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func damageStartupEndpoint(t *testing.T, h *harness.H) {
	t.Helper()
	h.MustDirect("damaged")
	h.Stop()
	for p := range snapshot(t, h) {
		if strings.HasPrefix(p, "state/endpoints/") {
			if err := fsx.Replace(h.Disk(), fsx.Tag{Tx: "offline", Role: "damage"}, p, []byte("broken"), fsx.ModePrivate); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatal("no endpoint to damage")
}

func expireStartupMessage(t *testing.T, h *harness.H) {
	t.Helper()
	h.MustDirect("a")
	h.MustDirect("b")
	if _, err := h.Agent("a").Send(bg(), fakeagent.SendInput{To: []string{h.Addr("b")}, Body: "expire while down", AcceptBeforeS: 120}); err != nil {
		t.Fatal(err)
	}
	if err := h.Quiesce(); err != nil {
		t.Fatal(err)
	}
	page, err := h.Agent("b").Recv(bg(), fakeagent.RecvInput{})
	if err != nil || len(page.Page.Messages) == 0 {
		t.Fatalf("cannot leave an outstanding lease for recovery: %v, %v", page, err)
	}
	h.Stop()
	h.Clock().Advance(delivery.LeaseDuration + time.Minute)
}

func TestAControlledStartReturnsOrdinaryErrorsAndCancellationWithoutACrash(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tx      string
		prepare func(*testing.T, *harness.H)
	}{
		{"transaction", "txn-cleanup", leaveStartupDebris},
		{"scan", "quarantine", damageStartupEndpoint},
		{"delivery", "tick", expireStartupMessage},
	} {
		for _, cancel := range []bool{false, true} {
			name := tc.name + "/error"
			if cancel {
				name = tc.name + "/cancel"
			}
			t.Run(name, func(t *testing.T) {
				h := harness.New(t, harness.Options{Adapter: &harness.AdapterOptions{}})
				tc.prepare(t, h)
				old := h.Disk()
				ctx, end := context.WithCancel(bg())
				defer end()
				want := errors.New("startup storage failure")
				o := harness.StartOptions{}
				if cancel {
					want = context.Canceled
					o.CrashAt = func(p fsx.Point, _ txn.Kind) (harness.CrashMode, bool) {
						if p.Tag.Tx == tc.tx && p.Step == fsx.StepCommit {
							end()
						}
						return harness.CrashMode{}, false
					}
				} else {
					o.Arm = func(f *fsxtest.Faulty) {
						// A directory sync follows the transaction's commit step,
						// so it is fatal and cannot be retried as a normal tick.
						f.Fail(fsxtest.Match{Tx: tc.tx, Step: fsx.StepSyncDir}, want)
					}
				}
				crashed, err := h.TryStart(ctx, o)
				if crashed || !errors.Is(err, want) {
					t.Fatalf("controlled start = (%v, %v), want (false, %v)", crashed, err, want)
				}
				if h.Disk() != old {
					t.Error("an ordinary startup failure replaced the disk with a crash snapshot")
				}
				if err := h.Quiesce(); !errors.Is(err, engineagent.ErrNodeNotRunning) {
					t.Fatalf("a failed start was published: %v", err)
				}
				for _, launch := range h.AdapterLaunches(harness.Instance) {
					if !launch.Ended() {
						t.Error("a failed start left its adapter alive")
					}
				}
				log, err := h.Ledger()
				if err != nil || len(log.Kind(ledger.KindFixtureCrash)) != 0 {
					t.Fatalf("an ordinary failure was counted as a crash: %v, %v", log, err)
				}
				h.Start()
				if err := h.Quiesce(); err != nil {
					t.Fatalf("resources were not reclaimed before the retry: %v", err)
				}
			})
		}
	}
}
