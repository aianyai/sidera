package localhttp

import (
	"bytes"
	"context"
	"encoding"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"reflect"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/ipc"
	"github.com/aianyai/sidera/sdk/go/strictjson"
)

// ClientConfig binds a client to one node, local interface and method prefix.
type ClientConfig struct {
	Home       string
	Kind       ipc.Kind
	PathPrefix string
}

// Request supplies a method's body, credential and deadline. The facade owns
// method selection, credential selection and the timeout policy.
// Body contains structs, string-key maps, pointers, interfaces, slices, JSON
// scalars or json.RawMessage. Other custom marshalers are unsupported.
type Request struct {
	Method      string
	Body        any
	BearerToken string
	Timeout     time.Duration
}

// Client calls only the peer checked by ipc. It never retries a request.
type Client struct {
	pathPrefix string
	transport  *http.Transport
	http       *http.Client
}

// NewClient resolves the home before deriving the checked local address.
func NewClient(c ClientConfig) (*Client, error) {
	if !strings.HasPrefix(c.PathPrefix, "/") || !strings.HasSuffix(c.PathPrefix, "/") {
		return nil, errors.New("localhttp: method path prefix must begin and end with a slash")
	}
	addr, err := ipc.Locate(c.Home, c.Kind)
	if err != nil {
		return nil, err
	}
	t := &http.Transport{
		// Proxy is nil: no process-wide proxy can receive a local credential.
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return ipc.Dial(ctx, addr, ipc.Options{}) },
	}
	return &Client{pathPrefix: c.PathPrefix, transport: t, http: &http.Client{
		Transport:     t,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

// Close releases idle connections. In-flight calls belong to their contexts.
func (c *Client) Close() { c.transport.CloseIdleConnections() }

// Call posts one JSON request and reads its complete answer without re-encoding
// it. A missing or incomplete answer after writing has an unknown outcome.
func (c *Client) Call(ctx context.Context, in Request) ([]byte, error) {
	body, err := EncodeRequest(in.Body)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, in.Timeout)
	defer cancel()
	var wrote atomic.Bool
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		WroteRequest: func(httptrace.WroteRequestInfo) { wrote.Store(true) },
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://sidera"+c.pathPrefix+in.Method, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if in.BearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+in.BearerToken)
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

// EncodeRequest checks request text before encoding can replace invalid bytes.
// It returns JSON with HTML escaping disabled and a final newline. Facades may
// inspect that representation for method policy, then pass it to Client.Call as
// json.RawMessage. Input follows the same supported-container contract as Body.
func EncodeRequest(in any) (json.RawMessage, error) {
	if !validRequestText(reflect.ValueOf(in)) {
		return nil, apiwire.Malformed("request JSON contains invalid text or a malformed document", "")
	}
	var body bytes.Buffer
	enc := json.NewEncoder(&body)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(in); err != nil {
		return nil, apiwire.Malformed("request cannot be encoded as JSON", "")
	}
	return body.Bytes(), nil
}

// Check text before encoding/json can silently replace invalid UTF-8, including
// map keys and idempotency keys. RawMessage needs document validation before its
// custom encoder can normalize it. Domain rules remain the facade's concern.
func validRequestText(v reflect.Value) bool {
	if !v.IsValid() {
		return true
	}
	if v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return true
		}
		// Pointer encoders are checked below before following ordinary pointers.
		if v.Elem().Type() == reflect.TypeFor[json.RawMessage]() {
			return validRequestText(v.Elem())
		}
	}
	if v.Type() == reflect.TypeFor[json.RawMessage]() {
		if v.IsNil() {
			return true
		}
		var raw json.RawMessage
		return strictjson.Decode(v.Bytes(), &raw) == nil
	}
	// A custom encoder could emit unchecked text. Fail loudly for these unsupported
	// programmer-supplied types instead of offering a partial marshaler framework.
	typ := v.Type()
	for _, custom := range []reflect.Type{reflect.TypeFor[json.Marshaler](), reflect.TypeFor[encoding.TextMarshaler]()} {
		if typ.Implements(custom) || (v.Kind() != reflect.Pointer && reflect.PointerTo(typ).Implements(custom)) {
			panic("localhttp: custom request marshalers are unsupported")
		}
	}
	switch v.Kind() {
	case reflect.String:
		return utf8.ValidString(v.String())
	case reflect.Pointer, reflect.Interface:
		return v.IsNil() || validRequestText(v.Elem())
	case reflect.Struct:
		for i := range v.NumField() {
			field := v.Type().Field(i)
			if field.Tag.Get("json") == "-" {
				continue
			}
			if !field.IsExported() {
				// encoding/json can expose exported members of an unexported
				// embedded struct, including one reached through a pointer.
				typ := field.Type
				if typ.Kind() == reflect.Pointer {
					typ = typ.Elem()
				}
				if !field.Anonymous || typ.Kind() != reflect.Struct {
					continue
				}
			}
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
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String {
			panic("localhttp: request maps require string keys")
		}
		entries := v.MapRange()
		for entries.Next() {
			if !validRequestText(entries.Key()) || !validRequestText(entries.Value()) {
				return false
			}
		}
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr, reflect.Float32, reflect.Float64:
	default:
		panic("localhttp: unsupported request field kind " + v.Kind().String())
	}
	return true
}
