package engine_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// A session endpoint that is closed leaves a native session behind, and the node owes the
// adapter a release of it until the adapter says it is released (or that it is gone). The
// fact is on the endpoint record, so what the node owes is found again after a restart.
// The tests of the first part run the whole node over a fake adapter and judge from the
// ledger of the fakes and the records on the disk; those of the second part run the engine
// over a host that answers as the test says, which is how an adapter that never answers
// and a crash between an answer and its record are made.

// nativeSessionReleased says whether the runtime released the native session the endpoint
// maps to, from the ledger of the fake runtime.
func nativeSessionReleased(t testing.TB, h *harness.H, ep store.EndpointRecord) bool {
	t.Helper()
	log, err := h.Ledger()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range log.Kind(ledger.KindRuntimeClosed) {
		if e.Attrs.NativeRef == ep.Session.NativeRef {
			return true
		}
	}
	return false
}

// endpointRecord is the record of the endpoint on the disk.
func endpointRecord(t testing.TB, sc *store.Scan, id ident.EndpointID) store.EndpointRecord {
	t.Helper()
	for _, ep := range sc.Endpoints {
		if ep.ID == id {
			return ep
		}
	}
	t.Fatalf("no endpoint record for %s", id)
	return store.EndpointRecord{}
}

func wantReleased(t testing.TB, ep store.EndpointRecord, released bool) {
	t.Helper()
	if ep.Status != store.EndpointClosed || ep.Session == nil || ep.Session.NativeRef == "" {
		t.Fatalf("the endpoint is %v with session %+v, want a closed session endpoint with a native session", ep.Status, ep.Session)
	}
	if ep.Session.Released != released {
		t.Errorf("the native session is recorded released = %v, want %v", ep.Session.Released, released)
	}
}

// uncontrolledAlerts is how many alerts of a session nobody controls name the endpoint.
func uncontrolledAlerts(t testing.TB, sc *store.Scan, id ident.EndpointID) int {
	t.Helper()
	n := 0
	for _, a := range sc.Alerts {
		if a.Kind == "control_unknown" && a.Subject == id.String() {
			n++
		}
	}
	return n
}

// The owner closes a session through the management interface: no session.close signal
// is sent by anyone, and the node releases the native session by itself.
func TestOwnerClosingASessionReleasesItsNativeSession(t *testing.T) {
	h := sessionNode(t, harness.Options{}, "a")
	_, id, _ := startedSession(t, h, "a", "work on it")
	if nativeSessionReleased(t, h, endpointRecord(t, scanOf(t, h), id)) {
		t.Fatal("the native session is released before the close")
	}

	if _, err := h.Engine().CloseEndpoint(h.Ctx(), engine.CloseRequest{Endpoint: id}); err != nil {
		t.Fatal(err)
	}
	quiet(t, h)

	if n := ledgerCount(t, h, ledger.KindCloseReceived, id); n != 1 {
		t.Errorf("the adapter was asked to close the session %d times, want once", n)
	}
	ep := endpointRecord(t, scanOf(t, h), id)
	wantReleased(t, ep, true)
	if !nativeSessionReleased(t, h, ep) {
		t.Error("the native session is not released after the owner closed its endpoint")
	}
	if n := uncontrolledAlerts(t, scanOf(t, h), id); n != 0 {
		t.Errorf("%d alerts about a session that was released at once, want none", n)
	}
}

// A close signal the adapter answers closed is the release itself: the answer is recorded
// with the close of the endpoint, and nobody asks the adapter a second time.
func TestAClosedAnswerToACloseSignalIsRecordedAsTheRelease(t *testing.T) {
	h := sessionNode(t, harness.Options{}, "a")
	_, id, _ := startedSession(t, h, "a", "")
	closing := signal(t, h, "a", addressOf(id), adapterproto.SignalSessionClose, "")
	wantClose(t, replyOf(t, h, "a", closing), adapterproto.CloseReplyClosed, 0)

	wantReleased(t, endpointRecord(t, scanOf(t, h), id), true)
	h.Advance(engine.DefaultParams().RetryBackoff)
	quiet(t, h)
	if n := ledgerCount(t, h, ledger.KindCloseReceived, id); n != 1 {
		t.Errorf("the adapter was asked to close the session %d times, want once", n)
	}
}

