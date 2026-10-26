package engine_test

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/handoff"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/adapterhost"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
)

// Delivery to a session is blocked when prompts have been failing for longer than the
// threshold because of a cause that is still active: the adapter is away, the runtime
// refuses its credentials, or its native service does not answer. The sender is told
// once, with the cause, however many messages wait; when the cause is gone it is told
// once more that the block is over. Whether delivery is blocked is the decision of
// package handoff; what is shown here is that the engine asks it at the moments that
// matter, records the facts in the order the floor needs, and tells the sender through
// the feedback it already has.

// blockedBackoff is how long the host waits before it starts an adapter that ended:
// longer than the threshold, so that a block can be reported while the adapter is
// away, and short enough for a test to bring the adapter back inside the queue time.
const blockedBackoff = 12 * time.Minute

func blockedHost() adapterhost.Params {
	p := sessionHostParams()
	p.RestartBackoffMin, p.RestartBackoffMax = blockedBackoff, blockedBackoff
	return p
}

// step moves the clock and lets the node settle.
func step(h *harness.H, settle func(), d time.Duration) {
	h.Advance(d)
	settle()
}

// wantBlockFacts fails the test unless every message is flagged blocked with the cause
// on its target record and the head record of the endpoint reports the block (blocked
// true), or none is and the head reports nothing (blocked false).
func wantBlockFacts(t testing.TB, h *harness.H, addr string, blocked bool, cause delivery.Cause, head handoff.Cause, msgs ...adapterproto.MessageID) {
	t.Helper()
	for _, m := range msgs {
		in := inOf(t, h, m)
		if in.Record.Blocked != blocked || (blocked && in.Record.BlockedCause != cause) {
			t.Errorf("message %s: blocked %v (cause %s), want %v (cause %s)", m, in.Record.Blocked, in.Record.BlockedCause, blocked, cause)
		}
	}
	got := attemptsOf(t, h, addr).Blocked
	if blocked && got.Cause != head || !blocked && got.Cause != 0 {
		t.Errorf("the head record of %s reports %+v, want cause %v (blocked %v)", addr, got, head, blocked)
	}
}

// wantBlockNotice fails the test unless the notice says kind of every message, each
// with the cause the line of the message names, and of nothing else.
func wantBlockNotice(t testing.TB, n notice, kind delivery.Kind, cause string, msgs ...adapterproto.MessageID) {
	t.Helper()
	if kinds := n.kinds(); len(kinds) != len(msgs) {
		t.Errorf("the notice covers %v, want %d items of %s", kinds, len(msgs), kind)
	}
	for _, k := range n.kinds() {
		if k != kind {
			t.Errorf("the notice covers a %s, want only %s", k, kind)
		}
	}
	for _, m := range msgs {
		if !strings.Contains(n.text, fmt.Sprintf("message %s ", m)) {
			t.Errorf("the notice does not name message %s:\n%s", m, n.text)
		}
	}
	if got := strings.Count(n.text, "(cause: "+cause+")"); got != len(msgs) {
		t.Errorf("the notice names the cause %s %d times, want %d:\n%s", cause, got, len(msgs), n.text)
	}
}

