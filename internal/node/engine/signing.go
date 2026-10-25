package engine

import (
	"errors"
	"fmt"
	"time"

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

// canSign says whether this node may sign a report now: it holds keys, and the
// wall clock can be trusted, because a signature carries the time and the reply
// window of the other side starts from it. It is the one rule of signing:
// signReport asks it, and so does an entry that would refuse before it has judged
// anything.
func (e *Engine) canSign() error {
	if e.d.Keys == nil {
		return errNoKeys
	}
	if e.paused {
		return refuse(ReasonClockRollback)
	}
	return nil
}

// signReport signs this node's report of a delivery it holds the target record
// of, with the node's network key, at now. The report is dated with the moment the
// record entered the state it reports (Record.StateAt), and a delivered one also
// carries the delivery time. It refuses while the node may not sign (see
// canSign).
func (e *Engine) signReport(now time.Time, in store.InRecord) (record.Record, error) {
	if err := e.canSign(); err != nil {
		return record.Record{}, err
	}
	rep := envelope.StatusReport{
		Sender: in.Key.SenderNode, Message: in.Key.Message, Recipient: in.Key.Recipient,
		Status: in.Record.StatusReport(), ReportedAt: in.Record.StateAt, DeliveredAt: in.Record.DeliveredAt(),
	}
	d := envelope.StatusReportDescriptor(e.d.Node, e.acceptedKey(e.d.Node), now)
	rec, err := record.Seal(d, rep, e.d.Keys.NetworkSigner())
	if err != nil {
		return record.Record{}, fmt.Errorf("engine: signing the report of %s: %w", in.Key.Message, err)
	}
	return rec, nil
}

// verifyReport verifies a report a node signed about a message this node sent to
// it.
func (e *Engine) verifyReport(now time.Time, rec record.Record, target ident.NodeID) (envelope.SignedReport, error) {
	return envelope.VerifyStatusReport(rec, target, e.d.Node, e.acceptedKey(target), now)
}
