package engine

import (
	"errors"
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/record"
)

// errNoKeys is what a method that signs or sends returns for an engine that was
// built without keys or without a transport: a defect of whoever composed the
// node, not a refusal of a request.
var (
	errNoKeys      = errors.New("engine: no keys to sign with")
	errNoTransport = errors.New("engine: no transport to send with")
)

// acceptedKey is the network key this node accepts for a node, with its
// revocation status and the highest serial seen, as record.NetworkKeyBelongs
// wants them. Only this node's own key is known: the engine has no trust graph
// yet, and a node it knows nothing about has no accepted key, so anything signed
// in its name fails to verify.
func (e *Engine) acceptedKey(node ident.NodeID) record.AcceptedNetworkKey {
	if e.d.Keys == nil || node != e.d.Node {
		return record.AcceptedNetworkKey{}
	}
	return e.d.Config.RevocationSet().NetworkKey(e.d.Keys.Certificate(), e.d.Keys.SerialSeen().Serial)
}

// SignRefusalOf is why a node whose identity is retired or whose wall clock is in
// doubt may not sign, and zero when it may. The identity is judged first: a
// retirement is for good, a pause ends when the owner clears it. A signature
// carries the time, and the reply window of the other side starts from it, so a
// clock that cannot be trusted signs nothing. It is the one judgement of signing:
// canSign, the capacity check and the periodic tasks (TaskInput.SignRefusal) read
// it, and so does the start of the daemon, before an engine exists.
func SignRefusalOf(retired, paused bool) Reason {
	switch {
	case retired:
		return ReasonIdentityRetired
	case paused:
		return ReasonClockRollback
	}
	return 0
}

// canSign says whether this node may sign a report now: it holds keys and
// SignRefusalOf finds nothing against it. signReport asks it, and so does an entry
// that would refuse before it has judged anything.
func (e *Engine) canSign() error {
	if e.d.Keys == nil {
		return errNoKeys
	}
	if r := e.signRefusal(); r != 0 {
		return refuse(r)
	}
	return nil
}

// signRefusal is SignRefusalOf for the state of the loop.
func (e *Engine) signRefusal() Reason { return SignRefusalOf(e.retired, e.paused) }

// signReport signs this node's report of a delivery it holds the target record
// of, with the node's network key, at now. The report is dated with the moment the
// record entered the state it reports (Record.StateAt), and a delivered one also
// carries the delivery time. It refuses while the node may not sign (see
// canSign).
func (e *Engine) signReport(now time.Time, in store.InRecord) (record.Record, error) {
	return e.signRecordReport(now, in.Key, in.Record)
}

// signControlReport is signReport for the record of a control signal.
func (e *Engine) signControlReport(now time.Time, c store.CtlRecord) (record.Record, error) {
	return e.signRecordReport(now, c.Key, c.Record)
}

// signRecordReport is the one place a report of a target delivery is made, whether
// the record is a message's or a control signal's.
func (e *Engine) signRecordReport(now time.Time, key store.TargetKey, rec delivery.Record) (record.Record, error) {
	if err := e.canSign(); err != nil {
		return record.Record{}, err
	}
	rep := envelope.StatusReport{
		Sender: key.SenderNode, Message: key.Message, Recipient: key.Recipient,
		Status: rec.StatusReport(), ReportedAt: rec.StateAt, DeliveredAt: rec.DeliveredAt(),
	}
	d := envelope.StatusReportDescriptor(e.d.Node, e.acceptedKey(e.d.Node), now)
	signed, err := record.Seal(d, rep, e.d.Keys.NetworkSigner())
	if err != nil {
		return record.Record{}, fmt.Errorf("engine: signing the report of %s: %w", key.Message, err)
	}
	return signed, nil
}

// controlReplyPayload is the payload of the control reply of a signal, which the
// target stores with the signal's deduplication record: the reply is made once, at
// the moment the result is committed, and signed again each time it is sent.
func (e *Engine) controlReplyPayload(now time.Time, key store.TargetKey, result envelope.ControlResult) ([]byte, error) {
	now = now.UTC().Truncate(time.Second) // what a reply can say
	reply := envelope.ControlReply{Sender: key.SenderNode, Message: key.Message, Recipient: key.Recipient, IssuedAt: now, Result: result}
	d := envelope.ControlReplyDescriptor(e.d.Node, e.acceptedKey(e.d.Node), now)
	payload, err := record.EncodePayload(d, reply)
	if err != nil {
		return nil, fmt.Errorf("engine: the control reply of %s: %w", key.Message, err)
	}
	return payload, nil
}

// signControlReply signs the stored control reply of a control record, with the
// node's current network key.
func (e *Engine) signControlReply(c store.CtlRecord) (record.Record, error) {
	if err := e.canSign(); err != nil {
		return record.Record{}, err
	}
	signed, err := record.Sign(envelope.ControlReplyType, c.Dedup.Reply, e.d.Keys.NetworkSigner())
	if err != nil {
		return record.Record{}, fmt.Errorf("engine: signing the control reply of %s: %w", c.Key.Message, err)
	}
	return signed, nil
}

// verifyReport verifies a report a node signed about a message this node sent to
// it.
func (e *Engine) verifyReport(now time.Time, rec record.Record, target ident.NodeID) (envelope.SignedReport, error) {
	return envelope.VerifyStatusReport(rec, target, e.d.Node, e.acceptedKey(target), now)
}

// verifyControlReply verifies a control reply a node signed about a signal this
// node sent to it.
func (e *Engine) verifyControlReply(now time.Time, rec record.Record, target ident.NodeID) (envelope.SignedControlReply, error) {
	return envelope.VerifyControlReply(rec, target, e.d.Node, e.acceptedKey(target), now)
}
