package mcp

import (
	"context"
	"fmt"

	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

var _ fakeagent.Agent = (*Client)(nil)

func (c *Client) Send(ctx context.Context, in fakeagent.SendInput) (fakeagent.SendResult, error) {
	args := map[string]any{"to": in.To, "body": in.Body}
	optional(args, "content_type", in.ContentType, in.ContentType != "")
	optional(args, "thread", in.Thread, in.Thread != "")
	optional(args, "disclose_recipients", in.DiscloseRecipients, in.DiscloseRecipients)
	optional(args, "accept_before_s", in.AcceptBeforeS, in.AcceptBeforeS != 0)
	optional(args, "idempotency_key", in.IdemKey, in.IdemKey != "")
	return c.sent(ctx, "send", args)
}
func (c *Client) Reply(ctx context.Context, in fakeagent.ReplyInput) (fakeagent.SendResult, error) {
	args := map[string]any{"message_id": in.MessageID, "body": in.Body}
	optional(args, "content_type", in.ContentType, in.ContentType != "")
	optional(args, "thread", in.Thread, in.Thread != "")
	optional(args, "idempotency_key", in.IdemKey, in.IdemKey != "")
	return c.sent(ctx, "reply", args)
}
func (c *Client) Control(ctx context.Context, in fakeagent.ControlInput) (fakeagent.SendResult, error) {
	args := map[string]any{"to": in.To, "signal": in.Signal}
	optional(args, "preset", in.Preset, in.Preset != "")
	optional(args, "body", in.Body, in.Body != "")
	optional(args, "idempotency_key", in.IdemKey, in.IdemKey != "")
	var wire struct {
		MessageID adapterproto.MessageID `json:"message_id"`
		To        string                 `json:"to"`
		Replayed  bool                   `json:"replayed"`
	}
	_, err := c.call(ctx, "control", args, &wire)
	return fakeagent.SendResult{MessageID: wire.MessageID, To: []string{wire.To}, Replayed: wire.Replayed}, err
}
func (c *Client) sent(ctx context.Context, name string, args map[string]any) (fakeagent.SendResult, error) {
	var wire struct {
		MessageID adapterproto.MessageID `json:"message_id"`
		To        []string               `json:"to"`
		Replayed  bool                   `json:"replayed"`
	}
	_, err := c.call(ctx, name, args, &wire)
	return fakeagent.SendResult{MessageID: wire.MessageID, To: wire.To, Replayed: wire.Replayed}, err
}
func (c *Client) Recv(ctx context.Context, in fakeagent.RecvInput) (fakeagent.RecvResult, error) {
	if in.Frontend != "" && in.Frontend != adapterproto.FrontendMCP {
		return fakeagent.RecvResult{}, fmt.Errorf("mcp test client: recv supports only the mcp front end")
	}
	args := map[string]any{}
	if in.Msg != "" || in.Part != 0 {
		args["msg"], args["part"] = in.Msg, in.Part
	}
	if in.WaitS != 0 || (in.Msg == "" && in.Part == 0) {
		args["wait_s"] = in.WaitS
	}
	optional(args, "ack_lease", in.AckLease, in.AckLease != "")
	var page adapterproto.RecvPage
	raw, err := c.call(ctx, "recv", args, &page)
	return fakeagent.RecvResult{Raw: raw, Page: page}, err
}
func (c *Client) Ack(ctx context.Context, in fakeagent.AckInput) (fakeagent.AckResult, error) {
	args := map[string]any{"lease": in.Lease}
	optional(args, "items", in.Items, in.Items != nil)
	var wire struct {
		Items []adapterproto.PartAck `json:"items"`
	}
	_, err := c.call(ctx, "ack", args, &wire)
	return fakeagent.AckResult{Items: wire.Items}, err
}
func (c *Client) Status(ctx context.Context, in fakeagent.StatusInput) (fakeagent.StatusResult, error) {
	var out fakeagent.StatusResult
	_, err := c.call(ctx, "status", map[string]any{"message_id": in.MessageID}, &out)
	return out, err
}
func (c *Client) Declare(ctx context.Context, in fakeagent.DeclareInput) error {
	args := map[string]any{}
	optional(args, "paused", in.Paused, in.Paused != nil)
	optional(args, "refuse_multi_recipient", in.RefuseMultiRecipient, in.RefuseMultiRecipient != nil)
	optional(args, "restrict_senders", in.RestrictSenders, in.RestrictSenders != nil)
	optional(args, "hidden_from_directory", in.HiddenFromDirectory, in.HiddenFromDirectory != nil)
	optional(args, "accept_from", in.AcceptFrom, in.AcceptFrom != nil)
	_, err := c.call(ctx, "declare", args, nil)
	return err
}
func (c *Client) Probe(ctx context.Context, in fakeagent.ProbeInput) (fakeagent.ProbeResult, error) {
	if in.Frontend != "" && in.Frontend != adapterproto.FrontendMCP {
		return fakeagent.ProbeResult{}, fmt.Errorf("mcp test client: probe supports only the mcp front end")
	}
	var wire struct {
		OK       bool                  `json:"ok"`
		Preset   string                `json:"preset"`
		Frontend adapterproto.Frontend `json:"frontend"`
	}
	_, err := c.call(ctx, "probe", map[string]any{}, &wire)
	return fakeagent.ProbeResult{OK: wire.OK, Preset: wire.Preset, Frontend: wire.Frontend}, err
}
func (c *Client) Peers(ctx context.Context) (fakeagent.PeersResult, error) {
	var out fakeagent.PeersResult
	_, err := c.call(ctx, "peers", map[string]any{}, &out)
	return out, err
}
func (c *Client) Directory(ctx context.Context, in fakeagent.DirectoryInput) (fakeagent.DirectoryResult, error) {
	args := map[string]any{}
	optional(args, "node", in.Node, in.Node != "")
	optional(args, "cursor", in.Cursor, in.Cursor != "")
	optional(args, "limit", in.Limit, in.Limit != 0)
	var wire struct {
		Endpoints []struct {
			Address     string   `json:"address"`
			Kind        string   `json:"kind"`
			DisplayName string   `json:"display_name"`
			Description string   `json:"description"`
			Accepts     []string `json:"accepts"`
		} `json:"endpoints"`
		NextCursor string `json:"next_cursor"`
	}
	_, err := c.call(ctx, "directory", args, &wire)
	out := fakeagent.DirectoryResult{NextCursor: wire.NextCursor}
	for _, ep := range wire.Endpoints {
		out.Endpoints = append(out.Endpoints, fakeagent.DirectoryEndpoint{Address: ep.Address, Kind: ep.Kind, DisplayName: ep.DisplayName, Description: ep.Description, Accepts: ep.Accepts})
	}
	return out, err
}
func (c *Client) Me(ctx context.Context) (fakeagent.MeResult, error) {
	var wire struct {
		Address     string `json:"address"`
		Node        string `json:"node"`
		Kind        string `json:"kind"`
		DisplayName string `json:"display_name"`
		Declaration struct {
			Paused               bool     `json:"paused"`
			RefuseMultiRecipient bool     `json:"refuse_multi_recipient"`
			RestrictSenders      bool     `json:"restrict_senders"`
			AcceptFrom           []string `json:"accept_from"`
			HiddenFromDirectory  bool     `json:"hidden_from_directory"`
		} `json:"declaration"`
		Frontends map[adapterproto.Frontend]struct {
			RecvWaitMaxS int              `json:"recv_wait_max_s"`
			Budget       fakeagent.Budget `json:"budget"`
		} `json:"frontends"`
	}
	_, err := c.call(ctx, "me", map[string]any{}, &wire)
	out := fakeagent.MeResult{Address: wire.Address, Node: wire.Node, Kind: wire.Kind, DisplayName: wire.DisplayName, Declaration: fakeagent.Declaration{Paused: wire.Declaration.Paused, RefuseMultiRecipient: wire.Declaration.RefuseMultiRecipient, RestrictSenders: wire.Declaration.RestrictSenders, AcceptFrom: wire.Declaration.AcceptFrom, HiddenFromDirectory: wire.Declaration.HiddenFromDirectory}, Frontends: make(map[adapterproto.Frontend]fakeagent.FrontendLimits)}
	for f, limits := range wire.Frontends {
		out.Frontends[f] = fakeagent.FrontendLimits{RecvWaitMaxS: limits.RecvWaitMaxS, Budget: limits.Budget}
	}
	return out, err
}
