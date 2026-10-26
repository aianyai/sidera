package engine

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/transport"
	"github.com/aianyai/sidera/internal/publication"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// A control signal is taken in by the target in a fixed order, and what it writes
// is one transaction (control-intake): the control record, and for a creation that
// goes on to the adapter the session endpoint it makes and the creation intent.
//
//  1. A repeat is answered from the record the node holds (the report, and the
//     control reply when one is stored); another request under the same
//     identifier is a conflict that changes nothing.
//  2. The endpoint must exist and be open, and the signal must be allowed (the
//     declarations of the endpoint, and for a source that is not on this node the
//     rules of the node) and in time. A signal that fails here is refused: its
//     control record is written rejected, with the reason, and holds nothing of an
//     execution, and the receipt carries its report and no control reply. That is
//     the one way a control record is rejected (delivery.NewRejectedTargetControlRecord):
//     accepting one hands it to be executed, so an accepted record is never rejected
//     afterwards. A repeat is answered from that record like any other.
//  3. The signal is accepted. What the node answers itself is answered in the same
//     transaction as the acceptance: endpoint.status from what the engine knows,
//     and a creation it refuses before it calls the adapter (the preset is not
//     defined, the preset is closed to sources on this node, the adapter is not
//     connected), with the reply that adapterreg.DaemonCreateFailure gives. No
//     session endpoint is made for these and no intent is recorded.
//  4. Otherwise a creation makes the session endpoint, with its token, and the
//     intent to create, on disk before the adapter hears of it (see create.go).

// takeInControl takes in an envelope that carries a control signal.
func (e *Engine) takeInControl(now time.Time, env envelope.Envelope, from transport.Origin, sender ident.EndpointID, digest delivery.BodyDigest) (transport.Receipt, error) {
	recipients := env.RecipientsOn(e.d.Node)
	if len(recipients) != 1 {
		return transport.Receipt{}, invalidArgument("to", errors.New("a control signal has one recipient on this node"))
	}
	key := store.TargetKey{SenderNode: from.Node(), Sender: sender, Message: env.ID, Recipient: recipients[0]}
	if old, ok := e.view.ctl[key]; ok {
		if delivery.CheckDedup(&old.Dedup, digest) == delivery.DedupConflict {
			return transport.Receipt{}, refuse(ReasonMessageConflict)
		}
		return e.controlReceipt(now, old)
	}
	if _, ok := e.view.in[key]; ok {
		return transport.Receipt{}, refuse(ReasonMessageConflict) // the identifier is a message's
	}
	st, refusal, err := e.controlTarget(now, env, from, sender, key.Recipient)
	if err != nil {
		return transport.Receipt{}, err
	}
	if refusal != delivery.RejectNone {
		return e.refuseControl(now, env, key, digest, refusal)
	}
	c := newCtlRecord(now, env, key, digest, e.d.Config.EndpointSettings(st.rec.Instance, st.rec.ID).MaxQueue)
	switch env.Kind {
	case envelope.KindEndpointStatus:
		err = e.answerControl(now, c, envelope.ControlResult{Signal: adapterproto.SignalEndpointStatus, Status: e.statusReply(now, st)})
	case envelope.KindSessionCreate:
		err = e.takeInCreate(now, env, from, st, c)
	default:
		// Only the signals the node offers are taken in; the others come with the
		// features that execute them.
		return transport.Receipt{}, invalidArgument("signal", fmt.Errorf("%s is not taken in yet", env.Kind))
	}
	if err != nil {
		return transport.Receipt{}, err
	}
	return e.controlReceipt(now, e.view.ctl[key])
}

