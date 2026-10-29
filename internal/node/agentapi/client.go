package agentapi

import (
	"context"
	"encoding/json"
	"time"

	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/ipc"
	"github.com/aianyai/sidera/internal/node/localhttp"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/manifest"
	"github.com/aianyai/sidera/sdk/go/strictjson"
)

// ClientConfig names the node and the two distinct credentials an Agent tool may
// hold. A probe uses ProbeToken; every other method uses Token.
type ClientConfig struct {
	Home       string
	Token      adapterproto.Token
	ProbeToken adapterproto.Token
}

// Client calls one node through the shared, peer-checked HTTP transport. It
// never retries a request: a resend without an idempotency key is a new message.
type Client struct {
	cfg       ClientConfig
	transport *localhttp.Client
}

// NewClient binds the two separate Agent credentials to one node. The server
// checks credentials, giving malformed tokens the same error for every client.
func NewClient(c ClientConfig) (*Client, error) {
	transport, err := localhttp.NewClient(localhttp.ClientConfig{Home: c.Home, Kind: ipc.KindAgent, PathPrefix: pathPrefix})
	if err != nil {
		return nil, err
	}
	return &Client{cfg: c, transport: transport}, nil
}

// Close releases idle connections. In-flight calls belong to their contexts.
func (c *Client) Close() { c.transport.Close() }

// ProbeResult is the front end that successfully presented its one-use token.
type ProbeResult struct {
	OK       bool                  `json:"ok"`
	Preset   string                `json:"preset"`
	Frontend adapterproto.Frontend `json:"frontend"`
}

// Probe presents the probe credential, including while the node recovers.
func (c *Client) Probe(ctx context.Context, frontend adapterproto.Frontend) (ProbeResult, error) {
	return clientResult[ProbeResult](c, ctx, "probe", struct {
		Frontend adapterproto.Frontend `json:"frontend"`
	}{frontend})
}

// ackWireRequest preserves an explicit empty list, which the server must reject
// rather than interpreting as a whole-page acknowledgement.
type ackWireRequest struct {
	Lease adapterproto.LeaseRef  `json:"lease"`
	Items []adapterproto.PartRef `json:"items,omitzero"`
}

// Call preserves the supplied JSON members, including explicit empty values.
// Input supports json.RawMessage or ordinary structs, string-key maps, slices,
// pointers, interfaces and JSON scalars; other custom marshalers are unsupported.
// The method determines credentials, deadline and concrete result type, as listed
// in the package README. A recv result is the original validated page []byte.
func (c *Client) Call(ctx context.Context, method string, in any) (any, error) {
	return c.call(ctx, method, in)
}

func (c *Client) call(ctx context.Context, method string, in any) (any, error) {
	token := c.cfg.Token
	timeout := 60 * time.Second
	var decode func([]byte) (any, error)
	switch method {
	case "probe":
		token = c.cfg.ProbeToken
		decode = clientDecode[ProbeResult]
	case "me":
		decode = clientDecode[MeResult]
	case "declare":
		decode = clientDecode[DeclareResult]
	case "peers":
		decode = clientDecode[PeersResult]
	case "directory":
		decode = clientDecode[DirectoryResult]
	case "send", "reply":
		decode = clientDecode[SendResult]
	case "recv":
		decode = clientDecodePage
	case "ack":
		decode = clientDecode[AckResult]
		switch request := in.(type) {
		case AckRequest:
			in = ackWireRequest(request)
		case *AckRequest:
			in = (*ackWireRequest)(request)
		}
	case "status":
		decode = clientDecode[adapterproto.MessageStatusResult]
	case "control":
		decode = clientDecode[ControlResult]
	default:
		return nil, &apiwire.Error{Category: apiwire.CategoryNotFound, Code: "method_unknown", Message: "the Agent method is not known"}
	}
	body, err := localhttp.EncodeRequest(in)
	if err != nil {
		return nil, err
	}
	if method == "recv" {
		// Read only the deadline hint from the safely encoded original object.
		// The unchanged document still reaches the server for all domain rules.
		var members map[string]json.RawMessage
		var seconds int64
		if json.Unmarshal(body, &members) == nil {
			if err := json.Unmarshal(members["wait_s"], &seconds); err != nil {
				seconds = 0
			}
		}
		seconds = max(0, min(seconds, int64(manifest.MaxRecvWaitSeconds)))
		timeout = (time.Duration(seconds) + 30) * time.Second
	}
	raw, err := c.transport.Call(ctx, localhttp.Request{Method: method, Body: body, BearerToken: string(token), Timeout: timeout})
	if err != nil {
		return nil, err
	}
	return decode(raw)
}

func clientDecode[T any](raw []byte) (any, error) {
	var out T
	if err := strictjson.Decode(raw, &out); err != nil {
		return nil, apiwire.OutcomeUnknown("")
	}
	return out, nil
}

func clientDecodePage(raw []byte) (any, error) {
	if _, err := clientDecode[adapterproto.RecvPage](raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func clientResult[T any](c *Client, ctx context.Context, method string, in any) (T, error) {
	out, err := c.call(ctx, method, in)
	if err != nil {
		var zero T
		return zero, err
	}
	return out.(T), nil
}