// Prompts that cannot reach a session for longer than the threshold, with a cause that
// still holds, block delivery: the sender is told once with the cause however many
// messages wait (they merge into one notice), nothing more however long it lasts, and
// once more, merged the same way, when the cause is gone.
func TestBlockedReportedOnceWithCauseAndRecoveredOnce(t *testing.T) {
	cases := []struct {
		name string
		// begin starts the cause after the session is idle; end ends it and returns when
		// prompts can reach the session again. settle waits for the node while the cause
		// holds: an adapter that is away leaves the fixture's own counts unsettled.
		begin  func(t *testing.T, h *harness.H) (settle func())
		end    func(t *testing.T, h *harness.H)
		cause  delivery.Cause
		hcause handoff.Cause
		text   string
	}{
		{"the adapter is offline",
			func(t *testing.T, h *harness.H) func() {
				adapterEnded(t, h, h.AdapterLaunches(harness.Instance)[0])
				return func() { engineQuiet(t, h) }
			},
			func(t *testing.T, h *harness.H) { adapterRestartedAfter(t, h, 2, time.Minute) },
			delivery.CauseAdapterOffline, handoff.CauseAdapterOffline, "adapter_offline"},
		{"the runtime refuses the credentials",
			func(t *testing.T, h *harness.H) func() {
				ctrl, err := h.Adapter(harness.Instance)
				if err != nil {
					t.Fatal(err)
				}
				refuse := fakeadapter.Outcome{Kind: fakeadapter.OutcomeRefuse, Reason: string(adapterproto.RefusalRuntimeAuth)}
				if err := ctrl.SetOutcome(h.Ctx(), fakeadapter.RequestPrompt, refuse); err != nil {
					t.Fatal(err)
				}
				since := h.Clock().Now().UnixMilli()
				if err := ctrl.Availability(h.Ctx(), []adapterproto.Availability{{Condition: adapterproto.ConditionRuntimeAuth, SinceMS: since}}); err != nil {
					t.Fatal(err)
				}
				return func() { quiet(t, h) }
			},
			func(t *testing.T, h *harness.H) {
				ctrl, err := h.Adapter(harness.Instance)
				if err != nil {
					t.Fatal(err)
				}
				if err := ctrl.Availability(h.Ctx(), nil); err != nil {
					t.Fatal(err)
				}
				if err := ctrl.ClearOutcomes(h.Ctx()); err != nil {
					t.Fatal(err)
				}
				quiet(t, h)
				// The prompt that failed is sent again after the retry time.
				h.Advance(handoff.DefaultRetryAfterFailure)
				quiet(t, h)
			},
			delivery.CauseRuntimeAuth, handoff.CauseAuthInvalid, "runtime_auth"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := sessionNodeOf(t, harness.Options{}, blockedHost(), "a")
			_, addr := idleSession(t, h, "a")
			settle := tc.begin(t, h)
			settle()

			m1 := sendToAddr(t, h, "a", addr, "first")
			settle()
			step(h, settle, time.Minute)
			m2 := sendToAddr(t, h, "a", addr, "second")
			settle()
			step(h, settle, time.Minute)
			m3 := sendToAddr(t, h, "a", addr, "third")
			settle()
			msgs := []adapterproto.MessageID{m1, m2, m3}

			// Exactly the threshold after the cause began is not yet blocked; a moment
			// later it is, and every message that waits is marked with the cause.
			step(h, settle, handoff.DefaultBlockedThreshold-2*time.Minute)
			wantBlockFacts(t, h, addr, false, 0, 0, msgs...)
			step(h, settle, time.Second)
			wantBlockFacts(t, h, addr, true, tc.cause, tc.hcause, msgs...)

			// The sender learns it by its next pull, and the three merge into one notice.
			for i := 0; i < 4; i++ {
				step(h, settle, time.Minute)
			}
			ns := noticesFor(t, h, "a")
			if len(ns) != 1 {
				t.Fatalf("a holds %d notices (%v) while the block lasts, want the one that reports it", len(ns), toldKinds(t, h, "a"))
			}
			wantBlockNotice(t, ns[0], delivery.KindDeliveryBlocked, tc.text, msgs...)
			if n := pending(t, h, "a"); n != 0 {
				t.Errorf("%d feedback items wait for a, want none", n)
			}

			// However long it lasts the block is reported once.
			for i := 0; i < 2; i++ {
				step(h, settle, time.Minute)
			}
			if got := len(noticesFor(t, h, "a")); got != 1 {
				t.Errorf("a holds %d notices while the block goes on, want 1", got)
			}

			// The cause is gone: the marks come off, the prompt reaches the session, and
			// the correction is one notice, merged the same way.
			tc.end(t, h)
			wantBlockFacts(t, h, addr, false, 0, 0, msgs...)
			for i := 0; i < 3; i++ {
				step(h, func() { quiet(t, h) }, time.Minute)
			}
			ns = noticesFor(t, h, "a")
			if len(ns) != 2 {
				t.Fatalf("a holds %d notices (%v) after the recovery, want the report and the correction", len(ns), toldKinds(t, h, "a"))
			}
			wantBlockNotice(t, ns[1], delivery.KindBlockedRecovered, tc.text, msgs...)
			step(h, func() { quiet(t, h) }, 3*time.Minute)
			if got := len(noticesFor(t, h, "a")); got != 2 {
				t.Errorf("a holds %d notices once the block is over, want 2", got)
			}
		})
	}
}