// A close signal the adapter could not confirm closes the endpoint all the same; the
// native session is still the node's to release, and the adapter is asked again until it
// says it is.
func TestAnUnconfirmedCloseSignalIsFollowedByAReleaseThatSucceeds(t *testing.T) {
	h := sessionNode(t, harness.Options{}, "a")
	_, id, _ := startedSession(t, h, "a", "")
	ctrl, err := h.Adapter(harness.Instance)
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.QueueOutcome(h.Ctx(), fakeadapter.RequestClose, fakeadapter.Outcome{Kind: fakeadapter.OutcomeUnconfirmed, Reason: string(adapterproto.CloseNativeUnreachable)}); err != nil {
		t.Fatal(err)
	}
	closing := signal(t, h, "a", addressOf(id), adapterproto.SignalSessionClose, "")
	wantClose(t, replyOf(t, h, "a", closing), adapterproto.CloseReplyUnconfirmed, adapterproto.CloseReasonNativeUnreachable)
	wantReleased(t, endpointRecord(t, scanOf(t, h), id), false)

	h.Advance(engine.DefaultParams().RetryBackoff)
	quiet(t, h)
	wantReleased(t, endpointRecord(t, scanOf(t, h), id), true)
	if n := ledgerCount(t, h, ledger.KindCloseReceived, id); n != 2 {
		t.Errorf("the adapter was asked to close the session %d times, want the signal's and one release", n)
	}
}

// The adapter is away when the endpoint is closed: nothing can be asked then, and the
// release is sent when the adapter connects again. The adapter that comes back is a new
// process that holds no session of a closed endpoint, so it is the native reference the
// node sends that lets it find the native session.
func TestAReleaseThatCouldNotBeSentIsSentWhenTheAdapterConnects(t *testing.T) {
	h := sessionNode(t, harness.Options{}, "a")
	_, id, _ := startedSession(t, h, "a", "")
	ep := endpointRecord(t, scanOf(t, h), id)
	adapterEnded(t, h, h.AdapterLaunches(harness.Instance)[0])

	if _, err := h.Engine().CloseEndpoint(h.Ctx(), engine.CloseRequest{Endpoint: id}); err != nil {
		t.Fatal(err)
	}
	engineQuiet(t, h)
	if n := ledgerCount(t, h, ledger.KindCloseReceived, id); n != 0 {
		t.Fatalf("the adapter was asked to close the session %d times while it was away", n)
	}
	wantReleased(t, endpointRecord(t, scanOf(t, h), id), false)

	adapterRestarted(t, h, 2)
	if n := ledgerCount(t, h, ledger.KindCloseReceived, id); n != 1 {
		t.Errorf("the adapter was asked to close the session %d times once it was back, want once", n)
	}
	wantReleased(t, endpointRecord(t, scanOf(t, h), id), true)
	if !nativeSessionReleased(t, h, ep) {
		t.Error("the native session is not released after the adapter that was away came back")
	}
}

// The owner closes an endpoint while its session is being made, and the adapter then
// reports the session as created: the endpoint stays closed, and the session that was
// made for it is released at once.
func TestASessionCreatedForAnEndpointThatWasClosedMeanwhileIsReleased(t *testing.T) {
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
	if len(eps) != 1 {
		t.Fatalf("%d session endpoints while the creation is held, want the one being made", len(eps))
	}
	id := eps[0].ID
	if _, err := h.Engine().CloseEndpoint(h.Ctx(), engine.CloseRequest{Endpoint: id}); err != nil {
		t.Fatal(err)
	}

	if err := ctrl.ReleaseHeld(h.Ctx()); err != nil {
		t.Fatal(err)
	}
	quiet(t, h)
	_ = msg

	if n := nativeSessions(t, h); n != 1 {
		t.Fatalf("%d native sessions were made, want the one the held creation made", n)
	}
	ep := endpointRecord(t, scanOf(t, h), id)
	wantReleased(t, ep, true)
	if n := ledgerCount(t, h, ledger.KindCloseReceived, id); n != 1 {
		t.Errorf("the adapter was asked to close the session %d times, want once", n)
	}
	if !nativeSessionReleased(t, h, ep) {
		t.Error("the session made for a closed endpoint is not released")
	}
}

// ---------------------------------------------------------------------------
// The engine over a host that answers as the test says

// closeHost is the adapter host whose session.close answers as a test says, and that
// remembers every call.
type closeHost struct {
	*hostDouble
	mu     sync.Mutex
	asked  []adapterproto.CloseParams
	answer func(n int) (adapterproto.CloseResult, error) // n counts the calls from 1
}

func newCloseHost(answer func(n int) (adapterproto.CloseResult, error)) *closeHost {
	return &closeHost{hostDouble: newHostDouble(), answer: answer}
}

func (h *closeHost) Close(_ context.Context, _ string, p adapterproto.CloseParams) (adapterproto.CloseResult, error) {
	h.mu.Lock()
	h.asked = append(h.asked, p)
	n := len(h.asked)
	h.mu.Unlock()
	return h.answer(n)
}

