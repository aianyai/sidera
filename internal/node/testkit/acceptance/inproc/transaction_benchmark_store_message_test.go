package inproc

import (
	"bytes"
	"fmt"
	"path"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/handoff"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/txn"
)

func transactionBenchmarkStoreMessageCases() []transactionBenchmarkCase {
	names := []string{
		"intake", "deliver", "deliver-reply", "source-update",
		"control-intake", "control-result", "control-reply",
		"prompt-begin", "prompt-end", "blocked", "lease", "ack", "tick",
		"endpoint-update", "endpoint-close", "endpoint-drain",
	}
	var cases []transactionBenchmarkCase
	for _, name := range names {
		cases = append(cases, transactionBenchmarkCase{name: name, prepare: func(b *testing.B, observer txn.Observer) transactionBenchmarkFixture {
			return prepareTransactionStoreMessage(b, observer, name)
		}})
	}
	return cases
}

// Every sample has one recipient and a fixed small body. Updates start from
// owner-written records; delivery transitions are computed before measurement.
func prepareTransactionStoreMessage(b *testing.B, observer txn.Observer, name string) transactionBenchmarkFixture {
	b.Helper()
	fs, w := newTransactionBenchmarkWriter(b, observer)
	harness.MakeLayout(b, fs)
	st := store.New(w)
	inputs := newTransactionBenchmarkStoreRecords(b)
	at, body := harness.DefaultStart, inputs.body
	ref := store.BodyRefOf(body)
	out, in, ctl, ep := inputs.out, inputs.in, inputs.ctl, inputs.endpoint
	var err error
	head := store.AttemptsRecord{Endpoint: in.Key.Recipient, InFlight: &store.InFlightPrompt{
		Attempt: ctl.Intent.Attempt, Kind: handoff.PromptKindFirst, Covers: []store.TargetKey{in.Key}}}
	must := func(err error) {
		b.Helper()
		if err != nil {
			b.Fatal(err)
		}
	}
	apply := func(r delivery.Record, ev delivery.Event) delivery.Record {
		b.Helper()
		next, outcome, err := r.Apply(at.Add(time.Second), ev)
		if err != nil || outcome.Result != delivery.ResultApplied {
			b.Fatalf("prepare delivery event %v: %+v, %v", ev.Type, outcome, err)
		}
		return next
	}
	var checks []func() error
	expect := func(dir string, rec store.Record) {
		checks = append(checks, func() error {
			want, err := store.EncodeRecord(rec)
			if err != nil {
				return err
			}
			got, err := fs.ReadFile(path.Join(dir, rec.Name()))
			if err == nil && !bytes.Equal(got, want) {
				return fmt.Errorf("%s: persisted record differs from owner input", rec.Name())
			}
			return err
		})
	}
	expectBody := func(owner string, present bool) {
		checks = append(checks, func() error {
			if present {
				got, err := st.ReadBody(owner, ref)
				if err == nil && !bytes.Equal(got, body) {
					return fmt.Errorf("%s: body differs", owner)
				}
				return err
			}
			there, err := fsx.Exists(fs, path.Join(store.BodiesDir, store.BodyFileName(owner)))
			if err == nil && there {
				return fmt.Errorf("%s: dropped body still exists", owner)
			}
			return err
		})
	}
	var invoke func() error
	switch name {
	case "intake":
		invoke = func() error { return st.Intake(body, out) }
		expect(store.OutDir, out)
		expectBody(out.Name(), true)
	case "deliver":
		invoke = func() error { return st.Deliver(body, []store.InRecord{in}) }
		expect(store.InDir, in)
		expectBody(in.Name(), true)
	case "deliver-reply", "source-update", "control-reply":
		if name == "control-reply" {
			out.Key.Control, out.Outbound.Kind, out.Envelope.Preset = true, envelope.KindSessionCreate, "default"
		}
		must(st.Intake(body, out))
		out.Outbound.Recipients[0].Delivery = apply(out.Outbound.Recipients[0].Delivery, delivery.Event{Type: delivery.EventDelivered})
		out.Body = nil
		switch name {
		case "deliver-reply":
			in.Handout.ReplyTo = out.Key.Message
			invoke = func() error { return st.DeliverReply(body, &out, []store.InRecord{in}) }
			expect(store.InDir, in)
			expectBody(in.Name(), true)
		case "source-update":
			invoke = func() error { return st.SourceUpdate(out) }
		case "control-reply":
			out.ControlReply = []byte("stored control reply")
			invoke = func() error { return st.ControlReply(out) }
		}
		expect(store.OutDir, out)
		expectBody(out.Name(), false)
	case "control-intake", "control-result":
		ep = inputs.creatingEndpoint
		if name == "control-intake" {
			ctl.Initial = &store.InitialMessage{ContentType: "text/plain", Body: ref}
			in = inputs.derived
			invoke = func() error { return st.ControlIntake(body, &ep, ctl, &in) }
			expect(store.InDir, in)
			expectBody(in.Name(), true)
		} else {
			must(st.ControlIntake(body, &ep, ctl, &inputs.derived))
			expectBody(inputs.derived.Name(), true)
			ctl.Intent.State, ctl.Intent.NativeRef = store.IntentCreated, "benchmark-native"
			ctl.Record = apply(ctl.Record, delivery.Event{Type: delivery.EventReplyCommitted})
			ctl.Dedup, err = ctl.Dedup.WithReply([]byte("stored control reply"))
			must(err)
			ctl.Result = []byte(`{"created":true}`)
			ep.Status, ep.Session.NativeRef = store.EndpointOpen, ctl.Intent.NativeRef
			effect := store.CreationEffect(ctl, ep, &inputs.derived)
			head, notified, err := effect.PromptRecords()
			must(err)
			if head != nil {
				expect(store.AttemptsDir, *head)
			}
			for _, target := range notified {
				expect(store.InDir, target)
			}
			invoke = func() error { return st.ControlResult(ctl, &effect) }
		}
		expect(store.CtlDir, ctl)
		expect(store.EndpointsDir, ep)
	case "prompt-begin", "prompt-end", "blocked":
		must(st.Deliver(body, []store.InRecord{in}))
		switch name {
		case "prompt-begin":
			invoke = func() error { return st.PromptBegin(head) }
		case "prompt-end":
			must(st.PromptBegin(head))
			head.InFlight = nil
			head.Attempts = []handoff.Attempt{{At: at.Add(time.Second), Outcome: handoff.AttemptDelivered, Kind: handoff.PromptKindFirst}}
			in.Record = apply(in.Record, delivery.Event{Type: delivery.EventNotified})
			invoke = func() error { return st.PromptEnd(head, []store.InRecord{in}) }
		case "blocked":
			head.InFlight = nil
			head.Blocked = handoff.BlockedReport{Cause: handoff.CauseAdapterOffline, Since: at.Add(time.Second)}
			in.Record = apply(in.Record, delivery.Event{Type: delivery.EventBlocked, Cause: delivery.CauseAdapterOffline})
			invoke = func() error { return st.Blocked(head, []store.InRecord{in}) }
		}
		expect(store.InDir, in)
		expect(store.AttemptsDir, head)
		expectBody(in.Name(), true)
	case "lease", "ack":
		must(st.Deliver(body, []store.InRecord{in}))
		lease, err := delivery.NewLeaseID(bytes.NewReader(bytes.Repeat([]byte{5}, delivery.LeaseIDLen)))
		must(err)
		var handout delivery.HandoutResult
		in.Record, handout, err = in.Record.Handout(at.Add(time.Second), delivery.AuthorizationAllow, 1, 1, lease)
		must(err)
		if handout.Action != delivery.HandoutGranted {
			b.Fatalf("prepare handout: %+v", handout)
		}
		if name == "lease" {
			invoke = func() error { return st.Lease([]store.InRecord{in}) }
		} else {
			must(st.Lease([]store.InRecord{in}))
			var verdict delivery.AckVerdict
			in.Record, verdict, _, err = in.Record.Ack(at.Add(2*time.Second), delivery.Ack{Part: 1, Lease: lease, TokenMatches: true})
			must(err)
			if verdict != delivery.AckCompleted {
				b.Fatalf("prepare acknowledgement: %v", verdict)
			}
			in.Body = nil
			invoke = func() error { return st.Ack([]store.InRecord{in}) }
		}
		expect(store.InDir, in)
		expectBody(in.Name(), name == "lease")
	case "tick", "endpoint-drain":
		must(st.Deliver(body, []store.InRecord{in}))
		if name == "tick" {
			var outcomes []delivery.Outcome
			in.Record, outcomes, err = in.Record.Tick(at.Add(delivery.DefaultEndpointMaxQueue + time.Second))
			must(err)
			if len(outcomes) == 0 || in.Record.NeedsBody() {
				b.Fatal("prepare tick did not close the queued delivery")
			}
			in.Body = nil
			invoke = func() error { return st.Tick(store.TickBatch{In: []store.InRecord{in}}) }
		} else {
			must(st.EndpointUpdate(ep))
			ep.Status, ep.Token = store.EndpointClosed, nil
			must(st.EndpointClose(ep, nil))
			in.Record = apply(in.Record, delivery.Event{Type: delivery.EventRejected, Reject: delivery.RejectEndpointClosed})
			in.Body = nil
			invoke = func() error { return st.EndpointDrain(store.DrainBatch{In: []store.InRecord{in}}) }
			expect(store.EndpointsDir, ep)
		}
		expect(store.InDir, in)
		expectBody(in.Name(), false)
	case "endpoint-update", "endpoint-close":
		must(st.EndpointUpdate(ep))
		if name == "endpoint-update" {
			ep.DisplayName = "Updated benchmark endpoint"
			invoke = func() error { return st.EndpointUpdate(ep) }
		} else {
			ep.Status, ep.Token = store.EndpointClosed, nil
			invoke = func() error { return st.EndpointClose(ep, nil) }
		}
		expect(store.EndpointsDir, ep)
	default:
		b.Fatalf("unknown store message benchmark %q", name)
	}
	return transactionBenchmarkFixture{invoke: invoke, verify: func() error {
		for _, check := range checks {
			if err := check(); err != nil {
				return err
			}
		}
		scan, err := st.Scan()
		if err == nil && (len(scan.Damaged) != 0 || len(scan.Unknown) != 0) {
			return fmt.Errorf("store scan found damage or unknown files: %+v, %v", scan.Damaged, scan.Unknown)
		}
		return err
	}}
}
