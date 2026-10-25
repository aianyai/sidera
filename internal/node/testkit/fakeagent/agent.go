package fakeagent

import (
	"context"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// Agent is what a test does as the agent of one endpoint: the eleven methods of
// the agent interface, each with the input and the result its definition gives.
type Agent interface {
	// Me asks for the endpoint's own record and the limits of its front ends.
	Me(ctx context.Context) (MeResult, error)
	// Peers lists the nodes the endpoint can see.
	Peers(ctx context.Context) (PeersResult, error)
	// Directory lists the endpoints the endpoint can see.
	Directory(ctx context.Context, in DirectoryInput) (DirectoryResult, error)
	// Probe checks that the tools reach the daemon. It is the one method that
	// authenticates with the probe token and not the endpoint's own.
	Probe(ctx context.Context, in ProbeInput) (ProbeResult, error)

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

// MeResult is the endpoint's own record. Frontends lists only the front ends of the
// adapter the endpoint belongs to.
type MeResult struct {
	Address     string
	Node        string
	Kind        string
	DisplayName string
	Declaration Declaration
	Frontends   map[adapterproto.Frontend]FrontendLimits
}

// Declaration is what an endpoint says about itself, as Declare patches it.
type Declaration struct {
	Paused               bool
	RefuseMultiRecipient bool
	RestrictSenders      bool
	AcceptFrom           []string
	HiddenFromDirectory  bool
}

// FrontendLimits are what a front end allows: the longest a recv may wait, and the
// output budget of a page.
type FrontendLimits struct {
	RecvWaitMaxS int
	Budget       Budget
}

// Budget is a size limit and its unit (chars or tokens).
type Budget struct {
	Unit  string
	Limit int
}

// PeersResult lists the nodes the endpoint can see.
type PeersResult struct {
	Nodes []PeerNode
}

// PeerNode is a node; Self marks the endpoint's own.
type PeerNode struct {
	Node string
	Self bool
}

// DirectoryInput asks for a page of the directory. A zero Limit takes the default.
type DirectoryInput struct {
	// Node narrows the listing to one node.
	Node string
	// Cursor continues an earlier listing; it is opaque.
	Cursor string
	Limit  int
}

// DirectoryResult is a page of endpoints and where the next one starts, empty at the
// end.
type DirectoryResult struct {
	Endpoints  []DirectoryEndpoint
	NextCursor string
}

// DirectoryEndpoint is an endpoint the directory shows.
type DirectoryEndpoint struct {
	Address     string
	Kind        string
	DisplayName string
	Description string
	// Accepts are the control signals and messages the endpoint's declaration accepts.
	Accepts []string
}

// ProbeInput names the front end whose tools are probed; it must be the one the
// probe token is bound to.
type ProbeInput struct {
	Frontend adapterproto.Frontend
}

// ProbeResult says which preset and front end the daemon now knows are connected.
type ProbeResult struct {
	OK       bool
	Preset   string
	Frontend adapterproto.Frontend
}
