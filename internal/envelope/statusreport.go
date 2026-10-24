package envelope

import (
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/timing"
)

// StatusReportType is the type string of a signed status report.
const StatusReportType = "sidera/status-report/1"

// StatusReport is a target node's signed statement about one delivery: where a
// message stands at one of its endpoints. It is the evidence that lets a reply
// come back (the message arrived, and when) and what the sender pulls to learn
// the state of a delivery. The signer is the target node's network key; the
// node is not part of the payload, the verifier knows it from its own record of
// the message (StatusReportDescriptor).
type StatusReport struct {
	// Sender is the node that sent the message, as the target saw it from the
	// connection. A message is identified by its sender, id and recipient, so a
	// report names all three; a node that receives a report for another sender
	// refuses it.
	Sender ident.NodeID
	// Message is the id of the message reported on.
	Message delivery.MessageID
	// Recipient is the endpoint the message was delivered to. For the initial
	// message of a created session it is the session endpoint, not the runtime
	// endpoint the control signal was addressed to.
	Recipient ident.EndpointID
	// Status is the state, with the reason of a rejection and the cause of a
	// block, as delivery reports it. The wire form writes the cause only while
	// the delivery is blocked.
	Status delivery.StatusReport
	// ReportedAt is the moment the target recorded the state the report states,
	// not the moment it signed the report. A target that sends the report again
	// for an unchanged state repeats the same ReportedAt under a fresh
	// signature, so the earliest report that proves arrival (the start of the
	// reply window) cannot be moved by resending.
	ReportedAt time.Time
	// DeliveredAt is when the target saw the delivery acknowledged. Present
	// exactly when the state is delivered.
	DeliveredAt time.Time
}

// ProvesArrival reports whether the report shows that the message reached the
// target node and was kept: accepted (queued or leased), delivered, or handed
// out with an unknown result. A rejection and a closed delivery do not.
func (r StatusReport) ProvesArrival() bool {
	switch r.Status.State {
	case delivery.StateAccepted, delivery.StateDelivered, delivery.StateDeliveryUnknown:
		return true
	}
	return false
}

// Wire values of the reportable states, reject reasons and block causes: tables
// independent of the Go constants of delivery, so that reordering those never
// changes what a signed report means.
var (
	stateWire = []delivery.State{
		1: delivery.StateAccepted,
		2: delivery.StateDelivered,
		3: delivery.StateRejected,
		4: delivery.StateDeliveryUnknown,
		5: delivery.StateClosedUnconfirmed,
	}
	rejectWire = []delivery.RejectReason{
		1:  delivery.RejectUnauthorized,
		2:  delivery.RejectEndpointMissing,
		3:  delivery.RejectEndpointClosed,
		4:  delivery.RejectEndpointDeclined,
		5:  delivery.RejectTooLarge,
		6:  delivery.RejectExpired,
		7:  delivery.RejectBacklogExpired,
		8:  delivery.RejectSecondCheckFailed,
		9:  delivery.RejectMalformed,
		10: delivery.RejectDeadlineOutOfRange,
	}
	causeWire = []delivery.Cause{
		1: delivery.CauseAdapterOffline,
		2: delivery.CauseRuntimeAuth,
		3: delivery.CauseNativeUnavailable,
	}
)

func toWireCode[T comparable](table []T, v T) (uint64, bool) {
	for i := 1; i < len(table); i++ {
		if table[i] == v {
			return uint64(i), true
		}
	}
	return 0, false
}

func fromWireCode[T any](table []T, c uint64) (T, bool) {
	if c < 1 || c >= uint64(len(table)) {
		var zero T
		return zero, false
	}
	return table[c], true
}

type statusReportWire struct {
	Message      string `cbor:"1,keyasint"`
	Recipient    []byte `cbor:"2,keyasint"`
	State        uint64 `cbor:"3,keyasint"`
	Reject       uint64 `cbor:"4,keyasint,omitempty"`
	BlockedCause uint64 `cbor:"5,keyasint,omitempty"`
	ReportedAt   int64  `cbor:"6,keyasint"`
	DeliveredAt  *int64 `cbor:"7,keyasint,omitempty"`
	Sender       []byte `cbor:"8,keyasint"`
}