// Messages that already wait when the adapter goes away are blocked once it has stayed
// away for the threshold: the loss of the connection is the event that starts the count,
// and nothing else asks about an endpoint whose messages have not changed.
func TestMessagesThatWaitWhenTheAdapterGoesAwayAreBlockedAfterTheThreshold(t *testing.T) {
	h := sessionNodeOf(t, harness.Options{}, blockedHost(), "a")
	_, addr := idleSession(t, h, "a")
	msg := sendToAddr(t, h, "a", addr, "sent while the adapter is here")
	quiet(t, h)
	wantBlockFacts(t, h, addr, false, 0, 0, msg)

	adapterEnded(t, h, h.AdapterLaunches(harness.Instance)[0])
	settle := func() { engineQuiet(t, h) }
	settle()
	step(h, settle, handoff.DefaultBlockedThreshold)
	wantBlockFacts(t, h, addr, false, 0, 0, msg)
	step(h, settle, time.Second)
	wantBlockFacts(t, h, addr, true, delivery.CauseAdapterOffline, handoff.CauseAdapterOffline, msg)
}

// A prompt that fails after a cause was reported counts from its own start: the failing
// run began when the prompt was sent, before the adapter said the native service is
// down. Settling the attempt is the event that tells the judgment, since the snapshot
// and the records are as they were, so a block is reported at the threshold after the
// prompt and not at the threshold after the report of the cause.
func TestAPromptThatFailsAfterTheCauseWasReportedCountsFromWhenItWasSent(t *testing.T) {
	h := sessionNodeOf(t, harness.Options{}, blockedHost(), "a")
	_, addr := idleSession(t, h, "a")
	ctrl, err := h.Adapter(harness.Instance)
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetOutcome(h.Ctx(), fakeadapter.RequestPrompt, fakeadapter.Outcome{Kind: fakeadapter.OutcomeHold}); err != nil {
		t.Fatal(err)
	}
	msg := sendToAddr(t, h, "a", addr, "sent before the service went down")
	quiet(t, h)
	wantPrompts(t, h, addr, 1)

	// The service goes down while the prompt waits for its answer, and the native
	// process with it, so that the answer is a refusal.
	h.Advance(time.Minute)
	rt, err := h.Runtime()
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.KillNative(nativeRef(t, h, addr)); err != nil {
		t.Fatal(err)
	}
	down := []adapterproto.Availability{{Condition: adapterproto.ConditionNativeUnavailable, SinceMS: h.Clock().Now().UnixMilli()}}
	if err := ctrl.Availability(h.Ctx(), down); err != nil {
		t.Fatal(err)
	}
	quiet(t, h)
	h.Advance(time.Minute)
	if err := ctrl.ReleaseHeld(h.Ctx()); err != nil {
		t.Fatal(err)
	}
	quiet(t, h)
	if got := attemptsOf(t, h, addr).Attempts; len(got) == 0 || got[0].Outcome != handoff.AttemptFailed {
		t.Fatalf("the attempts are %+v, want the prompt that was refused", got)
	}

	// The threshold after the prompt was sent is a minute before the threshold after the
	// service was reported down.
	settle := func() { quiet(t, h) }
	step(h, settle, handoff.DefaultBlockedThreshold-2*time.Minute)
	wantBlockFacts(t, h, addr, false, 0, 0, msg)
	step(h, settle, time.Second)
	wantBlockFacts(t, h, addr, true, delivery.CauseNativeUnavailable, handoff.CauseServiceUnavailable, msg)
}

