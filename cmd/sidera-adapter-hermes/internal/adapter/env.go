package adapter

import (
	"context"
	"log/slog"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/config"
	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/kanban"
	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/lane"
	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/state"
	"github.com/aianyai/sidera/sdk/go/adapterkit"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// Env is what the flows work with, available once the adapter has started.
type Env struct {
	Config config.Config
	Board  *kanban.Client
	State  *state.Store
	Log    *slog.Logger
	Now    func() time.Time

	conn      *adapterkit.Conn
	endpoints *endpoints
}

// Place returns the endpoint a card of the source sends from; see endpoints.Place.
func (e *Env) Place(ctx context.Context, src lane.Source, target config.Target) (Placement, error) {
	return e.endpoints.Place(ctx, src, target)
}

// Outgoing is a message to send. It has no delivery deadline on purpose: see Send.
type Outgoing struct {
	// From is the endpoint that sends.
	From adapterproto.EndpointRef
	To   adapterproto.Address
	Body string
	// ReplyTo is the message this one answers, received by From.
	ReplyTo adapterproto.MessageID
	// Control, when set, makes the message a control signal such as session.create;
	// Body is then the first message of the session, or empty.
	Control *adapterproto.Control
	// IdempotencyKey makes a repeated send the same message. The node keeps a key
	// only for adapterproto.SentKeyWindow.
	IdempotencyKey string
}

// Send sends a message from a direct endpoint.
//
// Send never sets accept_before_s, and Outgoing has no field for it. The window
// during which a resend with the same idempotency key is recognised,
// adapterproto.SentKeyWindow, is computed from the node's default delivery
// deadline; a send that names another deadline has another key lifetime, and the
// recovery rule that refuses to resend after the window would then be wrong. A flow
// that needs a deadline must derive the window from it first.
func (e *Env) Send(ctx context.Context, o Outgoing) (adapterproto.MessageID, error) {
	p := adapterproto.SendParams{
		Endpoint:       o.From,
		To:             []adapterproto.Address{o.To},
		Body:           o.Body,
		ReplyTo:        o.ReplyTo,
		Control:        o.Control,
		IdempotencyKey: o.IdempotencyKey,
	}
	if o.Body != "" {
		p.ContentType = "text/plain"
	}
	res, err := e.conn.Send(ctx, p)
	return res.MessageID, err
}
