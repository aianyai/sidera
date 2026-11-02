package engine_test

import (
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/adapterhost"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/internal/publication"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// The attachment of a session a person opened: the owner's request makes (or finds)
// an endpoint for a native session the node did not create, gives it a token and asks
// the adapter to take hold of it. The tests of the first part run the engine over a
// host that answers as the test says, so that the answer of the adapter, and an
// adapter that is away, can be chosen; the last part runs the whole node, to see what
// a sender may do to such an endpoint.

// attachingAdmission is the admission of an adapter with a runtime that does, or does
// not, take attached sessions.
func attachingAdmission(t testing.TB, attach bool) adapterreg.Admission {
	t.Helper()
	caps := adapterproto.Capabilities{Runtime: &adapterproto.RuntimeCapabilities{Frontends: []adapterproto.Frontend{adapterproto.FrontendCLI}, Attach: attach}}
	adm, err := adapterreg.Admit(caps, mustManifest(t))
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	return adm
}

// attachRig is a node over a host double whose adapter is connected and in service, and
// takes attached sessions when attach is true and says so.
func attachRig(t *testing.T, attach bool, opts ...rigOption) (*rig, *engine.Engine, *hostDouble) {
	t.Helper()
	noLeaks(t)
	d := newHostDouble()
	r := newRig(t, 10, append([]rigOption{withAdapters(d)}, opts...)...)
	r.admit = attachingAdmission(t, attach)
	e := r.start()
	started(t, e, "chat")
	if err := e.AdapterProbed(ctx, "chat", probeResult("claude", "1.0.0")); err != nil {
		t.Fatalf("AdapterProbed: %v", err)
	}
	return r, e, d
}

var attachedAnswer = adapterproto.SessionAttachResult{Outcome: adapterproto.SessionAttachAttached}

func attachChat(ref string) engine.AttachRequest {
	return engine.AttachRequest{Adapter: "chat", NativeRef: ref}
}

func viewOf(t testing.TB, e *engine.Engine, id ident.EndpointID) engine.EndpointView {
	t.Helper()
	views, err := e.Endpoints(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range views {
		if v.ID == id {
			return v
		}
	}
	t.Fatalf("the owner's list holds no endpoint %s", id)
	return engine.EndpointView{}
}

func tokenOf(t testing.TB, text string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(text)
	if err != nil {
		t.Fatalf("the token %q is not the text of a session token: %v", text, err)
	}
	return b
}

// A session attached for the first time is a new open endpoint of origin attached with
// a token; the same session attached again while its endpoint is open is the same
// endpoint with the same token, the wake information and the name it carries replace
// those held, and the native session never changes; once the endpoint is closed the
// session attached again is a new endpoint. The wake information, which is a secret of
// the person's session, is on no disk.
func TestAttachingTheSameSessionAgainIsTheSameEndpointUntilItIsClosed(t *testing.T) {
	r, e, d := attachRig(t, true)
	d.answerAttach(attachedAnswer, nil)
	const secret = "wake-secret-value-0001"

	req := engine.AttachRequest{Adapter: "chat", NativeRef: "native-a", DisplayName: "Mine", Wake: adapterproto.WakeInfo{"WAKE_KEY": secret}}
	first, err := e.AttachSession(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	id := first.Endpoint.Endpoint
	if first.Instance != "chat" || first.Wake != (engine.WakeReport{State: engine.WakeReady}) || first.Endpoint.Node != r.node {
		t.Fatalf("the first attachment answered %+v", first)
	}
	rec := endpointRecord(t, r.scan(), id)
	if rec.Kind != adapterreg.KindSession || rec.Status != store.EndpointOpen || rec.Origin() != adapterreg.OriginAttached || rec.Instance != "chat" ||
		rec.Session.NativeRef != "native-a" || rec.Session.Preset != "" || len(rec.Session.ToolAccess) != 0 || rec.CreatedBy != "" || rec.DisplayName != "Mine" {
		t.Fatalf("the record of the attached endpoint is %+v (session %+v)", rec, rec.Session)
	}
	if got := tokenOf(t, first.Token); string(got) != string(rec.Token) {
		t.Errorf("the token returned is not the one on the endpoint")
	}
	if v := viewOf(t, e, id); v.Origin != adapterreg.OriginAttached || v.DisplayName != "Mine" {
		t.Errorf("the owner sees %+v", v)
	}

	// The adapter connecting again is listed the session with its wake information,
	// which is then in the entry and nowhere else.
	gone(t, e, "chat")
	start, err := admitFull(ctx, e, "chat", attachingAdmission(t, true))
	if err != nil {
		t.Fatal(err)
	}
	if len(start.Sessions) != 1 || start.Sessions[0].Wake["WAKE_KEY"] != secret {
		t.Fatalf("adapter.start lists %+v, want the session with its wake information", start.Sessions)
	}
	started(t, e, "chat")
	if err := e.AdapterProbed(ctx, "chat", probeResult("claude", "1.0.0")); err != nil {
		t.Fatal(err)
	}

	// The same session again: the same endpoint and token, the name replaced when one
	// is given, the wake replaced by none.
	renamed := attachChat("native-a")
	renamed.DisplayName = "Renamed"
	second, err := e.AttachSession(ctx, renamed)
	if err != nil {
		t.Fatal(err)
	}
	if second.Endpoint != first.Endpoint || second.Token != first.Token {
		t.Errorf("attached again, the answer is %+v, want the endpoint and token of %+v", second, first)
	}
	if got := endpointRecord(t, r.scan(), id); got.DisplayName != "Renamed" || got.Session.NativeRef != "native-a" || string(got.Token) != string(rec.Token) {
		t.Errorf("the record after the second attachment is %+v", got)
	}
	if _, err := e.AttachSession(ctx, attachChat("native-a")); err != nil {
		t.Fatal(err)
	}
	if got := endpointRecord(t, r.scan(), id); got.DisplayName != "Renamed" {
		t.Errorf("an attachment with no name changed the name to %q", got.DisplayName)
	}
	sent := d.sentAttaches()
	if len(sent) != 3 || sent[0].instance != "chat" || sent[0].params.Endpoint != adapterproto.EndpointRef(id.String()) ||
		sent[0].params.NativeRef != "native-a" || sent[0].params.Wake["WAKE_KEY"] != secret || len(sent[1].params.Wake) != 0 {
		t.Fatalf("the adapter was sent %+v", sent)
	}
	for path, content := range r.snapshot() {
		if strings.Contains(content, secret) {
			t.Errorf("the wake information is on the disk, in %s", path)
		}
	}

	// Closed, the endpoint is never opened again: the session attached once more is a
	// new endpoint with a new token.
	if _, err := e.CloseEndpoint(ctx, engine.CloseRequest{Endpoint: id}); err != nil {
		t.Fatal(err)
	}
	third, err := e.AttachSession(ctx, attachChat("native-a"))
	if err != nil {
		t.Fatal(err)
	}
	if third.Endpoint == first.Endpoint || third.Token == first.Token {
		t.Errorf("attached after the close, the answer is %+v, want a new endpoint and token", third)
	}
	if v := viewOf(t, e, id); v.Status != store.EndpointClosed {
		t.Errorf("the first endpoint is %v, want closed", v.Status)
	}
	for _, m := range r.log.Messages() {
		if strings.Contains(m, secret) {
			t.Errorf("the wake information is in the log: %q", m)
		}
	}
}

// A request that only finds the endpoint of a session is the ordinary attachment when
// the session has an open endpoint (the token, the wake held and the adapter's answer),
// and makes nothing when it has none, before the session was ever attached and after
// its endpoint is closed.
func TestFindingTheEndpointOfASessionMakesNoEndpoint(t *testing.T) {
	_, e, d := attachRig(t, true)
	d.answerAttach(attachedAnswer, nil)
	find := engine.AttachRequest{Adapter: "chat", NativeRef: "native-a", FindOnly: true, Wake: adapterproto.WakeInfo{"WAKE_KEY": "wake-secret-value-0002"}}

	open := openEndpoints(t, e)
	_, err := e.AttachSession(ctx, find)
	wantReason(t, err, engine.ReasonSessionNotAttached)
	if got := openEndpoints(t, e); got != open {
		t.Fatalf("a find made an endpoint: %d open, want %d", got, open)
	}

	made, err := e.AttachSession(ctx, attachChat("native-a"))
	if err != nil {
		t.Fatal(err)
	}
	found, err := e.AttachSession(ctx, find)
	if err != nil {
		t.Fatalf("finding the endpoint of an attached session: %v", err)
	}
	if found.Endpoint != made.Endpoint || found.Token != made.Token || found.Wake != (engine.WakeReport{State: engine.WakeReady}) {
		t.Errorf("the find answered %+v, want the endpoint and token of %+v and a wake that is ready", found, made)
	}
	sent := d.sentAttaches()
	if len(sent) != 2 || sent[1].params.Wake["WAKE_KEY"] != "wake-secret-value-0002" {
		t.Errorf("the adapter was sent %+v, want the wake of the find in the second attach", sent)
	}

	if _, err := e.CloseEndpoint(ctx, engine.CloseRequest{Endpoint: made.Endpoint.Endpoint}); err != nil {
		t.Fatal(err)
	}
	open = openEndpoints(t, e)
	_, err = e.AttachSession(ctx, find)
	wantReason(t, err, engine.ReasonSessionNotAttached)
	if got := openEndpoints(t, e); got != open {
		t.Errorf("a find after the close made an endpoint: %d open, want %d", got, open)
	}
}

// The ways an attachment is refused, and that a refusal writes nothing.
func TestAttachingASessionIsRefused(t *testing.T) {
	createdOpen := store.EndpointRecord{
		ID: mustEndpoint(t, 150), Kind: sessionKind, Instance: "chat", Status: store.EndpointOpen, Token: []byte("0123456789abcdef0123456789abcdef"), Declaration: []byte("{}"),
		Session: &store.SessionInfo{Preset: "default", NativeRef: "native-open", ToolAccess: []string{engine.TokenEnv, engine.HomeEnv}},
	}
	createdOwed := closedSession(t, 151, false)
	createdOwed.Session.NativeRef = "native-owed"
	createdReleased := closedSession(t, 152, true)
	createdReleased.Session.NativeRef = "native-released"

	type setup func(*rig)
	for _, tc := range []struct {
		name   string
		attach bool
		setup  setup
		req    engine.AttachRequest
		reason engine.Reason
		field  string
		detail engine.Detail
		limit  engine.Limit
	}{
		{name: "an adapter that does not take attached sessions", attach: false, req: attachChat("native-a"), reason: engine.ReasonAttachUnsupported},
		{name: "a native session an open created endpoint holds", attach: true, setup: func(r *rig) { r.seedRecord(createdOpen) },
			req: attachChat("native-open"), reason: engine.ReasonNativeSessionCreated},
		{name: "a native session whose release is owed", attach: true, setup: func(r *rig) { r.seedRecord(createdOwed) },
			req: attachChat("native-owed"), reason: engine.ReasonNativeSessionCreated},
		{name: "a native session whose release is confirmed is free again", attach: true, setup: func(r *rig) { r.seedRecord(createdReleased) },
			req: attachChat("native-released")},
		{name: "an adapter no instance runs", attach: true, req: engine.AttachRequest{Adapter: "other", NativeRef: "native-a"}, reason: engine.ReasonInstanceUnknown},
		{name: "an instance that does not exist", attach: true, req: engine.AttachRequest{Adapter: "chat", Instance: "nowhere", NativeRef: "native-a"}, reason: engine.ReasonInstanceUnknown},
		{name: "an instance that is disabled", attach: true, setup: func(r *rig) { r.enableInstance("second", ""); r.disableInstance("second") },
			req: engine.AttachRequest{Adapter: "chat", Instance: "second", NativeRef: "native-a"}, reason: engine.ReasonInstanceUnknown},
		{name: "an instance of another adapter", attach: true, req: engine.AttachRequest{Adapter: "other", Instance: "chat", NativeRef: "native-a"},
			reason: engine.ReasonInvalidArgument, field: "instance"},
		{name: "several instances and none named", attach: true, setup: func(r *rig) { r.enableInstance("second", "") },
			req: attachChat("native-a"), reason: engine.ReasonInvalidArgument, field: "instance", detail: engine.DetailInstanceRequired},
		{name: "several instances and one named, which the node has not seen declare anything yet", attach: true, setup: func(r *rig) { r.enableInstance("second", "") },
			req: engine.AttachRequest{Adapter: "chat", Instance: "second", NativeRef: "native-a"}},
		{name: "a native reference that is empty", attach: true, req: attachChat(""), reason: engine.ReasonInvalidArgument, field: "native_ref"},
		{name: "an adapter that is not an identifier", attach: true, req: engine.AttachRequest{Adapter: "Not An Id", NativeRef: "native-a"}, reason: engine.ReasonInvalidArgument, field: "adapter"},
		{name: "a name that is too long", attach: true, req: engine.AttachRequest{Adapter: "chat", NativeRef: "native-a", DisplayName: strings.Repeat("n", adapterproto.MaxDisplayName+1)},
			reason: engine.ReasonInvalidArgument, field: "display_name"},
		{name: "a wake key that is not a name", attach: true, req: engine.AttachRequest{Adapter: "chat", NativeRef: "native-a", Wake: adapterproto.WakeInfo{"bad-key": "x"}},
			reason: engine.ReasonInvalidArgument, field: "wake.bad-key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newHostDouble()
			r := newRig(t, 10, withAdapters(d))
			r.admit = attachingAdmission(t, tc.attach)
			if tc.setup != nil {
				tc.setup(r)
			}
			e := r.start()
			started(t, e, "chat")
			before := r.snapshot()
			_, err := e.AttachSession(ctx, tc.req)
			if tc.reason == 0 {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			ref := wantReason(t, err, tc.reason)
			if ref.Field != tc.field || ref.Detail != tc.detail || ref.Limit != tc.limit {
				t.Errorf("the refusal is %+v, want field %q, detail %v, limit %v", ref, tc.field, tc.detail, tc.limit)
			}
			if after := r.snapshot(); !equalSnapshots(before, after) {
				t.Errorf("a refused attachment wrote to the disk")
			}
			if n := len(d.sentAttaches()); n != 0 {
				t.Errorf("the adapter was asked %d times for a refused attachment", n)
			}
		})
	}
}

func equalSnapshots(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || v != w {
			return false
		}
	}
	return true
}

// An attached endpoint counts against the cap of the node as any endpoint does.
func TestAnAttachedEndpointTakesAPlaceOfTheNodeCap(t *testing.T) {
	_, e, d := attachRig(t, true)
	d.answerAttach(attachedAnswer, nil)
	held := openEndpoints(t, e)
	for i := held; i < publication.MaxEndpointsPerNode; i++ {
		if _, err := e.AttachSession(ctx, attachChat(fmt.Sprintf("native-%d", i))); err != nil {
			t.Fatalf("attachment %d: %v", i, err)
		}
	}
	_, err := e.AttachSession(ctx, attachChat("native-one-too-many"))
	if ref := wantReason(t, err, engine.ReasonEndpointLimit); ref.Limit != engine.LimitNode {
		t.Errorf("the limit is %v, want the node's", ref.Limit)
	}
	// An endpoint that is already there is found, not counted again.
	if _, err := e.AttachSession(ctx, attachChat("native-"+fmt.Sprint(held))); err != nil {
		t.Errorf("attaching a session that is attached already: %v", err)
	}
	if got := openEndpoints(t, e); got != publication.MaxEndpointsPerNode {
		t.Errorf("%d endpoints open, want %d", got, publication.MaxEndpointsPerNode)
	}
}

// The wake state is what the adapter said to session.attach, whatever it is: the
// endpoint and the token are returned all the same.
func TestTheWakeStateIsWhatTheAdapterSaid(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer adapterproto.SessionAttachResult
		err    error
		away   bool // the adapter's connection ended
		cold   bool // the adapter has not been probed
		hold   bool // the adapter does not answer in time
		want   engine.WakeReport
	}{
		{name: "attached", answer: attachedAnswer, want: engine.WakeReport{State: engine.WakeReady}},
		{name: "not found", answer: adapterproto.SessionAttachResult{Outcome: adapterproto.SessionAttachNotFound},
			want: engine.WakeReport{State: engine.WakeUnavailable, Reason: engine.WakeReasonNativeNotFound}},
		{name: "failed, wake unavailable, with a remark", answer: adapterproto.SessionAttachResult{Outcome: adapterproto.SessionAttachFailed, Reason: adapterproto.AttachFailWakeUnavailable, Detail: "runtime too old"},
			want: engine.WakeReport{State: engine.WakeUnavailable, Reason: "wake_unavailable", Detail: "runtime too old"}},
		{name: "failed, native unreachable", answer: adapterproto.SessionAttachResult{Outcome: adapterproto.SessionAttachFailed, Reason: adapterproto.AttachFailNativeUnreachable},
			want: engine.WakeReport{State: engine.WakeUnavailable, Reason: "native_unreachable"}},
		{name: "failed, native error", answer: adapterproto.SessionAttachResult{Outcome: adapterproto.SessionAttachFailed, Reason: adapterproto.AttachFailNativeError},
			want: engine.WakeReport{State: engine.WakeUnavailable, Reason: "native_error"}},
		{name: "a call that failed", err: errors.New("the connection broke"), want: engine.WakeReport{State: engine.WakeUnknown, Reason: engine.WakeReasonNoAnswer}},
		{name: "an outcome nobody knows", answer: adapterproto.SessionAttachResult{Outcome: "later"}, want: engine.WakeReport{State: engine.WakeUnknown, Reason: engine.WakeReasonNoAnswer}},
		{name: "an adapter that is not connected", err: adapterhost.ErrNotConnected, want: engine.WakeReport{State: engine.WakeUnknown, Reason: engine.WakeReasonAdapterOffline}},
		{name: "an adapter whose connection ended", answer: attachedAnswer, away: true, want: engine.WakeReport{State: engine.WakeUnknown, Reason: engine.WakeReasonAdapterOffline}},
		{name: "an adapter not in service yet", answer: attachedAnswer, cold: true, want: engine.WakeReport{State: engine.WakeUnknown, Reason: engine.WakeReasonAdapterOffline}},
		{name: "an adapter that does not answer", hold: true, want: engine.WakeReport{State: engine.WakeUnknown, Reason: engine.WakeReasonNoAnswer}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := engine.DefaultParams()
			p.AttachWait = 50 * time.Millisecond
			var (
				r *rig
				e *engine.Engine
				d *hostDouble
			)
			if tc.cold {
				noLeaks(t)
				d = newHostDouble()
				r = newRig(t, 10, withAdapters(d), withParams(p))
				r.admit = attachingAdmission(t, true)
				e = r.start()
				started(t, e, "chat")
			} else {
				r, e, d = attachRig(t, true, withParams(p))
			}
			if tc.away {
				gone(t, e, "chat")
			}
			d.answerAttach(tc.answer, tc.err)
			if tc.hold {
				d.mu.Lock()
				d.attachHold = make(chan struct{})
				d.mu.Unlock()
			}
			res, err := e.AttachSession(ctx, attachChat("native-a"))
			if err != nil {
				t.Fatal(err)
			}
			if res.Wake != tc.want {
				t.Errorf("the wake state is %+v, want %+v", res.Wake, tc.want)
			}
			if res.Token == "" || res.Endpoint.Endpoint.IsZero() {
				t.Errorf("the answer %+v lacks the endpoint or the token", res)
			}
			if rec := endpointRecord(t, r.scan(), res.Endpoint.Endpoint); rec.Status != store.EndpointOpen {
				t.Errorf("the endpoint is %v whatever the adapter said", rec.Status)
			}
			if sent := len(d.sentAttaches()); (tc.cold || tc.away) && sent != 0 {
				t.Errorf("the adapter was asked %d times though it cannot be", sent)
			}
		})
	}
}

