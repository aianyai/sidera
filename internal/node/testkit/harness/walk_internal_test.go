package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
	"github.com/aianyai/sidera/internal/node/txn"
)

type walkObserverFunc func(fsx.Observation, txn.Kind)

func (f walkObserverFunc) Reached(o fsx.Observation, k txn.Kind) { f(o, k) }

// The old config still holds its original writer. Calling it during the next
// generation reaches the actual observation chain, then fails at the old disk
// cutoff. The raw observer sees the attempt, but neither trace may include it.
func TestWalkBackendKeepsALateOldWriterOutOfTheNextGenerationsWindow(t *testing.T) {
	var old *config.Config
	var attempted atomic.Bool
	s := &WalkScenario{
		Options: Options{Observer: walkObserverFunc(func(o fsx.Observation, _ txn.Kind) {
			if o.Point.Tag.Tx == "node-set" {
				attempted.Store(true)
			}
		})},
		Script: scenario.Script{
			Name: "late old writer", Setup: scenario.Setup{Direct: []string{"a", "b"}},
			Steps: []scenario.Step{{Name: "send", Do: func(e scenario.Env) error {
				h := e.(*H)
				old = h.Config()
				_, err := e.Agent("a").Send(e.Ctx(), fakeagent.SendInput{To: []string{e.Addr("b")}, Body: "cut off"})
				return err
			}}},
		},
		Drain: func(h *H) error {
			req := config.Request{Key: admin.KeyFor(admin.LocalOwner(), admin.OperationID{99}), Expected: old.Node().Version}
			_, err := old.SetNodeSettings(req, []byte(`{"archive_retention_days":1}`), func(config.Node) (json.RawMessage, error) {
				return json.RawMessage(`{}`), nil
			})
			if !errors.Is(err, errCutOff) {
				return fmt.Errorf("the old config did not reach its abandoned disk: %w", err)
			}
			return nil
		},
	}
	p, err := fsx.ParsePoint("intake/out.rename#1")
	if err != nil {
		t.Fatal(err)
	}
	res, err := NewWalkBackend(t).Run(context.Background(), s, crashwalk.RunPlan{Targets: []crashwalk.Target{{Gen: 1, Point: p}}})
	if err != nil {
		t.Fatal(err)
	}
	if !attempted.Load() {
		t.Fatal("the raw observer did not see the old writer attempt")
	}
	if len(res.Hits) != 2 || !res.Reached[0] {
		t.Fatalf("did not restart: %+v", res)
	}
	for _, gen := range res.Hits {
		for _, hit := range gen {
			if hit.Point.Tag.Tx == "node-set" {
				t.Fatalf("a late old callback entered a recording: %+v", hit)
			}
		}
	}
}

func TestWalkBackendReturnsLedgerJudgementsAndHonorsScriptWaivers(t *testing.T) {
	for _, waived := range []bool{false, true} {
		t.Run(fmt.Sprint("waived=", waived), func(t *testing.T) {
			s := &WalkScenario{Script: scenario.Script{
				Name: "duplicate prompt",
				Steps: []scenario.Step{{Name: "duplicate", Do: func(e scenario.Env) error {
					h := e.(*H)
					attrs := ledger.Attrs{Attempt: "one-attempt", TextDigest: ledger.Digest([]byte("prompt"))}
					for range 2 {
						if err := h.fixture.Append(ledger.KindRuntimePromptReceived, attrs); err != nil {
							return err
						}
					}
					return nil
				}}},
			}}
			if waived {
				s.Script.Expect.Waive = map[scenario.Judgement]string{scenario.JudgePromptOncePerAttempt: "The script deliberately records the same effect twice."}
			}
			res, err := NewWalkBackend(t).Run(context.Background(), s, crashwalk.RunPlan{Record: true})
			if err != nil {
				t.Fatal(err)
			}
			var want []string
			if !waived {
				want = []string{"the ledger breaks judgement prompt-effect-once-per-attempt: prompt attempt one-attempt reached a runtime 2 times"}
			}
			if !reflect.DeepEqual(res.Violations, want) {
				t.Fatalf("ledger violations %v, want %v", res.Violations, want)
			}
		})
	}
}

// The script can fail while a background engine command is held at its target.
// Drive the common exit with the script's error already received, so the test
// covers that ownership boundary without depending on select's random choice.
func TestWalkBackendReleasesAClaimedCallbackWhenWorkAlreadyFailed(t *testing.T) {
	g := newWalkGeneration(1, nil)
	point, err := fsx.ParsePoint("intake/out.rename#1")
	if err != nil {
		t.Fatal(err)
	}
	g.target = crashwalk.Target{Gen: 1, Point: point}
	h := NewStopped(t, Options{})
	if crashed, err := h.tryStart(context.Background(), StartOptions{}, g); crashed || err != nil {
		t.Fatalf("start: %v, %v", crashed, err)
	}
	g.serving()
	h.MustDirect("a")
	h.MustDirect("b")
	if err := h.Quiesce(); err != nil {
		t.Fatal(err)
	}
	old := h.current()
	g.openWindow()
	sent := make(chan error, 1)
	go func() {
		_, err := h.Agent("a").Send(h.Ctx(), fakeagent.SendInput{To: []string{h.Addr("b")}, Body: "held when the script fails"})
		sent <- err
	}()
	<-g.hold
	defer func() {
		select {
		case <-g.release:
		default:
			h.Kill(ProcessCrash)
			close(g.release)
		}
		<-sent
	}()
	failure := errors.New("the script failed while the engine was writing")
	crashed, err := g.finishWork(h, nil, true, failure)
	if !errors.Is(err, failure) {
		t.Errorf("the original script failure was lost: %v", err)
	}
	if _, err := old.mounted.cut.Stat("."); !errors.Is(err, errCutOff) {
		t.Errorf("the abandoned process can still reach its disk: %v", err)
	}
	select {
	case <-g.release:
	default:
		t.Error("the callback is still held after the script failed")
	}
	if !crashed || h.current() != nil {
		t.Error("the claimed generation was not taken down")
	}
}
