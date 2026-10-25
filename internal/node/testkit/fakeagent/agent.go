package fakeagent

import (
	"context"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// Agent is what a test does as the agent of one endpoint.
type Agent interface {
	// Send sends a message.
	Send(ctx context.Context, in SendInput) (SendResult, error)
	// Reply answers a message the endpoint received.
	Reply(ctx context.Context, in ReplyInput) (SendResult, error)
	// Recv takes the next page of messages.
	Recv(ctx context.Context, in RecvInput) (RecvResult, error)
	// Ack acknowledges the parts of a page.
	Ack(ctx context.Context, in AckInput) (AckResult, error)
	// Control sends a control signal to an endpoint.
	Control(ctx context.Context, in ControlInput) (SendResult, error)
	// Declare changes the endpoint's own declaration.
	Declare(ctx context.Context, in DeclareInput) error
	// Status asks for the delivery state of a message the endpoint sent.
	Status(ctx context.Context, in StatusInput) (StatusResult, error)
}

// SendInput is a message to send. The channel takes one recipient.
type SendInput struct {
	To                 []string
	Body               string
	ContentType        string // text/plain when empty
	Thread             string
	DiscloseRecipients bool
	AcceptBeforeS      int64
	// IdemKey is the idempotency key: the same key and content again returns the
	// message that was sent the first time.
	IdemKey string
}

// ReplyInput answers a message. The recipient comes from the daemon's record of the
// message, never from the agent.
type ReplyInput struct {
	MessageID   adapterproto.MessageID
	Body        string
	ContentType string
	Thread      string
	IdemKey     string
}

// SendResult names the message that was created.
type SendResult struct {
	MessageID adapterproto.MessageID
	// To is the recipients as the channel resolved them.
	To []string
	// Replayed is true when an idempotency key found the message it names.
	Replayed bool
}

// RecvInput asks for a page.
type RecvInput struct {
	// Frontend decides the output budget and the longest wait.
	Frontend adapterproto.Frontend
	// WaitS is how long to wait for a message when none is queued.
	WaitS int
	// Msg and Part ask for one part of a message, as the page's next pointer names
	// it.
	Msg  adapterproto.MessageID
	Part int
	// AckLease acknowledges the whole of the previous page first.
	AckLease adapterproto.LeaseRef
}

// RecvResult is one page. Raw is the page exactly as the backend printed it, which
// is what the model sees and what the daemon compares before it acknowledges;
// Page is the same page decoded.
type RecvResult struct {
	Raw  []byte
	Page adapterproto.RecvPage
}

// AckInput acknowledges parts of a page; no items means every part the lease
// handed out.
type AckInput struct {
	Lease adapterproto.LeaseRef
	Items []adapterproto.PartRef
}

// AckResult is the outcome of each part.
type AckResult struct {
	Items []adapterproto.PartAck
}

// ControlInput sends a control signal. Preset and Body mean something for
// session.create only: the preset to start and the initial message.
type ControlInput struct {
	To      string
	Signal  adapterproto.ControlSignal
	Preset  string
	Body    string
	IdemKey string
}

// DeclareInput is a patch of the endpoint's declaration: a field left nil does not
// change; AcceptFrom, when set, replaces the whole list.
type DeclareInput struct {
	Paused               *bool
	RefuseMultiRecipient *bool
	RestrictSenders      *bool
	AcceptFrom           *[]string
	HiddenFromDirectory  *bool
}

// StatusInput names the message to ask about.
type StatusInput struct {
	MessageID adapterproto.MessageID
}

// StatusResult is the state of each recipient's delivery and, for a control signal,
// the reply once there is one.
type StatusResult = adapterproto.MessageStatusResult