// adapter.start lists an attached session, with its wake information, to an adapter
// that declared it takes attached sessions; the wake information is memory, so after a
// restart the entry comes without it; an adapter that does not declare it is listed
// none, and the attached endpoints it cannot take are closed, where they are not
// prompted against an adapter that refuses.
func TestAdapterStartListsAnAttachedSessionOnlyToAnAdapterThatTakesIt(t *testing.T) {
	r, e, d := attachRig(t, true)
	d.answerAttach(attachedAnswer, nil)
	res, err := e.AttachSession(ctx, engine.AttachRequest{Adapter: "chat", NativeRef: "native-a", Wake: adapterproto.WakeInfo{"WAKE_KEY": "wake-value"}})
	if err != nil {
		t.Fatal(err)
	}
	id := res.Endpoint.Endpoint
	want := adapterproto.SessionAttach{Endpoint: adapterproto.EndpointRef(id.String()), NativeRef: "native-a", Attached: true, Wake: adapterproto.WakeInfo{"WAKE_KEY": "wake-value"}}

	gone(t, e, "chat")
	start, err := admitFull(ctx, e, "chat", attachingAdmission(t, true))
	if err != nil {
		t.Fatal(err)
	}
	if len(start.Sessions) != 1 || !sameAttach(start.Sessions[0], want) {
		t.Fatalf("a declarer is sent %+v, want %+v", start.Sessions, want)
	}

	// A new process of the node knows the session and not its wake.
	r.restart()
	e = r.start()
	start, err = admitFull(ctx, e, "chat", attachingAdmission(t, true))
	if err != nil {
		t.Fatal(err)
	}
	want.Wake = nil
	if len(start.Sessions) != 1 || !sameAttach(start.Sessions[0], want) {
		t.Fatalf("after a restart a declarer is sent %+v, want %+v", start.Sessions, want)
	}

	// An adapter that does not take attached sessions is sent none and the endpoint is
	// closed; nothing is sent to it about that.
	gone(t, e, "chat")
	start, err = admitFull(ctx, e, "chat", attachingAdmission(t, false))
	if err != nil {
		t.Fatal(err)
	}
	if len(start.Sessions) != 0 {
		t.Errorf("an adapter that does not declare attach is sent %+v", start.Sessions)
	}
	if rec := endpointRecord(t, r.scan(), id); rec.Status != store.EndpointClosed || rec.Session.Released {
		t.Errorf("the attached endpoint is %v (released %v), want closed with no release recorded", rec.Status, rec.Session.Released)
	}
	if n := len(d.sentDetaches()); n != 0 {
		t.Errorf("an adapter that cannot take the session was told it is detached %d times", n)
	}
	if n := d.closes.Load(); n != 0 {
		t.Errorf("the adapter was asked %d times to close the person's session", n)
	}
}

