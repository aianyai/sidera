package adapterkit

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
)

// Conn is the adapter's side of the connection for what it sends: the requests
// and notifications of the adapter to the daemon. Both parameters and results go
// through the rules of the protocol: parameters are validated before they are
// sent, and a result is decoded strictly and validated before it is returned.
//
// An error from a request is the peer's: use peer.NoEffect and peer.ResultUnknown
// to tell whether the request did nothing. Conn is safe for concurrent use.
type Conn struct{ p *peer.Peer }

// Call sends a request the adapter may make and decodes its result into result,
// which is a pointer to the result type of the method. A method that is a
// notification, or that only the daemon may send, is refused without sending.
func (c *Conn) Call(ctx context.Context, method string, params, result adapterproto.Message) error {
	info, err := sendable(method, false)
	if err != nil {
		return err
	}
	if err := params.Validate(); err != nil {
		return fmt.Errorf("adapterkit: %s params: %w", info.Name, err)
	}
	var raw json.RawMessage
	if err := c.p.Call(ctx, method, params, &raw); err != nil {
		return err
	}
	if err := adapterproto.Decode(raw, result); err != nil {
		return fmt.Errorf("adapterkit: %s result: %w", info.Name, err)
	}
	return nil
}

// Notify sends a notification the adapter may make.
func (c *Conn) Notify(ctx context.Context, method string, params adapterproto.Message) error {
	info, err := sendable(method, true)
	if err != nil {
		return err
	}
	if err := params.Validate(); err != nil {
		return fmt.Errorf("adapterkit: %s params: %w", info.Name, err)
	}
	return c.p.Notify(ctx, method, params)
}

// sendable looks the method up and checks that an adapter may send it, as a
// notification or as a request.
func sendable(method string, notification bool) (adapterproto.MethodInfo, error) {
	info, ok := adapterproto.Lookup(method)
	switch {
	case !ok:
		return info, fmt.Errorf("adapterkit: %q is not a method of the protocol", method)
	case !info.Direction.CalledBy(false):
		return info, fmt.Errorf("adapterkit: %s is a method only the daemon sends", method)
	case info.Notification && !notification:
		return info, fmt.Errorf("adapterkit: %s is a notification, sent with Notify", method)
	case !info.Notification && notification:
		return info, fmt.Errorf("adapterkit: %s is a request, sent with Call", method)
	}
	return info, nil
}

// CheckpointPut stores state with the daemon.
func (c *Conn) CheckpointPut(ctx context.Context, p adapterproto.CheckpointPutParams) error {
	return c.Call(ctx, adapterproto.MethodCheckpointPut, p, &adapterproto.Empty{})
}

// RecvObserved reports what a recv of a session printed.
func (c *Conn) RecvObserved(ctx context.Context, p adapterproto.RecvObservedParams) (adapterproto.RecvObservedResult, error) {
	var r adapterproto.RecvObservedResult
	return r, c.Call(ctx, adapterproto.MethodRecvObserved, p, &r)
}

// Register registers a direct endpoint.
func (c *Conn) Register(ctx context.Context, p adapterproto.RegisterParams) (adapterproto.RegisterResult, error) {
	var r adapterproto.RegisterResult
	return r, c.Call(ctx, adapterproto.MethodEndpointRegister, p, &r)
}

// Unregister closes a direct endpoint.
func (c *Conn) Unregister(ctx context.Context, p adapterproto.UnregisterParams) error {
	return c.Call(ctx, adapterproto.MethodEndpointUnregister, p, &adapterproto.Empty{})
}

// Recv takes the messages that wait for a direct endpoint.
func (c *Conn) Recv(ctx context.Context, p adapterproto.RecvParams) (adapterproto.RecvResult, error) {
	var r adapterproto.RecvResult
	return r, c.Call(ctx, adapterproto.MethodEndpointRecv, p, &r)
}

// Ack acknowledges the messages of a lease.
func (c *Conn) Ack(ctx context.Context, p adapterproto.AckParams) (adapterproto.AckResult, error) {
	var r adapterproto.AckResult
	return r, c.Call(ctx, adapterproto.MethodEndpointAck, p, &r)
}

// Send sends a message from a direct endpoint.
func (c *Conn) Send(ctx context.Context, p adapterproto.SendParams) (adapterproto.SendResult, error) {
	var r adapterproto.SendResult
	return r, c.Call(ctx, adapterproto.MethodEndpointSend, p, &r)
}

// MessageStatus asks for the state of a message a direct endpoint sent.
func (c *Conn) MessageStatus(ctx context.Context, p adapterproto.MessageStatusParams) (adapterproto.MessageStatusResult, error) {
	var r adapterproto.MessageStatusResult
	return r, c.Call(ctx, adapterproto.MethodEndpointMessageStatus, p, &r)
}

// Availability sends the full snapshot of what keeps the adapter from working.
func (c *Conn) Availability(ctx context.Context, p adapterproto.AvailabilityParams) error {
	return c.Notify(ctx, adapterproto.MethodAdapterAvailability, p)
}

// PromptPersisted tells that an accepted prompt is durable in the native record.
func (c *Conn) PromptPersisted(ctx context.Context, p adapterproto.PromptPersistedParams) error {
	return c.Notify(ctx, adapterproto.MethodPromptPersisted, p)
}

// TurnStarted tells that a turn of a session began.
func (c *Conn) TurnStarted(ctx context.Context, p adapterproto.TurnStartedParams) error {
	return c.Notify(ctx, adapterproto.MethodTurnStarted, p)
}

// TurnEnded tells that a turn of a session ended.
func (c *Conn) TurnEnded(ctx context.Context, p adapterproto.TurnEndedParams) error {
	return c.Notify(ctx, adapterproto.MethodTurnEnded, p)
}

// NativeRequestHandled tells what the adapter did with a request the native runtime made.
func (c *Conn) NativeRequestHandled(ctx context.Context, p adapterproto.NativeRequestHandledParams) error {
	return c.Notify(ctx, adapterproto.MethodNativeRequestHandled, p)
}
