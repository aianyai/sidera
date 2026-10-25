package fakeadapter

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/aianyai/sidera/internal/node/testkit/fakectl"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
	"github.com/aianyai/sidera/sdk/go/strictjson"
)

// The names of the knobs on the control channel. They are the methods of Knobs
// under another spelling: a fixture in another process turns the knobs of a fake
// adapter by sending these names with their arguments.
const (
	KnobOutcome      = "outcome"
	KnobClear        = "outcome.clear"
	KnobPauseAt      = "pause.at"
	KnobPauseWait    = "pause.wait"
	KnobPauseRelease = "pause.release"
	KnobHoldRelease  = "hold.release"
	KnobHang         = "hang"
	KnobDisconnect   = "disconnect"
	KnobObserved     = "observed"
	KnobAvailability = "availability"
	KnobPersist      = "persist"
	KnobNativeReq    = "native-request"
	KnobPing         = "ping"
	KnobStats        = "stats"

	KnobDirectRegister      = "direct.register"
	KnobDirectRecv          = "direct.recv"
	KnobDirectAck           = "direct.ack"
	KnobDirectSend          = "direct.send"
	KnobDirectMessageStatus = "direct.message-status"
	KnobDirectUnregister    = "direct.unregister"
)

// The requests an outcome knob governs.
const (
	RequestCreate    = forCreate
	RequestPrompt    = forPrompt
	RequestInterrupt = forInterrupt
	RequestClose     = forClose
	RequestReconcile = forReconcile
	RequestStart     = forStart
)

var kindNames = []OutcomeKind{OutcomeFail, OutcomeIndeterminate, OutcomeRefuse, OutcomeAccepted, OutcomeRequested, OutcomeNotRunning, OutcomeUnconfirmed, OutcomeUnreachable, OutcomeError, OutcomeHold}

func outcomeKindOf(name string) (OutcomeKind, bool) {
	for _, k := range kindNames {
		if outcomeName(k) == name {
			return k, true
		}
	}
	return 0, false
}

// OutcomeArgs are the arguments of the outcome knob.
type OutcomeArgs struct {
	// Request is one of the Request constants.
	Request string `json:"request"`
	// Mode is "set" (every later request) or "queue" (the next one only).
	Mode string `json:"mode"`
	// Kind is the name of an OutcomeKind: fail, indeterminate, refuse, accepted,
	// requested, not-running, unconfirmed, unreachable, error or hold.
	Kind        string `json:"kind"`
	Reason      string `json:"reason,omitempty"`
	Code        int    `json:"code,omitempty"`
	AfterEffect bool   `json:"after_effect,omitempty"`
}

func (a OutcomeArgs) validate() error {
	if _, ok := validKinds[a.Request]; !ok {
		return fmt.Errorf("request %q is not one of %v", a.Request, []string{forCreate, forPrompt, forInterrupt, forClose, forReconcile, forStart})
	}
	if a.Mode != "set" && a.Mode != "queue" {
		return fmt.Errorf("mode %q is neither set nor queue", a.Mode)
	}
	kind, ok := outcomeKindOf(a.Kind)
	if !ok {
		return fmt.Errorf("kind %q is not an outcome", a.Kind)
	}
	return a.Outcome(kind).check(a.Request)
}

// Outcome returns the outcome the arguments describe.
func (a OutcomeArgs) Outcome(kind OutcomeKind) Outcome {
	return Outcome{Kind: kind, Reason: a.Reason, Code: a.Code, AfterEffect: a.AfterEffect}
}

// PointsArgs are the arguments of the knob that arms pause points.
type PointsArgs struct {
	Points []string `json:"points"`
}

func (a PointsArgs) validate() error {
	if len(a.Points) == 0 {
		return fmt.Errorf("no pause point named")
	}
	for _, p := range a.Points {
		if !slices.Contains(pauseNames, p) {
			return fmt.Errorf("%q is not a pause point", p)
		}
	}
	return nil
}

// PointArgs name one pause point.
type PointArgs struct {
	Point string `json:"point"`
}

func (a PointArgs) validate() error {
	if !slices.Contains(pauseNames, a.Point) {
		return fmt.Errorf("%q is not a pause point", a.Point)
	}
	return nil
}

// ObservedArgs are the arguments of the observed knob.
type ObservedArgs struct {
	Mode string `json:"mode"`
}

func (a ObservedArgs) validate() error {
	switch ObservedMode(a.Mode) {
	case ObservedFaithful, ObservedTruncated, ObservedReplaced:
		return nil
	}
	return fmt.Errorf("%q is not an observed mode", a.Mode)
}

