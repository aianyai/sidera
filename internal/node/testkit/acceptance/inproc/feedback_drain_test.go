package inproc

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
)

// drainTrace delegates every operation to the real fixture. Holding Advance's
// return through its due work exposes a real clock-write cut before that return.
// The clock and record audits below, rather than this trace, judge convergence.
type drainTrace struct {
	*harness.H
	waitBeforeAdvanceReturn bool
	events                  *[]drainEvent
}

// advanceInterrupted carries only the real fixture's Quiesce failure. Unwinding
// prevents the recipe from doing bookkeeping after an advance that never returned.
type advanceInterrupted struct{ err error }

type drainEvent struct {
	operation string
	at        time.Time
	crashes   int
}

func (e *drainTrace) note(operation string) {
	log, err := e.H.Ledger()
	if err == nil {
		*e.events = append(*e.events, drainEvent{operation, e.Clock().Now(), len(log.Kind(ledger.KindFixtureCrash))})
	}
}

func (e *drainTrace) Advance(d time.Duration) {
	e.note("advance")
	e.H.Advance(d)
	if e.waitBeforeAdvanceReturn {
		if err := e.H.Quiesce(); err != nil {
			e.note("advance-interrupted")
			panic(advanceInterrupted{err: err})
		}
	}
	e.note("advance-return")
}

func (e *drainTrace) drain(run func(scenario.Env) error) (err error) {
	defer func() {
		if interrupted := recover(); interrupted != nil {
			cut, ok := interrupted.(advanceInterrupted)
			if !ok {
				panic(interrupted)
			}
			// Preserve the real failure for WalkBackend's selected-cut handling.
			err = cut.err
		}
	}()
	return run(e)
}

func (e *drainTrace) Quiesce(opts ...scenario.QuiesceOption) error {
	e.note("quiesce")
	err := e.H.Quiesce(opts...)
	if err == nil {
		e.note("quiet")
	}
	return err
}

func feedbackScenario(t *testing.T, name string, waitBeforeReturn bool, events *[]drainEvent, final *time.Time) *harness.WalkScenario {
	t.Helper()
	b := newBindings()
	// Domain bindings resolve the actual process; only Env timing is wrapped.
	actualRuntimeAddr, actualSession := b.Sessions.RuntimeAddr, b.Sessions.Session
	unwrap := func(e scenario.Env) scenario.Env {
		if traced, ok := e.(*drainTrace); ok {
			return traced.H
		}
		return e
	}
	b.Sessions.RuntimeAddr = func(e scenario.Env, instance string) string { return actualRuntimeAddr(unwrap(e), instance) }
	b.Sessions.Session = func(e scenario.Env, endpoint string) fakeagent.Agent { return actualSession(unwrap(e), endpoint) }
	for _, r := range recipes.New(b).Scripts {
		if r.Script.Name != name {
			continue
		}
		return &harness.WalkScenario{Script: r.Script, Options: recipeOptions(r.Needs), Drain: func(h *harness.H) error {
			traced := &drainTrace{H: h, waitBeforeAdvanceReturn: waitBeforeReturn, events: events}
			return traced.drain(r.Drain)
		}, Check: func(h *harness.H) error {
			*final = h.Clock().Now()
			if err := r.Check(h); err != nil {
				return err
			}
			return b.Recovery.CheckClosedRecords(h)
		}}
	}
	t.Fatalf("unknown feedback recipe %q", name)
	return nil
}

func TestFeedbackDrainResumesAnIssuedMinute(t *testing.T) {
	for _, waitBeforeReturn := range []bool{true, false} {
		t.Run(fmt.Sprintf("advance-return-held=%t", waitBeforeReturn), func(t *testing.T) {
			first := crashwalk.Target{Gen: 1, AfterStep: "queue-deadline"}
			var baselineEvents []drainEvent
			var baselineTime time.Time
			backend := harness.NewWalkBackend(t)
			baseline, err := backend.Run(context.Background(), feedbackScenario(t, "delivery-expiry", waitBeforeReturn, &baselineEvents, &baselineTime), crashwalk.RunPlan{Targets: []crashwalk.Target{first}})
			if err != nil {
				t.Fatal(err)
			}
			if len(baseline.Hits) != 2 || len(baseline.Violations) != 0 {
				t.Fatalf("feedback baseline: generations=%d violations=%v", len(baseline.Hits), baseline.Violations)
			}
			point, err := fsx.ParsePoint("clock-high-water/clock.create#1")
			if err != nil {
				t.Fatal(err)
			}
			second := crashwalk.Target{Gen: 2, Point: point}
			var events []drainEvent
			var final time.Time
			r, err := backend.Run(context.Background(), feedbackScenario(t, "delivery-expiry", waitBeforeReturn, &events, &final), crashwalk.RunPlan{Targets: []crashwalk.Target{first, second}})
			if err != nil {
				t.Fatal(err)
			}
			if waitBeforeReturn {
				interruptedBeforeReturn := false
				for _, event := range events {
					if event.operation == "advance-interrupted" {
						interruptedBeforeReturn = event.crashes == 2
						break
					}
					if event.operation == "advance-return" {
						t.Fatalf("Advance returned before the real clock cut unwound its caller: %+v", events)
					}
				}
				if !interruptedBeforeReturn {
					t.Fatalf("real clock cut did not precede Advance's return: %+v", events)
				}
			}
			assertDeclaredWrites(t, r)
			if len(r.Reached) != 2 || !r.Reached[0] || !r.Reached[1] {
				t.Fatalf("feedback cuts reached=%v", r.Reached)
			}
			r.Reached = r.Reached[1:]
			if err := crashwalk.CheckRecoveryTarget(baseline.Hits[1], second, r); err != nil {
				t.Fatal(err)
			}
			if !final.Equal(baselineTime) {
				t.Fatalf("resumed feedback advanced again: final=%s uninterrupted=%s events=%+v", final, baselineTime, events)
			}
			// The first minute's actual clock write was interrupted. Reentry must
			// finish quiescing that minute before issuing the second one.
			quietAtFirstMinute := false
			for _, event := range events {
				if event.crashes != 2 {
					continue
				}
				if event.operation == "quiet" && event.at.Equal(baselineTime.Add(-time.Minute)) {
					quietAtFirstMinute = true
				}
				if event.operation == "advance" {
					if !quietAtFirstMinute {
						t.Fatalf("resumed advance preceded completion of interrupted minute: %+v", events)
					}
					break
				}
			}
			if !quietAtFirstMinute {
				t.Fatalf("interrupted minute was not quiesced on reentry: %+v", events)
			}
		})
	}
}

