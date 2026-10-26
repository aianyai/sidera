package store_test

import (
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// ctlName is the name, without the extension, of the control record i.
func ctlName(i int) string { return strings.TrimSuffix(ctlKey(i).FileName(store.KindCtl), ".rec") }

func statePath(dir, name string) string { return path.Join(dir, name) }

func isIn(dir string) func(string) bool {
	return func(p string) bool { return strings.HasPrefix(p, dir+"/") }
}

// outCtlRecord is a control signal of the local runtime to a session creation.
func outCtlRecord(i int) store.OutRecord {
	r := srcOut(i, false)
	r.Key.Control = true
	r.Outbound.Kind = envelope.KindSessionCreate
	r.Envelope.Preset = "default"
	return r
}

// applied returns the record after an event the delivery package applies.
func applied(t testing.TB, rec delivery.Record, ev delivery.Event) delivery.Record {
	t.Helper()
	out, o, err := rec.Apply(base, ev)
	if err != nil || o.Result != delivery.ResultApplied {
		t.Fatalf("Apply(%v): %v %v", ev.Type, o, err)
	}
	return out
}

// created is a session creation control record whose intent has ended in a
// session, with its stored reply and no initial-message reference.
func created(t testing.TB, i, session int) store.CtlRecord {
	c := sessionCtl(i, session)
	c.Intent.State = store.IntentCreated
	c.Intent.NativeRef = fmt.Sprintf("native-%d", session)
	c.Intent.Process = &adapterproto.ProcessIdentity{PID: int64(1000 + session), StartKey: fmt.Sprintf("k%d", session)}
	c.Record = applied(t, c.Record, delivery.Event{Type: delivery.EventReplyCommitted})
	d, err := c.Dedup.WithReply([]byte("reply"))
	if err != nil {
		t.Fatal(err)
	}
	c.Dedup = d
	c.Body = nil
	return c
}

// openedBy is the session endpoint record that the end of the creation in c
// makes of the one being created: what the replay of the effect must write.
func openedBy(c store.CtlRecord) store.EndpointRecord {
	e := session(int(c.Intent.Endpoint.Bytes()[0])-0x10, store.EndpointOpen, strings.TrimSuffix(c.Name(), ".rec"))
	e.Session.NativeRef = c.Intent.NativeRef
	e.Session.Process = c.Intent.Process
	return e
}

// notified is the record of in after a prompt was handed to the session.
func notified(t testing.TB, in store.InRecord) store.InRecord {
	in.Record = applied(t, in.Record, delivery.Event{Type: delivery.EventNotified})
	return in
}

func pendingOf(ev store.PendingEvent, cause delivery.Cause, ins ...store.InRecord) store.Pending {
	p := store.Pending{Event: ev, Cause: cause}
	for _, in := range ins {
		p.Items = append(p.Items, store.PendingItem{Key: in.Key, Rev: in.Rev, AcceptedAt: in.AcceptedAt})
	}
	return p
}

// seedWithBodies writes the records and, for each one that holds a reference, the
// body file it owns.
func seedWithBodies(t testing.TB, mem *fsxtest.Mem, recs ...store.Record) {
	t.Helper()
	seed(t, mem, recs...)
	seedBodies(t, mem, recs...)
}

// wantBody fails unless the body file of rec is on the disk with content.
func wantBody(t testing.TB, r *rig, rec store.Record, content []byte) {
	t.Helper()
	got, err := r.mem.ReadFile(bodyPath(rec))
	if err != nil {
		t.Errorf("the body of %s: %v", rec.Name(), err)
		return
	}
	if string(got) != string(content) {
		t.Errorf("the body of %s holds %q, want %q", rec.Name(), got, content)
	}
}

// wantNoBody fails when the body file of rec is on the disk.
func wantNoBody(t testing.TB, r *rig, rec store.Record) {
	t.Helper()
	if _, err := r.mem.Stat(bodyPath(rec)); err == nil {
		t.Errorf("the body of %s is still there", rec.Name())
	}
}

// wantBodyCount fails unless bodies/ holds exactly n files: with the checks of
// each record, that no other body was written or removed.
func wantBodyCount(t testing.TB, r *rig, n int) {
	t.Helper()
	if got := snapshot(t, r.mem, store.BodiesDir); len(got) != n {
		t.Errorf("bodies/ holds %d files %v, want %d", len(got), keysOf(got), n)
	}
}

// rejected is the target record in as the target writes it when it refuses the
// message: it refers to no body.
func rejected(in store.InRecord) store.InRecord {
	in.Body = nil
	in.Record.State, in.Record.Reject = delivery.StateRejected, delivery.RejectEndpointClosed
	return in
}

// ctlWithBody is ctlTo with a body of its own.
func ctlWithBody(i, e int) store.CtlRecord {
	c := ctlTo(i, e)
	c.Body = bodyRef(i)
	return c
}

func mustDo(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// scenarios are the workflows of the store, one per transaction and per
// recovery rule, with the crash states each may leave. The hand-written
// expectations are in `verify`; the walk checks everything else.
func scenarios() []scenario {
	farFuture := base.Add(100 * 24 * time.Hour)
	return []scenario{
		{
			name:    "intake",
			durable: "intake/out.sync-dir#1",
			mode:    atomic,
			run: func(t *testing.T, r *rig, s *sim) error {
				return r.st.Intake(bodyBytes(1), srcOut(1, true))
			},
			verify: func(t *testing.T, r *rig) {
				wantBody(t, r, srcOut(1, true), bodyBytes(1))
				wantBodyCount(t, r, 1)
			},
			bodiesResolve: true,
		},
		{
			// Records that refer to no body are written without one, and the bodies
			// of the source records on this node, which refer to theirs, are left.
			name: "deliver",
			setup: func(t *testing.T, mem *fsxtest.Mem) {
				seedWithBodies(t, mem, srcOut(1, true), srcOut(2, true))
			},
			durable: "deliver/in-dir.sync-dir#1",
			mode:    batch,
			run: func(t *testing.T, r *rig, s *sim) error {
				return dropWritten(r.st.Deliver(nil, []store.InRecord{rejected(inRev(1, 1)), rejected(inRev(2, 1))}))
			},
			verify: func(t *testing.T, r *rig) {
				wantNoBody(t, r, inRev(1, 1))
				wantNoBody(t, r, inRev(2, 1))
				wantBody(t, r, srcOut(1, true), bodyBytes(1))
				wantBody(t, r, srcOut(2, true), bodyBytes(2))
				wantBodyCount(t, r, 2)
			},
			bodiesResolve: true,
		},
		{
			name:    "deliver with a body",
			durable: "deliver/in-dir.sync-dir#1",
			mode:    batch,
			run: func(t *testing.T, r *rig, s *sim) error {
				return dropWritten(r.st.Deliver(bodyBytes(3), []store.InRecord{inRev(3, 1)}))
			},
			verify: func(t *testing.T, r *rig) {
				wantBody(t, r, inRev(3, 1), bodyBytes(3))
				wantBodyCount(t, r, 1)
			},
			bodiesResolve: true,
		},
		{
			// One message for three local endpoints: each record has a body file of
			// its own with a copy, none is shared.
			name: "deliver to several recipients",
			setup: func(t *testing.T, mem *fsxtest.Mem) {
				seed(t, mem, directEndpoint(7, "chat"), directEndpoint(8, "chat"))
			},
			durable: "deliver/in-dir.sync-dir#1",
			mode:    batch,
			run: func(t *testing.T, r *rig, s *sim) error {
				return dropWritten(r.st.Deliver(bodyBytes(1), []store.InRecord{inFor(1, 2, 1), inFor(1, 7, 1), inFor(1, 8, 1)}))
			},
			verify: func(t *testing.T, r *rig) {
				seen := map[string]bool{}
				for _, e := range []int{2, 7, 8} {
					in := inFor(1, e, 1)
					wantBody(t, r, in, bodyBytes(1))
					seen[bodyPath(in)] = true
				}
				if len(seen) != 3 {
					t.Errorf("the three records share body files: %v", seen)
				}
				wantBodyCount(t, r, 3)
			},
			bodiesResolve: true,
		},
		{
			// A refused record among the accepted has no body file; the accepted
			// ones each have theirs.
			name: "deliver with a rejected record among the accepted",
			setup: func(t *testing.T, mem *fsxtest.Mem) {
				seed(t, mem, directEndpoint(7, "chat"), directEndpoint(8, "chat"))
			},
			durable: "deliver/in-dir.sync-dir#1",
			mode:    batch,
			run: func(t *testing.T, r *rig, s *sim) error {
				return dropWritten(r.st.Deliver(bodyBytes(1), []store.InRecord{inFor(1, 2, 1), rejected(inFor(1, 7, 1)), inFor(1, 8, 1)}))
			},
			verify: func(t *testing.T, r *rig) {
				wantBody(t, r, inFor(1, 2, 1), bodyBytes(1))
				wantBody(t, r, inFor(1, 8, 1), bodyBytes(1))
				wantNoBody(t, r, inFor(1, 7, 1))
				wantBodyCount(t, r, 2)
			},
			bodiesResolve: true,
		},
		{
			name: "deliver-reply keeping the source body",
			setup: func(t *testing.T, mem *fsxtest.Mem) {
				seedWithBodies(t, mem, srcOut(1, true))
			},
			durable: "deliver-reply/in-dir.sync-dir#1",
			mode:    batch,
			run: func(t *testing.T, r *rig, s *sim) error {
				out := srcOut(1, true)
				out.Feedback = []delivery.Item{{
					Ref:      delivery.DeliveryRef{Sender: addr(local, 1), Message: msgID(1), Recipient: addr(local, 2)},
					Feedback: delivery.Feedback{Kind: delivery.KindDeliveryUnknown, State: delivery.StateDeliveryUnknown}, At: base,
				}}
				return dropWritten(r.st.DeliverReply(bodyBytes(9), &out, []store.InRecord{inRev(9, 1)}))
			},
			verify: func(t *testing.T, r *rig) {
				wantBody(t, r, srcOut(1, true), bodyBytes(1))
				wantBody(t, r, inRev(9, 1), bodyBytes(9))
				wantBodyCount(t, r, 2)
			},
			bodiesResolve: true,
			extra: func(t *testing.T, where string, crashed, fin map[string]string) {
				if _, ok := fin[bodyPath(srcOut(1, true))]; !ok {
					t.Errorf("%s: the body of the source record, which still refers to it, was removed", where)
				}
			},
		},
		{
			// The evidence in the reply finishes the delivery, so the source record
			// drops its body and the transaction deletes that file at its end, after
			// the records are durable. The body of another source record stays.
			name: "deliver-reply",
			setup: func(t *testing.T, mem *fsxtest.Mem) {
				seedWithBodies(t, mem, srcOut(1, true), srcOut(2, true))
			},
			durable: "deliver-reply/in-dir.sync-dir#1",
			mode:    batch,
			run: func(t *testing.T, r *rig, s *sim) error {
				out := finishedOut(1, true)
				out.Feedback = []delivery.Item{{
					Ref:      delivery.DeliveryRef{Sender: addr(local, 1), Message: msgID(1), Recipient: addr(local, 2)},
					Feedback: delivery.Feedback{Kind: delivery.KindDeliveryUnknown, State: delivery.StateDeliveryUnknown}, At: base,
				}}
				return dropWritten(r.st.DeliverReply(bodyBytes(9), &out, []store.InRecord{inRev(9, 1)}))
			},
			verify: func(t *testing.T, r *rig) {
				wantNoBody(t, r, srcOut(1, true))
				wantBody(t, r, srcOut(2, true), bodyBytes(2))
				wantBody(t, r, inRev(9, 1), bodyBytes(9))
				wantBodyCount(t, r, 2)
			},
			bodiesResolve: true,
			extra: func(t *testing.T, where string, crashed, fin map[string]string) {
				// The source record is updated before the target record that depends on it.
				in := path.Join(store.InDir, inRev(9, 1).Name())
				out := path.Join(store.OutDir, srcOut(1, true).Name())
				if _, ok := crashed[in]; ok && crashed[out] == fmt.Sprint(mustEncode(t, srcOut(1, true))) {
					t.Errorf("%s: the reply is there but the evidence it depends on is not", where)
				}
			},
		},
		{
			name: "source-update",
			setup: func(t *testing.T, mem *fsxtest.Mem) {
				seedWithBodies(t, mem, srcOut(1, true), srcOut(2, true))
			},
			durable: "source-update/out.sync-dir#1",
			mode:    atomic,
			run: func(t *testing.T, r *rig, s *sim) error {
				return r.st.SourceUpdate(finishedOut(1, true))
			},
			verify: func(t *testing.T, r *rig) {
				wantNoBody(t, r, srcOut(1, true))
				wantBody(t, r, srcOut(2, true), bodyBytes(2))
				wantBodyCount(t, r, 1)
			},
			bodiesResolve: true,
		},
		{
			name: "control-intake",
			setup: func(t *testing.T, mem *fsxtest.Mem) {
				seed(t, mem, directEndpoint(4, "claude"))
			},
			durable: "control-intake/ctl.sync-dir#1",
			mode:    atomic,
			run: func(t *testing.T, r *rig, s *sim) error {
				c := sessionCtl(1, 5)
				c.Body = bodyRef(1)
				return r.st.ControlIntake(bodyBytes(1), ptr(session(5, store.EndpointCreating, ctlName(1))), c)
			},
			bodiesResolve: true,
		},
		{
			name: "control-result",
			setup: func(t *testing.T, mem *fsxtest.Mem) {
				c := sessionCtl(1, 5)
				c.Body = bodyRef(1)
				seedWithBodies(t, mem, session(5, store.EndpointCreating, ctlName(1)), c, srcOut(2, true))
			},
			durable: "control-result/ctl.sync-dir#1",
			mode:    atomic,
			run: func(t *testing.T, r *rig, s *sim) error {
				c := created(t, 1, 5)
				return r.st.ControlResult(c, ptr(openedBy(c)))
			},
			verify: func(t *testing.T, r *rig) {
				wantNoBody(t, r, sessionCtl(1, 5))
				wantBody(t, r, srcOut(2, true), bodyBytes(2))
				wantBodyCount(t, r, 1)
			},
			bodiesResolve: true,
		},
		{
			// The reply finishes the delivery of the source record, which drops its
			// body; the body goes after the record, and the body of another source
			// record stays.
			name: "control-reply",
			setup: func(t *testing.T, mem *fsxtest.Mem) {
				seedWithBodies(t, mem, outCtlRecord(1), srcOut(2, false))
			},
			durable: "control-reply/out.sync-dir#1",
			mode:    atomic,
			run: func(t *testing.T, r *rig, s *sim) error {
				o := outCtlRecord(1)
				o.Outbound.Recipients[0].Delivery = applied(t, o.Outbound.Recipients[0].Delivery, delivery.Event{Type: delivery.EventDelivered})
				o.Body = nil
				o.ControlReply = []byte("reply")
				o.Outbound.ControlReplyDigest = [32]byte{1}
				return r.st.ControlReply(o)
			},
			verify: func(t *testing.T, r *rig) {
				wantNoBody(t, r, outCtlRecord(1))
				wantBody(t, r, srcOut(2, false), bodyBytes(2))
				wantBodyCount(t, r, 1)
			},
			bodiesResolve: true,
		},
		{
			name:    "prompt-begin",
			durable: "prompt-begin/attempts.sync-dir#1",
			mode:    atomic,
			run: func(t *testing.T, r *rig, s *sim) error {
				return r.st.PromptBegin(store.AttemptsRecord{Endpoint: ep(2), InFlight: &store.InFlightPrompt{
					Attempt: "0b4d6e1a-2f3c-4d5e-8f60-718293a4b5c6", Kind: 1, Covers: []store.TargetKey{tgtKey(1)},
				}})
			},
		},
		promptScenario("prompt-end", store.PendingNotified, delivery.CauseNone),
		promptScenario("blocked", store.PendingBlocked, delivery.CauseAdapterOffline),
		{
			name: "lease",
			setup: func(t *testing.T, mem *fsxtest.Mem) {
				seedWithBodies(t, mem, inRev(1, 1), inRev(2, 1), inRev(3, 1))
			},
			durable: "lease/in-dir.sync-dir#1",
			mode:    batch,
			run: func(t *testing.T, r *rig, s *sim) error {
				var ins []store.InRecord
				for i := 1; i <= 3; i++ {
					in := inRev(i, 1)
					in.Record = leasedRecord(in.Record, base)
					ins = append(ins, in)
				}
				return dropWritten(r.st.Lease(ins))
			},
			bodiesResolve: true,
		},
		{
			// Each record has a body of its own, and acknowledging both removes both.
			name: "ack",
			setup: func(t *testing.T, mem *fsxtest.Mem) {
				seedWithBodies(t, mem, inRev(1, 2), inRev(2, 2))
			},
			durable: "ack/in-dir.sync-dir#1",
			mode:    batch,
			run: func(t *testing.T, r *rig, s *sim) error {
				a, b := inRev(1, 2), inRev(2, 2)
				a.Body, b.Body = nil, nil
				a.Record, b.Record = deliveredRecord(a.Record), deliveredRecord(b.Record)
				return dropWritten(r.st.Ack([]store.InRecord{a, b}))
			},
			verify: func(t *testing.T, r *rig) {
				wantNoBody(t, r, inRev(1, 2))
				wantNoBody(t, r, inRev(2, 2))
				wantBodyCount(t, r, 0)
			},
			bodiesResolve: true,
		},
		{
			// Three records with a body each, two acknowledged: only their own bodies
			// go, in no crash state does a record point to a missing body, and the
			// third keeps its record and its body.
			name: "ack removes only its own body",
			setup: func(t *testing.T, mem *fsxtest.Mem) {
				seedWithBodies(t, mem, inRev(1, 2), inRev(2, 2), inRev(3, 2))
			},
			durable: "ack/in-dir.sync-dir#1",
			mode:    batch,
			run: func(t *testing.T, r *rig, s *sim) error {
				a, c := inRev(1, 2), inRev(3, 2)
				a.Body, c.Body = nil, nil
				a.Record, c.Record = deliveredRecord(a.Record), deliveredRecord(c.Record)
				return dropWritten(r.st.Ack([]store.InRecord{a, c}))
			},
			verify: func(t *testing.T, r *rig) {
				wantNoBody(t, r, inRev(1, 2))
				wantNoBody(t, r, inRev(3, 2))
				wantBody(t, r, inRev(2, 2), bodyBytes(2))
				wantBodyCount(t, r, 1)
			},
			bodiesResolve: true,
		},
		{
			name: "tick",
			setup: func(t *testing.T, mem *fsxtest.Mem) {
				c := sessionCtl(3, 5)
				c.Body = bodyRef(3)
				seedWithBodies(t, mem, srcOut(1, false), inRev(2, 1), c, inRev(4, 1))
			},
			durable: "tick/body-dir.sync-dir#1",
			mode:    batch,
			run: func(t *testing.T, r *rig, s *sim) error {
				out := srcOutFinished(1)
				in := inRev(2, 1)
				in.Body = nil
				in.Record.State, in.Record.Reject = delivery.StateRejected, delivery.RejectBacklogExpired
				c := sessionCtl(3, 5)
				c.Body = nil
				c.Record = applied(t, c.Record, delivery.Event{Type: delivery.EventRejected, Reject: delivery.RejectExpired})
				return dropWritten(r.st.Tick(store.TickBatch{
					Out: []store.OutRecord{out}, In: []store.InRecord{in}, Ctl: []store.CtlRecord{c},
				}))
			},
			verify: func(t *testing.T, r *rig) {
				wantNoBody(t, r, srcOut(1, false))
				wantNoBody(t, r, inRev(2, 1))
				wantNoBody(t, r, sessionCtl(3, 5))
				wantBody(t, r, inRev(4, 1), bodyBytes(4))
				wantBodyCount(t, r, 1)
			},
			bodiesResolve: true,
		},
		{
			name: "endpoint-update",
			setup: func(t *testing.T, mem *fsxtest.Mem) {
				seed(t, mem, directEndpoint(7, "chat"))
			},
			durable: "endpoint-update/endpoint.sync-dir#1",
			mode:    atomic,
			run: func(t *testing.T, r *rig, s *sim) error {
				e := directEndpoint(7, "chat")
				e.Declaration = []byte(`{"name":"renamed"}`)
				return r.st.EndpointUpdate(e)
			},
		},
		{
			name: "endpoint-close",
			setup: func(t *testing.T, mem *fsxtest.Mem) {
				seedWithBodies(t, mem, directEndpoint(7, "chat"), inFor(1, 7, 1), inFor(2, 7, 1), inFor(3, 2, 1), ctlWithBody(4, 7))
			},
			durable:  "endpoint-close/endpoint.sync-dir#1",
			mode:     atomic,
			optional: isIn(store.AdminDir),
			run: func(t *testing.T, r *rig, s *sim) error {
				closed := directEndpoint(7, "chat")
				closed.Status, closed.Token = store.EndpointClosed, nil
				res := adminResult(1)
				if err := r.st.EndpointClose(closed, &res); err != nil {
					return err
				}
				return dropWritten(r.st.EndpointDrain(s.drainBatch(r.scan())))
			},
			verify: func(t *testing.T, r *rig) {
				wantNoBody(t, r, inFor(1, 7, 1))
				wantNoBody(t, r, inFor(2, 7, 1))
				wantNoBody(t, r, ctlWithBody(4, 7))
				wantBody(t, r, inFor(3, 2, 1), bodyBytes(3))
				wantBodyCount(t, r, 1)
			},
			bodiesResolve: true,
		},
		{
			name: "instance-drain",
			setup: func(t *testing.T, mem *fsxtest.Mem) {
				seed(t, mem, directEndpoint(7, "chat"), directEndpoint(8, "chat"), directEndpoint(9, "other"),
					inFor(1, 7, 1), inFor(2, 8, 1), inFor(3, 9, 1), ctlWithBody(4, 8))
				seedBodies(t, mem, inFor(1, 7, 1), inFor(2, 8, 1), inFor(3, 9, 1), ctlWithBody(4, 8))
			},
			mode:         atomic,
			mustComplete: true,
			enabled:      func(inst string) bool { return inst != "chat" },
			run: func(t *testing.T, r *rig, s *sim) error {
				var closed []store.EndpointRecord
				for _, e := range r.scan().Endpoints {
					if e.Instance == "chat" {
						e.Status, e.Token = store.EndpointClosed, nil
						closed = append(closed, e)
					}
				}
				// The head and the first chunk in one transaction; what is left of the
				// drain, if any, is another.
				if _, err := r.st.InstanceDrain(closed, s.drainFor(r.scan(), closed)); err != nil {
					return err
				}
				return nil
			},
			verify: func(t *testing.T, r *rig) {
				wantNoBody(t, r, inFor(1, 7, 1))
				wantNoBody(t, r, inFor(2, 8, 1))
				wantNoBody(t, r, ctlWithBody(4, 8))
				wantBody(t, r, inFor(3, 9, 1), bodyBytes(3))
				wantBodyCount(t, r, 1)
			},
			bodiesResolve: true,
		},
		{
			name: "feedback-form",
			setup: func(t *testing.T, mem *fsxtest.Mem) {
				o := srcOut(1, false)
				o.Feedback = []delivery.Item{feedbackItem(1)}
				seedWithBodies(t, mem, o)
			},
			durable: "feedback-form/notice.sync-dir#1",
			mode:    atomic,
			run: func(t *testing.T, r *rig, s *sim) error {
				o := srcOut(1, false)
				o.Feedback = []delivery.Item{feedbackItem(1)}
				if _, err := r.st.FeedbackForm([]byte("feedback wording"), noticeFor(o)); err != nil {
					return err
				}
				o.Feedback = nil
				return r.st.FeedbackClear([]store.OutRecord{o})
			},
			verify: func(t *testing.T, r *rig) {
				wantBody(t, r, srcOut(1, false), bodyBytes(1))
				wantBody(t, r, noticeFor(srcOut(1, false)), []byte("feedback wording"))
				wantBodyCount(t, r, 2)
			},
		},
		{
			// The notice's coverage clears the feedback of a record that is finished
			// by now: it drops its body, which goes after the record, and the body
			// of another source record stays.
			name: "feedback-clear",
			setup: func(t *testing.T, mem *fsxtest.Mem) {
				o := srcOut(1, false)
				o.Feedback = []delivery.Item{feedbackItem(1)}
				seedWithBodies(t, mem, o, srcOut(2, false))
			},
			durable: "feedback-clear/out-dir.sync-dir#1",
			mode:    atomic,
			run: func(t *testing.T, r *rig, s *sim) error {
				return r.st.FeedbackClear([]store.OutRecord{srcOutFinished(1)})
			},
			verify: func(t *testing.T, r *rig) {
				wantNoBody(t, r, srcOut(1, false))
				wantBody(t, r, srcOut(2, false), bodyBytes(2))
				wantBodyCount(t, r, 1)
			},
			bodiesResolve: true,
		},
		single("checkpoint-put", "checkpoint-put/checkpoint.sync-dir#1", func(r *rig) error {
			return r.st.CheckpointPut(store.CheckpointRecord{Key: store.CheckpointKey{Scope: "session", ID: ep(5).String()}, State: []byte(`{"cursor":1}`)})
		}),
		single("alert-raise", "alert-raise/alert.sync-dir#1", func(r *rig) error {
			return r.st.AlertRaise(store.AlertRecord{ID: store.AlertID{9}, Kind: "version_untested", At: base, Detail: "d"})
		}),
		single("daemon-stop", "daemon-stop/result.sync-dir#1", func(r *rig) error {
			return r.st.DaemonStop(adminResult(7))
		}),
		{
			name: "archive-events",
			mode: batch,
			run: func(t *testing.T, r *rig, s *sim) error {
				return r.st.ArchiveEvents(base, []store.ArchiveEvent{eventAccepted(), eventChunk()})
			},
			ignore: func(p string) bool { return true }, // events are diagnostics; the check is the reader's, below
			extra: func(t *testing.T, where string, crashed, fin map[string]string) {
				got := store.ParseArchive([]byte(fin[dayFile]))
				if len(got.Summaries) != 0 || len(got.Events) > 2 || got.Dropped > 1 {
					t.Errorf("%s: archive reads as %d summaries, %d events, %d dropped", where, len(got.Summaries), len(got.Events), got.Dropped)
				}
				for _, e := range got.Events {
					if !equalRecords(e, eventAccepted()) && !equalRecords(e, eventChunk()) {
						t.Errorf("%s: archive holds an event nobody wrote: %#v", where, e)
					}
				}
			},
		},
		sweepScenario(farFuture),
		sweepClosedSessionScenario(farFuture),
		{
			name: "archive-expire",
			setup: func(t *testing.T, mem *fsxtest.Mem) {
				for _, n := range []string{"2026-09-01.jsonl", "2026-09-02.jsonl", "2026-10-24.jsonl"} {
					seedRaw(t, mem, store.ArchiveDir, n, []byte("\n"+archiveLines.event))
				}
			},
			durable: "archive-expire/dir.sync-dir#1",
			mode:    batch,
			run: func(t *testing.T, r *rig, s *sim) error {
				return r.st.ArchiveExpire([]string{"2026-09-01.jsonl", "2026-09-02.jsonl"})
			},
		},
		{
			name: "alert-ack",
			setup: func(t *testing.T, mem *fsxtest.Mem) {
				seed(t, mem, store.AlertRecord{ID: store.AlertID{5}, Kind: "corrupt_record", At: base, Detail: "d", Subject: "endpoints/x"})
			},
			durable: "alert-ack/result.sync-dir#1",
			mode:    batch,
			run: func(t *testing.T, r *rig, s *sim) error {
				a := store.AlertRecord{ID: store.AlertID{5}, Kind: "corrupt_record", At: base, Detail: "d", Subject: "endpoints/x",
					Acked: &store.AlertAcked{At: base.Add(time.Hour), Operation: adminResult(2).Key.ID}}
				return r.st.AlertAck(store.AlertAckInput{Alert: a, Result: adminResult(2)})
			},
		},
	}
}

func ptr[T any](v T) *T { return &v }

func mustEncode(t testing.TB, rec store.Record) []byte {
	t.Helper()
	b, err := store.EncodeRecord(rec)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func feedbackItem(i int) delivery.Item {
	return delivery.Item{
		Ref:      delivery.DeliveryRef{Sender: addr(local, 1), Message: msgID(i), Recipient: addr(2, 2)},
		Feedback: delivery.Feedback{Kind: delivery.KindRejected, Reject: delivery.RejectEndpointClosed, State: delivery.StateRejected}, At: base,
	}
}

// noticeFor is the notice that covers the pending feedback of out.
func noticeFor(o store.OutRecord) store.InRecord {
	n := noticeIn(99)
	n.Key = store.TargetKey{SenderNode: node(local), Sender: ident.SystemEndpoint(), Message: "notice-" + o.Key.Message, Recipient: o.Key.Sender}
	rec, err := delivery.NewTargetNoticeRecord(base, base.Add(10*time.Minute), delivery.ReportBy(base.Add(10*time.Minute)), delivery.DefaultEndpointMaxQueue)
	if err != nil {
		panic(err)
	}
	n.Rev, n.Record = 1, rec
	n.Dedup = delivery.NewDedupRecord(n.Key.Dedup(), digestOf(99), rec.ReportBy)
	n.AcceptedAt = base
	n.Handout = store.HandoutFields{ContentType: "text/plain", SentAt: base}
	n.Body = func() *store.BodyRef { b := store.BodyRefOf([]byte("feedback wording")); return &b }()
	for _, it := range o.Feedback {
		n.Covers = append(n.Covers, store.CoverRef{Out: o.Name(), Recipient: it.Ref.Recipient, Kind: it.Feedback.Kind})
	}
	return n
}

// single is a scenario of one single-file transaction.
func single(name, durable string, run func(r *rig) error) scenario {
	return scenario{name: name, durable: durable, mode: atomic, run: func(t *testing.T, r *rig, s *sim) error { return run(r) }}
}

// promptScenario is the head record of a prompt or a block and its followers.
func promptScenario(name string, ev store.PendingEvent, cause delivery.Cause) scenario {
	event := delivery.Event{Type: delivery.EventNotified}
	if ev == store.PendingBlocked {
		event = delivery.Event{Type: delivery.EventBlocked, Cause: cause}
	}
	return scenario{
		name: name,
		setup: func(t *testing.T, mem *fsxtest.Mem) {
			seedWithBodies(t, mem, inRev(1, 1), inRev(2, 1), store.AttemptsRecord{Endpoint: ep(2), InFlight: &store.InFlightPrompt{
				Attempt: "0b4d6e1a-2f3c-4d5e-8f60-718293a4b5c6", Kind: 1, Covers: []store.TargetKey{tgtKey(1), tgtKey(2)},
			}})
		},
		durable: name + "/attempts.sync-dir#1",
		mode:    atomic,
		run: func(t *testing.T, r *rig, s *sim) error {
			a, b := inRev(1, 1), inRev(2, 1)
			head := store.AttemptsRecord{Endpoint: ep(2), Pending: pendingOf(ev, cause, a, b)}
			a.Record, b.Record = applied(t, a.Record, event), applied(t, b.Record, event)
			if name == "blocked" {
				return dropWritten(r.st.Blocked(head, []store.InRecord{a, b}))
			}
			return dropWritten(r.st.PromptEnd(head, []store.InRecord{a, b}))
		},
		bodiesResolve: true,
	}
}

// sweepScenario is a sweep of every kind of thing a sweep removes.
func sweepScenario(farFuture time.Time) scenario {
	world := func(t *testing.T, mem *fsxtest.Mem) {
		o1 := srcOutFinished(1)                        // a finished source record whose number must be remembered
		o2 := srcOutFinished(2)                        // another, of the same pair
		inDone := inRev(3, 2)                          // a finished target record
		inDone.Record = deliveredRecord(inDone.Record) // it still refers to its body: the crash came before it was dropped
		c := created(t, 4, 5)                          // a control record of a session creation
		c.Record = deliveredRecord(c.Record)
		derived := derivedIn(4, 5, 2) // its derived delivery
		derived.Record = deliveredRecord(derived.Record)
		derived.Body = nil
		lost := lostOutAt(srcOut(6, false).Key, base) // a replacement
		closed := session(5, store.EndpointClosed, ctlName(4))
		notified := lostNotice(1, 6, base) // an out-lost record whose notice was given
		notified.Feedback = nil
		keep := srcOut(8, false) // a queued source record: not finished, never swept
		seedWithBodies(t, mem, o1, o2, inDone, c, derived, lost, closed, notified, keep,
			store.AlertRecord{ID: store.AlertID{6}, Kind: "corrupt_record", At: base, Detail: "d", Subject: "out/x"},
			adminResult(1))
		// A finished source record that holds no reference still owns the file a
		// crash left; the sweep removes it with the record.
		seedBodyOf(t, mem, o1, bodyBytes(1))
	}
	return scenario{
		name:  "archive-sweep",
		setup: world,
		mode:  batch,
		ignore: func(p string) bool {
			return strings.HasPrefix(p, store.ArchiveDir+"/")
		},
		run: func(t *testing.T, r *rig, s *sim) error {
			plan := store.PlanSweep(r.scan(), farFuture)
			if len(plan.Records) == 0 {
				t.Fatalf("the plan sweeps nothing")
			}
			return r.st.ArchiveSweep(plan.Chunk(farFuture))
		},
		retry: func(t *testing.T, r *rig, s *sim) {
			plan := store.PlanSweep(r.scan(), farFuture)
			if len(plan.Records)+len(plan.Ctl)+len(plan.Outs)+len(plan.OutLosts)+len(plan.Endpoints) == 0 {
				return
			}
			mustDo(t, r.st.ArchiveSweep(plan.Chunk(farFuture)))
		},
		verify: func(t *testing.T, r *rig) {
			if sc := r.scan(); len(sc.OutLost) != 0 {
				t.Errorf("an out-lost record whose notice was given is left: %v", sc.OutLost)
			}
			wantNoBody(t, r, srcOutFinished(1))
			wantNoBody(t, r, inRev(3, 2))
			wantBody(t, r, srcOut(8, false), bodyBytes(8))
			wantBodyCount(t, r, 1)
		},
		crashed: func(t *testing.T, where string, snap map[string]string) {
			sweepInvariants(t, where, snap)
		},
		bodiesResolve: true,
	}
}

// sweepInvariants are what holds in every crash state of a sweep: nothing is
// removed whose sequence floor is not durable, and a control record goes before
// the delivery derived from it. The summaries of the archive are best effort and
// are not among them.
func sweepInvariants(t *testing.T, where string, snap map[string]string) {
	t.Helper()
	o1, o2 := srcOutFinished(1), srcOutFinished(2)
	for _, o := range []store.OutRecord{o1, o2} {
		if _, there := snap[statePath(store.OutDir, o.Name())]; there {
			continue
		}
		// The floor of the pair is remembered before the record goes.
		floors := snap[statePath(store.SeqDir, store.SeqRecord{Sender: o.Key.Sender}.Name())]
		if floors == "" {
			t.Errorf("%s: the source record %s is gone and no sequence floor is durable", where, o.Key.Message)
			continue
		}
		rec, dmg := store.DecodeRecord(store.SeqDir, store.SeqRecord{Sender: o.Key.Sender}.Name(), []byte(floors))
		if dmg != nil {
			t.Errorf("%s: the sequence floor file is damaged: %+v", where, dmg)
			continue
		}
		if got := rec.(store.SeqRecord).Floors[o.Key.Recipients[0]]; got < o.Envelope.Seq[o.Key.Recipients[0].Endpoint] {
			t.Errorf("%s: floor %d for a pair whose record with number %d is gone", where, got, o.Envelope.Seq[o.Key.Recipients[0].Endpoint])
		}
	}
	ctlThere := false
	if _, ok := snap[statePath(store.CtlDir, created(t, 4, 5).Name())]; ok {
		ctlThere = true
	}
	if _, derivedThere := snap[statePath(store.InDir, derivedIn(4, 5, 2).Name())]; ctlThere && !derivedThere {
		t.Errorf("%s: the derived delivery is gone while its control record is still there", where)
	}
}

// TestEveryTransactionConvergesFromEveryCrashState walks every crash point of
// every workflow of the store, in the kinds of crash that crashKinds takes, and
// checks the state recovery ends in and that recovery is idempotent. It crashes
// again the recovery of the first crash state that reaches each point of a
// recovery rule, at every point of it, and checks that a crash during recovery
// ends in the same state. It then checks that the walks together reached and
// crashed at every crash point the store declares.
func TestEveryTransactionConvergesFromEveryCrashState(t *testing.T) {
	sink := newSink()
	all := append(append(scenarios(), recoveryScenarios()...), doubleFaultScenarios()...)
	total := scenarioStats{}
	var mu sync.Mutex
	// The walks are independent: they run side by side, and the coverage is
	// asserted when the group of them is done.
	t.Run("walks", func(t *testing.T) {
		for i := range all {
			sc := all[i]
			t.Run(sc.name, func(t *testing.T) {
				t.Parallel()
				st := runScenario(t, &sc, sink, uint64(100+i))
				t.Logf("%d points, %d crash states, %d recoveries", st.points, st.clones, st.recoveries)
				mu.Lock()
				defer mu.Unlock()
				total.points += st.points
				total.clones += st.clones
				total.recoveries += st.recoveries
			})
		}
	})
	total.nested = runNestedWalks(t, sink)
	t.Logf("in all: %d points, %d crash states, %d recoveries, %d recoveries after a crash during recovery", total.points, total.clones, total.recoveries, total.nested)

	// The coverage assertion: the walks reached every declared point, and the
	// table the keys come from is the one the store declares, not one the walks
	// built.
	tab, err := txnTable()
	if err != nil {
		t.Fatal(err)
	}
	var missing []string
	for _, k := range tab {
		if sink.hits[k.Key] == 0 {
			missing = append(missing, fmt.Sprintf("%s (%v)", k.Key, kindName(k.Kind)))
		}
	}
	if len(missing) > 0 {
		t.Errorf("%d declared crash points were never reached by the walks:\n%s", len(missing), strings.Join(missing, "\n"))
	}
	// A write declared as many is covered when one transaction made it more than
	// once: the quarantine closes every creating endpoint of the damaged control
	// record, so one quarantine transaction must have written the session role
	// twice (a count of the hits of a whole run could come from two quarantines).
	for _, k := range tab {
		if k.Tag.Tx == "quarantine" && k.Tag.Role == "session" && sink.maxInTx[k.Key] < 2 {
			t.Errorf("%s was reached %d time(s) at most in one transaction: no walk closed two session endpoints in one quarantine", k.Key, sink.maxInTx[k.Key])
		}
	}
	// Reaching a point is not crashing at it: every declared point must have had
	// a crash state taken at it, of each kind every walk takes (the process crash
	// and the worst power loss), by the walk of a workflow (a point only a
	// recovery after another workflow's crash reaches needs a workflow of its own
	// that starts from that state).
	models := floorKindNames()
	var uncrashed []string
	for _, k := range tab {
		for _, m := range models {
			if sink.taken[k.Key][m] == 0 {
				uncrashed = append(uncrashed, fmt.Sprintf("%s (%v) under %s", k.Key, kindName(k.Kind), m))
			}
		}
	}
	if len(uncrashed) > 0 {
		t.Errorf("%d declared crash points have no crash state taken at them by a walk, for the kind of crash named:\n%s", len(uncrashed), strings.Join(uncrashed, "\n"))
	}
	// The points of the recovery rules must have been crashed at during a
	// recovery too, in the same kinds: the walks above crash them while a
	// workflow recovers, and the cleanup and the settle that txn.Recover runs at
	// every start belong to no workflow, so only a recovery reaches them. The
	// store never discards a directory, so no disk of its workflows holds a
	// ".trash-" directory and the removal of one cannot be reached here.
	var gaps []string
	for k := range recoveryDomain() {
		for _, m := range models {
			if sink.nestedTaken[k][m] == 0 {
				gaps = append(gaps, fmt.Sprintf("%s under %s", k, m))
			}
		}
	}
	sort.Strings(gaps)
	if len(gaps) > 0 {
		t.Errorf("%d crash points of the recovery rules have no crash during a recovery taken at them, for the kind of crash named:\n%s", len(gaps), strings.Join(gaps, "\n"))
	}
	if total.nested == 0 {
		t.Error("no recovery was crashed in the middle")
	}
}

var _ = fsx.StepWrite

// ctlTo is an accepted control record for the local endpoint e.
func ctlTo(i, e int) store.CtlRecord {
	c := sessionCtl(i, 5)
	c.Key.Recipient = ep(e)
	c.Dedup = delivery.NewDedupRecord(c.Key.Dedup(), digestOf(i), c.Record.ReportBy)
	c.Intent, c.SessionEndpoint, c.Preset = nil, ident.EndpointID{}, ""
	c.Kind = envelope.KindEndpointStatus
	return c
}
