package fakeadapter

import (
	"context"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// Controller is what a test does with the knobs of a fake adapter, whichever layer
// the adapter runs in: Knobs.Controller for an adapter in the test process,
// RemoteKnobs for one that runs as a program of its own. A script written against
// it drives the knobs of both, as one written against fakeruntime.Controller
// drives the turns of both.
type Controller interface {
	// SetOutcome makes every later request of a kind (a Request constant) come to
	// the outcome; QueueOutcome only the next one, ahead of the setting.
	SetOutcome(ctx context.Context, request string, o Outcome) error
	QueueOutcome(ctx context.Context, request string, o Outcome) error
	// ClearOutcomes drops every setting and every queued outcome.
	ClearOutcomes(ctx context.Context) error

	// PauseAt arms pause points, WaitPaused returns when a request stopped at one,
	// Release lets it go; ReleaseHeld lets every request held by an outcome go.
	PauseAt(ctx context.Context, points ...string) error
	WaitPaused(ctx context.Context, point string) error
	Release(ctx context.Context, point string) error
	ReleaseHeld(ctx context.Context) error

	// Hang stops every answer; Disconnect closes the protocol stream.
	Hang(ctx context.Context) error
	Disconnect(ctx context.Context) error

	// SetObserved sets what recv results are reported as.
	SetObserved(ctx context.Context, m ObservedMode) error

	// Availability, Persist, NativeRequest and Ping send what the name says.
	Availability(ctx context.Context, conditions []adapterproto.Availability) error
	Persist(ctx context.Context, endpoint adapterproto.EndpointRef, attempt adapterproto.AttemptID) error
	NativeRequest(ctx context.Context, endpoint adapterproto.EndpointRef, method string, d adapterproto.Disposition) error
	Ping(ctx context.Context) error

	// Stats is what a fixture needs to know whether the system is quiet: the
	// notifications sent, the requests of the adapter in flight and the requests
	// held on purpose, by method.
	Stats(ctx context.Context) (StatsReply, error)

	// The methods of a chat bridge on a direct endpoint.
	Register(ctx context.Context, p adapterproto.RegisterParams) (adapterproto.RegisterResult, error)
	Recv(ctx context.Context, p adapterproto.RecvParams) (adapterproto.RecvResult, error)
	Ack(ctx context.Context, p adapterproto.AckParams) (adapterproto.AckResult, error)
	Send(ctx context.Context, p adapterproto.SendParams) (adapterproto.SendResult, error)
	MessageStatus(ctx context.Context, p adapterproto.MessageStatusParams) (adapterproto.MessageStatusResult, error)
	Unregister(ctx context.Context, p adapterproto.UnregisterParams) error
}

// Controller returns the knobs as a Controller: a thin wrapper that gives the
// methods of Knobs the context that the knobs of a remote adapter need.
func (k *Knobs) Controller() Controller { return localController{k} }

type localController struct{ k *Knobs }

func (c localController) SetOutcome(ctx context.Context, request string, o Outcome) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.k.set(request, o)
}

func (c localController) QueueOutcome(ctx context.Context, request string, o Outcome) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.k.queue(request, o)
}

func (c localController) ClearOutcomes(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.k.ClearOutcomes()
	return nil
}

func (c localController) PauseAt(ctx context.Context, points ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.k.PauseAt(points...)
}

func (c localController) WaitPaused(ctx context.Context, point string) error {
	return c.k.WaitPaused(ctx, point)
}

func (c localController) Release(ctx context.Context, point string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.k.Release(point)
	return nil
}

func (c localController) ReleaseHeld(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.k.ReleaseHeld()
	return nil
}

func (c localController) Hang(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.k.Hang()
	return nil
}

func (c localController) Disconnect(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.k.Disconnect()
}

func (c localController) SetObserved(ctx context.Context, m ObservedMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.k.SetObserved(m)
}

func (c localController) Availability(ctx context.Context, conditions []adapterproto.Availability) error {
	return c.k.Availability(ctx, conditions)
}

func (c localController) Persist(ctx context.Context, endpoint adapterproto.EndpointRef, attempt adapterproto.AttemptID) error {
	return c.k.Persist(ctx, endpoint, attempt)
}

func (c localController) NativeRequest(ctx context.Context, endpoint adapterproto.EndpointRef, method string, d adapterproto.Disposition) error {
	return c.k.NativeRequest(ctx, endpoint, method, d)
}

func (c localController) Ping(ctx context.Context) error { return c.k.Ping(ctx) }

func (c localController) Stats(ctx context.Context) (StatsReply, error) {
	if err := ctx.Err(); err != nil {
		return StatsReply{}, err
	}
	return c.k.stats(), nil
}

func (c localController) Register(ctx context.Context, p adapterproto.RegisterParams) (adapterproto.RegisterResult, error) {
	return c.k.Register(ctx, p)
}

func (c localController) Recv(ctx context.Context, p adapterproto.RecvParams) (adapterproto.RecvResult, error) {
	return c.k.Recv(ctx, p)
}

func (c localController) Ack(ctx context.Context, p adapterproto.AckParams) (adapterproto.AckResult, error) {
	return c.k.Ack(ctx, p)
}

func (c localController) Send(ctx context.Context, p adapterproto.SendParams) (adapterproto.SendResult, error) {
	return c.k.Send(ctx, p)
}

func (c localController) MessageStatus(ctx context.Context, p adapterproto.MessageStatusParams) (adapterproto.MessageStatusResult, error) {
	return c.k.MessageStatus(ctx, p)
}

func (c localController) Unregister(ctx context.Context, p adapterproto.UnregisterParams) error {
	return c.k.Unregister(ctx, p)
}
