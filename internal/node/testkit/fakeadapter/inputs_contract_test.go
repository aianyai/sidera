package fakeadapter_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/internal/node/testkit/fakeruntime"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
)

// Every assertion uses the same daemon-side protocol and external ledger in
// both implementations; binary startup must obtain inputs through the handshake.
func TestExternalInputsContractInBothLayers(t *testing.T) {
	for _, binary := range []bool{false, true} {
		for _, declared := range []bool{false, true} {
			name := "inproc"
			if binary {
				name = "binary"
			}
			if declared {
				name += "/declared"
			} else {
				name += "/default"
			}
			t.Run(name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 2*waitLimit)
				defer cancel()
				inputs := declaredInputs()
				initial := map[string]fakeadapter.ExternalInputs{}
				if declared {
					initial["fake"] = inputs
				}
				owner, err := fakeadapter.NewInputRegistry([]string{"fake"}, initial)
				if err != nil {
					t.Fatal(err)
				}
				var ctl fakeadapter.Controller
				var call func(string, any, adapterproto.Message) error
				var create func(int, string) adapterproto.SessionCreateParams
				var notifications func(string, int) []json.RawMessage
				var readLog func() *ledger.Log
				var settle func(string)
				if binary {
					f := newProcFixture(t)
					owner.HandleControl(f.srv)
					f.cfg.RestoreInputs = true
					a := f.startAdapter()
					a.initialize()
					ctl = fakeadapter.RemoteKnobs{Conn: a.conn}
					call, create, notifications = a.call, a.createParams, a.waitNotifications
					readLog = func() *ledger.Log {
						l, err := ledger.ReadDir(f.cfg.LedgerDir)
						if err != nil {
							t.Fatal(err)
						}
						return l
					}
					settle = f.settle
				} else {
					r := newRig(t, rigOptions{inputs: owner})
					r.initialize()
					ctl = r.knobs.Controller()
					call = func(m string, p any, out adapterproto.Message) error {
						raw, err := r.call(m, p)
						if err == nil && out != nil {
							err = adapterproto.Decode(raw, out)
						}
						return err
					}
					create, notifications, readLog = r.createParams, r.waitNotifications, r.ledgerLog
					settle = func(ref string) {
						if err := r.runtime.SetPlan(ref, fakeruntime.TurnPlan{}); err != nil {
							t.Fatal(err)
						}
					}
				}
				before, err := ctl.Stats(ctx)
				if err != nil || before.NotificationsSent != 0 {
					t.Fatalf("snapshot sent before adapter.start: %+v, %v", before, err)
				}
				var started adapterproto.StartResult
				if err := call(adapterproto.MethodAdapterStart, adapterproto.StartParams{Node: "node-1", RuntimeEndpoint: "rt-1", Config: adapterproto.ConfigValues{}, Presets: []adapterproto.PresetConfig{{Name: "default", Config: adapterproto.ConfigValues{}}}, Sessions: []adapterproto.SessionAttach{}, DirectEndpoints: []adapterproto.DirectAttach{}}, &started); err != nil {
					t.Fatal(err)
				}
				if declared {
					var snapshot adapterproto.AvailabilityParams
					if err := adapterproto.Decode(notifications(adapterproto.MethodAdapterAvailability, 1)[0], &snapshot); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(snapshot.Conditions, inputs.Availability) {
						t.Fatalf("startup snapshot %+v, want %+v", snapshot.Conditions, inputs.Availability)
					}
				} else {
					stats, err := ctl.Stats(ctx)
					if err != nil || stats.NotificationsSent != 0 {
						t.Fatalf("default startup sent extra notifications: %+v, %v", stats, err)
					}
				}
				var created adapterproto.SessionCreateResult
				if err := call(adapterproto.MethodSessionCreate, create(1, "sess-1"), &created); err != nil {
					t.Fatal(err)
				}
				settle(string(created.NativeRef))
				if declared {
					// Clearing contemporary outcomes cannot erase a separately owned condition.
					if err := ctl.ClearOutcomes(ctx); err != nil {
						t.Fatal(err)
					}
					if err := ctl.QueueOutcome(ctx, fakeadapter.RequestPrompt, fakeadapter.Outcome{Kind: fakeadapter.OutcomeRefuse, Reason: string(adapterproto.RefusalBusy)}); err != nil {
						t.Fatal(err)
					}
					var refused adapterproto.PromptDeliverResult
					if err := call(adapterproto.MethodPromptDeliver, adapterproto.PromptDeliverParams{Endpoint: "sess-1", Attempt: attemptID(2), Text: "blocked"}, &refused); err != nil || refused.Stage != adapterproto.PromptRefused || refused.Reason != adapterproto.RefusalRuntimeAuth {
						t.Fatalf("first prompt %+v, %v", refused, err)
					}
					if got := readLog().Kind(ledger.KindPromptForwarded); len(got) != 0 {
						t.Fatal("refused prompt reached native runtime")
					}
					if err := owner.Set(ctx, "fake", fakeadapter.ExternalInputs{}); err != nil {
						t.Fatal(err)
					}
					var cleared adapterproto.AvailabilityParams
					if err := adapterproto.Decode(notifications(adapterproto.MethodAdapterAvailability, 2)[1], &cleared); err != nil || len(cleared.Conditions) != 0 {
						t.Fatalf("clear snapshot %+v, %v", cleared, err)
					}
					var queued adapterproto.PromptDeliverResult
					if err := call(adapterproto.MethodPromptDeliver, adapterproto.PromptDeliverParams{Endpoint: "sess-1", Attempt: attemptID(3), Text: "queued"}, &queued); err != nil || queued.Reason != adapterproto.RefusalBusy {
						t.Fatalf("external refusal consumed queued outcome: %+v %v", queued, err)
					}
				}
				var delivered adapterproto.PromptDeliverResult
				if err := call(adapterproto.MethodPromptDeliver, adapterproto.PromptDeliverParams{Endpoint: "sess-1", Attempt: attemptID(4), Text: "forward"}, &delivered); err != nil || delivered.Stage != adapterproto.PromptPersisted {
					t.Fatalf("unblocked prompt %+v, %v", delivered, err)
				}
				forwarded := readLog().Kind(ledger.KindPromptForwarded)
				if len(forwarded) != 1 || forwarded[0].Attrs.Attempt != string(attemptID(4)) {
					t.Fatalf("actual forwarding %+v", forwarded)
				}
			})
		}
	}
}