func sameAttach(a, b adapterproto.SessionAttach) bool {
	return a.Endpoint == b.Endpoint && a.NativeRef == b.NativeRef && a.Attached == b.Attached && a.Preset == "" && a.ToolAccess == nil &&
		len(a.Wake) == len(b.Wake) && (len(a.Wake) == 0 || a.Wake["WAKE_KEY"] == b.Wake["WAKE_KEY"])
}

// An attached session that is closed is told to its adapter as detached, and the
// adapter is never asked to close the person's session; a close the adapter would not
// be told about (it is away) writes nothing more.
func TestClosingAnAttachedSessionDetachesItAndNeverClosesTheSession(t *testing.T) {
	_, e, d := attachRig(t, true)
	d.answerAttach(attachedAnswer, nil)
	res, err := e.AttachSession(ctx, attachChat("native-a"))
	if err != nil {
		t.Fatal(err)
	}
	id := res.Endpoint.Endpoint
	out, err := e.CloseEndpoint(ctx, engine.CloseRequest{Endpoint: id})
	if err != nil {
		t.Fatal(err)
	}
	settle(t, e)
	if out.Outcome != engine.CloseOutcomeClosed {
		t.Errorf("closing an attached endpoint says %v, want closed: no native session is to be released", out.Outcome)
	}
	want := []sentDetach{{"chat", adapterproto.SessionDetachedParams{Endpoint: adapterproto.EndpointRef(id.String())}}}
	if got := d.sentDetaches(); !slices.Equal(got, want) {
		t.Errorf("the adapter was told %+v, want %+v", got, want)
	}
	if n := d.closes.Load(); n != 0 {
		t.Errorf("the adapter was asked %d times to close the person's session (the close said %v)", n, out.Outcome)
	}
	// Closed again: nothing more is said.
	if _, err := e.CloseEndpoint(ctx, engine.CloseRequest{Endpoint: id}); err != nil {
		t.Fatal(err)
	}
	if got := d.sentDetaches(); len(got) != 1 {
		t.Errorf("the adapter was told %d times", len(got))
	}
}