// controlTarget is the endpoint a control signal is for, when the signal may be
// taken in (see the order above): it exists, is open, allows the sender and is in
// time. When it may not, the endpoint is nil and the reason is the one the sender
// is told (delivery.RejectNone when the signal may be taken in). An error is a
// failure of the node itself, not a refusal of the signal.
func (e *Engine) controlTarget(now time.Time, env envelope.Envelope, from transport.Origin, sender, recipient ident.EndpointID) (*endpointState, delivery.RejectReason, error) {
	st := e.view.endpoints[recipient]
	if reason := controlRefusal(st); reason != delivery.RejectNone {
		return nil, reason, nil
	}
	target := authz.Target{Kind: st.rec.Kind.TargetKind(), Endpoint: recipient}
	auth := delivery.IntakeAuthorization(e.judgeRequest(now, env, from, sender, target, st.decl, false))
	verdict, err := delivery.Intake(now, env.AcceptBefore, authz.TrafficControlSignal, auth, delivery.Backlog{})
	if err != nil {
		return nil, delivery.RejectNone, fmt.Errorf("engine: intake of %s: %w", env.ID, err)
	}
	switch verdict.Outcome {
	case delivery.IntakeAccept:
		return st, delivery.RejectNone, nil
	case delivery.IntakeReject:
		return nil, verdict.Reject, nil
	}
	return nil, delivery.RejectNone, fmt.Errorf("engine: intake of %s: a suspended request has no answer here", env.ID)
}

// controlRefusal says why an endpoint cannot be given a control signal, or
// RejectNone when it can: it is not there, it was closed, or its creation has not
// ended.
func controlRefusal(st *endpointState) delivery.RejectReason {
	switch {
	case st == nil:
		return delivery.RejectEndpointMissing
	case st.rec.Status == store.EndpointClosed:
		return delivery.RejectEndpointClosed
	case st.rec.Status != store.EndpointOpen:
		return delivery.RejectEndpointMissing
	}
	return delivery.RejectNone
}

// refuseControl writes the record of a control signal the target refuses and answers
// with its report. The record holds the refusal alone: the signal was never accepted,
// so there is no reply, no endpoint it made and no intent, and the queue time of the
// endpoint, which only a creation's first message uses, is not asked of an endpoint
// that may not be there.
func (e *Engine) refuseControl(now time.Time, env envelope.Envelope, key store.TargetKey, digest delivery.BodyDigest, reason delivery.RejectReason) (transport.Receipt, error) {
	rec, err := delivery.NewRejectedTargetControlRecord(now, env.AcceptBefore, env.ReportBy, reason)
	if err != nil {
		return transport.Receipt{}, fmt.Errorf("engine: refusing the control signal %s: %w", env.ID, err)
	}
	c := store.CtlRecord{
		Key: key, Record: rec,
		Dedup: delivery.NewDedupRecord(delivery.DedupKey{SenderNode: key.SenderNode, Message: env.ID, Recipient: key.Recipient}, digest, env.ReportBy),
		Kind:  env.Kind, Preset: env.Preset, AcceptedAt: now, SentAt: env.SentAt,
	}
	if err := e.d.Store.ControlIntake(nil, nil, c); err != nil {
		return transport.Receipt{}, err
	}
	e.view.setCtl(c)
	return e.controlReceipt(now, c)
}

// newCtlRecord is the record of a control signal at the moment it is accepted.
func newCtlRecord(now time.Time, env envelope.Envelope, key store.TargetKey, digest delivery.BodyDigest, maxQueue time.Duration) store.CtlRecord {
	return store.CtlRecord{
		Key:    key,
		Record: delivery.NewTargetControlRecord(now, env.AcceptBefore, env.ReportBy),
		Dedup:  delivery.NewDedupRecord(delivery.DedupKey{SenderNode: key.SenderNode, Message: env.ID, Recipient: key.Recipient}, digest, env.ReportBy),
		Kind:   env.Kind, Preset: env.Preset, AcceptedAt: now, EndpointMaxQueue: maxQueue, SentAt: env.SentAt,
	}
}

// answerControl commits the acceptance of a control signal together with its
// reply: the reply is made and stored with the deduplication record, and the
// delivery ends as delivered, in the one write that makes the record.
func (e *Engine) answerControl(now time.Time, c store.CtlRecord, result envelope.ControlResult) error {
	c, err := e.withReply(now, c, result)
	if err != nil {
		return err
	}
	if err := e.d.Store.ControlIntake(nil, nil, c); err != nil {
		return err
	}
	e.view.setCtl(c)
	return nil
}