// AvailabilityArgs are the arguments of the availability knob; no conditions is
// the snapshot that says all is well.
type AvailabilityArgs struct {
	Conditions []adapterproto.Availability `json:"conditions,omitempty"`
}

func (a AvailabilityArgs) validate() error {
	return adapterproto.AvailabilityParams{Conditions: append([]adapterproto.Availability{}, a.Conditions...)}.Validate()
}

// PersistArgs are the arguments of the persist knob.
type PersistArgs struct {
	Endpoint adapterproto.EndpointRef `json:"endpoint"`
	Attempt  adapterproto.AttemptID   `json:"attempt"`
}

func (a PersistArgs) validate() error {
	if err := a.Endpoint.Validate(); err != nil {
		return err
	}
	return a.Attempt.Validate()
}

// NativeRequestArgs are the arguments of the native-request knob.
type NativeRequestArgs struct {
	Endpoint    adapterproto.EndpointRef `json:"endpoint"`
	Method      string                   `json:"method"`
	Disposition adapterproto.Disposition `json:"disposition"`
}

func (a NativeRequestArgs) validate() error {
	// The time is the adapter's to fill in when it sends the notification.
	return adapterproto.NativeRequestHandledParams{Endpoint: a.Endpoint, Method: a.Method, Disposition: a.Disposition, AtMS: 1}.Validate()
}

// StatsReply is what the stats knob answers.
type StatsReply struct {
	NotificationsSent int `json:"notifications_sent"`
	Outstanding       int `json:"outstanding"`
	// Held are the methods of the requests held on purpose (see Knobs.Held), and
	// HeldPoints the pause points some of them stopped at.
	Held       []string `json:"held,omitempty"`
	HeldPoints []string `json:"held_points,omitempty"`
}

type validated interface{ validate() error }

func decodeArgs[T validated](data []byte) (any, error) {
	var v T
	if err := strictjson.Decode(data, &v); err != nil {
		return nil, err
	}
	if err := v.validate(); err != nil {
		return nil, err
	}
	return v, nil
}

// DecodeKnobArgs strictly decodes the arguments of a knob that has any, and the
// answer of the stats knob (named "stats.reply"). The value is the type itself,
// not a pointer. The direct knobs take the protocol's own parameter types and are
// decoded by the protocol package.
func DecodeKnobArgs(name string, data []byte) (any, error) {
	switch name {
	case KnobOutcome:
		return decodeArgs[OutcomeArgs](data)
	case KnobPauseAt:
		return decodeArgs[PointsArgs](data)
	case KnobPauseWait, KnobPauseRelease:
		return decodeArgs[PointArgs](data)
	case KnobObserved:
		return decodeArgs[ObservedArgs](data)
	case KnobAvailability:
		return decodeArgs[AvailabilityArgs](data)
	case KnobPersist:
		return decodeArgs[PersistArgs](data)
	case KnobNativeReq:
		return decodeArgs[NativeRequestArgs](data)
	case KnobStats + ".reply":
		var v StatsReply
		if err := strictjson.Decode(data, &v); err != nil {
			return nil, err
		}
		return v, nil
	}
	return nil, fmt.Errorf("fakeadapter: knob %q takes no arguments or is unknown", name)
}