// A native process that was killed refuses the prompt (or its answer is lost) with the
// adapter online and nothing reported unavailable: that is an agent that cannot be
// woken, which prompts retry for and no cause explains, so the sender is told nothing
// and the message stays queued for the agent to read.
func TestNativeKilledDuringPromptGivesNoFeedbackAndKeepsMessage(t *testing.T) {
	rows := []struct {
		name  string
		setup func(t *testing.T, h *harness.H)
		want  handoff.AttemptOutcome
		// retried says whether the node sends the prompt again: a failure is retried, an
		// outcome that is not known is not, since no event brings the endpoint back.
		retried bool
	}{
		{"the native process is gone and refuses the prompt", func(t *testing.T, h *harness.H) {}, handoff.AttemptFailed, true},
		{"the answer of the prompt is lost as the native process is gone", func(t *testing.T, h *harness.H) {
			ctrl, err := h.Adapter(harness.Instance)
			if err != nil {
				t.Fatal(err)
			}
			lost := fakeadapter.Outcome{Kind: fakeadapter.OutcomeError, Code: peer.CodeInternalError}
			if err := ctrl.SetOutcome(h.Ctx(), fakeadapter.RequestPrompt, lost); err != nil {
				t.Fatal(err)
			}
			// The adapter cannot tell either, so the outcome stays unknown.
			unreachable := fakeadapter.Outcome{Kind: fakeadapter.OutcomeIndeterminate}
			if err := ctrl.SetOutcome(h.Ctx(), fakeadapter.RequestReconcile, unreachable); err != nil {
				t.Fatal(err)
			}
		}, handoff.AttemptUnknown, false},
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			h := sessionNode(t, harness.Options{}, "a")
			agent, addr := idleSession(t, h, "a")
			rt, err := h.Runtime()
			if err != nil {
				t.Fatal(err)
			}
			if err := rt.KillNative(nativeRef(t, h, addr)); err != nil {
				t.Fatal(err)
			}
			tc.setup(t, h)
			msg := sendToAddr(t, h, "a", addr, "for a session whose native process is gone")
			quiet(t, h)
			wantPrompts(t, h, addr, 1)
			// The failure outlasts the threshold several times over, and the retries go on.
			for i := 0; i < 3*int(handoff.DefaultBlockedThreshold/time.Minute); i++ {
				h.Advance(time.Minute)
				quiet(t, h)
			}
			if got := len(promptsInto(t, h, addr)); tc.retried != (got > 1) {
				t.Errorf("%d prompts were sent, want the failure retried only for a prompt that certainly failed", got)
			}
			a := attemptsOf(t, h, addr)
			if len(a.Attempts) == 0 {
				t.Fatal("no attempt was recorded")
			}
			if got := a.Attempts[len(a.Attempts)-1].Outcome; got != tc.want {
				t.Errorf("the last attempt is %v, want %v", got, tc.want)
			}
			wantBlockFacts(t, h, addr, false, 0, 0, msg)
			wantNoFeedback(t, h, "a", msg)
			wantOnly(t, recvAs(t, h, agent, fakeagent.RecvInput{}), msg)
		})
	}
}

// A write of the report that fails reports nothing and loses nothing: no record is
// marked, the head says nothing, and the write is tried again after the backoff, so the
// sender is still told.
func TestBlockedReportIsWrittenAgainAfterAFailedWrite(t *testing.T) {
	h := sessionNodeOf(t, harness.Options{}, blockedHost(), "a")
	_, addr := idleSession(t, h, "a")
	adapterEnded(t, h, h.AdapterLaunches(harness.Instance)[0])
	settle := func() { engineQuiet(t, h) }
	msg := sendToAddr(t, h, "a", addr, "sent while the adapter is away")
	settle()
	rule := h.Faults().Fail(fsxtest.Match{Tx: "blocked", Role: "in", Step: fsx.StepWrite, Hit: 1}, fsxtest.DiskFull)
	step(h, settle, handoff.DefaultBlockedThreshold+time.Second)
	if rule.Hits() == 0 {
		t.Fatal("the write of the block was never reached, so the fault was not injected")
	}
	wantBlockFacts(t, h, addr, false, 0, 0, msg)

	step(h, settle, engine.DefaultParams().RetryBackoff)
	wantBlockFacts(t, h, addr, true, delivery.CauseAdapterOffline, handoff.CauseAdapterOffline, msg)
	for i := 0; i < 4; i++ {
		step(h, settle, time.Minute)
	}
	ns := noticesFor(t, h, "a")
	if len(ns) != 1 {
		t.Fatalf("a holds %d notices, want the one that reports the block", len(ns))
	}
	wantBlockNotice(t, ns[0], delivery.KindDeliveryBlocked, "adapter_offline", msg)
}

