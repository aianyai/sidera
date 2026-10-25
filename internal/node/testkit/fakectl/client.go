package fakectl

import (
	"context"
	"encoding/json"
	"fmt"
	"net"

	"github.com/aianyai/sidera/sdk/go/peer"
)

// Client is a fake process's end of the control channel.
type Client struct {
	p *peer.Peer
}

// Dial connects to the fixture's server. Register handlers, then call Start.
func Dial(ctx context.Context, addr string) (*Client, error) {
	nc, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	p, err := peer.New(nc, nc, peer.Config{})
	if err != nil {
		_ = nc.Close()
		return nil, err
	}
	return &Client{p: p}, nil
}

// HandleClock registers what the process does when the fixture sets its clock.
func (c *Client) HandleClock(set func(ms int64)) {
	c.p.Handle(MethodClockSet, func(_ context.Context, params json.RawMessage) (any, error) {
		v, err := DecodeParams(MethodClockSet, params)
		if err != nil {
			return nil, peer.NewError(peer.CodeInvalidParams, err.Error())
		}
		set(v.(ClockSetParams).ToMS)
		return struct{}{}, nil
	})
}

// HandleStep registers what the process does when the fixture announces a step.
func (c *Client) HandleStep(set func(step string)) {
	c.p.Handle(MethodStepSet, func(_ context.Context, params json.RawMessage) (any, error) {
		v, err := DecodeParams(MethodStepSet, params)
		if err != nil {
			return nil, peer.NewError(peer.CodeInvalidParams, err.Error())
		}
		set(v.(StepSetParams).Step)
		return struct{}{}, nil
	})
}

// HandleKnob registers the process's knobs: one handler that is told the knob's
// name and its raw arguments (nil for none) and answers with an object, or an
// error. A *peer.Error is sent as it is.
func (c *Client) HandleKnob(h func(ctx context.Context, name string, args json.RawMessage) (any, error)) {
	c.p.Handle(MethodKnob, func(ctx context.Context, params json.RawMessage) (any, error) {
		v, err := DecodeParams(MethodKnob, params)
		if err != nil {
			return nil, peer.NewError(peer.CodeInvalidParams, err.Error())
		}
		k := v.(KnobParams)
		res, err := h(ctx, k.Name, k.Args)
		if err != nil {
			return nil, err
		}
		if res == nil {
			res = struct{}{}
		}
		return res, nil
	})
}

// Handle registers the answer to a request method of the caller's own.
func (c *Client) Handle(method string, h func(ctx context.Context, params json.RawMessage) (any, error)) {
	c.p.Handle(method, h)
}

// HandleNotification registers the handling of a notification method of the
// caller's own.
func (c *Client) HandleNotification(method string, h func(ctx context.Context, params json.RawMessage)) {
	c.p.HandleNotification(method, h)
}

// Start begins the conversation and says hello. It returns once the server has
// accepted the identity.
func (c *Client) Start(ctx context.Context, hello HelloParams) error {
	if err := hello.validate(); err != nil {
		_ = c.p.Close()
		return fmt.Errorf("fakectl: hello: %w", err)
	}
	c.p.Start()
	if err := c.p.Call(ctx, MethodHello, hello, nil); err != nil {
		_ = c.p.Close()
		return fmt.Errorf("fakectl: hello: %w", err)
	}
	return nil
}

// Report tells the fixture that something happened in this process.
func (c *Client) Report(ctx context.Context, name, detail string) error {
	p := ReportParams{Name: name, Detail: detail}
	if err := p.validate(); err != nil {
		return err
	}
	return c.p.Notify(ctx, MethodReport, p)
}

// Call sends a request of the caller's own method to the server.
func (c *Client) Call(ctx context.Context, method string, params, result any) error {
	return c.p.Call(ctx, method, params, result)
}

// Notify sends a notification of the caller's own method to the server.
func (c *Client) Notify(ctx context.Context, method string, params any) error {
	return c.p.Notify(ctx, method, params)
}

// Done is closed when the connection has ended. A fake process exits then.
func (c *Client) Done() <-chan struct{} { return c.p.Done() }

// Close ends the connection.
func (c *Client) Close() error { return c.p.Close() }