// errReplyNotCommittable says that the delivery of a control record cannot take the
// reply: the record was closed before its signal was executed. Writing the reply
// again would fail every time.
var errReplyNotCommittable = errors.New("the delivery of the record no longer takes a reply")

// withReply is the control record with the reply of its signal stored and its
// delivery ended: the stored payload and the delivered state are never apart.
func (e *Engine) withReply(now time.Time, c store.CtlRecord, result envelope.ControlResult) (store.CtlRecord, error) {
	payload, err := e.controlReplyPayload(now, c.Key, result)
	if err != nil {
		return c, err
	}
	if c.Dedup, err = c.Dedup.WithReply(payload); err != nil {
		return c, err
	}
	rec, out, err := c.Record.Apply(now, delivery.Event{Type: delivery.EventReplyCommitted})
	if err != nil || out.Result != delivery.ResultApplied {
		return c, fmt.Errorf("engine: ending the control signal %s: %w: %v (%v)", c.Key.Message, errReplyNotCommittable, err, out.Ignored)
	}
	c.Record = rec
	return c, nil
}

// controlReceipt is what the target answers a control signal with: the signed
// report of its record and, once the record holds one, the signed control reply.
func (e *Engine) controlReceipt(now time.Time, c store.CtlRecord) (transport.Receipt, error) {
	rep, reply, err := e.controlAnswer(now, c)
	if err != nil {
		return transport.Receipt{}, err
	}
	return transport.Receipt{Reports: []record.Record{rep}, ControlReply: reply}, nil
}

// controlAnswer is the report of a control record and its control reply, signed
// again with the node's current key each time (the digest excludes signatures),
// nil while the record has none.
func (e *Engine) controlAnswer(now time.Time, c store.CtlRecord) (record.Record, *record.Record, error) {
	rep, err := e.signControlReport(now, c)
	if err != nil {
		return record.Record{}, nil, err
	}
	if !c.Dedup.HasReply {
		return rep, nil, nil
	}
	reply, err := e.signControlReply(c)
	if err != nil {
		return record.Record{}, nil, err
	}
	return rep, &reply, nil
}

// ctlName is the name the records a control record made refer to it by: the hash
// part of its file name.
func ctlName(c store.CtlRecord) string { return strings.TrimSuffix(c.Name(), ".rec") }

// ---------------------------------------------------------------------------
// endpoint.status

// statusReply is what the node answers to endpoint.status from what it has
// observed alone: it asks no adapter. An instance whose adapter is not connected
// has an activity that cannot be checked, and a preset is available when the
// adapter is connected and has reported nothing that stops a runtime.
func (e *Engine) statusReply(now time.Time, st *endpointState) *adapterproto.ControlStatusReply {
	ref := adapterproto.EndpointRef(st.rec.ID.String())
	a := e.adapters[st.rec.Instance]
	online := a != nil && a.online
	r := &adapterproto.ControlStatusReply{Activity: adapterproto.ActivityUnknown, Queued: e.view.backlog[st.rec.ID], AsOf: now.Unix()}
	var snapshot []adapterproto.Availability
	var rejected, controlUnknown, untested bool
	if a != nil {
		snapshot, rejected, controlUnknown, untested = a.snapshot, a.rejected[ref], a.controlUnknown && st.rec.Kind == adapterreg.KindRuntime, a.versionUntested
	}
	if online {
		r.Activity = adapterproto.ActivityIdle
		if a.busy[ref] {
			r.Activity = adapterproto.ActivityBusy
		}
	}
	r.Conditions = adapterreg.StatusConditions(snapshot, ref, !online, rejected, controlUnknown, untested)
	if st.rec.Kind == adapterreg.KindRuntime {
		stopped := containsAnyCondition(r.Conditions, adapterproto.ControlConditionAdapterOffline, adapterproto.ControlConditionRuntimeAuth, adapterproto.ControlConditionNativeUnavailable)
		presets := e.d.Config.Presets().Items
		for _, name := range sortedKeys(presets) {
			r.Presets = append(r.Presets, adapterproto.PresetAvailability{Name: name, Available: !stopped})
		}
	}
	return r
}