// A crash can leave some target records marked and the head not saying so. Whatever the
// disk, once the cause is gone no record keeps the mark and the head reports nothing,
// and a sender that was told of the block is told it is over: the marks are found by
// themselves and not by the head.
func TestNoRecordKeepsABlockMarkAfterACrashWhenTheCauseIsGone(t *testing.T) {
	cov := &coverage{hit: map[fsx.Key]int{}}
	taker := newDiskTaker(cov, "blocked")
	h := sessionNodeOf(t, harness.Options{Observer: taker}, blockedHost(), "a")
	taker.h = h
	_, addr := idleSession(t, h, "a")
	adapterEnded(t, h, h.AdapterLaunches(harness.Instance)[0])
	settle := func() { engineQuiet(t, h) }
	m1 := sendToAddr(t, h, "a", addr, "first")
	m2 := sendToAddr(t, h, "a", addr, "second")
	settle()
	step(h, settle, handoff.DefaultBlockedThreshold+time.Second)
	for i := 0; i < 3; i++ {
		step(h, settle, time.Minute)
	}
	wantBlockFacts(t, h, addr, true, delivery.CauseAdapterOffline, handoff.CauseAdapterOffline, m1, m2)
	adapterRestartedAfter(t, h, 2, blockedBackoff)
	quiet(t, h)
	wantBlockFacts(t, h, addr, false, 0, 0, m1, m2)
	if len(taker.disks) == 0 {
		t.Fatal("no step of the transaction of a block was reached")
	}
	h.Stop()
	for _, td := range taker.disks {
		t.Run(td.where, func(t *testing.T) {
			n := restartedWithRuntime(t, td.disk)
			for _, m := range []adapterproto.MessageID{m1, m2} {
				for _, in := range inRecords(t, n, m) {
					if in.Record.Blocked {
						t.Errorf("message %s is still marked blocked after the start", m)
					}
				}
			}
			if got := attemptsOf(t, n, addr).Blocked; got.Cause != 0 {
				t.Errorf("the head record still reports %+v after the start", got)
			}
			var blocked, recovered int
			for _, k := range toldKinds(t, n, "a") {
				switch k {
				case delivery.KindDeliveryBlocked:
					blocked++
				case delivery.KindBlockedRecovered:
					recovered++
				}
			}
			if blocked != recovered {
				t.Errorf("the sender was told %d blocks and %d recoveries, want as many of one as of the other", blocked, recovered)
			}
		})
	}
	for _, k := range harness.ProgramTable(t).Keys() {
		if k.Tag.Tx == "blocked" && cov.hit[k.Key] == 0 {
			t.Errorf("the step %s %q of transaction blocked was never reached", k.Step, k.Tag.Role)
		}
	}
}

// promptRecorder is an adapter host whose connection takes every prompt and counts them.
type promptRecorder struct {
	*hostDouble
	prompts atomic.Int64
}

func (p *promptRecorder) DeliverPrompt(context.Context, string, adapterproto.PromptDeliverParams) (adapterproto.PromptDeliverResult, error) {
	p.prompts.Add(1)
	return adapterproto.PromptDeliverResult{Stage: adapterproto.PromptPersisted}, nil
}

// waitingSession is a node whose session endpoint has a message from a local sender that
// has waited since long before the node started. The adapter has not connected.
func waitingSession(t *testing.T, host engine.Adapters) (*rig, ident.EndpointID, store.TargetKey) {
	t.Helper()
	session := mustEndpoint(t, 80)
	r := newRig(t, 10, withAdapters(host))
	r.seedRecord(store.EndpointRecord{ID: session, Kind: sessionKind, Instance: "chat", Status: store.EndpointOpen,
		Token: bytes.Repeat([]byte{0x5a}, store.TokenLen), Session: &store.SessionInfo{Preset: "default", NativeRef: "native-1"}})
	in := queuedIn(t, 1, session)
	in.Key.SenderNode = r.node
	in.Dedup = delivery.NewDedupRecord(in.Key.Dedup(), digestOf(1), in.Record.ReportBy)
	if err := r.st.Deliver(bodyOf(1), []store.InRecord{in}); err != nil {
		t.Fatal(err)
	}
	r.clk.Advance(20 * time.Minute)
	return r, session, in.Key
}

func rigQuiet(t *testing.T, e *engine.Engine) {
	t.Helper()
	qctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := e.Quiesce(qctx, engine.Expect{}); err != nil {
		t.Fatalf("the engine did not settle: %v", err)
	}
}

func blockedOn(t *testing.T, r *rig, key store.TargetKey) bool {
	t.Helper()
	for _, in := range r.scan().In {
		if in.Key == key {
			return in.Record.Blocked
		}
	}
	t.Fatalf("no record %v", key)
	return false
}

