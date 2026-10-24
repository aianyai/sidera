package envelope

import (
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// ControlReplyType is the type string of a signed control reply.
const ControlReplyType = "sidera/control-reply/1"

// CreateResult is the result of control.session.create. Its codes are those of
// package adapterproto; the one difference is that the session endpoint is an
// identifier here, where the adapter protocol holds the daemon's own reference
// to it.
type CreateResult struct {
	Outcome adapterproto.ControlCreateOutcome
	// SessionEndpoint is the endpoint allocated for the session, on the node
	// that signs the reply. Present when the outcome is created or unconfirmed.
	SessionEndpoint ident.EndpointID
	// Reason is present when the outcome is unconfirmed or failed.
	Reason adapterproto.ControlCreateReason
}

func (r CreateResult) adapterForm() adapterproto.ControlCreateReply {
	out := adapterproto.ControlCreateReply{Outcome: r.Outcome, Reason: r.Reason}
	if !r.SessionEndpoint.IsZero() {
		out.SessionEndpoint = adapterproto.EndpointRef(r.SessionEndpoint.String())
	}
	return out
}

// ControlResult is the result a control reply carries: the result of exactly
// the signal that was sent, in the values of package adapterproto.
type ControlResult struct {
	Signal    adapterproto.ControlSignal
	Create    *CreateResult
	Close     *adapterproto.ControlCloseReply
	Interrupt *adapterproto.ControlInterruptReply
	Status    *adapterproto.ControlStatusReply
}

// ControlReply is a target node's signed statement of what came of a control
// signal. It is committed with the execution result and stored with the
// request's deduplication record, so a retry of the same request gets the same
// reply. Like a status report it is signed by the target node's network key and
// the node is not in the payload.
type ControlReply struct {
	// Message is the id of the control signal.
	Message delivery.MessageID
	// Recipient is the endpoint the signal was addressed to.
	Recipient ident.EndpointID
	// IssuedAt is when the target committed the reply.
	IssuedAt time.Time
	Result   ControlResult
}

type createWire struct {
	Outcome         uint64 `cbor:"1,keyasint"`
	SessionEndpoint []byte `cbor:"2,keyasint,omitempty"`
	Reason          uint64 `cbor:"3,keyasint,omitempty"`
}

type outcomeReasonWire struct {
	Outcome uint64 `cbor:"1,keyasint"`
	Reason  uint64 `cbor:"2,keyasint,omitempty"`
}

type presetWire struct {
	Name      string `cbor:"1,keyasint"`
	Available bool   `cbor:"2,keyasint"`
}

type statusWire struct {
	Activity   uint64       `cbor:"1,keyasint"`
	Queued     uint64       `cbor:"2,keyasint"`
	Conditions []uint64     `cbor:"3,keyasint,omitempty"`
	Presets    []presetWire `cbor:"4,keyasint,omitempty"`
	AsOf       int64        `cbor:"5,keyasint"`
}

// controlReplyWire carries the result of exactly one signal: the key that is
// present says which (5 create, 6 close, 7 interrupt, 8 status), so no signal
// field can disagree with it.
type controlReplyWire struct {
	Message   string             `cbor:"1,keyasint"`
	Recipient []byte             `cbor:"2,keyasint"`
	IssuedAt  int64              `cbor:"3,keyasint"`
	Create    *createWire        `cbor:"5,keyasint,omitempty"`
	Close     *outcomeReasonWire `cbor:"6,keyasint,omitempty"`
	Interrupt *outcomeReasonWire `cbor:"7,keyasint,omitempty"`
	Status    *statusWire        `cbor:"8,keyasint,omitempty"`
}

func controlReplyToWire(r ControlReply) (controlReplyWire, error) {
	issued, err := record.TimeToWire(r.IssuedAt)
	if err != nil {
		return controlReplyWire{}, fmt.Errorf("issued_at: %w", err)
	}
	w := controlReplyWire{Message: string(r.Message), Recipient: r.Recipient.Bytes(), IssuedAt: issued}
	set := 0
	if c := r.Result.Create; c != nil {
		set++
		w.Create = &createWire{Outcome: uint64(c.Outcome), Reason: uint64(c.Reason)}
		if !c.SessionEndpoint.IsZero() {
			w.Create.SessionEndpoint = c.SessionEndpoint.Bytes()
		}
	}
	if c := r.Result.Close; c != nil {
		set++
		w.Close = &outcomeReasonWire{Outcome: uint64(c.Outcome), Reason: uint64(c.Reason)}
	}
	if c := r.Result.Interrupt; c != nil {
		set++
		w.Interrupt = &outcomeReasonWire{Outcome: uint64(c.Outcome), Reason: uint64(c.Reason)}
	}
	if c := r.Result.Status; c != nil {
		set++
		w.Status = &statusWire{Activity: uint64(c.Activity), Queued: uint64(max(c.Queued, 0)), AsOf: c.AsOf}
		for _, cond := range c.Conditions {
			w.Status.Conditions = append(w.Status.Conditions, uint64(cond))
		}
		for _, p := range c.Presets {
			w.Status.Presets = append(w.Status.Presets, presetWire{Name: p.Name, Available: p.Available})
		}
	}
	if set != 1 {
		return controlReplyWire{}, fmt.Errorf("%w: exactly one result is set, got %d", ErrControlReply, set)
	}
	return w, nil
}

func smallEnum[E ~uint8](v uint64) (E, error) {
	if v > 255 {
		return 0, fmt.Errorf("value %d out of range", v)
	}
	return E(v), nil
}

// fromWire is the one place that refuses an invalid reply. The rules between a
// signal's fields (which outcome takes which reason, when the endpoint is
// present) are those of package adapterproto, applied by its Validate; this
// function adds only what the wire form needs: ranges, the endpoint identifier
// and the one-result rule.
func controlReplyFromWire(w controlReplyWire) (ControlReply, error) {
	if err := checkToken(w.Message); err != nil {
		return ControlReply{}, fmt.Errorf("%w: message: %v", ErrControlReply, err)
	}
	recipient, err := ident.EndpointIDFromBytes(w.Recipient)
	if err != nil {
		return ControlReply{}, fmt.Errorf("%w: recipient: %w", ErrControlReply, err)
	}
	issued, err := record.TimeFromWire(w.IssuedAt)
	if err != nil {
		return ControlReply{}, fmt.Errorf("%w: issued_at: %w", ErrControlReply, err)
	}
	r := ControlReply{Message: delivery.MessageID(w.Message), Recipient: recipient, IssuedAt: issued}
	n := 0
	for _, present := range []bool{w.Create != nil, w.Close != nil, w.Interrupt != nil, w.Status != nil} {
		if present {
			n++
		}
	}
	if n != 1 {
		return ControlReply{}, fmt.Errorf("%w: exactly one result is given, got %d", ErrControlReply, n)
	}
	switch {
	case w.Create != nil:
		r.Result, err = createFromWire(*w.Create)
	case w.Close != nil:
		r.Result, err = closeFromWire(*w.Close)
	case w.Interrupt != nil:
		r.Result, err = interruptFromWire(*w.Interrupt)
	default:
		r.Result, err = statusResultFromWire(*w.Status)
	}
	if err != nil {
		return ControlReply{}, fmt.Errorf("%w: %w", ErrControlReply, err)
	}
	return r, nil
}

func createFromWire(w createWire) (ControlResult, error) {
	outcome, err := smallEnum[adapterproto.ControlCreateOutcome](w.Outcome)
	if err != nil {
		return ControlResult{}, fmt.Errorf("outcome: %w", err)
	}
	reason, err := smallEnum[adapterproto.ControlCreateReason](w.Reason)
	if err != nil {
		return ControlResult{}, fmt.Errorf("reason: %w", err)
	}
	c := CreateResult{Outcome: outcome, Reason: reason}
	if len(w.SessionEndpoint) > 0 {
		if c.SessionEndpoint, err = ident.EndpointIDFromBytes(w.SessionEndpoint); err != nil {
			return ControlResult{}, fmt.Errorf("session_endpoint: %w", err)
		}
	}
	if err := c.adapterForm().Validate(); err != nil {
		return ControlResult{}, err
	}
	return ControlResult{Signal: adapterproto.SignalSessionCreate, Create: &c}, nil
}

func closeFromWire(w outcomeReasonWire) (ControlResult, error) {
	outcome, err := smallEnum[adapterproto.ControlCloseOutcome](w.Outcome)
	if err != nil {
		return ControlResult{}, fmt.Errorf("outcome: %w", err)
	}
	reason, err := smallEnum[adapterproto.ControlCloseReason](w.Reason)
	if err != nil {
		return ControlResult{}, fmt.Errorf("reason: %w", err)
	}
	c := adapterproto.ControlCloseReply{Outcome: outcome, Reason: reason}
	if err := c.Validate(); err != nil {
		return ControlResult{}, err
	}
	return ControlResult{Signal: adapterproto.SignalSessionClose, Close: &c}, nil
}

func interruptFromWire(w outcomeReasonWire) (ControlResult, error) {
	outcome, err := smallEnum[adapterproto.ControlInterruptOutcome](w.Outcome)
	if err != nil {
		return ControlResult{}, fmt.Errorf("outcome: %w", err)
	}
	reason, err := smallEnum[adapterproto.ControlInterruptReason](w.Reason)
	if err != nil {
		return ControlResult{}, fmt.Errorf("reason: %w", err)
	}
	c := adapterproto.ControlInterruptReply{Outcome: outcome, Reason: reason}
	if err := c.Validate(); err != nil {
		return ControlResult{}, err
	}
	return ControlResult{Signal: adapterproto.SignalSessionInterrupt, Interrupt: &c}, nil
}

func statusResultFromWire(w statusWire) (ControlResult, error) {
	activity, err := smallEnum[adapterproto.ControlActivity](w.Activity)
	if err != nil {
		return ControlResult{}, fmt.Errorf("activity: %w", err)
	}
	if w.Queued > 1<<31-1 {
		return ControlResult{}, fmt.Errorf("queued: %d out of range", w.Queued)
	}
	c := adapterproto.ControlStatusReply{Activity: activity, Queued: int(w.Queued), AsOf: w.AsOf}
	for i, v := range w.Conditions {
		cond, err := smallEnum[adapterproto.ControlCondition](v)
		if err != nil {
			return ControlResult{}, fmt.Errorf("conditions %d: %w", i, err)
		}
		c.Conditions = append(c.Conditions, cond)
	}
	for _, p := range w.Presets {
		c.Presets = append(c.Presets, adapterproto.PresetAvailability{Name: p.Name, Available: p.Available})
	}
	if err := c.Validate(); err != nil {
		return ControlResult{}, err
	}
	return ControlResult{Signal: adapterproto.SignalEndpointStatus, Status: &c}, nil
}

// ControlReplyDescriptor describes the control reply record for a verification
// at now of a reply from node. Like StatusReportDescriptor it is signed by the
// network key that record.NetworkKeyOf accepts for node, taken from the
// verifier's own record of the signal.
func ControlReplyDescriptor(node ident.NodeID, accepted record.AcceptedNetworkKey, now time.Time) record.Descriptor[ControlReply] {
	return record.NewDescriptor(ControlReplyType,
		controlReplyToWire,
		controlReplyFromWire,
		func(ControlReply) []record.SignerSlot {
			return []record.SignerSlot{record.NetworkKeyOf(node, accepted, now)}
		},
	)
}

// Binds reports whether the reply tells the sender of a session.create with a
// body where the initial message went: the outcome is created or unconfirmed,
// so a session endpoint exists. The endpoint is returned.
func (r ControlReply) Binds() (ident.EndpointID, bool) {
	c := r.Result.Create
	if c == nil {
		return ident.EndpointID{}, false
	}
	switch c.Outcome {
	case adapterproto.CreateReplyCreated, adapterproto.CreateReplyUnconfirmed:
		return c.SessionEndpoint, true
	}
	return ident.EndpointID{}, false
}