func containsAnyCondition(have []adapterproto.ControlCondition, want ...adapterproto.ControlCondition) bool {
	for _, h := range have {
		for _, w := range want {
			if h == w {
				return true
			}
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// session.create

// takeInCreate takes in a session creation: the checks the node makes before it
// calls the adapter, in the order the reasons are told to the sender (the preset is
// defined, the preset is open to a source on this node, the adapter is connected),
// then the session endpoint with its token and the creation intent, written before
// the adapter hears of it.
func (e *Engine) takeInCreate(now time.Time, env envelope.Envelope, from transport.Origin, st *endpointState, c store.CtlRecord) error {
	if len(env.Body) > 0 {
		return invalidArgument("body", errors.New("a creation carries no initial message yet"))
	}
	if reason, refused := e.createRefusal(env.Preset, from, st); refused {
		return e.answerControl(now, c, envelope.ControlResult{Signal: adapterproto.SignalSessionCreate, Create: createResultOf(adapterreg.DaemonCreateFailure(reason))})
	}
	if e.view.OpenEndpoints() >= publication.MaxEndpointsPerNode {
		// The signal is acceptable and no session is made, which the reply of a failed
		// creation could say, but none of its reasons does: they name the preset, the
		// adapter and the native runtime. Until the protocol has one, the call is
		// refused and nothing is recorded.
		return &Refusal{Reason: ReasonEndpointLimit, Limit: LimitNode}
	}
	ep, err := e.newSessionEndpoint(st, c)
	if err != nil {
		return err
	}
	attempt, err := e.newAttemptID()
	if err != nil {
		return err
	}
	c.SessionEndpoint = ep.ID
	c.Intent = &store.CreateIntent{Attempt: attempt, Endpoint: ep.ID, State: store.IntentCreating}
	if err := e.d.Store.ControlIntake(nil, &ep, c); err != nil {
		return err
	}
	e.view.setCtl(c)
	e.view.putEndpoint(ep, authz.Declaration{})
	e.beginCreate(now, c)
	return nil
}

// createRefusal says whether the node refuses a creation by itself, before it
// calls the adapter, and why. The rule about a preset's statement on local sources
// is authz.LocalCreate's, and only a source on this node is judged by it: a source
// elsewhere is judged by the rules of the node, at intake.
func (e *Engine) createRefusal(preset string, from transport.Origin, st *endpointState) (adapterreg.DaemonCreateReason, bool) {
	p, ok := e.d.Config.Presets().Items[preset]
	if !ok {
		return adapterreg.DaemonCreatePresetUnknown, true
	}
	if from.Local() {
		if v := authz.LocalCreate(p.LocalSources); v.Outcome != authz.OutcomeAllow {
			if v.Reason != authz.ReasonPresetClosedToLocalSources {
				// The preset format refuses a preset without a statement, so this is a
				// defect elsewhere; it is refused all the same, and said.
				e.log.Error("a preset has no valid statement about local sources; it is closed to them", "preset", preset)
			}
			return adapterreg.DaemonCreatePresetClosedToLocalSources, true
		}
	}
	if a := e.adapters[st.rec.Instance]; a == nil || !a.online || e.d.Adapters == nil {
		return adapterreg.DaemonCreateAdapterOffline, true
	}
	return 0, false
}

// createResultOf is the reply a creation the node refuses itself carries, in the
// values of the signed record.
func createResultOf(r adapterproto.ControlCreateReply) *envelope.CreateResult {
	out := &envelope.CreateResult{Outcome: r.Outcome, Reason: r.Reason}
	if r.SessionEndpoint != "" {
		// The reference is the one the engine wrote from an identifier.
		out.SessionEndpoint, _ = ident.ParseEndpointID(string(r.SessionEndpoint))
	}
	return out
}
