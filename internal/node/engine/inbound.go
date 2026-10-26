package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/transport"
	"github.com/aianyai/sidera/internal/reply"
)

// The engine is the handler of every transport: what arrives for this node comes
// here, with the origin the transport says it has.
var _ transport.Handler = (*Engine)(nil)

type cmdInbound struct {
	call[transport.Receipt]
	from   transport.Origin
	sender ident.EndpointID
	data   []byte
}

func (c *cmdInbound) gate() gate { return gateLoopback }
func (c *cmdInbound) run(e *Engine, now time.Time) {
	rc, err := e.takeIn(now, c.from, c.sender, c.data)
	c.done(rc, e.classify(err))
}

// Inbound takes one envelope for this node, from the endpoint sender of the node
// the origin names, and answers with the signed reports of its deliveries. It is
// the entry of the receiving side of every transport.
//
// What is judged here is the target's: the envelope itself, the sender's repeat of
// a request already taken in (a replay gets the original record's report, another
// body under the same identifier is a conflict and changes nothing), the reply
// permission the envelope's evidence earns, the check of the endpoint's
// declarations and, for a source that is not on this node, of the node's rules,
// and the backlog of the endpoint. The records are on disk before the report is
// handed out, and a report is signed before anything is written, so a receipt
// that exists describes records that do.
func (e *Engine) Inbound(ctx context.Context, from transport.Origin, sender ident.EndpointID, envelope []byte) (transport.Receipt, error) {
	c := &cmdInbound{call: newCall[transport.Receipt](), from: from, sender: sender, data: envelope}
	return roundTrip(ctx, e, submitInternal, c, c.call)
}

type cmdPull struct {
	call[transport.PullAnswer]
	from transport.Origin
	req  transport.PullRequest
}

func (c *cmdPull) gate() gate { return gateLoopback }
func (c *cmdPull) run(e *Engine, now time.Time) {
	ans, err := e.answerPull(now, c.from, c.req)
	c.done(ans, e.classify(err))
}

// Pull answers a source's pull for the current reports of its deliveries of one
// message to the recipients the request names: for each, the signed report of the
// record the target holds, or the recipient listed as having no record. A
// recipient the request does not name is not answered.
func (e *Engine) Pull(ctx context.Context, from transport.Origin, req transport.PullRequest) (transport.PullAnswer, error) {
	c := &cmdPull{call: newCall[transport.PullAnswer](), from: from, req: req}
	return roundTrip(ctx, e, submitInternal, c, c.call)
}

// answerPull gives, for each recipient of the request, a signed report of its
// record or the recipient in NoRecord, and nothing else. Every record the target
// holds can be signed, so a held record is never left out of both lists. The
// whole answer is refused while the node may not sign: a "no record" with no
// report beside it would be given on the word of a node that cannot vouch for
// the others, and a clock in doubt dates nothing.
func (e *Engine) answerPull(now time.Time, from transport.Origin, req transport.PullRequest) (transport.PullAnswer, error) {
	if err := e.canSign(); err != nil {
		return transport.PullAnswer{}, err
	}
	if len(req.Recipients) == 0 {
		return transport.PullAnswer{}, invalidArgument("recipients", errors.New("a pull names at least one recipient"))
	}
	asked := make(map[ident.EndpointID]bool, len(req.Recipients))
	for _, r := range req.Recipients {
		asked[r] = true
	}
	var ans transport.PullAnswer
	answered := make(map[ident.EndpointID]bool, len(asked))
	for _, k := range e.view.arrived[arrivalKey{from.Node(), req.Message}] {
		if !asked[k.Recipient] {
			continue
		}
		rec, err := e.signReport(now, e.view.in[k])
		if err != nil {
			return transport.PullAnswer{}, err
		}
		ans.Reports = append(ans.Reports, rec)
		answered[k.Recipient] = true
	}
	for _, r := range req.Recipients {
		if !answered[r] {
			ans.NoRecord = append(ans.NoRecord, r)
			answered[r] = true // a recipient named twice is listed once
		}
	}
	return ans, nil
}

// arrival is one recipient's side of an envelope taken in: its target record, as
// it is or as it will be written.
type arrival struct {
	in    store.InRecord
	fresh bool
}

