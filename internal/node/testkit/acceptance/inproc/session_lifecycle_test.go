package inproc

import (
	"context"
	"testing"

	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
)

func TestSessionLifecycleRequiresItsOwnNativeClosure(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		interrupted, unrelatedClose bool
	}{
		{name: "clean"},
		{name: "another-native-closed", interrupted: true, unrelatedClose: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBindings()
			b.Sessions.Session = func(e scenario.Env, endpoint string) fakeagent.Agent {
				if wrapped, ok := e.(unrelatedClosedNativeEnv); ok {
					e = wrapped.H
				}
				return e.(*harness.H).Session(endpoint)
			}
			for _, r := range recipes.New(b).Scripts {
				if r.Script.Name != "session-lifecycle" {
					continue
				}
				s := &harness.WalkScenario{Script: r.Script, Options: recipeOptions(r.Needs), Check: func(h *harness.H) error { return r.Check(h) }, Drain: func(h *harness.H) error {
					if tc.unrelatedClose {
						return r.Drain(unrelatedClosedNativeEnv{h})
					}
					return r.Drain(h)
				}}
				plan := crashwalk.RunPlan{Record: true}
				generations := 1
				if tc.interrupted {
					plan = crashwalk.RunPlan{Targets: []crashwalk.Target{{Gen: 1, AfterStep: "send-for-prompt"}}}
					generations = 2
				}
				result, err := harness.NewWalkBackend(t).Run(context.Background(), s, plan)
				if err != nil {
					t.Fatal(err)
				}
				if len(result.Hits) != generations || len(result.Violations) != 0 {
					t.Fatalf("generations=%d violations=%v", len(result.Hits), result.Violations)
				}
				if tc.interrupted && (len(result.Reached) != 1 || !result.Reached[0]) {
					t.Fatalf("target not reached: %v", result.Reached)
				}
				return
			}
			t.Fatal("session-lifecycle recipe is missing")
		})
	}
}

// Only the drain sees this unrelated close. The original final judgement reads
// the actual ledger, so the pending bodies still need real receipt evidence.
type unrelatedClosedNativeEnv struct{ *harness.H }

func (e unrelatedClosedNativeEnv) Ledger() (*ledger.Log, error) {
	log, err := e.H.Ledger()
	if err != nil {
		return nil, err
	}
	copy := ledger.NewMemory()
	for _, entry := range log.Entries() {
		writer, err := copy.Open(entry.Writer, int(entry.PID), e.Clock().Now)
		if err != nil {
			return nil, err
		}
		if err := writer.Append(entry.Kind, entry.Attrs); err != nil {
			return nil, err
		}
		if err := writer.Close(); err != nil {
			return nil, err
		}
	}
	writer, err := copy.Open("runtime/another-native", 0, e.Clock().Now)
	if err != nil {
		return nil, err
	}
	if err := writer.Append(ledger.KindRuntimeClosed, ledger.Attrs{}); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return copy.Log()
}
