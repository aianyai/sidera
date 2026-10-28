package harness_test

import (
	"strings"
	"sync"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/txn"
)

func TestObserverReceivesCompleteOperandsDuringServingAndRecovery(t *testing.T) {
	var mu sync.Mutex
	var events []fsx.Observation
	observer := startupObserverFunc(func(o fsx.Observation, _ txn.Kind) {
		mu.Lock()
		events = append(events, o)
		mu.Unlock()
	})
	h := harness.New(t, harness.Options{Observer: observer})
	h.MustDirect("a")
	h.MustDirect("b")
	if _, err := h.Agent("a").Send(bg(), fakeagent.SendInput{To: []string{h.Addr("b")}, Body: "observed send"}); err != nil {
		t.Fatal(err)
	}
	if err := h.Quiesce(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	sent := append([]fsx.Observation(nil), events...)
	mu.Unlock()
	renamed := false
	for _, o := range sent {
		if o.Point.Tag.Tx == "intake" && o.Point.Step == fsx.StepRename {
			if o.Path == "" || !strings.Contains(o.From, fsx.TempPrefix) {
				t.Fatalf("intake rename lost its operands: %+v", o)
			}
			renamed = true
		}
	}
	if !renamed {
		t.Fatal("send did not expose an intake rename")
	}
	leaveStartupDebris(t, h)
	mu.Lock()
	events = nil
	mu.Unlock()
	crashed, err := h.TryStart(bg(), harness.StartOptions{CrashAt: func(p fsx.Point, k txn.Kind) (harness.CrashMode, bool) {
		if p.Tag.Tx == "txn-cleanup" && p.Step == fsx.StepCommit {
			return harness.ProcessCrash.At(p.String()), true
		}
		return harness.CrashMode{}, false
	}})
	if !crashed || err != nil {
		t.Fatalf("recovery crash = %v, %v", crashed, err)
	}
	mu.Lock()
	recovered := append([]fsx.Observation(nil), events...)
	mu.Unlock()
	removed, committed := false, false
	for _, o := range recovered {
		if o.Point.Tag.Tx != "txn-cleanup" {
			continue
		}
		switch o.Point.Step {
		case fsx.StepRemove:
			if !strings.HasPrefix(o.Path, "state/"+fsx.TempPrefix) || o.From != "" {
				t.Fatalf("cleanup removal lost its operands: %+v", o)
			}
			removed = true
		case fsx.StepCommit:
			if o.Path != "" || o.From != "" {
				t.Fatalf("logical recovery commit has operands: %+v", o)
			}
			committed = true
		}
	}
	if !removed || !committed {
		t.Fatalf("recovery events = %+v", recovered)
	}
	h.Start()
	if err := h.Quiesce(); err != nil {
		t.Fatal(err)
	}
}
