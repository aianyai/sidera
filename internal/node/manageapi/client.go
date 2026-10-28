package manageapi

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/ipc"
	"github.com/aianyai/sidera/internal/node/localhttp"
	"github.com/aianyai/sidera/sdk/go/strictjson"
)

// ClientConfig names the node whose management interface the owner calls.
type ClientConfig struct {
	Home string
}

// Request is a management envelope. The caller chooses an operation identifier
// once and retains it, the expected version and the params for any resend.
// A zero identifier, nil expected version or nil params omits that member.
// Params accepts the JSON values supported by localhttp.Request.Body.
type Request struct {
	OperationID     admin.OperationID
	ExpectedVersion *admin.Version
	Params          any
}

// Client calls the owner's peer-checked management interface. It never retries:
// only the caller knows which exact request to retain and resend.
type Client struct {
	transport *localhttp.Client
}

// NewClient binds the client to one node's management address.
func NewClient(c ClientConfig) (*Client, error) {
	transport, err := localhttp.NewClient(localhttp.ClientConfig{Home: c.Home, Kind: ipc.KindManage, PathPrefix: pathPrefix})
	if err != nil {
		return nil, err
	}
	return &Client{transport: transport}, nil
}

// Close releases idle connections. In-flight calls belong to their contexts.
func (c *Client) Close() { c.transport.Close() }

// Call posts one management envelope and returns the complete response unchanged,
// including operation_id and replayed. The server owns method and params rules.
// Calls last at most 60 seconds, or five minutes for adapters.install, and remain
// bounded by ctx. An unknown outcome retains the request's operation identifier.
func (c *Client) Call(ctx context.Context, method string, in Request) (json.RawMessage, error) {
	id := operationIDText(in.OperationID)
	envelope := struct {
		OperationID     string         `json:"operation_id,omitempty"`
		ExpectedVersion *admin.Version `json:"expected_version,omitempty"`
		Params          any            `json:"params,omitempty"`
	}{id, in.ExpectedVersion, in.Params}
	timeout := 60 * time.Second
	if method == "adapters.install" {
		timeout = 5 * time.Minute
	}
	raw, err := c.transport.Call(ctx, localhttp.Request{Method: method, Body: envelope, Timeout: timeout})
	if err != nil {
		return nil, withOperationID(err, id)
	}
	if err := decodeResponse(raw, in.OperationID); err != nil {
		return nil, err
	}
	return json.RawMessage(raw), nil
}

func operationIDText(id admin.OperationID) string {
	if id.IsZero() {
		return ""
	}
	return hex.EncodeToString(id[:])
}

// Validate the outer envelope only: method results remain the caller's JSON.
// Missing metadata cannot silently confirm a change or hide a replay.
func decodeResponse(raw []byte, id admin.OperationID) error {
	unknown := func() error { return apiwire.OutcomeUnknown(operationIDText(id)) }
	if id.IsZero() {
		var response struct {
			Result json.RawMessage `json:"result"`
		}
		if err := strictjson.Decode(raw, &response); err != nil {
			return unknown()
		}
	} else {
		var response struct {
			OperationID string          `json:"operation_id"`
			Replayed    bool            `json:"replayed"`
			Result      json.RawMessage `json:"result"`
		}
		if err := strictjson.Decode(raw, &response); err != nil || response.OperationID != operationIDText(id) {
			return unknown()
		}
	}
	return nil
}

func withOperationID(err error, id string) error {
	var wire *apiwire.Error
	if id == "" || !errors.As(err, &wire) || wire.Category != apiwire.CategoryOutcomeUnknown {
		return err
	}
	// A server-side unknown outcome can carry a more specific reason, such as
	// storage_fatal. Keep it while adding the identifier the caller can resend.
	out := *wire
	out.Details = maps.Clone(wire.Details)
	if out.Details == nil {
		out.Details = map[string]any{}
	}
	out.Details["operation_id"] = id
	return &out
}