func (h *closeHost) calls() []adapterproto.CloseParams {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]adapterproto.CloseParams(nil), h.asked...)
}

var (
	answerClosed = func(int) (adapterproto.CloseResult, error) {
		return adapterproto.CloseResult{Outcome: adapterproto.CloseClosed}, nil
	}
	answerNothing = func(int) (adapterproto.CloseResult, error) {
		return adapterproto.CloseResult{}, errors.New("the adapter does not answer")
	}
)

// closedSession is a closed session endpoint of the instance chat with a native session.
func closedSession(t testing.TB, b byte, released bool) store.EndpointRecord {
	t.Helper()
	return store.EndpointRecord{
		ID: mustEndpoint(t, b), Kind: sessionKind, Instance: "chat", Status: store.EndpointClosed, Declaration: []byte("{}"),
		Session: &store.SessionInfo{Preset: "default", NativeRef: fmt.Sprintf("native-%d", b), ToolAccess: []string{engine.TokenEnv, engine.HomeEnv}, Released: released},
	}
}

// Only an answer of closed records the release; an unconfirmed close and a call that got
// no answer leave the native session owed, and what is owed is asked again after the
// retry backoff. The call names the endpoint and the native session, which is what an
// adapter that restarted finds the session by.
func TestOnlyAClosedAnswerRecordsTheRelease(t *testing.T) {
	for _, c := range []struct {
		rule     string
		answer   func(int) (adapterproto.CloseResult, error)
		released bool
	}{
		{"closed", answerClosed, true},
		{"unconfirmed", func(int) (adapterproto.CloseResult, error) {
			return adapterproto.CloseResult{Outcome: adapterproto.CloseUnconfirmed, Reason: adapterproto.CloseNativeUnreachable}, nil
		}, false},
		{"no answer", answerNothing, false},
	} {
		t.Run(c.rule, func(t *testing.T) {
			noLeaks(t)
			host := newCloseHost(c.answer)
			r := newRig(t, 10, withAdapters(host))
			ep := closedSession(t, 70, false)
			r.seedRecord(ep)
			e := r.start()
			started(t, e, "chat")
			settle(t, e)

			calls := host.calls()
			if len(calls) != 1 || calls[0].Endpoint != adapterproto.EndpointRef(ep.ID.String()) || calls[0].NativeRef != adapterproto.NativeRef(ep.Session.NativeRef) {
				t.Fatalf("the adapter was asked %+v, want one close naming the endpoint and the native session", calls)
			}
			wantReleased(t, endpointRecord(t, r.scan(), ep.ID), c.released)

			advance(t, r, e, engine.DefaultParams().RetryBackoff)
			if got, want := len(host.calls()), map[bool]int{true: 1, false: 2}[c.released]; got != want {
				t.Errorf("%d calls after the retry backoff, want %d", got, want)
			}
		})
	}
}

// A native session that is released stays so: an endpoint that records the release is
// never asked about again.
func TestAReleasedNativeSessionIsNeverAskedAboutAgain(t *testing.T) {
	noLeaks(t)
	host := newCloseHost(answerClosed)
	r := newRig(t, 10, withAdapters(host))
	r.seedRecord(closedSession(t, 70, true))
	e := r.start()
	started(t, e, "chat")
	advance(t, r, e, 10*engine.DefaultParams().RetryBackoff)
	if calls := host.calls(); len(calls) != 0 {
		t.Errorf("the adapter was asked %+v about a native session that is released", calls)
	}
}

// What is owed is asked at every connection of the adapter, not only on a timer: a
// connection that opens asks at once, whether the adapter has just started or has been
// probed into service, even when the last try was a moment ago.
func TestAReleaseIsAskedAgainAtEveryConnectionOfItsAdapter(t *testing.T) {
	noLeaks(t)
	host := newCloseHost(answerNothing)
	r := newRig(t, 10, withAdapters(host))
	r.seedRecord(closedSession(t, 70, false))
	e := r.start()
	if n := len(host.calls()); n != 0 {
		t.Fatalf("the adapter was asked %d times before it connected", n)
	}

	started(t, e, "chat")
	settle(t, e)
	if n := len(host.calls()); n != 1 {
		t.Fatalf("%d calls when the adapter started, want 1", n)
	}
	gone(t, e, "chat")
	started(t, e, "chat")
	settle(t, e)
	if n := len(host.calls()); n != 2 {
		t.Errorf("%d calls when the adapter started again, want 2: the backoff of the retry was not over", n)
	}
	if err := e.AdapterProbed(ctx, "chat", probeResult("codex", "1.5.0")); err != nil {
		t.Fatal(err)
	}
	settle(t, e)
	if n := len(host.calls()); n != 3 {
		t.Errorf("%d calls when the adapter was probed into service, want 3", n)
	}
}

