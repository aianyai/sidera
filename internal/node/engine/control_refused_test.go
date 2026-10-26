package engine_test

import (
	"bytes"
	"slices"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/transport"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// A control signal the target refuses when it arrives (the endpoint is not there, is
// closed or is no runtime, or the rules of the node refuse a source elsewhere) was
// certainly not executed. The declarations of an endpoint do not refuse a control
// signal. The target writes a control record that is rejected from the start, with
// the reason, and answers with its report, so the sender is told at once what the
// refusal was and not, after minutes, that the target could not be reached or that
// the result could not be confirmed.

// refusedCtl is the one control record of the message, which must be a refusal that
// holds nothing of an execution.
func refusedCtl(t testing.TB, h *harness.H, msg adapterproto.MessageID, reason delivery.RejectReason) store.CtlRecord {
	t.Helper()
	var found []store.CtlRecord
	for _, c := range ctlRecords(t, h) {
		if c.Key.Message == delivery.MessageID(msg) {
			found = append(found, c)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d control records of %s, want one", len(found), msg)
	}
	c := found[0]
	if c.Record.State != delivery.StateRejected || c.Record.Reject != reason {
		t.Fatalf("the control record is %s (%s), want rejected (%s)", c.Record.State, c.Record.Reject, reason)
	}
	if c.Dedup.HasReply || c.Intent != nil || !c.SessionEndpoint.IsZero() || c.Initial != nil || len(c.Result) != 0 {
		t.Errorf("the refusal holds something of an execution: %+v", c)
	}
	return c
}

// onlyRejectedWasToldOnce fails the test unless the sender of the message has been
// told of the refusal exactly once and of nothing else.
func onlyRejectedWasToldOnce(t testing.TB, h *harness.H, who string) {
	t.Helper()
	h.Advance(mergeWindow)
	quiet(t, h)
	if got := toldKinds(t, h, who); !slices.Equal(got, []delivery.Kind{delivery.KindRejected}) {
		t.Errorf("%s was told %v, want the rejection once", who, got)
	}
}

func TestAControlSignalTheTargetRefusesAtIntakeIsRejectedAtOnceWithItsReason(t *testing.T) {
	for _, tc := range []struct {
		name   string
		setup  func(t *testing.T, h *harness.H)
		signal adapterproto.ControlSignal
		to     func(h *harness.H) string
		reason delivery.RejectReason
	}{
		// The policy reads a creation aimed at an endpoint that stands for no runtime as a
		// request that cannot be made: malformed.
		{"an endpoint that is no runtime takes no creation", nil, adapterproto.SignalSessionCreate,
			func(h *harness.H) string { return h.Addr("b") }, delivery.RejectMalformed},
		{"an endpoint that was closed", func(t *testing.T, h *harness.H) {
			if _, err := h.Engine().CloseEndpoint(h.Ctx(), engine.CloseRequest{Endpoint: endpointOf(t, h, "b")}); err != nil {
				t.Fatal(err)
			}
		}, adapterproto.SignalEndpointStatus, func(h *harness.H) string { return h.Addr("b") }, delivery.RejectEndpointClosed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := sessionNode(t, harness.Options{}, "a", "b")
			if tc.setup != nil {
				tc.setup(t, h)
				quiet(t, h)
			}
			in := controlInput(tc.to(h), tc.signal)
			res, err := h.Agent("a").Control(h.Ctx(), in)
			if err != nil {
				t.Fatalf("the signal is accepted by its sender's node: %v", err)
			}
			quiet(t, h)

			refusedCtl(t, h, res.MessageID, tc.reason)
			if d := sourceDelivery(t, h, res.MessageID); d.State != delivery.StateRejected || d.Reject != tc.reason {
				t.Errorf("the sender's record is %s (%s) at once, want rejected (%s): the receipt carried the report", d.State, d.Reject, tc.reason)
			}
			if r := controlReply(t, h, "a", res.MessageID); r != nil {
				t.Errorf("a refused signal has a control reply: %+v", r)
			}
			nothingWasMade(t, h)
			if n := createRequests(t, h); n != 0 {
				t.Errorf("%d session.create requests reached the adapter", n)
			}
			onlyRejectedWasToldOnce(t, h, "a")

			// Nothing follows from time: not the unreachable notice of a send that got no
			// answer, nor the close of a delivery with nothing learned.
			h.Advance(delivery.ReportByOffset + delivery.MaxAcceptBefore)
			quiet(t, h)
			if got := toldKinds(t, h, "a"); !slices.Equal(got, []delivery.Kind{delivery.KindRejected}) {
				t.Errorf("a was told %v after the delivery's time ran out, want the rejection alone", got)
			}
			if d := sourceDelivery(t, h, res.MessageID); d.State != delivery.StateRejected {
				t.Errorf("the sender's record is %s after the time ran out, want rejected", d.State)
			}
		})
	}
}

// controlInput is the signal of the test: a creation names the default preset.
func controlInput(to string, signal adapterproto.ControlSignal) (in fakeagent.ControlInput) {
	in = fakeagent.ControlInput{To: to, Signal: signal}
	if signal == adapterproto.SignalSessionCreate {
		in.Preset = "default"
	}
	return in
}

// The declarations of an endpoint (a pause, a list of the senders it accepts) govern
// messages and not control signals, so the owner can always reach an endpoint that
// refuses everything. What refuses a control signal for its sender is the rules of the
// node, which judge a source on another node. A preset that allows local sources
// opens nothing to a source elsewhere: its statement limits agents of this node,
// and a request from another node is judged by the rules of the node at intake,
// which no statement of a preset changes.
func TestAControlSignalFromANodeElsewhereThatTheRulesRefuseIsRejected(t *testing.T) {
	h := sessionNode(t, harness.Options{Presets: map[string]string{"open": `{"local_sources":"allow"}`}}, "a")
	other, err := ident.NodeIDFromBytes(bytes.Repeat([]byte{9}, ident.NodeIDLen))
	if err != nil {
		t.Fatal(err)
	}
	rt := runtimeEndpoint(t, h)
	to := ident.Address{Node: harness.NodeID(), Endpoint: rt}
	sender := endpointOf(t, h, "a")
	data := forgedControl(t, h, "control-from-afar", to, envelope.KindSessionCreate, "open")
	rc, err := h.Engine().Inbound(h.Ctx(), transport.RemoteOrigin(other), sender, data)
	if err != nil || len(rc.Reports) != 1 || rc.ControlReply != nil {
		t.Fatalf("the receipt: %+v, %v, want one report and no reply", rc, err)
	}
	// No node is trusted yet, so the rules refuse every source elsewhere.
	c := refusedCtl(t, h, "control-from-afar", delivery.RejectUnauthorized)
	if c.Key.SenderNode != other || c.Key.Recipient != rt {
		t.Errorf("the control record is of %v for %v, want %v for the runtime endpoint %v", c.Key.SenderNode, c.Key.Recipient, other, rt)
	}
	nothingWasMade(t, h)
	if n := createRequests(t, h); n != 0 {
		t.Errorf("%d session.create requests reached the adapter", n)
	}
}

// A signal that is repeated is answered from the record the node holds, with the
// same report and no control reply, and another request under its identifier is a
// conflict.
func TestARefusedControlSignalIsAnsweredFromItsRecordWhenItIsRepeated(t *testing.T) {
	h := sessionNode(t, harness.Options{}, "a", "b")
	closed := endpointOf(t, h, "b")
	if _, err := h.Engine().CloseEndpoint(h.Ctx(), engine.CloseRequest{Endpoint: closed}); err != nil {
		t.Fatal(err)
	}
	quiet(t, h)
	to := ident.Address{Node: harness.NodeID(), Endpoint: closed}
	from := endpointOf(t, h, "a")
	first := forgedControl(t, h, "control-refused", to, envelope.KindEndpointStatus, "")

	answer := func(data []byte) error {
		rc, err := h.Loopback().Send(h.Ctx(), harness.NodeID(), from, data)
		if err != nil {
			return err
		}
		if len(rc.Reports) != 1 || rc.ControlReply != nil {
			t.Fatalf("the receipt holds %d reports and the reply %v, want one report and no reply", len(rc.Reports), rc.ControlReply)
		}
		rep, err := h.VerifyReport(rc.Reports[0])
		if err != nil {
			t.Fatalf("the report does not verify: %v", err)
		}
		if rep.Status.State != delivery.StateRejected || rep.Status.Reject != delivery.RejectEndpointClosed || rep.Recipient != closed {
			t.Fatalf("the report is %+v for %v, want rejected (endpoint_closed) for %v", rep.Status, rep.Recipient, closed)
		}
		if !rep.DeliveredAt.IsZero() || rep.ReportedAt.IsZero() {
			t.Errorf("a refusal was reported delivered at %v, reported at %v", rep.DeliveredAt, rep.ReportedAt)
		}
		return nil
	}
	if err := answer(first); err != nil {
		t.Fatalf("the first send: %v", err)
	}
	if err := answer(first); err != nil {
		t.Fatalf("the repeat: %v", err)
	}
	if n := len(ctlRecords(t, h)); n != 1 {
		t.Errorf("%d control records after a repeat, want 1", n)
	}
	if _, err := h.Loopback().Send(h.Ctx(), harness.NodeID(), from, forgedControl(t, h, "control-refused", to, envelope.KindSessionCreate, "default")); err == nil {
		t.Error("another request under the identifier of a refused control signal was taken in")
	} else if reason, ok := engine.ReasonOf(err); !ok || reason != engine.ReasonMessageConflict {
		t.Errorf("another request under the identifier got %v, want the conflict", err)
	}
}

// The refusal is kept as long as a control record that ended is: a sender that asks
// before the retention is over gets the report, and one that asks after it is told the
// node holds no record, never a report made of nothing.
func TestARefusedControlSignalIsKeptWhileASenderCanAskAndSweptAfter(t *testing.T) {
	h := sessionNode(t, harness.Options{ArchiveSweepEvery: sweepEvery}, "a", "b")
	if _, err := h.Engine().CloseEndpoint(h.Ctx(), engine.CloseRequest{Endpoint: endpointOf(t, h, "b")}); err != nil {
		t.Fatal(err)
	}
	quiet(t, h)
	res, err := h.Agent("a").Control(h.Ctx(), controlInput(h.Addr("b"), adapterproto.SignalEndpointStatus))
	if err != nil {
		t.Fatal(err)
	}
	quiet(t, h)
	c := refusedCtl(t, h, res.MessageID, delivery.RejectEndpointClosed)
	until, finished := store.RetainUntil(c)
	if !finished || until.Before(c.Record.ReportBy) {
		t.Fatalf("RetainUntil = %v, %v; want a finished record kept at least until report_by %v", until, finished, c.Record.ReportBy)
	}

	for h.Clock().Now().Add(sweepEvery).Before(until) {
		passes(t, h, 1)
	}
	b := endpointOf(t, h, "b")
	ans, err := pullFor(h, res.MessageID, b)
	if err != nil || len(ans.Reports) != 1 || len(ans.NoRecord) != 0 || ans.ControlReply != nil {
		t.Fatalf("a pull before the retention ended: %+v, %v, want the report alone", ans, err)
	}
	if rep, err := h.VerifyReport(ans.Reports[0]); err != nil || rep.Status.State != delivery.StateRejected || rep.Status.Reject != delivery.RejectEndpointClosed {
		t.Errorf("the report is %+v (%v), want rejected (endpoint_closed)", rep.Status, err)
	}

	h.Advance(3 * 24 * time.Hour)
	quiet(t, h)
	passes(t, h, 3)
	if n := len(ctlRecords(t, h)); n != 0 {
		t.Fatalf("%d control records are left after the sweeps, want none", n)
	}
	ans, err = pullFor(h, res.MessageID, b)
	if err != nil || len(ans.Reports) != 0 || !slices.Equal(ans.NoRecord, []ident.EndpointID{b}) {
		t.Errorf("a pull after the sweep: %+v, %v, want only the endpoint as no record", ans, err)
	}
}

// A crash walk of the writes that take a refused control signal in: the signal
// reaches an endpoint that stands for no runtime, and before every step of the
// transactions that accept it at the source, write the refusal at the target and
// take the report in, the disks a crash at that instant can leave are taken and each
// is started as a node of its own. Wherever the node died, a signal its source holds
// ends with one rejected control record that holds nothing of an execution, the
// sender's record rejected by the report and the sender told once; a signal the
// source never accepted leaves nothing.
func TestARefusedControlSignalRecoversFromACrashAtEveryPoint(t *testing.T) {
	cov := &coverage{hit: map[fsx.Key]int{}}
	taker := newDiskTaker(cov, "intake", "control-intake", "control-reply", "source-update")
	h := nodeWith(t, harness.Options{Observer: taker}, "a", "b")
	taker.h = h
	res, err := h.Agent("a").Control(h.Ctx(), controlInput(h.Addr("b"), adapterproto.SignalSessionCreate))
	if err != nil {
		t.Fatalf("the signal is accepted by its sender's node: %v", err)
	}
	quiet(t, h)
	h.Advance(pullInterval)
	quiet(t, h)
	if len(taker.disks) == 0 {
		t.Fatal("no step of the watched transactions was reached")
	}
	h.Stop()

	var never, settled int // the disks of a crash before the source held the signal, and after
	for _, td := range taker.disks {
		t.Run(td.where, func(t *testing.T) {
			r := restarted(t, td.disk, "a", "b")
			sc := scanOf(t, r)
			var outs []store.OutRecord
			for _, o := range sc.Out {
				if o.Key.Message == delivery.MessageID(res.MessageID) {
					outs = append(outs, o)
				}
			}
			var ctls []store.CtlRecord
			for _, c := range sc.Ctl {
				if c.Key.Message == delivery.MessageID(res.MessageID) {
					ctls = append(ctls, c)
				}
			}
			switch {
			case len(outs) == 0:
				never++
				if len(ctls) != 0 {
					t.Errorf("%d control records of a signal the source never accepted", len(ctls))
				}
			case len(outs) > 1 || len(ctls) != 1:
				t.Fatalf("%d source records and %d control records of one signal, want 1 and 1", len(outs), len(ctls))
			default:
				settled++
				refusedCtl(t, r, res.MessageID, delivery.RejectMalformed)
				if d := outs[0].Outbound.Recipients[0].Delivery; d.State != delivery.StateRejected || d.Reject != delivery.RejectMalformed {
					t.Errorf("the source record is %s (%s), want rejected (malformed)", d.State, d.Reject)
				}
				r.Advance(mergeWindow)
				quiet(t, r)
				if got := toldKinds(t, r, "a"); !slices.Equal(got, []delivery.Kind{delivery.KindRejected}) {
					t.Errorf("a was told %v, want the rejection once", got)
				}
			}
			checkNoOrphanBody(t, sc)
		})
	}

	if never == 0 || settled == 0 {
		t.Errorf("the walk reached %d disks before the source held the signal and %d after: both must be walked", never, settled)
	}
	t.Logf("%d disks before the source held the signal, %d after", never, settled)

	// The refusal's own write is one of the steps walked.
	ctlSteps := 0
	for _, k := range harness.ProgramTable(t).Keys() {
		if k.Tag.Tx == "control-intake" && k.Tag.Role == "ctl" {
			ctlSteps++
			if cov.hit[k.Key] == 0 {
				t.Errorf("the step %s %q of transaction control-intake was never reached: the walk proves nothing about a crash while the refusal is written", k.Step, k.Tag.Role)
			}
		}
	}
	if ctlSteps == 0 {
		t.Fatal("no step of the write of a control record is declared")
	}
}