func TestFeedbackDrainStagesHaveIndependentMinutes(t *testing.T) {
	var events []drainEvent
	var final time.Time
	s := feedbackScenario(t, "delivery-register-declare", false, &events, &final)
	r, err := harness.NewWalkBackend(t).Run(context.Background(), s, crashwalk.RunPlan{Targets: []crashwalk.Target{{Gen: 1, AfterStep: "create"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Hits) != 2 || len(r.Reached) != 1 || !r.Reached[0] || len(r.Violations) != 0 {
		t.Fatalf("independent phases reached=%v generations=%d violations=%v", r.Reached, len(r.Hits), r.Violations)
	}
	if want := harness.DefaultStart.Add(4 * time.Minute); !final.Equal(want) {
		t.Fatalf("creation resolution and post-receive feedback need two minutes each: final=%s want=%s", final, want)
	}
}

func TestFeedbackDrainResumesReceiveAndAcknowledgement(t *testing.T) {
	first := crashwalk.Target{Gen: 1, AfterStep: "report-deadline"}
	var events []drainEvent
	var baselineTime time.Time
	backend := harness.NewWalkBackend(t)
	baseline, err := backend.Run(context.Background(), feedbackScenario(t, "delivery-unreachable", false, &events, &baselineTime), crashwalk.RunPlan{Targets: []crashwalk.Target{first}})
	if err != nil {
		t.Fatal(err)
	}
	// Discover the actual receive and acknowledgement transactions from this
	// recipe's complete second generation instead of manufacturing feedback.
	for _, tx := range []string{"lease", "ack"} {
		t.Run(tx, func(t *testing.T) {
			var second crashwalk.Target
			for _, hit := range baseline.Hits[1] {
				if hit.Point.Tag.Tx == tx && !crashwalk.IsSync(hit.Point.Step) {
					second = crashwalk.Target{Gen: 2, Point: hit.Point}
					break
				}
			}
			if second.Gen != 2 {
				t.Fatalf("feedback drain did not record %s", tx)
			}
			var final time.Time
			var resumedEvents []drainEvent
			r, err := backend.Run(context.Background(), feedbackScenario(t, "delivery-unreachable", false, &resumedEvents, &final), crashwalk.RunPlan{Targets: []crashwalk.Target{first, second}})
			if err != nil {
				t.Fatal(err)
			}
			assertDeclaredWrites(t, r)
			if len(r.Reached) != 2 || !r.Reached[0] || !r.Reached[1] {
				t.Fatalf("feedback cuts reached=%v", r.Reached)
			}
			r.Reached = r.Reached[1:]
			if err := crashwalk.CheckRecoveryTarget(baseline.Hits[1], second, r); err != nil {
				t.Fatal(err)
			}
			if !final.Equal(baselineTime) {
				t.Fatalf("receive/ack replay repeated completed minutes: final=%s want=%s", final, baselineTime)
			}
		})
	}
}

func TestInstanceFeedbackSurvivesEveryRecoveryClockWrite(t *testing.T) {
	const name = "management-instance"
	point, err := fsx.ParsePoint("instance-drain/endpoint.create#1")
	if err != nil {
		t.Fatal(err)
	}
	trigger := crashwalk.CrashAt(point)
	clean, err := recordScenario(t, freshScenario(t, name))
	if err != nil {
		t.Fatal(err)
	}
	backend := harness.NewWalkBackend(t)
	baseline, err := backend.Run(context.Background(), freshScenario(t, name), crashwalk.RunPlan{Trigger: &trigger})
	if err != nil {
		t.Fatal(err)
	}
	if err := crashwalk.CheckRecoveryRecord(clean.Hits[0], trigger, baseline); err != nil {
		t.Fatal(err)
	}
	clockTargets := 0
	for _, hit := range baseline.Hits[1] {
		if hit.Point.Tag.Tx != "clock-high-water" || crashwalk.IsSync(hit.Point.Step) {
			continue
		}
		clockTargets++
		t.Run(hit.Point.String(), func(t *testing.T) {
			target := crashwalk.Target{Gen: 2, Point: hit.Point}
			r, err := backend.Run(context.Background(), freshScenario(t, name), crashwalk.RunPlan{Trigger: &trigger, Targets: []crashwalk.Target{target}})
			if err != nil {
				t.Fatal(err)
			}
			assertDeclaredWrites(t, r)
			if err := crashwalk.CheckRecoveryTrigger(clean.Hits[0], trigger, r); err != nil {
				t.Fatal(err)
			}
			if err := crashwalk.CheckRecoveryTarget(baseline.Hits[1], target, r); err != nil {
				t.Fatal(err)
			}
		})
	}
	if clockTargets == 0 {
		t.Fatal("instance feedback recorded no real recovery clock write")
	}
	t.Logf("interrupted every recovery clock write: targets=%d", clockTargets)
}