// The adapter answered closed and the node went down before the answer was on the disk:
// the endpoint still says the native session is owed, so the adapter is asked again when
// the node is back, and the second answer is the record. A close of a native session that
// is released already is answered closed by the adapter, so asking twice is harmless.
func TestAReleaseIsAskedAgainAfterACrashBetweenTheAnswerAndTheRecord(t *testing.T) {
	noLeaks(t)
	host := newCloseHost(answerClosed)
	r := newRig(t, 10, withAdapters(host))
	ep := closedSession(t, 70, false)
	r.seedRecord(ep)
	e := r.start()
	rule := r.faulty.Fail(failMatch("endpoint-update", fsx.StepWrite), errIO)
	started(t, e, "chat")
	settle(t, e)
	if rule.Hits() == 0 {
		t.Fatal("the fault was not injected")
	}
	if n := len(host.calls()); n != 1 {
		t.Fatalf("%d calls before the crash, want 1", n)
	}
	wantReleased(t, endpointRecord(t, r.scan(), ep.ID), false)

	r.restart()
	e = r.start()
	started(t, e, "chat")
	settle(t, e)
	if n := len(host.calls()); n != 2 {
		t.Errorf("%d calls after the restart, want the one before the crash and one more", n)
	}
	wantReleased(t, endpointRecord(t, r.scan(), ep.ID), true)
}

// A native session that stays unreleased for the time a control signal has is told to the
// owner once, as a session that may exist and that nobody controls, and the adapter keeps
// being asked. The alert is the same fact however many times the node starts.
func TestAReleaseThatIsNeverConfirmedRaisesOneAlertAfterTheDeadline(t *testing.T) {
	noLeaks(t)
	deadline := engine.DefaultParams().ControlReplyDeadline
	host := newCloseHost(answerNothing)
	r := newRig(t, 10, withAdapters(host))
	ep := closedSession(t, 70, false)
	r.seedRecord(ep)
	e := r.start()
	started(t, e, "chat")
	settle(t, e)

	advance(t, r, e, deadline-time.Second)
	if n := uncontrolledAlerts(t, r.scan(), ep.ID); n != 0 {
		t.Fatalf("%d alerts before the deadline, want none", n)
	}
	advance(t, r, e, time.Second)
	if n := uncontrolledAlerts(t, r.scan(), ep.ID); n != 1 {
		t.Fatalf("%d alerts at the deadline, want 1", n)
	}
	before := len(host.calls())
	advance(t, r, e, 10*engine.DefaultParams().RetryBackoff)
	if n := uncontrolledAlerts(t, r.scan(), ep.ID); n != 1 {
		t.Errorf("%d alerts after more time, want still 1", n)
	}
	if len(host.calls()) <= before {
		t.Error("the adapter is no longer asked after the alert")
	}

	for restart := 1; restart <= 2; restart++ {
		r.restart()
		e = r.start()
		started(t, e, "chat")
		advance(t, r, e, deadline+time.Second)
		if n := uncontrolledAlerts(t, r.scan(), ep.ID); n != 1 {
			t.Fatalf("%d alerts after restart %d, want 1", n, restart)
		}
	}
	wantReleased(t, endpointRecord(t, r.scan(), ep.ID), false)
}

// An endpoint whose native session is released in time raises no alert, and an alert the
// owner has is not made again by a release that comes late.
func TestAReleaseThatSucceedsBeforeTheDeadlineRaisesNoAlert(t *testing.T) {
	noLeaks(t)
	host := newCloseHost(func(n int) (adapterproto.CloseResult, error) {
		if n < 2 {
			return adapterproto.CloseResult{}, errors.New("the adapter does not answer yet")
		}
		return adapterproto.CloseResult{Outcome: adapterproto.CloseClosed}, nil
	})
	r := newRig(t, 10, withAdapters(host))
	ep := closedSession(t, 70, false)
	r.seedRecord(ep)
	e := r.start()
	started(t, e, "chat")
	settle(t, e)
	advance(t, r, e, engine.DefaultParams().RetryBackoff)
	wantReleased(t, endpointRecord(t, r.scan(), ep.ID), true)
	advance(t, r, e, 10*engine.DefaultParams().ControlReplyDeadline)
	if n := uncontrolledAlerts(t, r.scan(), ep.ID); n != 0 {
		t.Errorf("%d alerts for a native session that was released in time, want none", n)
	}
}