// An adapter that has not connected since the node began to serve is offline since
// then, as far as the node can say: a message that waited while the node was down is
// not reported blocked in the moment the node starts, when the adapter is about to
// connect. It is, once the adapter has stayed away for the threshold.
func TestAdapterNotYetConnectedIsOfflineOnlySinceTheNodeBeganToServe(t *testing.T) {
	r, _, key := waitingSession(t, newHostDouble())
	e := r.start()
	rigQuiet(t, e)
	if blockedOn(t, r, key) {
		t.Fatal("the message is blocked at the start of the node, for a wait the node did not see")
	}
	r.clk.Advance(handoff.DefaultBlockedThreshold)
	rigQuiet(t, e)
	if blockedOn(t, r, key) {
		t.Fatal("the message is blocked after exactly the threshold")
	}
	r.clk.Advance(time.Second)
	rigQuiet(t, e)
	if !blockedOn(t, r, key) {
		t.Fatal("the message is not blocked after the adapter stayed away longer than the threshold")
	}
}

// headOf is the attempts record of the endpoint on the disk.
func headOf(t *testing.T, r *rig, id ident.EndpointID) store.AttemptsRecord {
	t.Helper()
	for _, a := range r.scan().Attempts {
		if a.Endpoint == id {
			return a
		}
	}
	return store.AttemptsRecord{Endpoint: id}
}

// A node that restarts while a block is reported does not know the adapter's cause is
// gone: the adapter has not connected yet, and "offline since the node began to serve"
// is only for a block nobody has reported. Telling the sender the block is over at the
// start would be false, and it would never hear of the block again, since a delivery
// is told as blocked once. The block goes on until a connection shows the cause gone,
// and then it is corrected once. A block counts as reported when the head says so or when
// a waiting target record carries the mark: a crash between the two writes leaves marks
// that senders may already have been told of.
func TestARestartWhileABlockIsReportedKeepsItUntilTheAdapterShowsTheCauseGone(t *testing.T) {
	for _, tc := range []struct {
		name        string
		headWritten bool
	}{
		{"the head reports the block", true},
		{"a crash left the targets marked and the head not written", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := &promptRecorder{hostDouble: newHostDouble()}
			r, session, key := waitingSession(t, host)
			since := r.clk.Now().Add(-10 * time.Minute)
			for _, in := range r.scan().In {
				rec, _, err := in.Record.Apply(since, delivery.Event{Type: delivery.EventBlocked, Cause: delivery.CauseAdapterOffline})
				if err != nil {
					t.Fatal(err)
				}
				in.Record = rec
				r.seedRecord(in)
			}
			if tc.headWritten {
				r.seedRecord(store.AttemptsRecord{Endpoint: session, Blocked: handoff.BlockedReport{Cause: handoff.CauseAdapterOffline, Since: since}})
			}
			written := r.commits("blocked")

			e := r.start()
			rigQuiet(t, e)
			r.clk.Advance(handoff.DefaultBlockedThreshold + time.Second)
			rigQuiet(t, e)
			if !blockedOn(t, r, key) {
				t.Fatal("the mark was lifted although the adapter has not connected since the node began to serve")
			}
			if got := headOf(t, r, session).Blocked.Cause; got != handoff.CauseAdapterOffline {
				t.Fatalf("the head reports cause %v, want the block that is still on", got)
			}
			// A head that was written reports nothing new; one that was not is written once,
			// when the failing run is long enough, and the marks are not touched.
			wantWrites := 0
			if !tc.headWritten {
				wantWrites = 1
			}
			if n := r.commits("blocked") - written; n != wantWrites {
				t.Fatalf("%d writes of a block while the cause held, want %d", n, wantWrites)
			}

			if err := e.AdapterStarted(ctx, "chat", adapterproto.StartResult{Sessions: []adapterproto.SessionState{}}); err != nil {
				t.Fatal(err)
			}
			r.clk.Advance(engine.DefaultParams().RetryBackoff)
			rigQuiet(t, e)
			waitUntil(t, func() bool { return host.prompts.Load() == 1 })
			rigQuiet(t, e)
			if blockedOn(t, r, key) || headOf(t, r, session).Blocked.Cause != 0 {
				t.Error("the block is still recorded after the adapter connected and the prompt was delivered")
			}
			if n := r.commits("blocked") - written; n != wantWrites+1 {
				t.Errorf("%d writes of a block in all, want %d: the report if it was missing and the one that lifts it", n, wantWrites+1)
			}
		})
	}
}

