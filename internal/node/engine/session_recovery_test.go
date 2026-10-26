package engine_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"path"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// The control record is a record of the node like the others: time settles it, a
// body that is not what it says is a loss that is told, and what it made is what
// the adapter is asked to take back. A creation that was not settled when the node
// went down is settled or gone on with when it comes back.

// seedControl writes a control record of a creation, accepted at accepted, onto the
// disk of the rig, with the endpoints it refers to: the runtime endpoint it is for,
// open, and when intent is not nil the session endpoint being created.
func seedControl(t *testing.T, r *rig, i int, intent *store.CreateIntent) (store.CtlRecord, store.EndpointRecord) {
	t.Helper()
	runtime := store.EndpointRecord{ID: mustEndpoint(t, 4), Kind: adapterreg.KindRuntime, Instance: "chat", Status: store.EndpointOpen, Token: bytes.Repeat([]byte{4}, store.TokenLen), Declaration: []byte("{}")}
	r.seedRecord(runtime)
	c := sessionCtlRecord(t, i, mustEndpoint(t, 60))
	c.Intent = intent
	if intent == nil {
		c.SessionEndpoint = ident.EndpointID{}
	}
	var ep store.EndpointRecord
	if intent != nil {
		ep = store.EndpointRecord{
			ID: intent.Endpoint, Kind: sessionKind, Instance: "chat", Status: store.EndpointCreating, Token: bytes.Repeat([]byte{9}, store.TokenLen),
			Declaration: []byte("{}"), Session: &store.SessionInfo{Preset: "default", ToolAccess: []string{engine.TokenEnv, engine.HomeEnv}}, CreatedBy: strings.TrimSuffix(c.Name(), ".rec"),
		}
	}
	return c, ep
}

// A control record that is accepted and has no reply is settled by time like a
// target record: at the settle deadline its outcome is unknown, since the signal was
// handed to be executed, and after report_by it is closed.
func TestControlRecordIsSettledByTime(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	c, _ := seedControl(t, r, 1, nil)
	r.seedRecord(c)
	e := r.start()

	stateOf := func() delivery.State {
		t.Helper()
		sc := r.scan()
		if len(sc.Ctl) != 1 {
			t.Fatalf("%d control records, want 1", len(sc.Ctl))
		}
		return sc.Ctl[0].Record.State
	}
	if got := stateOf(); got != delivery.StateAccepted {
		t.Fatalf("the control record is %s before its time, want accepted", got)
	}
	settleBy := delivery.SettleBy(c.Record.ReportBy)
	advance(t, r, e, settleBy.Sub(r.clk.Now())-time.Second)
	if got := stateOf(); got != delivery.StateAccepted {
		t.Errorf("the control record is %s a second before the settle deadline, want accepted", got)
	}
	advance(t, r, e, 2*time.Second)
	if got := stateOf(); got != delivery.StateDeliveryUnknown {
		t.Errorf("the control record is %s at the settle deadline, want delivery_unknown", got)
	}
	advance(t, r, e, c.Record.ReportBy.Sub(r.clk.Now())+time.Second)
	if got := stateOf(); got != delivery.StateClosedUnconfirmed {
		t.Errorf("the control record is %s after report_by, want closed_unconfirmed", got)
	}
}