// Dispatch turns the knob a fixture named over the control channel and returns
// the object the knob answers with (nil for none). A refusal is a *peer.Error.
func (k *Knobs) Dispatch(ctx context.Context, name string, args json.RawMessage) (any, error) {
	invalid := func(err error) (any, error) { return nil, peer.NewError(peer.CodeInvalidParams, err.Error()) }
	failed := func(err error) (any, error) {
		if err == nil {
			return nil, nil
		}
		return nil, err
	}
	switch name {
	case KnobOutcome:
		v, err := DecodeKnobArgs(name, args)
		if err != nil {
			return invalid(err)
		}
		a := v.(OutcomeArgs)
		kind, _ := outcomeKindOf(a.Kind)
		if a.Mode == "set" {
			return failed(k.set(a.Request, a.Outcome(kind)))
		}
		return failed(k.queue(a.Request, a.Outcome(kind)))
	case KnobClear:
		k.ClearOutcomes()
		return nil, nil
	case KnobPauseAt:
		v, err := DecodeKnobArgs(name, args)
		if err != nil {
			return invalid(err)
		}
		return failed(k.PauseAt(v.(PointsArgs).Points...))
	case KnobPauseWait:
		v, err := DecodeKnobArgs(name, args)
		if err != nil {
			return invalid(err)
		}
		return failed(k.WaitPaused(ctx, v.(PointArgs).Point))
	case KnobPauseRelease:
		v, err := DecodeKnobArgs(name, args)
		if err != nil {
			return invalid(err)
		}
		k.Release(v.(PointArgs).Point)
		return nil, nil
	case KnobHoldRelease:
		k.ReleaseHeld()
		return nil, nil
	case KnobHang:
		k.Hang()
		return nil, nil
	case KnobDisconnect:
		return failed(k.Disconnect())
	case KnobObserved:
		v, err := DecodeKnobArgs(name, args)
		if err != nil {
			return invalid(err)
		}
		return failed(k.SetObserved(ObservedMode(v.(ObservedArgs).Mode)))
	case KnobAvailability:
		var conds []adapterproto.Availability
		if args != nil {
			v, err := DecodeKnobArgs(name, args)
			if err != nil {
				return invalid(err)
			}
			conds = v.(AvailabilityArgs).Conditions
		}
		return failed(k.Availability(ctx, conds))
	case KnobPersist:
		v, err := DecodeKnobArgs(name, args)
		if err != nil {
			return invalid(err)
		}
		a := v.(PersistArgs)
		return failed(k.Persist(ctx, a.Endpoint, a.Attempt))
	case KnobNativeReq:
		v, err := DecodeKnobArgs(name, args)
		if err != nil {
			return invalid(err)
		}
		a := v.(NativeRequestArgs)
		return failed(k.NativeRequest(ctx, a.Endpoint, a.Method, a.Disposition))
	case KnobPing:
		return failed(k.Ping(ctx))
	case KnobStats:
		return k.stats(), nil
	case KnobDirectRegister:
		var p adapterproto.RegisterParams
		if err := adapterproto.Decode(args, &p); err != nil {
			return invalid(err)
		}
		return answerOf(k.Register(ctx, p))
	case KnobDirectRecv:
		var p adapterproto.RecvParams
		if err := adapterproto.Decode(args, &p); err != nil {
			return invalid(err)
		}
		return answerOf(k.Recv(ctx, p))
	case KnobDirectAck:
		var p adapterproto.AckParams
		if err := adapterproto.Decode(args, &p); err != nil {
			return invalid(err)
		}
		return answerOf(k.Ack(ctx, p))
	case KnobDirectSend:
		var p adapterproto.SendParams
		if err := adapterproto.Decode(args, &p); err != nil {
			return invalid(err)
		}
		return answerOf(k.Send(ctx, p))
	case KnobDirectMessageStatus:
		var p adapterproto.MessageStatusParams
		if err := adapterproto.Decode(args, &p); err != nil {
			return invalid(err)
		}
		return answerOf(k.MessageStatus(ctx, p))
	case KnobDirectUnregister:
		var p adapterproto.UnregisterParams
		if err := adapterproto.Decode(args, &p); err != nil {
			return invalid(err)
		}
		return failed(k.Unregister(ctx, p))
	}
	return nil, peer.NewError(peer.CodeMethodNotFound, "no such knob")
}

func answerOf[T any](res T, err error) (any, error) {
	if err != nil {
		return nil, err
	}
	return res, nil
}

// RemoteKnobs turns the knobs of a fake adapter that runs as a process of its own,
// over the connection it made to the fixture's control server. It is a Controller,
// as the knobs of an adapter in process are through Knobs.Controller; Do reaches any
// knob by name.
type RemoteKnobs struct {
	Conn *fakectl.Conn
}

var _ Controller = RemoteKnobs{}

// Do turns the named knob; args is marshalled to a JSON object (nil for none) and
// the answer decoded into result (nil to discard it).
func (r RemoteKnobs) Do(ctx context.Context, name string, args, result any) error {
	return r.Conn.Knob(ctx, name, args, result)
}

func (r RemoteKnobs) outcome(ctx context.Context, mode, request string, o Outcome) error {
	return r.Do(ctx, KnobOutcome, OutcomeArgs{Request: request, Mode: mode, Kind: outcomeName(o.Kind), Reason: o.Reason, Code: o.Code, AfterEffect: o.AfterEffect}, nil)
}

// SetOutcome makes every later request of a kind come to the outcome; QueueOutcome
// only the next one. request is one of the Request constants.
func (r RemoteKnobs) SetOutcome(ctx context.Context, request string, o Outcome) error {
	return r.outcome(ctx, "set", request, o)
}

// QueueOutcome makes the next request of a kind come to the outcome.
func (r RemoteKnobs) QueueOutcome(ctx context.Context, request string, o Outcome) error {
	return r.outcome(ctx, "queue", request, o)
}

// ClearOutcomes drops every sticky and queued outcome.
func (r RemoteKnobs) ClearOutcomes(ctx context.Context) error { return r.Do(ctx, KnobClear, nil, nil) }

