package agentapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/ipc"
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

// Client calls one node through ipc's peer checks. It never retries a request:
// sending a message twice without an explicit idempotency key makes two messages.
type Client struct {
	cfg       ClientConfig
	transport *http.Transport
	http      *http.Client
}

// NewClient resolves the home before deriving the local address. Credentials are
// checked by the server, so the same error names a malformed token in any client.
func NewClient(c ClientConfig) (*Client, error) {
	if c.Home == "" {
		return nil, errors.New("agentapi: no data directory")
	}
	home, err := filepath.Abs(c.Home)
	if err != nil {
		return nil, err
	}
	addr, err := ipc.Locate(filepath.Clean(home), ipc.KindAgent)
	if err != nil {
		return nil, err
	}
	t := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return ipc.Dial(ctx, addr, ipc.Options{}) },
		// No proxy reads process-wide environment and no redirect carries a token
		// to another destination. The HTTP host is only a local protocol label.
	}
	return &Client{cfg: c, transport: t, http: &http.Client{
		Transport:     t,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

// Close releases idle connections. In-flight calls belong to their contexts.
func (c *Client) Close() { c.transport.CloseIdleConnections() }

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
	if !validRequestText(reflect.ValueOf(in)) {
		return nil, apiwire.Malformed("request text is not valid UTF-8", "")
	}
	var body bytes.Buffer
	enc := json.NewEncoder(&body)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(in); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var wrote atomic.Bool
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		WroteRequest: func(httptrace.WroteRequestInfo) { wrote.Store(true) },
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://sidera"+pathPrefix+method, &body)
	if err != nil {
		return nil, err
	}
	token := c.cfg.Token
	if method == "probe" {
		token = c.cfg.ProbeToken
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+string(token))
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		if wrote.Load() {
			return nil, apiwire.OutcomeUnknown("")
		}
		return nil, apiwire.FromError(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, apiwire.OutcomeUnknown("")
	}
	if resp.StatusCode != http.StatusOK {
		var answer apiwire.Body
		if err := strictjson.Decode(raw, &answer); err != nil {
			return nil, apiwire.OutcomeUnknown("")
		}
		return nil, &answer.Error
	}
	return raw, nil
}

// The client's fixed request types contain only structs, slices, pointers and
// scalar fields, with no custom marshalers. Check their text before encoding/json
// can silently replace invalid UTF-8, including text used as an idempotency key.
func validRequestText(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.String:
		return utf8.ValidString(v.String())
	case reflect.Pointer:
		return v.IsNil() || validRequestText(v.Elem())
	case reflect.Struct:
		for i := range v.NumField() {
			if !validRequestText(v.Field(i)) {
				return false
			}
		}
	case reflect.Slice:
		for i := range v.Len() {
			if !validRequestText(v.Index(i)) {
				return false
			}
		}
	case reflect.Bool, reflect.Int, reflect.Int64:
	default:
		panic("agentapi: unsupported request field kind " + v.Kind().String())
	}
	return true
}