func (e *Engine) takeIn(now time.Time, from transport.Origin, sender ident.EndpointID, data []byte) (transport.Receipt, error) {
	if err := e.canSign(); err != nil {
		return transport.Receipt{}, err
	}
	env, err := envelope.Decode(data, envelope.DefaultLimits())
	if err == nil {
		err = env.ValidateFor(e.d.Node)
	}
	if err != nil {
		return transport.Receipt{}, invalidArgument("envelope", err)
	}
	if env.Kind != envelope.KindMessage {
		return transport.Receipt{}, invalidArgument("kind", errors.New("a control signal is taken in with the creation of sessions"))
	}
	digest, ref := env.Digest(), store.BodyRefOf(env.Body)
	var arrivals []arrival
	var updatedOut *store.OutRecord
	var evidence []error
	for _, r := range env.RecipientsOn(e.d.Node) {
		key := store.TargetKey{SenderNode: from.Node(), Sender: sender, Message: env.ID, Recipient: r}
		if old, ok := e.view.in[key]; ok {
			if delivery.CheckDedup(&old.Dedup, digest) == delivery.DedupConflict {
				return transport.Receipt{}, refuse(ReasonMessageConflict)
			}
			arrivals = append(arrivals, arrival{in: old})
			continue
		}
		j, err := e.judge(now, env, from, sender, r, digest, ref)
		if err != nil {
			return transport.Receipt{}, err
		}
		if j.out != nil {
			updatedOut = j.out
		}
		if j.evidenceErr != nil {
			evidence = append(evidence, j.evidenceErr)
		}
		arrivals = append(arrivals, arrival{in: j.in, fresh: true})
	}
	var rc transport.Receipt
	for _, a := range arrivals {
		rec, err := e.signReport(now, a.in)
		if err != nil {
			return transport.Receipt{}, err
		}
		rc.Reports = append(rc.Reports, rec)
	}
	if err := e.writeArrivals(env, arrivals, updatedOut); err != nil {
		return transport.Receipt{}, err
	}
	for _, ee := range evidence {
		// Evidence that did not verify says a peer malfunctions or is hostile, or that a
		// key changed since it was signed. The reply was judged without it.
		e.log.Warn("the evidence of a reply was discarded", "message", string(env.ID), "error", ee.Error())
		if _, err := e.raiseAlert(now, "evidence_error", string(env.ID), ee.Error()); err != nil {
			e.classify(err)
		}
	}
	return rc, nil
}

// writeArrivals writes the target records of the new arrivals, with the source
// record the reply evidence changed in the same transaction, and takes what was
// written into the view. The message goes with them as the body of every record
// that holds one: each has a copy of its own, even when the source record on this
// node holds the same bytes.
func (e *Engine) writeArrivals(env envelope.Envelope, arrivals []arrival, out *store.OutRecord) error {
	var ins []store.InRecord
	var body []byte
	for _, a := range arrivals {
		if !a.fresh {
			continue
		}
		ins = append(ins, a.in)
		if a.in.Body != nil {
			body = env.Body
		}
	}
	if len(ins) == 0 {
		return nil
	}
	var written []store.InRecord
	var err error
	if out != nil {
		written, err = e.d.Store.DeliverReply(body, out, ins)
	} else {
		written, err = e.d.Store.Deliver(body, ins)
	}
	if err != nil {
		return err
	}
	if out != nil {
		e.view.setOut(*out)
	}
	for _, in := range written {
		e.view.setIn(in)
	}
	return nil
}

// judgement is what the target decided about one recipient of an envelope.
type judgement struct {
	in store.InRecord
	// out is the source record that the evidence of a reply changed, nil when it
	// changed nothing.
	out *store.OutRecord
	// evidenceErr is why the evidence was discarded, nil when it was not.
	evidenceErr error
}

