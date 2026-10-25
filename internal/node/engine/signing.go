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

// intakeRejects are the reasons a target record is rejected with at the moment
// it is taken in, so that the time it was accepted is the time of the rejection.
// A rejection with any other reason comes later (the endpoint was closed, the
// backlog deadline passed), and the record does not say when.
var intakeRejects = map[delivery.RejectReason]bool{
	delivery.RejectUnauthorized: true, delivery.RejectEndpointMissing: true, delivery.RejectEndpointDeclined: true,
	delivery.RejectTooLarge: true, delivery.RejectExpired: true, delivery.RejectMalformed: true,
	delivery.RejectDeadlineOutOfRange: true, delivery.RejectBacklogFull: true,
}

// reportedAt is the moment a target record entered the state it reports, when the
// record says. A report is dated with the moment the target recorded the state,
// and a target record keeps one time only: when it was accepted. That dates the
// state accepted and a rejection made at intake; every other state (delivered,
// delivery_unknown, a later rejection) is undated, and no report is made of it.
func reportedAt(in store.InRecord) (time.Time, bool) {
	switch in.Record.State {
	case delivery.StateAccepted:
		return in.AcceptedAt, true
	case delivery.StateRejected:
		return in.AcceptedAt, intakeRejects[in.Record.Reject]
	}
	return time.Time{}, false
}

// signReport signs this node's report of a delivery it holds the target record
// of, with the node's network key, at now. It returns false when the record's
// state cannot be dated (see reportedAt): there is nothing the target can say
// yet, which is what an empty answer to a pull means.
func (e *Engine) signReport(now time.Time, in store.InRecord) (record.Record, bool, error) {
	if e.d.Keys == nil {
		return record.Record{}, false, errNoKeys
	}
	at, ok := reportedAt(in)
	if !ok {
		return record.Record{}, false, nil
	}
	rep := envelope.StatusReport{
		Sender: in.Key.SenderNode, Message: in.Key.Message, Recipient: in.Key.Recipient,
		Status: in.Record.StatusReport(), ReportedAt: at,
	}
	d := envelope.StatusReportDescriptor(e.d.Node, e.acceptedKey(e.d.Node), now)
	rec, err := record.Seal(d, rep, e.d.Keys.NetworkSigner())
	if err != nil {
		return record.Record{}, false, fmt.Errorf("engine: signing the report of %s: %w", in.Key.Message, err)
	}
	return rec, true, nil
}

// verifyReport verifies a report a node signed about a message this node sent to
// it.
func (e *Engine) verifyReport(now time.Time, rec record.Record, target ident.NodeID) (envelope.SignedReport, error) {
	return envelope.VerifyStatusReport(rec, target, e.d.Node, e.acceptedKey(target), now)
}