// The host logs a probe that failed and goes on, with the connection in service. What
// waited for the adapter is judged once the probe has had time to answer all the same,
// and not left for an event that may never come.
func TestSessionsOfAnAdapterWhoseProbeFailedAreStillPromptedAfterAWait(t *testing.T) {
	host := &promptRecorder{hostDouble: newHostDouble()}
	r, _, _ := waitingSession(t, host)
	e := r.start()
	if err := e.AdapterStarted(ctx, "chat", adapterproto.StartResult{Sessions: []adapterproto.SessionState{}}); err != nil {
		t.Fatal(err)
	}
	rigQuiet(t, e)
	if n := host.prompts.Load(); n != 0 {
		t.Fatalf("%d prompts were sent before the probe had time to answer, want none", n)
	}
	r.clk.Advance(engine.DefaultParams().RetryBackoff)
	rigQuiet(t, e)
	waitUntil(t, func() bool { return host.prompts.Load() == 1 })
}

// blockedSession is a session whose adapter has been away for more than the threshold
// with messages waiting, the block reported to the sender: the adapter stays away for
// the rest of the test.
func blockedSession(t *testing.T, msgs int) (h *harness.H, agent fakeagent.Agent, addr string, sent []adapterproto.MessageID) {
	t.Helper()
	h = sessionNodeOf(t, harness.Options{}, blockedHost(), "a")
	agent, addr = idleSession(t, h, "a")
	adapterEnded(t, h, h.AdapterLaunches(harness.Instance)[0])
	for i := 0; i < msgs; i++ {
		sent = append(sent, sendToAddr(t, h, "a", addr, fmt.Sprintf("message %d", i)))
	}
	engineQuiet(t, h)
	step(h, func() { engineQuiet(t, h) }, handoff.DefaultBlockedThreshold+time.Second)
	wantBlockFacts(t, h, addr, true, delivery.CauseAdapterOffline, handoff.CauseAdapterOffline, sent...)
	return h, agent, addr, sent
}

// told counts the items of each kind the endpoint has been told.
func told(t testing.TB, h *harness.H, who string) map[delivery.Kind]int {
	t.Helper()
	out := map[delivery.Kind]int{}
	for _, k := range toldKinds(t, h, who) {
		out[k]++
	}
	return out
}

// A message that arrives while delivery to its endpoint is blocked is blocked too: it
// waits behind the same cause, and its sender is told as the senders of the others were.
func TestMessageArrivingDuringABlockIsMarkedBlockedToo(t *testing.T) {
	h, _, addr, sent := blockedSession(t, 1)
	settle := func() { engineQuiet(t, h) }
	late := sendToAddr(t, h, "a", addr, "sent after the report")
	settle()
	wantBlockFacts(t, h, addr, true, delivery.CauseAdapterOffline, handoff.CauseAdapterOffline, late, sent[0])
	for i := 0; i < 4; i++ {
		step(h, settle, time.Minute)
	}
	if got := told(t, h, "a"); got[delivery.KindDeliveryBlocked] != 2 || len(got) != 1 {
		t.Errorf("a was told %v, want one block for each of the two messages and nothing else", got)
	}
}

// The agent reads what waits although its adapter is away: nothing waits any more, so
// the block is over for what it has taken, and the sender who was told of it is told it
// is over. No record keeps the mark.
func TestReadingTheBlockedMessagesEndsTheBlock(t *testing.T) {
	h, agent, addr, sent := blockedSession(t, 2)
	settle := func() { engineQuiet(t, h) }
	for i := 0; i < 3; i++ {
		step(h, settle, time.Minute)
	}
	if got := told(t, h, "a"); got[delivery.KindDeliveryBlocked] != 2 || len(got) != 1 {
		t.Fatalf("a was told %v before the read, want the two blocks", got)
	}
	if page := recvAs(t, h, agent, fakeagent.RecvInput{}); len(page.Page.Messages) != 2 {
		t.Fatalf("the page holds %v, want both messages", pageIDs(page))
	}
	settle()
	wantBlockFacts(t, h, addr, false, 0, 0, sent...)
	for i := 0; i < 3; i++ {
		step(h, settle, time.Minute)
	}
	if got := told(t, h, "a"); got[delivery.KindDeliveryBlocked] != 2 || got[delivery.KindBlockedRecovered] != 2 || len(got) != 2 {
		t.Errorf("a was told %v, want each block and each recovery once", got)
	}
}
