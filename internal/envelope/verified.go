package envelope

import (
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
)

// SignedReport is a status report that record.Verify accepted as signed by the
// network key of Node. It keeps the node it was verified for, so that applying
// it can check that the delivery it reports on is one the node was the target
// of. Only VerifyStatusReport returns a non-zero one.
type SignedReport struct {
	node ident.NodeID
	v    record.Verified[StatusReport]
}

// VerifyStatusReport verifies r as a status report signed by node's network
// key (see StatusReportDescriptor) about a message that local, the verifier's
// own node, sent. node is the node the verifier sent the message to, from its
// own records. A report naming another sending node is refused: it was issued
// for someone else's message.
func VerifyStatusReport(r record.Record, node, local ident.NodeID, accepted record.AcceptedNetworkKey, now time.Time) (SignedReport, error) {
	if node.IsZero() || local.IsZero() {
		return SignedReport{}, fmt.Errorf("%w: no node", ErrReport)
	}
	v, err := record.Verify(StatusReportDescriptor(node, accepted, now), r)
	if err != nil {
		return SignedReport{}, err
	}
	if got := v.Payload().Sender; got != local {
		return SignedReport{}, fmt.Errorf("%w: the report names another sending node", ErrMismatch)
	}
	return SignedReport{node: node, v: v}, nil
}

// Node returns the node whose network key signed the report.
func (s SignedReport) Node() ident.NodeID { return s.node }

// Report returns the verified content.
func (s SignedReport) Report() StatusReport { return s.v.Payload() }

// Digest returns the digest of the signed record.
func (s SignedReport) Digest() record.Digest { return s.v.Digest() }

func (s SignedReport) isZero() bool { return s.v.Digest().IsZero() }

// SignedControlReply is a control reply that record.Verify accepted as signed
// by the network key of Node. Only VerifyControlReply returns a non-zero one.
type SignedControlReply struct {
	node ident.NodeID
	v    record.Verified[ControlReply]
}

// VerifyControlReply verifies r as a control reply signed by node's network key
// (see ControlReplyDescriptor) to a signal that local, the verifier's own node,
// sent. node is the node the control signal was sent to, from the verifier's
// own records. A reply naming another sending node is refused.
func VerifyControlReply(r record.Record, node, local ident.NodeID, accepted record.AcceptedNetworkKey, now time.Time) (SignedControlReply, error) {
	if node.IsZero() || local.IsZero() {
		return SignedControlReply{}, fmt.Errorf("%w: no node", ErrControlReply)
	}
	v, err := record.Verify(ControlReplyDescriptor(node, accepted, now), r)
	if err != nil {
		return SignedControlReply{}, err
	}
	if got := v.Payload().Sender; got != local {
		return SignedControlReply{}, fmt.Errorf("%w: the reply names another sending node", ErrMismatch)
	}
	return SignedControlReply{node: node, v: v}, nil
}

// Node returns the node whose network key signed the reply.
func (s SignedControlReply) Node() ident.NodeID { return s.node }

// Reply returns the verified content.
func (s SignedControlReply) Reply() ControlReply { return s.v.Payload() }

// Digest returns the digest of the signed record.
func (s SignedControlReply) Digest() record.Digest { return s.v.Digest() }

func (s SignedControlReply) isZero() bool { return s.v.Digest().IsZero() }