func statusReportToWire(r StatusReport) (statusReportWire, error) {
	state, ok := toWireCode(stateWire, r.Status.State)
	if !ok {
		return statusReportWire{}, fmt.Errorf("%w: a target does not report state %s", ErrReport, r.Status.State)
	}
	w := statusReportWire{Message: string(r.Message), Recipient: r.Recipient.Bytes(), State: state, Sender: r.Sender.Bytes()}
	if r.Status.Reject != delivery.RejectNone {
		// An unknown reason has no wire value; a known one on a state other than
		// rejected is refused by the decoding path that encoding runs.
		w.Reject, _ = toWireCode(rejectWire, r.Status.Reject)
		if w.Reject == 0 {
			return statusReportWire{}, fmt.Errorf("%w: reject reason %s", ErrReport, r.Status.Reject)
		}
	}
	if r.Status.Blocked {
		w.BlockedCause, ok = toWireCode(causeWire, r.Status.BlockedCause)
		if !ok {
			return statusReportWire{}, fmt.Errorf("%w: block cause %s", ErrReport, r.Status.BlockedCause)
		}
	}
	var err error
	if w.ReportedAt, err = record.TimeToWire(r.ReportedAt); err != nil {
		return statusReportWire{}, fmt.Errorf("reported_at: %w", err)
	}
	if w.DeliveredAt, err = record.OptionalTimeToWire(r.DeliveredAt); err != nil {
		return statusReportWire{}, fmt.Errorf("delivered_at: %w", err)
	}
	return w, nil
}

func statusReportFromWire(w statusReportWire) (StatusReport, error) {
	if err := checkToken(w.Message); err != nil {
		return StatusReport{}, fmt.Errorf("%w: message: %v", ErrReport, err)
	}
	recipient, err := ident.EndpointIDFromBytes(w.Recipient)
	if err != nil {
		return StatusReport{}, fmt.Errorf("%w: recipient: %w", ErrReport, err)
	}
	sender, err := ident.NodeIDFromBytes(w.Sender)
	if err != nil {
		return StatusReport{}, fmt.Errorf("%w: sender: %w", ErrReport, err)
	}
	state, ok := fromWireCode(stateWire, w.State)
	if !ok {
		return StatusReport{}, fmt.Errorf("%w: state %d", ErrReport, w.State)
	}
	r := StatusReport{Sender: sender, Message: delivery.MessageID(w.Message), Recipient: recipient, Status: delivery.StatusReport{State: state}}
	if (w.Reject != 0) != (state == delivery.StateRejected) {
		return StatusReport{}, fmt.Errorf("%w: a reject reason is given exactly with the state rejected", ErrReport)
	}
	if w.Reject != 0 {
		if r.Status.Reject, ok = fromWireCode(rejectWire, w.Reject); !ok {
			return StatusReport{}, fmt.Errorf("%w: reject reason %d", ErrReport, w.Reject)
		}
	}
	if w.BlockedCause != 0 {
		if state != delivery.StateAccepted {
			return StatusReport{}, fmt.Errorf("%w: a block cause is given only with the state accepted", ErrReport)
		}
		r.Status.Blocked = true
		if r.Status.BlockedCause, ok = fromWireCode(causeWire, w.BlockedCause); !ok {
			return StatusReport{}, fmt.Errorf("%w: block cause %d", ErrReport, w.BlockedCause)
		}
	}
	if r.ReportedAt, err = record.TimeFromWire(w.ReportedAt); err != nil {
		return StatusReport{}, fmt.Errorf("%w: reported_at: %w", ErrReport, err)
	}
	if r.DeliveredAt, err = record.OptionalTimeFromWire(w.DeliveredAt); err != nil {
		return StatusReport{}, fmt.Errorf("%w: delivered_at: %w", ErrReport, err)
	}
	if (state == delivery.StateDelivered) != !r.DeliveredAt.IsZero() {
		return StatusReport{}, fmt.Errorf("%w: a delivery time is given exactly with the state delivered", ErrReport)
	}
	if r.DeliveredAt.After(r.ReportedAt) {
		return StatusReport{}, fmt.Errorf("%w: delivered after the report was issued", ErrReport)
	}
	return r, nil
}

// StatusReportDescriptor describes the status report record for a verification
// at now of a report from node. It is signed by one key: the network key that
// record.NetworkKeyOf accepts for node, which the verifier takes from its own
// record of the message (the node the message was sent to), never from the
// report. A report signed by any other key, including another node's, is
// refused, with the binding reason in the error.
func StatusReportDescriptor(node ident.NodeID, accepted record.AcceptedNetworkKey, now time.Time) record.Descriptor[StatusReport] {
	return record.NewDescriptor(StatusReportType,
		statusReportToWire,
		statusReportFromWire,
		func(StatusReport) []record.SignerSlot {
			return []record.SignerSlot{record.NetworkKeyOf(node, accepted, now)}
		},
	)
}

// checkNotFuture refuses a time later than now plus the clock tolerance: a
// target whose clock is ahead by no more than the tolerance is believed.
func checkNotFuture(now, t time.Time) error {
	if t.After(now.Add(timing.ClockTolerance)) {
		return fmt.Errorf("%w: %s, now %s", ErrFromFuture, t.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339))
	}
	return nil
}
