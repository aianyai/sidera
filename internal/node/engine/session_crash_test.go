package engine_test

import (
	"testing"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// A crash walk of the writes that make a session: before every step of the
// transactions that take a control signal in, settle its creation and take the
// reply in, the disks a crash at that instant can leave are taken, and each is
// started as a node of its own, with an adapter and a runtime that know nothing of
// the creation before the crash. Wherever the node died, the next start has no
// session endpoint that is still being created, and if the source holds the signal
// it is settled once: one control record with its reply, the endpoint open or
// closed as the reply says, and the sender's record ended by the reply. A signal the
// source never accepted leaves nothing: no record, and no endpoint.

// restartedWithRuntime starts a node with an adapter and a runtime on a disk a crash
// left, registers the endpoint of the scenario and lets it go on until nothing is
// left to do.
func restartedWithRuntime(t *testing.T, td takenDisk) *harness.H {
	t.Helper()
	h := harness.New(t, harness.Options{Disk: td.disk, Start: td.at, Adapter: &harness.AdapterOptions{Host: sessionHostParams(), Runtime: true}})
	quiet(t, h)
	h.MustDirect("a")
	quiet(t, h)
	for i := 0; i < 3; i++ {
		h.Advance(resendBackoff + pullInterval)
		quiet(t, h)
	}
	return h
}

// checkCreationSettled holds a restarted node to the invariants of a signal sent
// by a to the runtime endpoint, whose creation ended as want says.
func checkCreationSettled(t *testing.T, h *harness.H, msg adapterproto.MessageID, want adapterproto.ControlCreateOutcome, withMessage bool) {
	t.Helper()
	sc := scanOf(t, h)
	var outs []store.OutRecord
	for _, o := range sc.Out {
		if o.Key.Message == delivery.MessageID(msg) {
			outs = append(outs, o)
		}
	}
	var ctls []store.CtlRecord
	for _, c := range sc.Ctl {
		if c.Key.Message == delivery.MessageID(msg) {
			ctls = append(ctls, c)
		}
	}
	for _, ep := range sc.Endpoints {
		if ep.Kind == sessionKind && ep.Status == store.EndpointCreating {
			t.Errorf("the session endpoint %s is still being created after the start", ep.ID)
		}
	}
	switch {
	case len(outs) == 0:
		if len(ctls) != 0 {
			t.Errorf("%d control records of a signal the source never accepted", len(ctls))
		}
		for _, ep := range sc.Endpoints {
			if ep.Kind == sessionKind {
				t.Errorf("the session endpoint %s of a signal the source never accepted", ep.ID)
			}
		}
	case len(outs) > 1:
		t.Errorf("%d source records of one signal", len(outs))
	case len(ctls) != 1:
		t.Errorf("%d control records of a signal the source holds, want 1", len(ctls))
	default:
		c := ctls[0]
		if c.Record.State != delivery.StateDelivered || !c.Dedup.HasReply {
			t.Errorf("the control record is %s (reply stored: %v), want delivered with its reply", c.Record.State, c.Dedup.HasReply)
		}
		if want == adapterproto.CreateReplyFailed && c.Intent != nil {
			t.Errorf("a creation the node refused itself holds an intent: %+v", c.Intent)
		}
		for _, ep := range sc.Endpoints {
			if ep.Kind != sessionKind {
				continue
			}
			switch want {
			case adapterproto.CreateReplyCreated:
				if ep.Status != store.EndpointOpen {
					t.Errorf("the session endpoint is %v, want open", ep.Status)
				}
			default:
				if ep.Status != store.EndpointClosed {
					t.Errorf("the session endpoint is %v, want closed", ep.Status)
				}
			}
		}
		if d := outs[0].Outbound.Recipients[0].Delivery; d.State != delivery.StateDelivered || outs[0].Outbound.ControlReplyDigest.IsZero() {
			t.Errorf("the source record is %s with reply digest %v, want delivered by the reply", d.State, outs[0].Outbound.ControlReplyDigest)
		}
		if withMessage {
			checkInitialMessage(t, sc, msg, want)
		}
	}
	checkNoOrphanBody(t, sc)
}

// checkInitialMessage holds a restarted node to what a creation that carried an initial
// message leaves: the delivery of the message to the session, on the disk with its body
// when the session was made, and when it was not either there rejected as the endpoint
// closed or not there at all (the node refused the creation itself and wrote none).
func checkInitialMessage(t *testing.T, sc *store.Scan, msg adapterproto.MessageID, want adapterproto.ControlCreateOutcome) {
	t.Helper()
	var derived []store.InRecord
	for _, in := range sc.In {
		if in.Key.Message == delivery.MessageID(msg) {
			derived = append(derived, in)
		}
	}
	switch {
	case want == adapterproto.CreateReplyCreated && len(derived) != 1:
		t.Fatalf("%d deliveries of the initial message of a creation that made a session, want 1", len(derived))
	case len(derived) > 1:
		t.Fatalf("%d deliveries of one initial message", len(derived))
	case len(derived) == 0:
		return
	}
	d := derived[0]
	switch want {
	case adapterproto.CreateReplyCreated:
		if d.Record.State != delivery.StateAccepted || d.Body == nil {
			t.Errorf("the initial message is %s with body %v, want queued with its body", d.Record.State, d.Body)
		}
	default:
		if d.Record.State != delivery.StateRejected || d.Record.Reject != delivery.RejectEndpointClosed || d.Body != nil {
			t.Errorf("the initial message of a creation that made no session is %s (%s), want rejected as the endpoint closed", d.Record.State, d.Record.Reject)
		}
	}
}

func TestSessionCreationRecoversFromACrashAtEveryPoint(t *testing.T) {
	walked := []string{"intake", "control-intake", "control-result", "control-reply"}
	cov := &coverage{hit: map[fsx.Key]int{}}
	scenario := func(name, preset, initial string, want adapterproto.ControlCreateOutcome) {
		t.Run(name, func(t *testing.T) {
			taker := newDiskTaker(cov, walked...)
			h := sessionNode(t, harness.Options{Observer: taker}, "a")
			taker.h = h
			in := fakeagent.ControlInput{To: h.RuntimeAddr(), Signal: adapterproto.SignalSessionCreate, Preset: preset, Body: initial}
			res, err := h.Agent("a").Control(h.Ctx(), in)
			if err != nil {
				t.Fatalf("session.create: %v", err)
			}
			msg := res.MessageID
			quiet(t, h)
			h.Advance(pullInterval)
			quiet(t, h)
			if len(taker.disks) == 0 {
				t.Fatal("no step of the watched transactions was reached")
			}
			// The node that made the disks is done; the nodes of the walk run alone.
			h.Stop()
			for _, td := range taker.disks {
				t.Run(td.where, func(t *testing.T) {
					checkCreationSettled(t, restartedWithRuntime(t, td), msg, want, initial != "")
				})
			}
		})
	}
	scenario("a creation that makes a session", "default", "", adapterproto.CreateReplyCreated)
	scenario("a creation that makes a session and carries an initial message", "default", "start with the tests", adapterproto.CreateReplyCreated)
	scenario("a creation the node refuses itself", "no-such-preset", "", adapterproto.CreateReplyFailed)
	scenario("a creation the node refuses itself, with an initial message", "no-such-preset", "start with the tests", adapterproto.CreateReplyFailed)

	t.Run("a close", func(t *testing.T) {
		taker := newDiskTaker(cov, walked...)
		h := sessionNode(t, harness.Options{Observer: taker}, "a")
		_, id, _ := startedSession(t, h, "a", "")
		taker.h = h // only what the close writes is walked
		msg := signal(t, h, "a", addressOf(id), adapterproto.SignalSessionClose, "")
		quiet(t, h)
		h.Advance(pullInterval)
		quiet(t, h)
		if len(taker.disks) == 0 {
			t.Fatal("no step of the watched transactions was reached")
		}
		h.Stop()
		for _, td := range taker.disks {
			t.Run(td.where, func(t *testing.T) { checkCloseSettled(t, restartedWithRuntime(t, td), msg, id) })
		}
	})
	declared := 0
	for _, k := range harness.ProgramTable(t).Keys() {
		for _, tx := range walked {
			if k.Tag.Tx != tx {
				continue
			}
			declared++
			if cov.hit[k.Key] == 0 {
				t.Errorf("the step %s %q of transaction %s was never reached: the walk proves nothing about a crash at it", k.Step, k.Tag.Role, tx)
			}
		}
	}
	if declared == 0 {
		t.Fatal("no step of the walked transactions is declared")
	}
}

// checkCloseSettled holds a restarted node to the invariants of a close sent by a to a
// session: whatever the crash, the signal the source holds ended once with its reply,
// and the session endpoint is closed.
func checkCloseSettled(t *testing.T, h *harness.H, msg adapterproto.MessageID, session ident.EndpointID) {
	t.Helper()
	sc := scanOf(t, h)
	var outs []store.OutRecord
	for _, o := range sc.Out {
		if o.Key.Message == delivery.MessageID(msg) {
			outs = append(outs, o)
		}
	}
	var ctls []store.CtlRecord
	for _, c := range sc.Ctl {
		if c.Key.Message == delivery.MessageID(msg) {
			ctls = append(ctls, c)
		}
	}
	switch {
	case len(outs) == 0:
		if len(ctls) != 0 {
			t.Errorf("%d control records of a signal the source never accepted", len(ctls))
		}
	case len(outs) > 1 || len(ctls) != 1:
		t.Fatalf("%d source records and %d control records of one signal, want 1 and 1", len(outs), len(ctls))
	default:
		c := ctls[0]
		if c.Record.State != delivery.StateDelivered || !c.Dedup.HasReply {
			t.Errorf("the control record is %s (reply stored: %v), want delivered with its reply", c.Record.State, c.Dedup.HasReply)
		}
		if d := outs[0].Outbound.Recipients[0].Delivery; d.State != delivery.StateDelivered || outs[0].Outbound.ControlReplyDigest.IsZero() {
			t.Errorf("the source record is %s with reply digest %v, want delivered by the reply", d.State, outs[0].Outbound.ControlReplyDigest)
		}
		for _, ep := range sc.Endpoints {
			if ep.ID == session && (ep.Status != store.EndpointClosed || len(ep.Token) != 0) {
				t.Errorf("the session endpoint is %v with a token of %d bytes after its close was answered, want closed and none", ep.Status, len(ep.Token))
			}
		}
	}
	checkNoOrphanBody(t, sc)
}
