package agentapi

import (
	"context"
	"time"

	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/manifest"
	"github.com/aianyai/sidera/sdk/go/strictjson"
)

func (c *Client) result(ctx context.Context, method string, in, out any) error {
	raw, err := c.call(ctx, method, in, 60*time.Second)
	if err != nil {
		return err
	}
	if err := strictjson.Decode(raw, out); err != nil {
		return apiwire.OutcomeUnknown("")
	}
	return nil
}

// Me reads this endpoint's identity, declaration and declared front-end limits.
func (c *Client) Me(ctx context.Context) (MeResult, error) {
	var out MeResult
	err := c.result(ctx, "me", struct{}{}, &out)
	return out, err
}

// Declare applies a patch to this endpoint's own reception declaration.
func (c *Client) Declare(ctx context.Context, in DeclarationPatch) (DeclareResult, error) {
	var out DeclareResult
	err := c.result(ctx, "declare", in, &out)
	return out, err
}

// Peers lists the nodes this endpoint may see.
func (c *Client) Peers(ctx context.Context) (PeersResult, error) {
	var out PeersResult
	err := c.result(ctx, "peers", struct{}{}, &out)
	return out, err
}

// Directory reads one live, authorized page of endpoints.
func (c *Client) Directory(ctx context.Context, in DirectoryRequest) (DirectoryResult, error) {
	var out DirectoryResult
	err := c.result(ctx, "directory", in, &out)
	return out, err
}

// Send accepts a message for delivery, without any automatic resend.
func (c *Client) Send(ctx context.Context, in SendRequest) (SendResult, error) {
	var out SendResult
	err := c.result(ctx, "send", in, &out)
	return out, err
}

// Recv returns the page exactly as the node wrote it, including its newline.
// Its deadline leaves thirty seconds beyond the protocol's longest wait.
func (c *Client) Recv(ctx context.Context, in RecvRequest) ([]byte, error) {
	var seconds int64
	if in.WaitS != nil {
		seconds = max(0, min(*in.WaitS, int64(manifest.MaxRecvWaitSeconds)))
	}
	raw, err := c.call(ctx, "recv", in, (time.Duration(seconds)+30)*time.Second)
	if err != nil {
		return nil, err
	}
	var page adapterproto.RecvPage
	if err := strictjson.Decode(raw, &page); err != nil {
		return nil, apiwire.OutcomeUnknown("")
	}
	return raw, nil
}

// Ack acknowledges this endpoint's page or the explicitly named parts.
func (c *Client) Ack(ctx context.Context, in AckRequest) (adapterproto.AckResult, error) {
	var out adapterproto.AckResult
	err := c.result(ctx, "ack", in, &out)
	return out, err
}

// Reply answers a message received by this endpoint, to its original sender.
func (c *Client) Reply(ctx context.Context, in ReplyRequest) (SendResult, error) {
	var out SendResult
	err := c.result(ctx, "reply", in, &out)
	return out, err
}

// Status reads the deliveries and eventual control reply of a sent message.
func (c *Client) Status(ctx context.Context, id adapterproto.MessageID) (adapterproto.MessageStatusResult, error) {
	var out adapterproto.MessageStatusResult
	err := c.result(ctx, "status", StatusRequest{MessageID: id}, &out)
	return out, err
}

// Control accepts one signal. Poll Status to learn its eventual control reply.
func (c *Client) Control(ctx context.Context, in ControlRequest) (ControlResult, error) {
	var out ControlResult
	err := c.result(ctx, "control", in, &out)
	return out, err
}