// blockedAdapterOutput pauses real stream writes so the peer's existing outbound
// limit can refuse a notification without ending the connection.
type blockedAdapterOutput struct {
	io.WriteCloser
	armed                    atomic.Bool
	entered, release         chan struct{}
	enteredOnce, releaseOnce sync.Once
}

func (w *blockedAdapterOutput) Write(b []byte) (int, error) {
	if w.armed.Load() {
		w.enteredOnce.Do(func() { close(w.entered) })
		<-w.release
	}
	return w.WriteCloser.Write(b)
}
func (w *blockedAdapterOutput) unblock() { w.releaseOnce.Do(func() { close(w.release) }) }

func TestExternalInputsRetriesAnUnqueuedAvailabilityClear(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()
	value := declaredInputs()
	owner, err := fakeadapter.NewInputRegistry([]string{"fake"}, map[string]fakeadapter.ExternalInputs{"fake": value})
	if err != nil {
		t.Fatal(err)
	}
	output := &blockedAdapterOutput{entered: make(chan struct{}), release: make(chan struct{})}
	r := newRig(t, rigOptions{inputs: owner, peer: peer.Params{MaxOutbound: 1}, output: func(w io.WriteCloser) io.WriteCloser { output.WriteCloser = w; return output }})
	t.Cleanup(output.unblock)
	r.initialize()
	r.start(nil, nil)
	r.waitNotifications(adapterproto.MethodAdapterAvailability, 1)
	output.armed.Store(true)
	// One line is inside Write; the next fills the one-line outbound queue.
	if err := r.knobs.Availability(ctx, value.Availability); err != nil {
		t.Fatal(err)
	}
	select {
	case <-output.entered:
	case <-ctx.Done():
		t.Fatal("output write did not block")
	}
	if err := r.knobs.Availability(ctx, value.Availability); err != nil {
		t.Fatal(err)
	}
	if err := owner.Set(ctx, "fake", fakeadapter.ExternalInputs{}); !errors.Is(err, peer.ErrOverloaded) {
		t.Fatalf("first clear: %v, want an unqueued notification", err)
	}
	output.unblock()
	r.waitNotifications(adapterproto.MethodAdapterAvailability, 3)
	before, err := r.knobs.Controller().Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Set(ctx, "fake", fakeadapter.ExternalInputs{}); err != nil {
		t.Fatal(err)
	}
	after, err := r.knobs.Controller().Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.NotificationsSent != before.NotificationsSent+1 {
		t.Fatalf("same-value clear retry returned success without enqueueing: sent %d then %d", before.NotificationsSent, after.NotificationsSent)
	}
	var cleared adapterproto.AvailabilityParams
	if err := adapterproto.Decode(r.waitNotifications(adapterproto.MethodAdapterAvailability, 4)[3], &cleared); err != nil || len(cleared.Conditions) != 0 {
		t.Fatalf("retried clear snapshot: %+v, %v", cleared, err)
	}
}