// A body that is not what the delivery of an initial message says, found when the node
// starts, settles the delivery,
// and the creation it came with, which is not settled, is failed with the reply
// control_unknown (no session will be attached, but one may remain), and the endpoint it
// was making is closed, so that nothing is made of a creation whose first message is
// gone. The owner has an alert of the damage and one of a session nobody may control.
func TestBodyLostFromTheInitialMessageFailsItsUnsettledCreation(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	attempt := adapterproto.AttemptID("0b4d6e1a-2f3c-4d5e-8f60-718293a4b5c6")
	intent := &store.CreateIntent{Attempt: attempt, Endpoint: mustEndpoint(t, 61), State: store.IntentCreating}
	c, ep := seedControl(t, r, 2, intent)
	body := []byte("the initial message")
	c.Initial = &store.InitialMessage{ContentType: "text/plain", Body: store.BodyRefOf(body)}
	c.EndpointMaxQueue = delivery.DefaultEndpointMaxQueue
	d, err := engine.DerivedDeliveryOf(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.st.ControlIntake(body, &ep, c, &d); err != nil {
		t.Fatalf("ControlIntake: %v", err)
	}
	// The body is damaged on the disk.
	r.seedFile(store.BodiesDir, store.BodyFileName(d.Name()), []byte("not the initial message"))
	// The start reads the body of the message of a creation it goes on with.
	e := r.start()
	settle(t, e)
	sc := r.scan()
	if len(sc.Ctl) != 1 || len(sc.In) != 0 {
		t.Fatalf("%d control records and %d deliveries, want the control record that is kept and no delivery", len(sc.Ctl), len(sc.In))
	}
	got := sc.Ctl[0]
	if got.Record.State != delivery.StateDelivered || !got.Dedup.HasReply || got.Intent == nil || got.Intent.State != store.IntentFailed {
		t.Errorf("the control record is %+v: want delivered with a reply and a failed intent", got)
	}
	for _, e := range sc.Endpoints {
		if e.ID == intent.Endpoint && (e.Status != store.EndpointClosed || len(e.Token) != 0) {
			t.Errorf("the endpoint of the creation is %v with a token, want closed and none", e.Status)
		}
	}
	var corrupt, uncontrolled bool
	for _, a := range sc.Alerts {
		corrupt = corrupt || a.Kind == "corrupt_body"
		uncontrolled = uncontrolled || (a.Kind == "control_unknown" && a.Subject == c.SessionEndpoint.String())
	}
	if !corrupt {
		t.Errorf("alerts %+v, want one of kind corrupt_body", sc.Alerts)
	}
	if !uncontrolled {
		t.Errorf("alerts %+v, want one of kind control_unknown about %s: the creation fails with the reason that says a session may remain", sc.Alerts, c.SessionEndpoint)
	}
}

// A control record that was closed while its creation was unsettled can no longer take
// the reply, and no retry would change that: the creation is not tried again forever in
// silence. The log says which record, the owner has an alert of a session that may
// exist, and the work for the creation stops, so that waiting longer says nothing more.
func TestCreationWhoseControlRecordWasClosedIsReportedOnceAndNotRetried(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	intent := &store.CreateIntent{Attempt: "0b4d6e1a-2f3c-4d5e-8f60-718293a4b5c6", Endpoint: mustEndpoint(t, 61), State: store.IntentCreating}
	c, ep := seedControl(t, r, 3, intent)
	closed, _, err := c.Record.Tick(c.Record.ReportBy.Add(time.Second))
	if err != nil || closed.State != delivery.StateClosedUnconfirmed {
		t.Fatalf("the record is %s after report_by (%v), want closed_unconfirmed", closed.State, err)
	}
	c.Record = closed
	if err := r.st.ControlIntake(nil, &ep, c, nil); err != nil {
		t.Fatalf("ControlIntake: %v", err)
	}
	e := r.start()
	advance(t, r, e, engine.DefaultParams().CreateReplyDeadline+time.Second)
	advance(t, r, e, 10*engine.DefaultParams().RetryBackoff)

	reported := 0
	for _, m := range r.log.Messages() {
		if strings.Contains(m, "its control record was closed before it") {
			reported++
		}
	}
	if reported != 1 {
		t.Errorf("the log says %d times that the creation cannot be settled, want once: %q", reported, r.log.Messages())
	}
	var told bool
	for _, a := range r.scan().Alerts {
		told = told || (a.Kind == "control_unknown" && a.Subject == c.SessionEndpoint.String())
	}
	if !told {
		t.Errorf("alerts %+v, want one of kind control_unknown about %s", r.scan().Alerts, c.SessionEndpoint)
	}
}

// The alert of a session nobody controls is about one fact, the creation that could not
// be settled, and a restart that finds the creation unsettled again does not make another:
// the owner has one alert for it, however many times the node starts.
func TestAnUncontrolledCreationIsToldOnceAcrossRestarts(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	intent := &store.CreateIntent{Attempt: "0b4d6e1a-2f3c-4d5e-8f60-718293a4b5c6", Endpoint: mustEndpoint(t, 61), State: store.IntentCreating}
	c, ep := seedControl(t, r, 3, intent)
	closed, _, err := c.Record.Tick(c.Record.ReportBy.Add(time.Second))
	if err != nil || closed.State != delivery.StateClosedUnconfirmed {
		t.Fatalf("the record is %s after report_by (%v), want closed_unconfirmed", closed.State, err)
	}
	c.Record = closed
	if err := r.st.ControlIntake(nil, &ep, c, nil); err != nil {
		t.Fatalf("ControlIntake: %v", err)
	}
	uncontrolled := func() int {
		n := 0
		for _, a := range r.scan().Alerts {
			if a.Kind == "control_unknown" && a.Subject == c.SessionEndpoint.String() {
				n++
			}
		}
		return n
	}
	for start := 1; start <= 3; start++ {
		e := r.start()
		advance(t, r, e, engine.DefaultParams().CreateReplyDeadline+time.Second)
		if got := uncontrolled(); got != 1 {
			t.Fatalf("%d alerts of a session nobody controls after start %d, want 1", got, start)
		}
		if got, err := e.Alerts(context.Background()); err != nil || len(got) != 1 {
			t.Fatalf("the engine lists %d alerts after start %d (%v), want 1", len(got), start, err)
		}
		r.restart()
	}
}

// What adapter.start carries for a session is what the adapter needs to take it back:
// the native session it maps to, the tool access its agent was created with (its own
// token and the data directory) and the state the adapter kept for it. A session
// that is not open, one still being created and one of another instance are not
// listed.
func TestAdapterStartCarriesTheOpenSessionsWithTheirAccess(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	token := bytes.Repeat([]byte{7}, store.TokenLen)
	open := store.EndpointRecord{
		ID: mustEndpoint(t, 70), Kind: sessionKind, Instance: "chat", Status: store.EndpointOpen, Token: token, Declaration: []byte("{}"),
		Session: &store.SessionInfo{Preset: "default", NativeRef: "native-7", ToolAccess: []string{engine.TokenEnv, engine.HomeEnv}},
	}
	creating := store.EndpointRecord{ID: mustEndpoint(t, 71), Kind: sessionKind, Instance: "chat", Status: store.EndpointCreating, Token: token, Declaration: []byte("{}"), Session: &store.SessionInfo{Preset: "default"}}
	closed := store.EndpointRecord{ID: mustEndpoint(t, 72), Kind: sessionKind, Instance: "chat", Status: store.EndpointClosed, Declaration: []byte("{}"), Session: &store.SessionInfo{Preset: "default", NativeRef: "native-8"}}
	for _, ep := range []store.EndpointRecord{open, creating, closed} {
		r.seedRecord(ep)
	}
	if err := r.st.CheckpointPut(store.CheckpointRecord{Key: store.CheckpointKey{Scope: "session", ID: open.ID.String()}, State: []byte(`{"n":1}`)}); err != nil {
		t.Fatal(err)
	}
	e := r.start()
	start, err := admitFull(context.Background(), e, "chat", r.admit)
	if err != nil {
		t.Fatalf("AdapterAdmitted: %v", err)
	}
	if len(start.Sessions) != 1 {
		t.Fatalf("adapter.start lists %d sessions, want the one that is open: %+v", len(start.Sessions), start.Sessions)
	}
	s := start.Sessions[0]
	wantEnv := map[string]string{engine.TokenEnv: base64.RawURLEncoding.EncodeToString(token), engine.HomeEnv: testHome}
	if s.Endpoint != adapterproto.EndpointRef(open.ID.String()) || s.Preset != "default" || s.NativeRef != "native-7" || s.TurnActive || string(s.Checkpoint) != `{"n":1}` {
		t.Errorf("the session entry is %+v", s)
	}
	if len(s.ToolAccess.Env) != len(wantEnv) || s.ToolAccess.Env[engine.TokenEnv] != wantEnv[engine.TokenEnv] || s.ToolAccess.Env[engine.HomeEnv] != wantEnv[engine.HomeEnv] {
		t.Errorf("the tool access is %v, want %v", s.ToolAccess.Env, wantEnv)
	}
}

// A creation the node was making when it went down is not forgotten: its intent is
// on disk, and when the node is back the adapter is asked again with the attempt it
// was asked with, which the runtime answers with the session it made.
func TestCreationCutOffByACrashIsGoneOnWithOneSession(t *testing.T) {
	h := sessionNode(t, harness.Options{}, "a")
	first := h.AdapterLaunches(harness.Instance)[0]
	if err := first.Controller().PauseAt(h.Ctx(), fakeadapter.PauseCreateAfterNative); err != nil {
		t.Fatal(err)
	}
	msg := create(t, h, "a", "default")
	if err := first.Controller().WaitPaused(h.Ctx(), fakeadapter.PauseCreateAfterNative); err != nil {
		t.Fatal(err)
	}
	h.Crash(harness.ProcessCrash)
	quiet(t, h)
	r := replyAfterMoving(t, h, "a", msg)

	if n := nativeSessions(t, h); n != 1 {
		t.Errorf("%d native sessions after the crash and the retry, want 1", n)
	}
	eps := sessionEndpoints(t, h)
	if r == nil || r.Create == nil || r.Create.Outcome != adapterproto.CreateReplyCreated || len(eps) != 1 || eps[0].Status != store.EndpointOpen {
		t.Fatalf("after the crash the reply is %+v and the endpoints %+v, want created and one open endpoint", r, eps)
	}
	oneSessionPerAttempt(t, h)
}

// startAsTheDaemonDoes opens the engine and runs the start in the daemon's order: the
// adapter is admitted and, when connected, answers adapter.start (step 10), then the
// creations, closes and prompts that were not settled are settled or asked again
// (step 11), then the node serves. rig.start puts the adapter after step 11 instead, which a test of what step 11
// finds already connected cannot use.
func startAsTheDaemonDoes(t *testing.T, r *rig, connected bool) *engine.Engine {
	t.Helper()
	e, _, err := r.open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	r.eng = e
	t.Cleanup(func() { r.stop(e) })
	e.Start()
	if err := admitOn(context.Background(), e, "chat", r.admit); err != nil {
		t.Fatalf("AdapterAdmitted: %v", err)
	}
	if connected {
		started(t, e, "chat")
		settle(t, e)
	}
	if err := e.RecoverDelivery(context.Background()); err != nil {
		t.Fatalf("RecoverDelivery: %v", err)
	}
	e.Serve()
	settle(t, e)
	return e
}

// createHost is the adapter host that answers every session.create with a created
// session and remembers how many it was asked.
type createHost struct {
	*hostDouble
	asked atomic.Int32
}

func (h *createHost) CreateSession(context.Context, string, adapterproto.SessionCreateParams) (adapterproto.SessionCreateResult, error) {
	h.asked.Add(1)
	return adapterproto.SessionCreateResult{Outcome: adapterproto.CreateCreated, NativeRef: "native-made", Frontends: []adapterproto.Frontend{adapterproto.FrontendCLI}}, nil
}

// A creation whose initial message cannot be read when the node starts is failed before
// the adapter is asked for anything: the check of the message is step 11 of the start,
// and the adapter has connected (step 10) before it, so a call made when the adapter
// connects would make the session, open its endpoint and leave the check with nothing to
// fail.
func TestCreationWhoseInitialMessageIsLostIsNotAskedOfTheAdapterBeforeItIsChecked(t *testing.T) {
	noLeaks(t)
	host := &createHost{hostDouble: newHostDouble()}
	r := newRig(t, 10, withAdapters(host))
	intent := &store.CreateIntent{Attempt: "0b4d6e1a-2f3c-4d5e-8f60-718293a4b5c6", Endpoint: mustEndpoint(t, 61), State: store.IntentCreating}
	c, ep := seedControl(t, r, 2, intent)
	body := []byte("the initial message")
	c.Initial = &store.InitialMessage{ContentType: "text/plain", Body: store.BodyRefOf(body)}
	c.EndpointMaxQueue = delivery.DefaultEndpointMaxQueue
	d, err := engine.DerivedDeliveryOf(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.st.ControlIntake(body, &ep, c, &d); err != nil {
		t.Fatalf("ControlIntake: %v", err)
	}
	r.seedFile(store.BodiesDir, store.BodyFileName(d.Name()), []byte("not the initial message"))

	startAsTheDaemonDoes(t, r, true)

	if n := host.asked.Load(); n != 0 {
		t.Errorf("the adapter was asked %d times to create a session whose first message is lost, want none", n)
	}
	sc := r.scan()
	if len(sc.Ctl) != 1 {
		t.Fatalf("%d control records, want 1", len(sc.Ctl))
	}
	if got := sc.Ctl[0].Intent; got == nil || got.State != store.IntentFailed {
		t.Fatalf("the intent of the creation is %+v, want failed", got)
	}
	for _, e := range sc.Endpoints {
		if e.ID == intent.Endpoint && e.Status != store.EndpointClosed {
			t.Errorf("the endpoint of the creation is %v, want closed", e.Status)
		}
	}
}

// A close or an interrupt that was not settled when the node went down is settled when
// it is back as time settles it: a close whose time is not up is asked of the adapter
// again, and one whose time is up is told as unconfirmed with the endpoint closed (the
// channel closes whatever the adapter did); an interrupt whose time is up is told as
// failed, and the adapter is not asked for a signal that may reach a later turn.
func TestUnsettledCloseAndInterruptAreSettledAtStart(t *testing.T) {
	deadline := engine.DefaultParams().ControlReplyDeadline
	for _, tc := range []struct {
		name string
		kind envelope.Kind
		// age is how long before the start the signal was accepted, and connected says the
		// adapter has answered adapter.start when step 11 runs.
		age       time.Duration
		connected bool
		wantCalls int
		// released says the native session is recorded released, as a close that the
		// adapter answered, and not owed, as one told as unconfirmed.
		released bool
	}{
		{"a close whose time is not up", envelope.KindSessionClose, 0, true, 1, true},
		{"a close whose time is up", envelope.KindSessionClose, 2 * deadline, false, 0, false},
		{"an interrupt whose time is up", envelope.KindSessionInterrupt, 2 * deadline, false, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			noLeaks(t)
			host := newCloseHost(answerClosed)
			r := newRig(t, 10, withAdapters(host))
			session := store.EndpointRecord{
				ID: mustEndpoint(t, 70), Kind: sessionKind, Instance: "chat", Status: store.EndpointOpen, Token: bytes.Repeat([]byte{7}, store.TokenLen), Declaration: []byte("{}"),
				Session: &store.SessionInfo{Preset: "default", NativeRef: "native-7", ToolAccess: []string{engine.TokenEnv, engine.HomeEnv}},
			}
			r.seedRecord(session)
			c := sessionCtlRecord(t, 5, session.ID)
			c.Kind, c.Key.Recipient, c.Intent, c.SessionEndpoint, c.Preset = tc.kind, session.ID, nil, ident.EndpointID{}, ""
			c.Dedup = delivery.NewDedupRecord(c.Key.Dedup(), c.Dedup.Digest, c.Record.ReportBy)
			c.AcceptedAt = t0.Add(-tc.age)
			r.seedRecord(c)

			startAsTheDaemonDoes(t, r, tc.connected)

			sc := r.scan()
			if len(sc.Ctl) != 1 {
				t.Fatalf("%d control records, want 1", len(sc.Ctl))
			}
			if got := sc.Ctl[0]; got.Record.State != delivery.StateDelivered || !got.Dedup.HasReply {
				t.Fatalf("the signal is %s (reply stored: %v) after the start, want delivered with its reply", got.Record.State, got.Dedup.HasReply)
			}
			if got := len(host.calls()); got != tc.wantCalls {
				t.Errorf("the adapter was asked %d times to close, want %d", got, tc.wantCalls)
			}
			got := endpointRecord(t, sc, session.ID)
			if tc.kind == envelope.KindSessionClose {
				wantReleased(t, got, tc.released)
			} else if got.Status != store.EndpointOpen {
				t.Errorf("the endpoint is %v after an interrupt, want open", got.Status)
			}
		})
	}
}

// A creation whose time was up while the node was down is told as unconfirmed when it
// comes back, with the endpoint it allocated, instead of being asked for again as if
// no time had passed.
func TestCreationOverdueAtStartIsSettledAsUnconfirmed(t *testing.T) {
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
	h.Stop()
	h.Advance(engine.DefaultParams().CreateReplyDeadline + time.Second)
	h.Start()
	quiet(t, h)
	h.Advance(pullInterval)
	quiet(t, h)

	r := controlReply(t, h, "a", msg)
	if r == nil || r.Create == nil || r.Create.Outcome != adapterproto.CreateReplyUnconfirmed || r.Create.SessionEndpoint == "" {
		t.Fatalf("the reply is %+v, want unconfirmed, naming the endpoint", r)
	}
	if n := nativeSessions(t, h); n != 0 {
		t.Errorf("%d native sessions, want none: the node did not ask again", n)
	}
	for _, ep := range sessionEndpoints(t, h) {
		if ep.Status != store.EndpointClosed {
			t.Errorf("the endpoint is %v, want closed", ep.Status)
		}
	}
}

// A control record the node lost is answered as a record it does not hold, and its
// sender is told: a signal the node accepted is not answered as one it never saw.
func TestControlRecordLostBeforeItsReplyIsTold(t *testing.T) {
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
	if ans, err := pullFor(h, msg, runtimeEndpoint(t, h)); err != nil || len(ans.Reports) != 1 || len(ans.NoRecord) != 0 {
		t.Fatalf("before the loss the pull is answered %+v, %v, want the report", ans, err)
	}
	ctl := ctlRecords(t, h)
	if len(ctl) != 1 {
		t.Fatalf("%d control records, want 1", len(ctl))
	}
	h.Stop()
	if err := h.Disk().Remove(fsx.Tag{Tx: "offline", Role: "f"}, path.Join(store.CtlDir, ctl[0].Name())); err != nil {
		t.Fatal(err)
	}
	h.Start()
	quiet(t, h)
	if ans, err := pullFor(h, msg, runtimeEndpoint(t, h)); err != nil || len(ans.Reports) != 0 || len(ans.NoRecord) != 1 {
		t.Fatalf("after the loss the pull is answered %+v, %v, want the recipient as no record", ans, err)
	}
	if d := sourceDelivery(t, h, msg); d.State != delivery.StateDeliveryUnknown {
		t.Errorf("the sender's record is %s, want delivery_unknown: it holds the target's receipt and the target says it has no record", d.State)
	}
	if kinds := feedbackOf(t, h, msg); len(kinds) == 0 {
		t.Error("the sender is owed no feedback for a signal whose record the target lost")
	}
}

// A session endpoint that is damaged while its creation is not settled is removed with
// its alert, and the creation, which cannot go on without it, is failed: the reply says
// failed with control_unknown, the intent says failed and the owner is told that a
// session may remain, instead of a creation that waits for an endpoint that is not there.
func TestCreationWhoseEndpointWasDamagedIsFailed(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	intent := &store.CreateIntent{Attempt: "0b4d6e1a-2f3c-4d5e-8f60-718293a4b5c6", Endpoint: mustEndpoint(t, 61), State: store.IntentCreating}
	c, ep := seedControl(t, r, 4, intent)
	if err := r.st.ControlIntake(nil, &ep, c, nil); err != nil {
		t.Fatalf("ControlIntake: %v", err)
	}
	r.seedFile(store.EndpointsDir, ep.Name(), []byte("not a record"))
	e := r.start()
	settle(t, e)

	sc := r.scan()
	if len(sc.Ctl) != 1 {
		t.Fatalf("%d control records, want 1", len(sc.Ctl))
	}
	got := sc.Ctl[0]
	if got.Record.State != delivery.StateDelivered || !got.Dedup.HasReply || got.Intent == nil || got.Intent.State != store.IntentFailed {
		t.Errorf("the control record is %+v: want delivered with a reply and a failed intent", got)
	}
	for _, e := range sc.Endpoints {
		if e.ID == intent.Endpoint {
			t.Errorf("the endpoint that was damaged is back: %+v", e)
		}
	}
	var uncontrolled bool
	for _, a := range sc.Alerts {
		uncontrolled = uncontrolled || (a.Kind == "control_unknown" && a.Subject == c.SessionEndpoint.String())
	}
	if !uncontrolled {
		t.Errorf("alerts %+v, want one of kind control_unknown about %s", sc.Alerts, c.SessionEndpoint)
	}
}