// PauseAt arms pause points.
func (r RemoteKnobs) PauseAt(ctx context.Context, points ...string) error {
	return r.Do(ctx, KnobPauseAt, PointsArgs{Points: points}, nil)
}

// WaitPaused returns when a request has stopped at the pause point.
func (r RemoteKnobs) WaitPaused(ctx context.Context, point string) error {
	return r.Do(ctx, KnobPauseWait, PointArgs{Point: point}, nil)
}

// Release lets the request that stopped at the pause point go on.
func (r RemoteKnobs) Release(ctx context.Context, point string) error {
	return r.Do(ctx, KnobPauseRelease, PointArgs{Point: point}, nil)
}

// ReleaseHeld lets every held request go on.
func (r RemoteKnobs) ReleaseHeld(ctx context.Context) error {
	return r.Do(ctx, KnobHoldRelease, nil, nil)
}

// Hang makes the adapter stop answering.
func (r RemoteKnobs) Hang(ctx context.Context) error { return r.Do(ctx, KnobHang, nil, nil) }

// Disconnect makes the adapter close the protocol stream, which ends its process.
func (r RemoteKnobs) Disconnect(ctx context.Context) error {
	return r.Do(ctx, KnobDisconnect, nil, nil)
}

// SetObserved sets what recv results are reported as.
func (r RemoteKnobs) SetObserved(ctx context.Context, m ObservedMode) error {
	return r.Do(ctx, KnobObserved, ObservedArgs{Mode: string(m)}, nil)
}

// Availability sends an availability snapshot.
func (r RemoteKnobs) Availability(ctx context.Context, conditions []adapterproto.Availability) error {
	var args any
	if len(conditions) > 0 {
		args = AvailabilityArgs{Conditions: conditions}
	}
	return r.Do(ctx, KnobAvailability, args, nil)
}

// Persist sends prompt.persisted for an attempt that was only accepted.
func (r RemoteKnobs) Persist(ctx context.Context, endpoint adapterproto.EndpointRef, attempt adapterproto.AttemptID) error {
	return r.Do(ctx, KnobPersist, PersistArgs{Endpoint: endpoint, Attempt: attempt}, nil)
}

// Stats reports the notifications sent, the requests in flight and what is held.
func (r RemoteKnobs) Stats(ctx context.Context) (StatsReply, error) {
	var s StatsReply
	err := r.Do(ctx, KnobStats, nil, &s)
	return s, err
}

// NativeRequest reports a request of the native runtime that the adapter answered
// itself.
func (r RemoteKnobs) NativeRequest(ctx context.Context, endpoint adapterproto.EndpointRef, method string, d adapterproto.Disposition) error {
	return r.Do(ctx, KnobNativeReq, NativeRequestArgs{Endpoint: endpoint, Method: method, Disposition: d}, nil)
}

// Ping pings the daemon.
func (r RemoteKnobs) Ping(ctx context.Context) error { return r.Do(ctx, KnobPing, nil, nil) }

// Register registers a direct endpoint under a key.
func (r RemoteKnobs) Register(ctx context.Context, p adapterproto.RegisterParams) (adapterproto.RegisterResult, error) {
	var res adapterproto.RegisterResult
	err := r.Do(ctx, KnobDirectRegister, p, &res)
	return res, err
}

// Recv reads the messages waiting for a direct endpoint.
func (r RemoteKnobs) Recv(ctx context.Context, p adapterproto.RecvParams) (adapterproto.RecvResult, error) {
	var res adapterproto.RecvResult
	err := r.Do(ctx, KnobDirectRecv, p, &res)
	return res, err
}

// Ack acknowledges parts of a page.
func (r RemoteKnobs) Ack(ctx context.Context, p adapterproto.AckParams) (adapterproto.AckResult, error) {
	var res adapterproto.AckResult
	err := r.Do(ctx, KnobDirectAck, p, &res)
	return res, err
}

// Send sends a message from a direct endpoint.
func (r RemoteKnobs) Send(ctx context.Context, p adapterproto.SendParams) (adapterproto.SendResult, error) {
	var res adapterproto.SendResult
	err := r.Do(ctx, KnobDirectSend, p, &res)
	return res, err
}

// MessageStatus asks for the delivery state of a message a direct endpoint sent.
func (r RemoteKnobs) MessageStatus(ctx context.Context, p adapterproto.MessageStatusParams) (adapterproto.MessageStatusResult, error) {
	var res adapterproto.MessageStatusResult
	err := r.Do(ctx, KnobDirectMessageStatus, p, &res)
	return res, err
}

// Unregister closes a direct endpoint.
func (r RemoteKnobs) Unregister(ctx context.Context, p adapterproto.UnregisterParams) error {
	return r.Do(ctx, KnobDirectUnregister, p, nil)
}
