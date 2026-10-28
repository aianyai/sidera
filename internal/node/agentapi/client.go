package agentapi

import (
	"context"
	"time"

	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/ipc"
	"github.com/aianyai/sidera/internal/node/localhttp"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
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
	var out ProbeResult
	raw, err := c.call(ctx, "probe", struct {
		Frontend adapterproto.Frontend `json:"frontend"`
	}{frontend}, 60*time.Second)
	if err != nil {
		return out, err
	}
	if err := strictjson.Decode(raw, &out); err != nil {
		return out, apiwire.OutcomeUnknown("")
	}
	return out, nil
}

func (c *Client) call(ctx context.Context, method string, in any, timeout time.Duration) ([]byte, error) {
	token := c.cfg.Token
	if method == "probe" {
		token = c.cfg.ProbeToken
	}
	return c.transport.Call(ctx, localhttp.Request{Method: method, Body: in, BearerToken: string(token), Timeout: timeout})
}