// The wake information does not outlive the process, but the endpoint does: one that is
// closed after a restart, before the session attaches again, is still told to the
// adapter as detached.
func TestClosingAnAttachedSessionAfterARestartStillDetachesIt(t *testing.T) {
	r, e, d := attachRig(t, true)
	d.answerAttach(attachedAnswer, nil)
	res, err := e.AttachSession(ctx, engine.AttachRequest{Adapter: "chat", NativeRef: "native-a", Wake: adapterproto.WakeInfo{"WAKE_KEY": "wake-value"}})
	if err != nil {
		t.Fatal(err)
	}
	id := res.Endpoint.Endpoint
	r.restart()
	e = r.start()
	started(t, e, "chat")
	if _, err := e.CloseEndpoint(ctx, engine.CloseRequest{Endpoint: id}); err != nil {
		t.Fatal(err)
	}
	settle(t, e)
	want := []sentDetach{{"chat", adapterproto.SessionDetachedParams{Endpoint: adapterproto.EndpointRef(id.String())}}}
	if got := d.sentDetaches(); !slices.Equal(got, want) {
		t.Errorf("the adapter was told %+v, want %+v", got, want)
	}
}

// The pages of an attached session are read by the person's own runtime, so there is
// nothing for the adapter to observe about them.
func TestRecvObservedIsRefusedForAnAttachedSession(t *testing.T) {
	_, e, d := attachRig(t, true)
	d.answerAttach(attachedAnswer, nil)
	res, err := e.AttachSession(ctx, attachChat("native-a"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.RecvObserved(ctx, "chat", adapterproto.RecvObservedParams{Endpoint: adapterproto.EndpointRef(res.Endpoint.Endpoint.String())})
	wantReason(t, err, engine.ReasonWrongEndpointKind)
}

// What the directory says an attached endpoint accepts is what a close and an
// interrupt are answered: a signal the directory does not list is unsupported, the
// adapter is not asked, and a session the node created, which lists them, is not
// answered unsupported.
func TestTheDirectoryAndTheAnswerToASignalAgreeOnAnAttachedSession(t *testing.T) {
	h := sessionNode(t, harness.Options{}, "a")
	_, made, _ := startedSession(t, h, "a", "")
	res, err := h.Engine().AttachSession(h.Ctx(), engine.AttachRequest{Adapter: harness.Instance, NativeRef: "a-session-a-person-opened"})
	if err != nil {
		t.Fatal(err)
	}
	quiet(t, h)
	attached := res.Endpoint.Endpoint
	if res.Wake.State != engine.WakeUnavailable || res.Wake.Reason != engine.WakeReasonNativeNotFound {
		t.Errorf("the fake runtime has no such session, and the wake state is %+v", res.Wake)
	}

	page, err := h.Engine().Directory(h.Ctx(), directoryCaller(t, h, "a"), engine.DirectoryQuery{})
	if err != nil {
		t.Fatal(err)
	}
	accepts := map[ident.EndpointID][]authz.Action{}
	for _, ep := range page.Endpoints {
		accepts[ep.Address.Endpoint] = ep.Accepts
	}
	if got, want := accepts[attached], []authz.Action{authz.ActionMessageSend, authz.ActionEndpointStatus}; !slices.Equal(got, want) {
		t.Fatalf("the directory lists %v for the attached session, want %v", got, want)
	}
	for _, a := range []authz.Action{authz.ActionSessionClose, authz.ActionSessionInterrupt} {
		if !slices.Contains(accepts[made], a) {
			t.Errorf("the directory does not list %s for the session the node created: %v", a, accepts[made])
		}
	}

	closeAsked, interruptAsked := ledgerCount(t, h, ledger.KindCloseReceived, attached), ledgerCount(t, h, ledger.KindInterruptReceived, attached)
	wantClose(t, replyOf(t, h, "a", signal(t, h, "a", addressOf(attached), adapterproto.SignalSessionClose, "")), adapterproto.CloseReplyUnsupported, 0)
	wantInterrupt(t, replyOf(t, h, "a", signal(t, h, "a", addressOf(attached), adapterproto.SignalSessionInterrupt, "")), adapterproto.InterruptReplyUnsupported, 0)
	if ledgerCount(t, h, ledger.KindCloseReceived, attached) != closeAsked || ledgerCount(t, h, ledger.KindInterruptReceived, attached) != interruptAsked {
		t.Errorf("the adapter was asked to close or interrupt the person's session")
	}
	if r := replyOf(t, h, "a", signal(t, h, "a", addressOf(made), adapterproto.SignalSessionInterrupt, "")); r == nil || r.Interrupt == nil || r.Interrupt.Outcome == adapterproto.InterruptReplyUnsupported {
		t.Errorf("an interrupt to the session the node created was answered %+v", r)
	}
}