// judge decides what becomes of an envelope for one recipient it holds no record
// for: whether the reply it may be earns permission, whether the endpoint takes it
// in, and so the record that is written for it, accepted or rejected.
func (e *Engine) judge(now time.Time, env envelope.Envelope, from transport.Origin, sender, recipient ident.EndpointID,
	digest delivery.BodyDigest, ref store.BodyRef) (judgement, error) {
	st := e.view.endpoints[recipient]
	settings := e.d.Config.Node().Settings.EndpointDefaults
	if st != nil {
		settings = e.d.Config.EndpointSettings(st.rec.Instance, recipient)
	}
	var j judgement
	decision := reply.Decision{Reason: reply.ReasonNoOutbound}
	if env.ReplyTo != "" {
		adm, ob, outRec, err := e.admitReply(now, env, from, sender, recipient)
		if err != nil {
			return j, err
		}
		decision, j.evidenceErr = adm.Decision, adm.EvidenceError
		if adm.Outbound != nil && adm.Outbound != ob {
			j.out = e.withEvidence(now, *outRec, adm, ident.Address{Node: from.Node(), Endpoint: sender})
		}
	}
	key := store.TargetKey{SenderNode: from.Node(), Sender: sender, Message: env.ID, Recipient: recipient}
	rec, err := delivery.NewTargetRecord(now, env.AcceptBefore, env.ReportBy, settings.MaxQueue)
	if err != nil {
		return j, fmt.Errorf("engine: the record of %s: %w", env.ID, err)
	}
	in := store.InRecord{
		Key: key, Record: rec, Dedup: delivery.NewDedupRecord(delivery.DedupKey{SenderNode: key.SenderNode, Message: env.ID, Recipient: recipient}, digest, env.ReportBy),
		ReplyDecision: decision, AcceptedAt: now,
		Handout: store.HandoutFields{ContentType: env.ContentType, ReplyTo: env.ReplyTo, Thread: env.Thread, Seq: env.Seq[recipient], SentAt: env.SentAt},
	}
	if reason := messageRefusal(st); reason != delivery.RejectNone {
		in.Auth = delivery.Authorization{Kind: delivery.AuthorizationDeny, Reject: reason}
		j.in, err = rejected(now, in, reason)
		return j, err
	}
	in.Auth = delivery.IntakeAuthorization(e.judgeAuthz(now, env, from, sender, recipient, st, decision.Granted()))
	verdict, err := delivery.Intake(now, env.AcceptBefore, authz.TrafficMessage, in.Auth,
		delivery.Backlog{Queued: e.view.backlog[recipient], Limit: settings.BacklogLimit})
	if err != nil {
		return j, fmt.Errorf("engine: intake of %s: %w", env.ID, err)
	}
	switch verdict.Outcome {
	case delivery.IntakeAccept:
		in.Body = &ref
		j.in = in
	case delivery.IntakeReject:
		j.in, err = rejected(now, in, verdict.Reject)
	default:
		// Only a request judged by node rules can be suspended, and this node has
		// no trust that lapses yet.
		err = fmt.Errorf("engine: intake of %s: a suspended request has no answer here", env.ID)
	}
	return j, err
}

// messageRefusal says why an endpoint cannot be given an ordinary message, or
// RejectNone when it can. An endpoint that was closed says so: it was there. One
// that is not there, one whose creation has not ended and a runtime endpoint are
// answered as missing: a runtime endpoint stands for an adapter instance and
// takes control signals only, so for a message it is no endpoint.
func messageRefusal(st *endpointState) delivery.RejectReason {
	switch {
	case st == nil || st.rec.Kind == adapterreg.KindRuntime:
		return delivery.RejectEndpointMissing
	case st.rec.Status == store.EndpointClosed:
		return delivery.RejectEndpointClosed
	case st.rec.Status != store.EndpointOpen:
		return delivery.RejectEndpointMissing
	}
	return delivery.RejectNone
}

// rejected is the record of a delivery the target refused for a reason: accepted
// by the constructor and then closed by the delivery rules, so that the state is
// reached by the one transition function.
func rejected(now time.Time, in store.InRecord, reason delivery.RejectReason) (store.InRecord, error) {
	rec, o, err := in.Record.Apply(now, delivery.Event{Type: delivery.EventRejected, Reject: reason})
	if err != nil || o.Result != delivery.ResultApplied {
		return in, fmt.Errorf("engine: rejecting %s as %s: %v (%v)", in.Key.Message, reason, err, o.Ignored)
	}
	in.Record = rec
	in = settledIn(in) // a rejected record holds no body; none was given to it
	return in, nil
}

