package engine

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/reply"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func idOf(t testing.TB, b byte) ident.EndpointID {
	t.Helper()
	id, err := ident.EndpointIDFromBytes(bytes.Repeat([]byte{b}, ident.EndpointIDLen))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func nodeOf(t testing.TB, b byte) ident.NodeID {
	t.Helper()
	n, err := ident.NodeIDFromBytes(bytes.Repeat([]byte{b}, ident.NodeIDLen))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func tokenBytes(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }

func tokenText(b byte) adapterproto.Token {
	return adapterproto.Token(base64.RawURLEncoding.EncodeToString(tokenBytes(b)))
}

// loose is a text that a lenient decoder reads as the bytes of t: the last
// character of 32 bytes in base64url carries four bits, and its other two bits are
// spare.
func loose(t adapterproto.Token) adapterproto.Token {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	s := string(t)
	i := strings.IndexByte(alphabet, s[len(s)-1])
	return adapterproto.Token(s[:len(s)-1] + string(alphabet[i+1]))
}

func TestTokenIndexFindsOnlyWhatWasAddedAndNotWhatWasRemoved(t *testing.T) {
	x := newTokenIndex()
	a, b := idOf(t, 1), idOf(t, 2)
	x.add(a, tokenBytes(0x41))
	x.add(b, tokenBytes(0x42))

	for _, tc := range []struct {
		name  string
		token adapterproto.Token
		want  ident.EndpointID
		found bool
	}{
		{"the first", tokenText(0x41), a, true},
		{"the second", tokenText(0x42), b, true},
		{"a token nobody holds", tokenText(0x43), ident.EndpointID{}, false},
		{"nothing", "", ident.EndpointID{}, false},
		{"text that is not base64url", "!!!", ident.EndpointID{}, false},
		{"too short", tokenText(0x41)[:20], ident.EndpointID{}, false},
		{"padded", adapterproto.Token(base64.StdEncoding.EncodeToString(tokenBytes(0x41))), ident.EndpointID{}, false},
		{"a spelling of the same bytes with the spare bits of the last character set", loose(tokenText(0x41)), ident.EndpointID{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := x.lookup(tc.token)
			if ok != tc.found || (ok && got != tc.want) {
				t.Errorf("lookup = %s, %v; want %s, %v", got, ok, tc.want, tc.found)
			}
		})
	}

	// What looks like the token to a lenient decoder is not the token: the format
	// is adapterproto's, and the lookup asks it.
	for name, text := range NotTheToken(string(tokenText(0x41))) {
		t.Run("not the token: "+name, func(t *testing.T) {
			if got, ok := x.lookup(adapterproto.Token(text)); ok {
				t.Errorf("lookup(%q) found %s, want nothing", text, got)
			}
		})
	}

	x.remove(a)
	if _, ok := x.lookup(tokenText(0x41)); ok {
		t.Errorf("a removed token still finds its endpoint")
	}
	if _, ok := x.lookup(tokenText(0x42)); !ok {
		t.Errorf("removing one endpoint's token took another's")
	}
	// An endpoint holds one token: a new one replaces the old, which finds nothing.
	x.add(b, tokenBytes(0x44))
	if _, ok := x.lookup(tokenText(0x42)); ok {
		t.Errorf("the replaced token of an endpoint still finds it")
	}
	if got, ok := x.lookup(tokenText(0x44)); !ok || got != b {
		t.Errorf("the new token of an endpoint finds %s, %v", got, ok)
	}
	x.remove(idOf(t, 9)) // an endpoint without a token: nothing happens
	// Nothing is kept for a token that no endpoint holds any more.
	if len(x.byHash) != 1 || len(x.byEndpoint) != 1 {
		t.Errorf("the index holds %d hashes for %d tokens, want 1 and 1", len(x.byHash), len(x.byEndpoint))
	}
}

// FuzzTokenIndexLookup looks up arbitrary text in an index that holds one token.
// Whatever the text is, the lookup does not panic, and it finds the endpoint only
// for the exact text of the token.
func FuzzTokenIndexLookup(f *testing.F) {
	held := tokenText(0x41)
	x := newTokenIndex()
	id := idOf(f, 1)
	x.add(id, tokenBytes(0x41))
	f.Add(string(held))
	for _, text := range NotTheToken(string(held)) {
		f.Add(text)
	}
	f.Fuzz(func(t *testing.T, text string) {
		got, ok := x.lookup(adapterproto.Token(text))
		if ok != (text == string(held)) || (ok && got != id) {
			t.Fatalf("lookup(%q) = %s, %v; the token is %q", text, got, ok, held)
		}
	})
}

// scanOf is a scan of the given endpoints and records, as the store returns it.
func endpointRec(t testing.TB, id byte, kind adapterreg.EndpointKind, status store.EndpointStatus, instance string) store.EndpointRecord {
	t.Helper()
	r := store.EndpointRecord{ID: idOf(t, id), Kind: kind, Instance: instance, Status: status, Token: tokenBytes(0x40 + id)}
	if status == store.EndpointClosed {
		r.Token = nil
	}
	if kind == adapterreg.KindSession {
		r.Session = &store.SessionInfo{Preset: "default"}
	}
	return r
}

func inRec(t testing.TB, i int, recipient ident.EndpointID, state delivery.State) store.InRecord {
	t.Helper()
	k := store.TargetKey{SenderNode: nodeOf(t, 2), Sender: idOf(t, 3), Message: delivery.MessageID(fmt.Sprintf("msg-%04d", i)), Recipient: recipient}
	accept := time.Date(2026, 10, 24, 9, i, 0, 0, time.UTC)
	rec, err := delivery.NewTargetRecord(accept, accept, delivery.ReportBy(accept), delivery.DefaultEndpointMaxQueue)
	if err != nil {
		t.Fatal(err)
	}
	body := store.BodyRefOf([]byte(fmt.Sprintf("body %d", i)))
	in := store.InRecord{Key: k, Record: rec, Auth: delivery.Authorization{Kind: delivery.AuthorizationAllow}, ReplyDecision: reply.Decision{Reason: reply.ReasonDisabled}, AcceptedAt: accept, Body: &body}
	in.Record.State = state
	if state.IsTerminal() {
		in.Body = nil
	}
	return in
}

func outRec(t testing.TB, i int, states ...delivery.State) store.OutRecord {
	t.Helper()
	k := store.OutKey{Sender: idOf(t, 1), Message: delivery.MessageID(fmt.Sprintf("out-%04d", i))}
	o := store.OutRecord{Key: k, Outbound: envelope.Outbound{Message: k.Message, From: k.Sender}}
	body := store.BodyRefOf([]byte(fmt.Sprintf("out body %d", i)))
	o.Body = &body
	queued := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	for j, st := range states {
		to := ident.Address{Node: nodeOf(t, 2), Endpoint: idOf(t, byte(10+j))}
		k.Recipients = append(k.Recipients, to)
		rec := delivery.NewSourceRecord(queued, queued)
		rec.State = st
		o.Outbound.Recipients = append(o.Outbound.Recipients, envelope.Tracked{Address: to, Delivery: rec})
	}
	o.Key = k
	return o
}

// ctlRec is a control record for recipient in the given state.
func ctlRec(t testing.TB, i int, recipient ident.EndpointID, state delivery.State) store.CtlRecord {
	t.Helper()
	k := store.TargetKey{SenderNode: nodeOf(t, 2), Sender: idOf(t, 3), Message: delivery.MessageID(fmt.Sprintf("ctl-%04d", i)), Recipient: recipient}
	accept := time.Date(2026, 10, 24, 9, i, 0, 0, time.UTC)
	rec := delivery.NewTargetControlRecord(accept, accept, delivery.ReportBy(accept))
	rec.State = state
	return store.CtlRecord{Key: k, Record: rec, Kind: envelope.KindSessionClose, AcceptedAt: accept}
}

// The view is built from a scan and holds what the loop decides with: the
// endpoints by id and by token, and the number of unfinished deliveries; the
// records it holds say which of them refer to a body.
func TestBuildViewCountsWhatTheLoopDecidesWith(t *testing.T) {
	direct := endpointRec(t, 1, adapterreg.KindDirect, store.EndpointOpen, "chat")
	openSession := endpointRec(t, 2, adapterreg.KindSession, store.EndpointOpen, "claude")
	creating := endpointRec(t, 3, adapterreg.KindSession, store.EndpointCreating, "claude")
	closed := endpointRec(t, 4, adapterreg.KindSession, store.EndpointClosed, "claude")
	leased := inRec(t, 7, direct.ID, delivery.StateAccepted)
	leased.Record.Lease = delivery.Lease{Active: true, Handouts: 1}
	sc := &store.Scan{
		Endpoints: []store.EndpointRecord{direct, openSession, creating, closed},
		In: []store.InRecord{
			inRec(t, 1, direct.ID, delivery.StateAccepted), // queued
			inRec(t, 2, direct.ID, delivery.StateAccepted), // queued
			leased, // leased: still accepted
			inRec(t, 3, direct.ID, delivery.StateDeliveryUnknown),
			inRec(t, 4, direct.ID, delivery.StateDelivered),
			inRec(t, 5, direct.ID, delivery.StateRejected),
			inRec(t, 6, direct.ID, delivery.StateClosedUnconfirmed),
		},
		Ctl: []store.CtlRecord{
			ctlRec(t, 1, direct.ID, delivery.StateAccepted),
			ctlRec(t, 2, direct.ID, delivery.StateDelivered),
			ctlRec(t, 3, direct.ID, delivery.StateDeliveryUnknown),
		},
		Out: []store.OutRecord{
			outRec(t, 1, delivery.StateQueued, delivery.StateQueued, delivery.StateAccepted),
			outRec(t, 2, delivery.StateDelivered),
			outRec(t, 3, delivery.StateDeliveryUnknown, delivery.StateQueued),
		},
	}
	v, err := buildView(nodeOf(t, 7), sc)
	if err != nil {
		t.Fatalf("buildView: %v", err)
	}
	// The oracle is the definition of an unfinished delivery, written out here from
	// what each kind of record is waiting for, not from how the view counts:
	//   - a message for an endpoint is unfinished while it is accepted, queued or
	//     leased, for it still has to be handed out;
	//   - a control signal is unfinished while it is accepted, for it is pending
	//     work that entered through a new-work entry;
	//   - a delivery of a message this node sent is unfinished while no receipt
	//     answered it (state queued).
	// A delivery whose outcome is unknown, or that is final, holds nothing to hand
	// out and is not counted.
	want := 0
	for _, in := range sc.In {
		if in.Record.State == delivery.StateAccepted {
			want++
		}
	}
	for _, c := range sc.Ctl {
		if c.Record.State == delivery.StateAccepted {
			want++
		}
	}
	for _, o := range sc.Out {
		for _, r := range o.Outbound.Recipients {
			if r.Delivery.State == delivery.StateQueued {
				want++
			}
		}
	}
	if want != 3+1+3 { // messages 1, 2 and 7; the accepted control record; three queued source deliveries
		t.Fatalf("the oracle counts %d, the fixture is meant to hold 7", want)
	}
	if got := v.Unfinished(); got != want {
		t.Errorf("Unfinished = %d, want %d", got, want)
	}
	if got := v.OpenEndpoints(); got != 3 {
		t.Errorf("OpenEndpoints = %d, want the open and the creating ones, 3", got)
	}
	// Bodies: the three target records that are waiting hold theirs; the one whose
	// outcome is unknown is a fixture that keeps its reference; a final one holds
	// none; each source record holds one. The view keeps the records as they are
	// stored, so what a record refers to is read off it.
	holders := func(text string) int {
		ref := store.BodyRefOf([]byte(text))
		n := 0
		for _, in := range v.in {
			if in.Body != nil && *in.Body == ref {
				n++
			}
		}
		for _, o := range v.out {
			if o.Body != nil && *o.Body == ref {
				n++
			}
		}
		return n
	}
	for _, tc := range []struct {
		body string
		want int
	}{
		{"body 1", 1}, {"body 3", 1}, {"body 4", 0}, {"body 7", 1}, {"out body 1", 1}, {"out body 2", 1}, {"unknown", 0},
	} {
		if got := holders(tc.body); got != tc.want {
			t.Errorf("records that refer to %q: %d, want %d", tc.body, got, tc.want)
		}
	}
}

func TestResolveGivesTheEndpointOfACallerAndRefusesTheRest(t *testing.T) {
	direct := endpointRec(t, 1, adapterreg.KindDirect, store.EndpointOpen, "chat")
	openSession := endpointRec(t, 2, adapterreg.KindSession, store.EndpointOpen, "claude")
	creating := endpointRec(t, 3, adapterreg.KindSession, store.EndpointCreating, "claude")
	closedSession := endpointRec(t, 4, adapterreg.KindSession, store.EndpointClosed, "claude")
	closedDirect := endpointRec(t, 5, adapterreg.KindDirect, store.EndpointClosed, "chat")
	runtime := endpointRec(t, 6, adapterreg.KindRuntime, store.EndpointOpen, "chat")
	v, err := buildView(nodeOf(t, 7), &store.Scan{Endpoints: []store.EndpointRecord{direct, openSession, creating, closedSession, closedDirect, runtime}})
	if err != nil {
		t.Fatal(err)
	}
	ref := func(id ident.EndpointID) adapterproto.EndpointRef { return adapterproto.EndpointRef(id.String()) }

	for _, tc := range []struct {
		name   string
		caller Caller
		want   ident.EndpointID // zero: refused
		reason Reason
	}{
		{"the token of an open session endpoint", ByToken(tokenText(0x42)), openSession.ID, 0},
		{"the token of a session endpoint still being created", ByToken(tokenText(0x43)), ident.EndpointID{}, ReasonUnauthenticated},
		{"the token of a direct endpoint", ByToken(tokenText(0x41)), ident.EndpointID{}, ReasonUnauthenticated},
		{"the token of a runtime endpoint", ByToken(tokenText(0x46)), ident.EndpointID{}, ReasonUnauthenticated},
		{"the token of a closed endpoint", ByToken(tokenText(0x44)), ident.EndpointID{}, ReasonUnauthenticated},
		{"no caller", Caller{}, ident.EndpointID{}, ReasonUnauthenticated},
		{"an adapter for its direct endpoint", ByAdapter("chat", ref(direct.ID)), direct.ID, 0},
		{"an adapter for the direct endpoint of another instance", ByAdapter("claude", ref(direct.ID)), ident.EndpointID{}, ReasonEndpointUnknown},
		{"an adapter for a session endpoint", ByAdapter("claude", ref(openSession.ID)), ident.EndpointID{}, ReasonEndpointUnknown},
		{"an adapter for its runtime endpoint", ByAdapter("chat", ref(runtime.ID)), ident.EndpointID{}, ReasonEndpointUnknown},
		{"an adapter for a closed direct endpoint", ByAdapter("chat", ref(closedDirect.ID)), ident.EndpointID{}, ReasonEndpointUnknown},
		{"an adapter for an endpoint nobody has", ByAdapter("chat", ref(idOf(t, 99))), ident.EndpointID{}, ReasonEndpointUnknown},
		{"an adapter with text that is no endpoint", ByAdapter("chat", "x"), ident.EndpointID{}, ReasonEndpointUnknown},
		{"an adapter for the system endpoint", ByAdapter("chat", ref(ident.SystemEndpoint())), ident.EndpointID{}, ReasonEndpointUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := v.resolve(tc.caller)
			if tc.want.IsZero() {
				if err == nil {
					t.Fatalf("resolved to %s, want a refusal", got.rec.ID)
				}
				if r, ok := ReasonOf(err); !ok || r != tc.reason {
					t.Errorf("error %v, want reason %s", err, tc.reason)
				}
				return
			}
			if err != nil || got.rec.ID != tc.want {
				t.Errorf("resolve = %v, %v; want %s", got, err, tc.want)
			}
		})
	}
}

