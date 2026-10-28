package agentapi

import (
	"context"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// Me reads this endpoint's identity, declaration and declared front-end limits.
func (c *Client) Me(ctx context.Context) (MeResult, error) {
	return clientResult[MeResult](c, ctx, "me", struct{}{})
}

// Declare applies a patch to this endpoint's own reception declaration.
func (c *Client) Declare(ctx context.Context, in DeclarationPatch) (DeclareResult, error) {
	return clientResult[DeclareResult](c, ctx, "declare", in)
}

// Peers lists the nodes this endpoint may see.
func (c *Client) Peers(ctx context.Context) (PeersResult, error) {
	return clientResult[PeersResult](c, ctx, "peers", struct{}{})
}

// Directory reads one live, authorized page of endpoints.
func (c *Client) Directory(ctx context.Context, in DirectoryRequest) (DirectoryResult, error) {
	return clientResult[DirectoryResult](c, ctx, "directory", in)
}

// Send accepts a message for delivery, without any automatic resend.
func (c *Client) Send(ctx context.Context, in SendRequest) (SendResult, error) {
	return clientResult[SendResult](c, ctx, "send", in)
}

// Recv returns the page exactly as the node wrote it, including its newline.
// Its deadline leaves thirty seconds beyond the protocol's longest wait.
func (c *Client) Recv(ctx context.Context, in RecvRequest) ([]byte, error) {
	return clientResult[[]byte](c, ctx, "recv", in)
}

// Ack acknowledges this endpoint's page or the explicitly named parts.
func (c *Client) Ack(ctx context.Context, in AckRequest) (AckResult, error) {
	// Nil means the whole page. A non-nil empty list must reach the server as
	// an explicit empty list, where it is rejected without confirming anything.
	return clientResult[AckResult](c, ctx, "ack", struct {
		Lease adapterproto.LeaseRef  `json:"lease"`
		Items []adapterproto.PartRef `json:"items,omitzero"`
	}{in.Lease, in.Items})
}

// Reply answers a message received by this endpoint, to its original sender.
func (c *Client) Reply(ctx context.Context, in ReplyRequest) (SendResult, error) {
	return clientResult[SendResult](c, ctx, "reply", in)
}

// Status reads the deliveries and eventual control reply of a sent message.
func (c *Client) Status(ctx context.Context, id adapterproto.MessageID) (adapterproto.MessageStatusResult, error) {
	return clientResult[adapterproto.MessageStatusResult](c, ctx, "status", StatusRequest{MessageID: id})
}

// Control accepts one signal. Poll Status to learn its eventual control reply.
func (c *Client) Control(ctx context.Context, in ControlRequest) (ControlResult, error) {
	return clientResult[ControlResult](c, ctx, "control", in)
}