// judgeAuthz is the authorization check of the intake: the declarations of the
// endpoint and, for a request that did not come over the loopback, the node's
// rules. A local source is never judged by node rules; which requests are local
// is the transport's to say, and nothing the request claims.
func (e *Engine) judgeAuthz(now time.Time, env envelope.Envelope, from transport.Origin, sender ident.EndpointID,
	recipient ident.EndpointID, st *endpointState, replyGranted bool) authz.Verdict {
	action, _ := env.Kind.Action()
	snd := authz.Sender{Node: from.Node(), Endpoint: sender}
	target := authz.Target{Kind: authz.TargetEndpoint, Endpoint: recipient}
	if from.Local() {
		return authz.EvaluateLocal(authz.LocalRequest{
			Stage: authz.StageIntake, Sender: snd, Action: action, Target: target,
			MultiRecipient: env.MultiRecipient, Reply: replyGranted, TargetDeclaration: st.decl,
		})
	}
	// The trust of a node elsewhere is not known yet, and the request carries the
	// zero value, which is denied: a remote sender is refused until trust exists.
	return authz.Evaluate(e.d.Config.Policy().Policy, authz.Request{
		Now: now, Stage: authz.StageIntake, Sender: snd, Action: action, Target: target,
		BodySize: int64(len(env.Body)), MultiRecipient: env.MultiRecipient, Reply: replyGranted, TargetDeclaration: st.decl,
	})
}

// admitReply decides the reply permission of an envelope that says it answers a
// message, with this node's own record of that message (the one the recipient
// sent, if the recipient sent it), and applies the evidence the envelope carries.
func (e *Engine) admitReply(now time.Time, env envelope.Envelope, from transport.Origin, sender, recipient ident.EndpointID) (reply.Admission, *envelope.Outbound, *store.OutRecord, error) {
	var ob *envelope.Outbound
	var outRec *store.OutRecord
	if name, ok := e.view.byMessage[msgKey{recipient, env.ReplyTo}]; ok {
		if o := e.view.out[name]; o.Lost == nil {
			outRec, ob = &o, &o.Outbound
		}
	}
	adm, err := reply.Admit(reply.Request{
		Now: now, Enabled: e.d.Config.Node().Settings.ReplyPermission, Local: e.d.Node,
		Replier: ident.Address{Node: from.Node(), Endpoint: sender}, Target: recipient, ReplyTo: env.ReplyTo,
		Outbound: ob, Evidence: env.Evidence, Accepted: e.acceptedKey(from.Node()),
	})
	if err != nil {
		return reply.Admission{}, nil, nil, fmt.Errorf("engine: the reply %s: %w", env.ID, err)
	}
	return adm, ob, outRec, nil
}

// withEvidence is the source record after the evidence of a reply was applied: the
// outbound the admission returns, the feedback the evidence caused, and the body
// reference gone once no delivery of the record needs the body.
func (e *Engine) withEvidence(now time.Time, o store.OutRecord, adm reply.Admission, replier ident.Address) *store.OutRecord {
	o.Outbound = *adm.Outbound
	o = e.withFeedback(now, o, replier, adm.Reported)
	o = settledSource(o)
	return &o
}

// settledSource drops the body reference of a source record none of whose
// deliveries needs the body any more: once every receipt is in, only the digest
// of the request is kept, for the comparison of a repeat.
func settledSource(o store.OutRecord) store.OutRecord {
	if o.Body != nil && outUnfinished(o) == 0 {
		o.Body = nil
	}
	return o
}

// withFeedback adds the feedback that the outcomes of applying a report to the
// delivery to recipient call for to the source record, to be written with it: the
// feedback is decided with the state change, and a record that said "reported"
// without the feedback being stored beside it would lose it.
func (e *Engine) withFeedback(now time.Time, o store.OutRecord, recipient ident.Address, outcomes []delivery.Outcome) store.OutRecord {
	items := append([]delivery.Item(nil), o.Feedback...)
	for _, oc := range outcomes {
		for _, fb := range oc.Feedback {
			items = append(items, delivery.Item{Ref: delivery.DeliveryRef{
				Sender:  ident.Address{Node: e.d.Node, Endpoint: o.Key.Sender},
				Message: o.Outbound.Message, Recipient: recipient,
			}, Feedback: fb, At: now})
		}
	}
	o.Feedback = items
	return o
}
