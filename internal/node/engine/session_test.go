package engine_test

import (
	"context"
	"slices"
	"testing"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// A session is made by a control signal, session.create, sent to the runtime
// endpoint of an adapter. The node that holds the runtime endpoint answers with a
// control reply that its sender reads in the status of the message. These tests
// run one node, whose source and target sides are the same process, over an
// adapter host that runs a fake adapter and one fake runtime, and judge what
// happened from the ledger the fakes write, from the records on the disk and from
// the status of the message.

// runtimeEndpoint is the runtime endpoint of the adapter.
func runtimeEndpoint(t testing.TB, h *harness.H) ident.EndpointID {
	t.Helper()
	a, err := ident.ParseAddress(h.RuntimeAddr())
	if err != nil {
		t.Fatal(err)
	}
	return a.Endpoint
}

// nothingWasMade fails the test when the node holds a session endpoint that is not
// closed, or the runtime made a native session: what a creation the node refused
// leaves is the control record and nothing else.
func nothingWasMade(t testing.TB, h *harness.H) {
	t.Helper()
	if n := nativeSessions(t, h); n != 0 {
		t.Errorf("the runtime made %d native sessions, want none", n)
	}
	for _, ep := range sessionEndpoints(t, h) {
		if ep.Status != store.EndpointClosed {
			t.Errorf("the session endpoint %s is %v, want none that is not closed", ep.ID, ep.Status)
		}
	}
}

// A creation of a preset the node does not define is refused by the node itself,
// before any adapter hears of it: the reply is failed with preset_unknown and no
// session endpoint, it comes back with the receipt, and nothing is made.
func TestSessionCreateWithUnknownPresetIsRefused(t *testing.T) {
	h := sessionNode(t, harness.Options{}, "a")
	msg := create(t, h, "a", "no-such-preset")
	quiet(t, h)

	wantFailed(t, controlReply(t, h, "a", msg), adapterproto.CreateReasonPresetUnknown)
	if n := createRequests(t, h); n != 0 {
		t.Errorf("%d session.create requests reached the adapter, want none: the node refuses a preset it does not define itself", n)
	}
	nothingWasMade(t, h)
	if eps := sessionEndpoints(t, h); len(eps) != 0 {
		t.Errorf("%d session endpoints exist, want none: no endpoint is allocated for a creation the node refused", len(eps))
	}
	if d := sourceDelivery(t, h, msg); d.State != delivery.StateDelivered {
		t.Errorf("the sender's record of the signal is %s, want delivered: the reply ended it", d.State)
	}
	ctl := ctlRecords(t, h)
	if len(ctl) != 1 || ctl[0].Record.State != delivery.StateDelivered || !ctl[0].Dedup.HasReply || ctl[0].Intent != nil {
		t.Errorf("the control records are %+v, want one that is delivered with its reply stored and no creation intent", ctl)
	}
	oneSessionPerAttempt(t, h)
}

// A creation the adapter says is beyond what the preset allows (its runtime finds
// the preset invalid) fails with preset_unavailable, the reason the protocol gives
// both that and a preset closed to local sources, and the endpoint the node
// allocated is closed: no session is made.
func TestSessionCreateWithParamsBeyondPresetIsRefused(t *testing.T) {
	h := sessionNode(t, harness.Options{}, "a")
	ctrl, err := h.Adapter(harness.Instance)
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetOutcome(h.Ctx(), fakeadapter.RequestCreate, fakeadapter.Outcome{Kind: fakeadapter.OutcomeFail, Reason: string(adapterproto.CreateFailPresetInvalid)}); err != nil {
		t.Fatal(err)
	}
	msg := create(t, h, "a", "default")
	quiet(t, h)
	h.Advance(pullInterval)
	quiet(t, h)

	wantFailed(t, controlReply(t, h, "a", msg), adapterproto.CreateReasonPresetUnavailable)
	if n := createRequests(t, h); n != 1 {
		t.Errorf("%d session.create requests reached the adapter, want 1", n)
	}
	nothingWasMade(t, h)
	eps := sessionEndpoints(t, h)
	if len(eps) != 1 || eps[0].Status != store.EndpointClosed || len(eps[0].Token) != 0 {
		t.Errorf("the session endpoints are %+v, want the one allocated, closed, its token dropped", eps)
	}
	ctl := ctlRecords(t, h)
	if len(ctl) != 1 || ctl[0].Intent == nil || ctl[0].Intent.State != store.IntentFailed {
		t.Errorf("the control record is %+v, want an intent that failed", ctl)
	}
	oneSessionPerAttempt(t, h)
}

// A preset says whether an endpoint on this node may start a session with it. One
// that says no is refused to a local sender, with preset_unavailable and with no
// adapter called and nothing made; one that says yes is used as any other.
func TestSessionCreateWithPresetClosedToLocalSourcesIsRefused(t *testing.T) {
	h := sessionNode(t, harness.Options{Presets: map[string]string{
		"closed": `{"local_sources":"deny","description":"not for agents on this node"}`,
		"open":   `{"local_sources":"allow"}`,
	}}, "a")

	closed := create(t, h, "a", "closed")
	quiet(t, h)
	wantFailed(t, controlReply(t, h, "a", closed), adapterreg.DaemonCreateFailure(adapterreg.DaemonCreatePresetClosedToLocalSources).Reason)
	if n := createRequests(t, h); n != 0 {
		t.Errorf("%d session.create requests reached the adapter for a preset closed to local sources, want none", n)
	}
	nothingWasMade(t, h)
	if eps := sessionEndpoints(t, h); len(eps) != 0 {
		t.Errorf("%d session endpoints exist for a refused creation, want none", len(eps))
	}

	open := create(t, h, "a", "open")
	quiet(t, h)
	h.Advance(pullInterval)
	quiet(t, h)
	r := controlReply(t, h, "a", open)
	if r == nil || r.Create == nil || r.Create.Outcome != adapterproto.CreateReplyCreated {
		t.Fatalf("the creation with the open preset: %+v, want created", r)
	}
	if n := nativeSessions(t, h); n != 1 {
		t.Errorf("%d native sessions after the creation the preset allows, want 1", n)
	}
	oneSessionPerAttempt(t, h)
}

// A creation is carried out once: the session endpoint is allocated with its token,
// the adapter is called with the attempt the intent holds, the control reply names
// the endpoint, and the sender learns it when it asks (its record of the signal is
// ended by the reply, which the target answers from the record it holds). The
// endpoint opens, mapped to the native session, and nobody is told of anything that
// went wrong, for nothing did.
func TestSessionCreateMakesAnEndpointAndTellsItsSender(t *testing.T) {
	h := sessionNode(t, harness.Options{}, "a")
	msg := create(t, h, "a", "default")
	quiet(t, h)

	// The control record answers a pull from itself, as a target record does: with
	// its report and, once it holds one, the reply. A recipient it holds nothing for
	// would be answered as no record, which the sender reads as a loss.
	ans, err := pullFor(h, msg, runtimeEndpoint(t, h))
	if err != nil || len(ans.Reports) != 1 || len(ans.NoRecord) != 0 || ans.ControlReply == nil {
		t.Fatalf("the answer to a pull of the signal: %+v, %v", ans, err)
	}
	if rep, err := h.VerifyReport(ans.Reports[0]); err != nil || rep.Status.State != delivery.StateDelivered {
		t.Fatalf("the report of the signal: %+v, %v, want delivered", rep, err)
	}

	h.Advance(pullInterval)
	quiet(t, h)
	r := controlReply(t, h, "a", msg)
	eps := sessionEndpoints(t, h)
	if len(eps) != 1 {
		t.Fatalf("%d session endpoints, want 1", len(eps))
	}
	ep := eps[0]
	if r == nil || r.Create == nil || r.Create.Outcome != adapterproto.CreateReplyCreated || r.Create.SessionEndpoint != adapterproto.EndpointRef(ep.ID.String()) {
		t.Fatalf("the creation reply is %+v, want created and naming %s", r, ep.ID)
	}
	if ep.Status != store.EndpointOpen || len(ep.Token) != store.TokenLen || ep.Session == nil || ep.Session.NativeRef == "" || ep.Session.Preset != "default" {
		t.Errorf("the session endpoint is %+v (%+v), want open, with a token and the native session it maps to", ep, ep.Session)
	}
	ctl := ctlRecords(t, h)
	if len(ctl) != 1 || ctl[0].Intent == nil || ctl[0].Intent.State != store.IntentCreated || ctl[0].Intent.NativeRef != ep.Session.NativeRef {
		t.Errorf("the control record is %+v, want an intent that created the session %s", ctl, ep.Session.NativeRef)
	}
	o := outOf(t, h, msg)
	if d := o.Outbound.Recipients[0].Delivery; d.State != delivery.StateDelivered || o.Outbound.ControlReplyDigest.IsZero() || len(o.ControlReply) == 0 {
		t.Errorf("the sender's record: %s, reply digest %v: want delivered and the reply kept", d.State, o.Outbound.ControlReplyDigest)
	}
	if kinds := feedbackOf(t, h, msg); len(kinds) != 0 {
		t.Errorf("the sender was owed feedback %v for a creation that went well", kinds)
	}
	if n := nativeSessions(t, h); n != 1 {
		t.Errorf("%d native sessions, want 1", n)
	}
	// The session works on its first prompt as soon as it exists, so its first turn
	// was reported before the result of its creation was in: the node kept it by the
	// endpoint reference and the endpoint is busy once it is open.
	v, err := h.Engine().Adapter(h.Ctx(), harness.Instance)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(v.Busy, adapterproto.EndpointRef(ep.ID.String())) {
		t.Errorf("the busy endpoints are %v, want the new session among them: its first turn started", v.Busy)
	}
	oneSessionPerAttempt(t, h)
}

// A native session that was made and an adapter that dies before it answers is
// still one session: the intent is on disk, the call is made again with the same
// attempt when the adapter is back, and the runtime answers the attempt it knows.
func TestAdapterKilledAfterNativeCreateYieldsOneSession(t *testing.T) {
	h := sessionNode(t, harness.Options{}, "a")
	first := h.AdapterLaunches(harness.Instance)[0]
	if err := first.Controller().PauseAt(h.Ctx(), fakeadapter.PauseCreateAfterNative); err != nil {
		t.Fatal(err)
	}
	msg := create(t, h, "a", "default")
	if err := first.Controller().WaitPaused(h.Ctx(), fakeadapter.PauseCreateAfterNative); err != nil {
		t.Fatal(err)
	}
	if n := nativeSessions(t, h); n != 1 {
		t.Fatalf("%d native sessions when the adapter is killed, want the 1 it made", n)
	}
	adapterEnded(t, h, first)
	if r := controlReply(t, h, "a", msg); r != nil {
		t.Fatalf("a reply %+v while the adapter is dead: the creation is not settled", r)
	}

	// The host starts the adapter again; the node asks again, with the same attempt.
	adapterRestarted(t, h, 2)
	h.Advance(pullInterval)
	quiet(t, h)

	if got := len(h.AdapterLaunches(harness.Instance)); got != 2 {
		t.Fatalf("%d adapter processes, want 2", got)
	}
	if n := nativeSessions(t, h); n != 1 {
		t.Errorf("%d native sessions after the retry, want 1", n)
	}
	if n := createRequests(t, h); n != 2 {
		t.Errorf("%d session.create requests, want 2: the one that was cut off and the retry", n)
	}
	r := controlReply(t, h, "a", msg)
	eps := sessionEndpoints(t, h)
	if r == nil || r.Create == nil || r.Create.Outcome != adapterproto.CreateReplyCreated || len(eps) != 1 || eps[0].Status != store.EndpointOpen {
		t.Fatalf("after the retry the reply is %+v and the endpoints %+v, want created and one open endpoint", r, eps)
	}
	oneSessionPerAttempt(t, h)
}

// A reply to an endpoint that takes messages from nobody still comes, and so does
// the reply to a session creation of one: neither is a message the endpoint is
// asked to take in, and a declaration that narrows what the endpoint takes does
// not stop what it asked for.
func TestRepliesAndCreateRepliesPassRestrictedDeclaration(t *testing.T) {
	h := sessionNode(t, harness.Options{}, "a", "b")
	if err := h.Agent("a").Declare(h.Ctx(), fakeagent.DeclareInput{RestrictSenders: boolp(true), AcceptFrom: &[]string{}}); err != nil {
		t.Fatal(err)
	}

	// Control: a message nobody asked for is declined.
	plain := send(t, h, "b", "a", "an unprompted message")
	quiet(t, h)
	if d := sourceDelivery(t, h, plain); d.State != delivery.StateRejected || d.Reject != delivery.RejectEndpointDeclined {
		t.Fatalf("the unprompted message is %s (%s), want declined: the premise of the test", d.State, d.Reject)
	}

	// The answer to a question of a's is taken in.
	question := send(t, h, "a", "b", "a question")
	quiet(t, h)
	res, err := h.Agent("b").Reply(h.Ctx(), fakeagent.ReplyInput{MessageID: question, Body: "an answer"})
	if err != nil {
		t.Fatalf("the reply: %v", err)
	}
	quiet(t, h)
	if d := sourceDelivery(t, h, res.MessageID); d.State != delivery.StateAccepted {
		t.Errorf("b's reply is %s (%s), want accepted by a", d.State, d.Reject)
	}

	// And so is the reply to a creation.
	msg := create(t, h, "a", "default")
	quiet(t, h)
	h.Advance(pullInterval)
	quiet(t, h)
	r := controlReply(t, h, "a", msg)
	if r == nil || r.Create == nil || r.Create.Outcome != adapterproto.CreateReplyCreated {
		t.Errorf("the reply to a's creation is %+v, want created", r)
	}
}

// An adapter that is not connected cannot be asked to create a session, and the
// node says so at once, with a reply and no endpoint, instead of keeping the signal
// until the adapter returns.
func TestSessionCreateIsRefusedWhileTheAdapterIsOffline(t *testing.T) {
	h := sessionNode(t, harness.Options{}, "a")
	adapterEnded(t, h, h.AdapterLaunches(harness.Instance)[0])

	msg := create(t, h, "a", "default")
	engineQuiet(t, h)
	wantFailed(t, controlReply(t, h, "a", msg), adapterproto.CreateReasonAdapterOffline)
	nothingWasMade(t, h)
	if eps := sessionEndpoints(t, h); len(eps) != 0 {
		t.Errorf("%d session endpoints for a creation refused for an adapter that is offline, want none", len(eps))
	}

	// The adapter comes back and the same preset is used.
	adapterRestarted(t, h, 2)
	again := create(t, h, "a", "default")
	quiet(t, h)
	h.Advance(pullInterval)
	quiet(t, h)
	if r := controlReply(t, h, "a", again); r == nil || r.Create == nil || r.Create.Outcome != adapterproto.CreateReplyCreated {
		t.Errorf("the creation after the adapter returned: %+v, want created", r)
	}
}

// A creation that does not show a ready session before its deadline is told as
// unconfirmed, with the endpoint the node allocated, and that endpoint is closed: a
// session made after that is one nobody controls, and the owner is told.
func TestSessionCreateNotReadyByItsDeadlineIsUnconfirmedAndALateSessionIsAlerted(t *testing.T) {
	h := sessionNode(t, harness.Options{}, "a")
	ctrl, err := h.Adapter(harness.Instance)
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetOutcome(h.Ctx(), fakeadapter.RequestCreate, fakeadapter.Outcome{Kind: fakeadapter.OutcomeHold}); err != nil {
		t.Fatal(err)
	}
	msg := create(t, h, "a", "default")
	quiet(t, h)
	if r := controlReply(t, h, "a", msg); r != nil {
		t.Fatalf("a reply %+v before the adapter answered", r)
	}

	h.Advance(engine.DefaultParams().CreateReplyDeadline)
	quiet(t, h)
	h.Advance(pullInterval)
	quiet(t, h)
	r := controlReply(t, h, "a", msg)
	eps := sessionEndpoints(t, h)
	if len(eps) != 1 {
		t.Fatalf("%d session endpoints, want the one allocated", len(eps))
	}
	if r == nil || r.Create == nil || r.Create.Outcome != adapterproto.CreateReplyUnconfirmed || r.Create.Reason != adapterproto.CreateReasonReadyTimeout ||
		r.Create.SessionEndpoint != adapterproto.EndpointRef(eps[0].ID.String()) {
		t.Fatalf("the reply is %+v, want unconfirmed (ready_timeout) naming %s", r, eps[0].ID)
	}
	if eps[0].Status != store.EndpointClosed || len(eps[0].Token) != 0 {
		t.Errorf("the endpoint of an unconfirmed creation is %v with a token of %d bytes, want closed and none", eps[0].Status, len(eps[0].Token))
	}

	// The owner is told when the creation is settled: a session may exist that
	// nobody controls.
	uncontrolled := func() int {
		alerts, err := h.Engine().Alerts(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, a := range alerts {
			if a.Kind == "control_unknown" && a.Subject == eps[0].ID.String() {
				n++
			}
		}
		return n
	}
	if n := uncontrolled(); n != 1 {
		t.Errorf("%d alerts of kind control_unknown about %s, want 1: a session may exist that nobody controls", n, eps[0].ID)
	}

	// The held call goes on, makes the session and is refused the state of an
	// endpoint that is closed: nothing changes, and the owner is not told again.
	if err := ctrl.ReleaseHeld(h.Ctx()); err != nil {
		t.Fatal(err)
	}
	quiet(t, h)
	if n := uncontrolled(); n != 1 {
		t.Errorf("%d alerts after the late call, want still 1", n)
	}
	if eps := sessionEndpoints(t, h); eps[0].Status != store.EndpointClosed {
		t.Errorf("the endpoint is %v after the late session, want closed for good", eps[0].Status)
	}
}

// What the adapter says it cannot tell, who controls the native session it was
// creating, is a reason of a failed creation and an alert for the owner: a process
// may remain.
func TestSessionCreateFailedAsControlUnknownAlertsTheOwner(t *testing.T) {
	h := sessionNode(t, harness.Options{}, "a")
	ctrl, err := h.Adapter(harness.Instance)
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetOutcome(h.Ctx(), fakeadapter.RequestCreate, fakeadapter.Outcome{Kind: fakeadapter.OutcomeFail, Reason: string(adapterproto.CreateFailControlUnknown), AfterEffect: true}); err != nil {
		t.Fatal(err)
	}
	msg := create(t, h, "a", "default")
	quiet(t, h)
	h.Advance(pullInterval)
	quiet(t, h)
	wantFailed(t, controlReply(t, h, "a", msg), adapterproto.CreateReasonControlUnknown)
	alerts, err := h.Engine().Alerts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var told bool
	for _, a := range alerts {
		told = told || a.Kind == "control_unknown"
	}
	if !told {
		t.Errorf("the alerts are %+v, want one of kind control_unknown", alerts)
	}
}

// endpoint.status is answered by the node from what it has observed, at once and
// without asking any adapter: the activity, the conditions and, for the runtime
// endpoint, which presets can be used.
func TestEndpointStatusIsAnsweredByTheDaemon(t *testing.T) {
	h := sessionNode(t, harness.Options{}, "a", "b")
	settle := func() { quiet(t, h) }
	status := func(to string) *adapterproto.ControlStatusReply {
		t.Helper()
		res, err := h.Agent("a").Control(h.Ctx(), fakeagent.ControlInput{To: to, Signal: adapterproto.SignalEndpointStatus})
		if err != nil {
			t.Fatalf("endpoint.status of %s: %v", to, err)
		}
		settle()
		r := controlReply(t, h, "a", res.MessageID)
		if r == nil || r.Status == nil {
			t.Fatalf("no status reply for %s: %+v", to, r)
		}
		return r.Status
	}
	now := h.Clock().Now().Unix()
	rt := status(h.RuntimeAddr())
	if rt.Activity != adapterproto.ActivityIdle || len(rt.Conditions) != 0 || rt.AsOf != now {
		t.Errorf("the status of the runtime endpoint: %+v, want idle, no condition, as of now", rt)
	}
	if len(rt.Presets) != 1 || rt.Presets[0].Name != "default" || !rt.Presets[0].Available {
		t.Errorf("the presets of the runtime endpoint: %+v, want default, available", rt.Presets)
	}
	if n := createRequests(t, h); n != 0 {
		t.Errorf("%d session.create requests for a status", n)
	}
	send(t, h, "a", "b", "waiting for b")
	quiet(t, h)
	if b := status(h.Addr("b")); b.Queued != 1 || b.Activity != adapterproto.ActivityIdle || len(b.Presets) != 0 {
		t.Errorf("the status of b: %+v, want one queued message, idle, no presets", b)
	}

	adapterEnded(t, h, h.AdapterLaunches(harness.Instance)[0])
	settle = func() { engineQuiet(t, h) }
	off := status(h.RuntimeAddr())
	if off.Activity != adapterproto.ActivityUnknown || len(off.Conditions) != 1 || off.Conditions[0] != adapterproto.ControlConditionAdapterOffline {
		t.Errorf("the status with the adapter gone: %+v, want unknown activity and the condition adapter_offline", off)
	}
	if len(off.Presets) != 1 || off.Presets[0].Available {
		t.Errorf("the presets with the adapter gone: %+v, want default, not available", off.Presets)
	}
}

// A control signal the node cannot take in has no record: a creation aimed at an
// endpoint that is no runtime is refused, and nothing is allocated or called.
func TestSessionCreateAtAnEndpointThatIsNoRuntimeLeavesNoRecord(t *testing.T) {
	h := sessionNode(t, harness.Options{}, "a", "b")
	res, err := h.Agent("a").Control(h.Ctx(), fakeagent.ControlInput{To: h.Addr("b"), Signal: adapterproto.SignalSessionCreate, Preset: "default"})
	if err != nil {
		t.Fatalf("the signal is accepted by its sender's node: %v", err)
	}
	quiet(t, h)
	if ctl := ctlRecords(t, h); len(ctl) != 0 {
		t.Errorf("%d control records for a signal the target cannot take in, want none", len(ctl))
	}
	if d := sourceDelivery(t, h, res.MessageID); d.State != delivery.StateQueued {
		t.Errorf("the sender's record is %s, want queued: it was given no receipt and sends again until its deadline", d.State)
	}
	nothingWasMade(t, h)
	if eps := sessionEndpoints(t, h); len(eps) != 0 {
		t.Errorf("%d session endpoints, want none", len(eps))
	}
}

// Only the signals the node executes are offered, and one that carries parameters
// it does not take is refused for the field.
func TestControlRequestsAreChecked(t *testing.T) {
	h := sessionNode(t, harness.Options{}, "a")
	for _, tc := range []struct {
		name string
		in   fakeagent.ControlInput
	}{
		{"a close is not offered yet", fakeagent.ControlInput{To: h.Addr("a"), Signal: adapterproto.SignalSessionClose}},
		{"an interrupt is not offered yet", fakeagent.ControlInput{To: h.Addr("a"), Signal: adapterproto.SignalSessionInterrupt}},
		{"a creation names its preset", fakeagent.ControlInput{To: h.RuntimeAddr(), Signal: adapterproto.SignalSessionCreate}},
		{"a status takes no preset", fakeagent.ControlInput{To: h.RuntimeAddr(), Signal: adapterproto.SignalEndpointStatus, Preset: "default"}},
		{"a creation carries no initial message yet", fakeagent.ControlInput{To: h.RuntimeAddr(), Signal: adapterproto.SignalSessionCreate, Preset: "default", Body: "hello"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := h.Agent("a").Control(h.Ctx(), tc.in)
			if got := fakeagent.CategoryOf(err); got != fakeagent.CategoryInvalidArgument {
				t.Fatalf("the signal got %v (category %q), want an invalid argument", err, got)
			}
		})
	}
	if out := scanOf(t, h).Out; len(out) != 0 {
		t.Errorf("%d source records for refused requests, want none", len(out))
	}
}

// A control signal is new work like a message: with the queue of the node full it
// is refused, and nothing is recorded.
func TestControlSignalIsRefusedWhenTheNodeQueueIsFull(t *testing.T) {
	h := sessionNode(t, harness.Options{NodeSettings: `{"node_queue_limit":1}`}, "a")
	ctrl, err := h.Adapter(harness.Instance)
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetOutcome(h.Ctx(), fakeadapter.RequestCreate, fakeadapter.Outcome{Kind: fakeadapter.OutcomeHold}); err != nil {
		t.Fatal(err)
	}
	create(t, h, "a", "default")
	quiet(t, h)
	_, err = h.Agent("a").Control(h.Ctx(), fakeagent.ControlInput{To: h.RuntimeAddr(), Signal: adapterproto.SignalSessionCreate, Preset: "default"})
	if got := fakeagent.CodeOf(err); err == nil || got != "node_queue_full" {
		t.Fatalf("a second signal got %v (code %q), want the refusal for a full queue", err, got)
	}
	if n := len(scanOf(t, h).Out); n != 1 {
		t.Errorf("%d source records, want only the first signal's", n)
	}
	if err := ctrl.ReleaseHeld(h.Ctx()); err != nil {
		t.Fatal(err)
	}
	quiet(t, h)
}

// The intent to create is on disk before the adapter hears of the creation: when the
// write of the control record fails, no adapter is called and no session is made, the
// signal is not acknowledged, and a send again makes the session once the disk has room.
func TestAdapterIsNotCalledBeforeTheIntentIsOnDisk(t *testing.T) {
	h := sessionNode(t, harness.Options{}, "a")
	rule := h.Faults().Fail(fsxtest.Match{Tx: "control-intake", Role: "ctl", Step: fsx.StepWrite, Hit: 1}, fsxtest.DiskFull)
	msg := create(t, h, "a", "default")
	quiet(t, h)
	if rule.Hits() == 0 {
		t.Fatal("the fault was never injected")
	}
	if n := createRequests(t, h); n != 0 {
		t.Errorf("%d session.create requests reached the adapter although the intent was not written", n)
	}
	if n := nativeSessions(t, h); n != 0 {
		t.Errorf("%d native sessions made without an intent on disk", n)
	}
	if ctl := ctlRecords(t, h); len(ctl) != 0 {
		t.Errorf("%d control records after a failed write", len(ctl))
	}
	if d := sourceDelivery(t, h, msg); d.State != delivery.StateQueued {
		t.Errorf("the sender's record is %s, want queued: no receipt came", d.State)
	}

	h.Advance(resendBackoff)
	quiet(t, h)
	h.Advance(pullInterval)
	quiet(t, h)
	if r := controlReply(t, h, "a", msg); r == nil || r.Create == nil || r.Create.Outcome != adapterproto.CreateReplyCreated {
		t.Fatalf("after the send again the reply is %+v, want created", r)
	}
	if n := nativeSessions(t, h); n != 1 {
		t.Errorf("%d native sessions, want 1", n)
	}
	oneSessionPerAttempt(t, h)
}

// A signal is carried out once: the same request again is answered from the record,
// with the reply it holds and no second execution, and another request under the
// identifier is a conflict that changes nothing.
func TestARepeatOfAControlSignalIsAnsweredFromItsRecord(t *testing.T) {
	h := sessionNode(t, harness.Options{}, "a")
	rt := runtimeEndpoint(t, h)
	to := ident.Address{Node: harness.NodeID(), Endpoint: rt}
	from := endpointOf(t, h, "a")
	envelopeOf := func(id, preset string) []byte { return forgedControl(t, h, id, to, envelope.KindSessionCreate, preset) }

	first := envelopeOf("control-once", "no-such-preset")
	rc, err := h.Loopback().Send(h.Ctx(), harness.NodeID(), from, first)
	if err != nil || rc.ControlReply == nil || len(rc.Reports) != 1 {
		t.Fatalf("the first send: %+v, %v, want a report and the control reply", rc, err)
	}
	again, err := h.Loopback().Send(h.Ctx(), harness.NodeID(), from, first)
	if err != nil || again.ControlReply == nil || len(again.Reports) != 1 {
		t.Fatalf("the repeat: %+v, %v, want the same answer", again, err)
	}
	if rc.ControlReply.Digest() != again.ControlReply.Digest() {
		t.Error("the reply of the repeat is not the reply of the first: the digest differs")
	}
	if _, err := h.VerifyControlReply(*again.ControlReply); err != nil {
		t.Errorf("the reply of the repeat does not verify: %v", err)
	}
	if n := len(ctlRecords(t, h)); n != 1 {
		t.Errorf("%d control records after a repeat, want 1", n)
	}
	// Another request under the same identifier changes nothing.
	other := envelopeOf("control-once", "default")
	if _, err := h.Loopback().Send(h.Ctx(), harness.NodeID(), from, other); err == nil {
		t.Error("another request under the identifier of a control signal was taken in")
	} else if reason, ok := engine.ReasonOf(err); !ok || reason != engine.ReasonMessageConflict {
		t.Errorf("another request under the identifier got %v, want the conflict", err)
	}
	if n := createRequests(t, h); n != 0 {
		t.Errorf("%d session.create requests reached the adapter, want none: the preset is not defined", n)
	}
}

// An endpoint the owner closes while it is being created stays closed: closed is
// absorbing, and the adapter, refused the state of a session of a closed endpoint, cannot
// show the session ready. The creation is told at its deadline as unconfirmed, and the
// owner is told that a session may exist that nobody controls.
func TestEndpointClosedWhileBeingCreatedEndsUnconfirmedAndIsAlerted(t *testing.T) {
	h := sessionNode(t, harness.Options{}, "a")
	ctrl, err := h.Adapter(harness.Instance)
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetOutcome(h.Ctx(), fakeadapter.RequestCreate, fakeadapter.Outcome{Kind: fakeadapter.OutcomeHold}); err != nil {
		t.Fatal(err)
	}
	msg := create(t, h, "a", "default")
	quiet(t, h)
	eps := sessionEndpoints(t, h)
	if len(eps) != 1 || eps[0].Status != store.EndpointCreating {
		t.Fatalf("the endpoints are %+v, want the one being created", eps)
	}
	if _, err := h.Engine().CloseEndpoint(h.Ctx(), engine.CloseRequest{Endpoint: eps[0].ID}); err != nil {
		t.Fatal(err)
	}
	if err := ctrl.ReleaseHeld(h.Ctx()); err != nil {
		t.Fatal(err)
	}
	quiet(t, h)
	h.Advance(engine.DefaultParams().CreateReplyDeadline)
	quiet(t, h)
	h.Advance(pullInterval)
	quiet(t, h)

	if eps := sessionEndpoints(t, h); eps[0].Status != store.EndpointClosed {
		t.Errorf("the endpoint is %v, want it closed for good", eps[0].Status)
	}
	if r := controlReply(t, h, "a", msg); r == nil || r.Create == nil || r.Create.Outcome != adapterproto.CreateReplyUnconfirmed {
		t.Errorf("the reply is %+v, want unconfirmed", r)
	}
	alerts, err := h.Engine().Alerts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var told bool
	for _, a := range alerts {
		told = told || (a.Kind == "control_unknown" && a.Subject == eps[0].ID.String())
	}
	if !told {
		t.Errorf("the alerts are %+v, want one of kind control_unknown about %s", alerts, eps[0].ID)
	}
}
