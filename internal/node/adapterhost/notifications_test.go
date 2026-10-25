package adapterhost_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/aianyai/sidera/internal/node/adapterhost"
	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
)

func turnStarted(endpoint string, turn int) adapterproto.TurnStartedParams {
	return adapterproto.TurnStartedParams{Endpoint: adapterproto.EndpointRef(endpoint), Turn: adapterproto.TurnRef(fmt.Sprintf("turn-%d", turn)), AtMS: 1}
}

func turnEnded(endpoint string, turn int) adapterproto.TurnEndedParams {
	return adapterproto.TurnEndedParams{Endpoint: adapterproto.EndpointRef(endpoint), Turn: adapterproto.TurnRef(fmt.Sprintf("turn-%d", turn)), Final: adapterproto.TurnCompleted, AtMS: 2}
}

// notifiedTurns returns the turn references of the notifications the engine
// received, in order.
func notifiedTurns(eng *engineDouble) []string {
	var out []string
	for _, e := range eng.of("notified") {
		switch p := e.Params.(type) {
		case adapterproto.TurnStartedParams:
			out = append(out, e.Method+" "+string(p.Turn))
		case adapterproto.TurnEndedParams:
			out = append(out, e.Method+" "+string(p.Turn))
		default:
			out = append(out, e.Method)
		}
	}
	return out
}

// What the adapter says reaches the engine in the order it was said, and what it
// said before it was admitted waits for the admission and is delivered after it.
func TestNotificationsReachTheEngineInTheOrderSentAndThoseBeforeAdmissionWaitForIt(t *testing.T) {
	r := newRig(t, rigOptions{})
	r.eng.admitGate = make(chan struct{})
	var a *scripted
	r.lau.launch = func(n int, inst adapterhost.Instance) (adapterhost.Process, error) {
		pp, ad := r.scriptedProcess(nil, false)
		a = ad
		return pp, nil
	}
	go func() { _ = r.host.Start(timeoutCtx(t), r.env(false)) }()
	r.waitEvents("admitted", 1)

	// Before the engine has returned the parameters of adapter.start: a turn of an
	// endpoint that does not exist yet starts and ends, a prompt is persisted, a
	// snapshot of availability arrives.
	a.notify(adapterproto.MethodTurnStarted, turnStarted("session-new", 1))
	a.notify(adapterproto.MethodTurnEnded, turnEnded("session-new", 1))
	a.notify(adapterproto.MethodPromptPersisted, adapterproto.PromptPersistedParams{Endpoint: "session-new", Attempt: "00000000-0000-4000-8000-000000000001"})
	a.notify(adapterproto.MethodAdapterAvailability, adapterproto.AvailabilityParams{Conditions: []adapterproto.Availability{}})
	eventually(t, "the host to hold the four notifications", func() bool { return r.host.Counts("chat").Buffered == 4 })
	if got := r.eng.of("notified"); len(got) != 0 {
		t.Fatalf("%d notifications reached the engine before the admission was complete", len(got))
	}

	close(r.eng.admitGate)
	r.waitConnected("chat")
	// After it, more, in order.
	a.notify(adapterproto.MethodTurnStarted, turnStarted("session-new", 2))
	a.notify(adapterproto.MethodTurnEnded, turnEnded("session-new", 2))
	r.waitEvents("notified", 6)

	want := []string{
		"turn.started turn-1", "turn.ended turn-1", "prompt.persisted", "adapter.availability",
		"turn.started turn-2", "turn.ended turn-2",
	}
	if got := notifiedTurns(r.eng); !slices.Equal(got, want) {
		t.Fatalf("the engine received\n got %v\nwant %v", got, want)
	}
	// The notification arrived as its own type, for the engine to switch on.
	first := r.eng.of("notified")[0].Params.(adapterproto.TurnStartedParams)
	if first.Endpoint != "session-new" {
		t.Fatalf("the first notification = %+v", first)
	}
	eventually(t, "the notifications to be counted as handled", func() bool {
		c := r.host.Counts("chat")
		return c.Handled == 6 && c.Buffered == 0
	})
}

// An invalid notification, one the declaration does not allow, is dropped and
// logged, and the ones around it are delivered in order.
func TestAnInvalidOrUndeclaredNotificationIsDroppedAndTheOthersAreDelivered(t *testing.T) {
	r := newRig(t, rigOptions{manifest: func(o *fakeadapter.ManifestOptions) {
		// No runtime: turn notifications are not for this adapter.
		o.Runtime = false
		o.Interrupt = false
		o.AckOnBehalf = false
		o.Frontends = nil
		o.RecvWaitS = nil
		o.RuntimeName, o.RuntimeVersion, o.Tested, o.AckMarkers = "", "", nil, nil
		o.Budgets = o.Budgets[2:]
	}})
	var a *scripted
	r.lau.launch = func(n int, inst adapterhost.Instance) (adapterhost.Process, error) {
		pp, ad := r.scriptedProcess(nil, false)
		a = ad
		return pp, nil
	}
	r.start()
	r.ready("chat", 1)

	a.notify(adapterproto.MethodAdapterAvailability, adapterproto.AvailabilityParams{Conditions: []adapterproto.Availability{}})
	// Not valid: a turn without an endpoint.
	a.notify(adapterproto.MethodTurnStarted, map[string]any{"turn": "t", "at_ms": 1})
	// Valid, but a direct-only adapter has no turns.
	a.notify(adapterproto.MethodTurnStarted, turnStarted("session-1", 1))
	a.notify(adapterproto.MethodAdapterAvailability, adapterproto.AvailabilityParams{Conditions: []adapterproto.Availability{}})
	r.waitEvents("notified", 2)
	eventually(t, "the four notifications to be dealt with", func() bool { return r.host.Counts("chat").Handled == 4 })
	if got := notifiedTurns(r.eng); !slices.Equal(got, []string{"adapter.availability", "adapter.availability"}) {
		t.Fatalf("the engine received %v, want the two valid notifications the declaration allows", got)
	}
	if !r.records.contains("dropped") {
		t.Fatalf("what was dropped was not logged: %v", r.records.msgs)
	}
	if !r.host.Counts("chat").Connected {
		t.Fatal("a notification that was dropped ended the connection")
	}
}