func TestBuildViewRefusesAnEndpointWhoseDeclarationItCannotRead(t *testing.T) {
	ep := endpointRec(t, 1, adapterreg.KindDirect, store.EndpointOpen, "chat")
	ep.Declaration = []byte(`{"paused":"yes"}`)
	if _, err := buildView(nodeOf(t, 7), &store.Scan{Endpoints: []store.EndpointRecord{ep}}); err == nil {
		t.Fatal("buildView accepted an endpoint record with a declaration of another shape")
	}
}

func TestViewKnowsAnEndpointIsDrainingWhileDeliveriesAreStillQueuedForItAfterItClosed(t *testing.T) {
	open := endpointRec(t, 1, adapterreg.KindDirect, store.EndpointOpen, "chat")
	closedQueued := endpointRec(t, 2, adapterreg.KindDirect, store.EndpointClosed, "chat")
	closedDone := endpointRec(t, 3, adapterreg.KindDirect, store.EndpointClosed, "chat")
	v, err := buildView(nodeOf(t, 7), &store.Scan{
		Endpoints: []store.EndpointRecord{open, closedQueued, closedDone},
		In: []store.InRecord{
			inRec(t, 1, open.ID, delivery.StateAccepted),
			inRec(t, 2, closedQueued.ID, delivery.StateAccepted),
			inRec(t, 3, closedDone.ID, delivery.StateRejected),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		id   ident.EndpointID
		want bool
	}{{"open with a queue", open.ID, false}, {"closed with a queue", closedQueued.ID, true}, {"closed with nothing queued", closedDone.ID, false}} {
		if got := v.Draining(tc.id); got != tc.want {
			t.Errorf("%s: Draining = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A target record taken out of the view leaves it as if it had never been there:
// what the view counts, indexes and schedules is what a view built without it holds.
func TestDroppingATargetRecordLeavesTheViewAsIfItWereNeverThere(t *testing.T) {
	node := nodeOf(t, 9)
	to := idOf(t, 4)
	kept, dropped := inRec(t, 1, to, delivery.StateAccepted), inRec(t, 2, to, delivery.StateAccepted)
	scan := func(ins ...store.InRecord) *store.Scan { return &store.Scan{In: ins} }
	v, err := buildView(node, scan(kept, dropped))
	if err != nil {
		t.Fatal(err)
	}
	var told []string
	v.onTick = func(record string, next time.Time) {
		if next.IsZero() {
			told = append(told, record)
		}
	}
	v.dropIn(dropped.Key)
	v.dropIn(dropped.Key) // a record that is not there changes nothing

	want, err := buildView(node, scan(kept))
	if err != nil {
		t.Fatal(err)
	}
	if v.unfinished != want.unfinished || len(v.in) != len(want.in) ||
		fmt.Sprint(v.backlog) != fmt.Sprint(want.backlog) || len(v.queued[to]) != len(want.queued[to]) ||
		fmt.Sprint(v.received) != fmt.Sprint(want.received) || fmt.Sprint(v.arrived) != fmt.Sprint(want.arrived) {
		t.Errorf("the view after the drop differs from the view without the record:\n%+v\n%+v", v, want)
	}
	if len(told) != 1 || told[0] != inTick(dropped.Key) {
		t.Errorf("the schedule was told %v, want that the record has no entry any more", told)
	}
}

// A control record taken out of the view leaves it as a target record does: what
// the view counts, indexes by arrival and schedules is what a view built without it
// holds, so a pull for it finds nothing.
func TestDroppingAControlRecordLeavesTheViewAsIfItWereNeverThere(t *testing.T) {
	node := nodeOf(t, 9)
	to := idOf(t, 4)
	kept, dropped := ctlRec(t, 1, to, delivery.StateAccepted), ctlRec(t, 2, to, delivery.StateAccepted)
	scan := func(ctls ...store.CtlRecord) *store.Scan { return &store.Scan{Ctl: ctls} }
	v, err := buildView(node, scan(kept, dropped))
	if err != nil {
		t.Fatal(err)
	}
	var told []string
	v.onTick = func(record string, next time.Time) {
		if next.IsZero() {
			told = append(told, record)
		}
	}
	v.dropCtl(dropped.Key)
	v.dropCtl(dropped.Key) // a record that is not there changes nothing

	want, err := buildView(node, scan(kept))
	if err != nil {
		t.Fatal(err)
	}
	if v.unfinished != want.unfinished || len(v.ctl) != len(want.ctl) || len(v.queued[to]) != len(want.queued[to]) ||
		fmt.Sprint(v.arrived) != fmt.Sprint(want.arrived) {
		t.Errorf("the view after the drop differs from the view without the record:\n%+v\n%+v", v, want)
	}
	if len(told) != 1 || told[0] != ctlTick(dropped.Key) {
		t.Errorf("the schedule was told %v, want that the record has no entry any more", told)
	}
}