// A stalled engine loses no notification while the stall is shorter than the buffer
// and the queue of the peer together hold, and beyond that the connection ends and
// the instance starts again.
func TestAStalledEngineLosesNoNotificationAndBeyondItsBoundTheConnectionEnds(t *testing.T) {
	const buffer, queue = 4, 4
	params := testParams()
	params.NotificationBuffer = buffer
	params.Peer = peer.Params{MaxPendingNotifications: queue}

	setup := func(t *testing.T) (*rig, *scripted) {
		r := newRig(t, rigOptions{params: params})
		r.eng.notifyGate = make(chan struct{})
		var a *scripted
		r.lau.launch = func(n int, inst adapterhost.Instance) (adapterhost.Process, error) {
			pp, ad := r.scriptedProcess(nil, false)
			a = ad
			return pp, nil
		}
		r.start()
		r.ready("chat", 1)
		return r, a
	}

	t.Run("just under the bound: all delivered in order, on the same connection", func(t *testing.T) {
		r, a := setup(t)
		const n = buffer + queue
		for i := range n {
			a.notify(adapterproto.MethodTurnStarted, turnStarted("s-1", i))
		}
		// The engine holds one, the buffer fills; the rest wait in the host's handler
		// and in the peer's queue.
		eventually(t, "the buffer to fill", func() bool { return r.host.Counts("chat").Buffered >= buffer+1 })
		close(r.eng.notifyGate)
		r.waitEvents("notified", n)
		var want []string
		for i := range n {
			want = append(want, fmt.Sprintf("turn.started turn-%d", i))
		}
		if got := notifiedTurns(r.eng); !slices.Equal(got, want) {
			t.Fatalf("delivered\n got %v\nwant %v", got, want)
		}
		eventually(t, "everything handled", func() bool { c := r.host.Counts("chat"); return c.Handled == n && c.Buffered == 0 })
		if !r.host.Counts("chat").Connected {
			t.Fatal("the connection ended although the stall was within the bound")
		}
		if got := r.eng.of("gone"); len(got) != 0 {
			t.Fatalf("the engine was told %v", got)
		}
		if n := r.lau.launches(); n != 1 {
			t.Fatalf("%d launches, want 1", n)
		}
	})

	t.Run("beyond the bound: the connection ends, what is buffered is delivered first, and a new pass follows", func(t *testing.T) {
		r, a := setup(t)
		const n = buffer + queue + 6
		for i := range n {
			a.notify(adapterproto.MethodTurnStarted, turnStarted("s-1", i))
		}
		r.waitDisconnected("chat")
		// The end is reported after the forwarder has delivered what was buffered,
		// and the forwarder waits for the engine.
		if got := r.eng.of("gone"); len(got) != 0 {
			t.Fatalf("the end of the connection was reported (%v) while the engine still held notifications", got)
		}
		close(r.eng.notifyGate)
		r.waitEvents("gone", 1)
		if got := r.goneCauses("chat"); !slices.Equal(got, []adapterhost.GoneCause{adapterhost.GoneConnectionEnded}) {
			t.Fatalf("the engine was told %v, want [connection_ended]", got)
		}
		// Everything before the report is a prefix of what was sent, in order, and
		// at least what the engine and the buffer held.
		var delivered []string
		for _, e := range r.eng.snapshot() {
			if e.Kind == "gone" {
				break
			}
			if e.Kind == "notified" {
				delivered = append(delivered, e.Method+" "+string(e.Params.(adapterproto.TurnStartedParams).Turn))
			}
		}
		if len(delivered) < buffer+1 || len(delivered) >= n {
			t.Fatalf("%d notifications delivered before the report, want between %d and %d", len(delivered), buffer+1, n-1)
		}
		for i, got := range delivered {
			if want := fmt.Sprintf("turn.started turn-%d", i); got != want {
				t.Fatalf("delivered[%d] = %q, want %q: not a prefix in order", i, got, want)
			}
		}
		// A new pass after the backoff.
		r.advance(params.RestartBackoffMin)
		r.waitLaunches(2)
		r.ready("chat", 2)
		if c := r.host.Counts("chat"); c.Handled != 0 || c.Buffered != 0 {
			t.Fatalf("the new connection starts with counts %+v", c)
		}
	})
}
