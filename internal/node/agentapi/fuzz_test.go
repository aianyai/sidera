package agentapi_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/aianyai/sidera/internal/node/agentapi"
	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// FuzzClientRawCall keeps arbitrary JSON at the public presence-preserving
// boundary. Unsafe text must never reach even an absent daemon.
func FuzzClientRawCall(f *testing.F) {
	for _, seed := range []string{`{}`, `{"items":[]}`, `{"body":""}`, `{"wait_s":0}`, `{"wait_s":9223372036854775808}`, `{"body":"\ud800"}`, `{"body":"a","body":"b"}`, "{\"body\":\"bad\xfftext\"}", `{} {}`} {
		f.Add(uint8(0), seed)
	}
	c, err := agentapi.NewClient(agentapi.ClientConfig{Home: socketHome(f)})
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(c.Close)
	methods := []string{"me", "declare", "peers", "directory", "send", "reply", "recv", "ack", "status", "control", "probe"}
	f.Fuzz(func(t *testing.T, selector uint8, body string) {
		_, err := c.Call(context.Background(), methods[int(selector)%len(methods)], json.RawMessage(body))
		e := apiwire.FromError(err)
		if e == nil || !((e.Category == apiwire.CategoryInvalidArgument && e.Code == "malformed_request") || (e.Category == apiwire.CategoryUnavailable && e.Code == "daemon_not_running")) {
			t.Fatalf("raw call: %v; want malformed_request or daemon_not_running", err)
		}
		if !utf8.ValidString(body) && e.Code != "malformed_request" {
			t.Fatalf("invalid UTF-8 reached transport: %v", err)
		}
	})
}

// FuzzClientRequestText checks the request containers through public methods.
// Invalid text must fail locally, even when no daemon is listening.
func FuzzClientRequestText(f *testing.F) {
	for _, text := range []string{"", "message <&> \ufffd", "bad\xfftext"} {
		f.Add(text)
	}
	c, err := agentapi.NewClient(agentapi.ClientConfig{Home: socketHome(f)})
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(c.Close)
	f.Fuzz(func(t *testing.T, text string) {
		ctx := context.Background()
		from := []string{text}
		calls := []struct {
			name string
			call func() error
		}{
			{"named string", func() error { _, err := c.Probe(ctx, adapterproto.Frontend(text)); return err }},
			{"string slice", func() error { _, err := c.Send(ctx, agentapi.SendRequest{To: from}); return err }},
			{"pointer to slice", func() error { _, err := c.Declare(ctx, agentapi.DeclarationPatch{AcceptFrom: &from}); return err }},
			{"nested slice", func() error {
				_, err := c.Ack(ctx, agentapi.AckRequest{Items: []adapterproto.PartRef{{MessageID: adapterproto.MessageID(text)}}})
				return err
			}},
		}
		category, code := apiwire.CategoryUnavailable, "daemon_not_running"
		if !utf8.ValidString(text) {
			category, code = apiwire.CategoryInvalidArgument, "malformed_request"
		}
		for _, tc := range calls {
			err := tc.call()
			if e := apiwire.FromError(err); e == nil || e.Category != category || e.Code != code {
				t.Fatalf("%s: %v; want %s/%s", tc.name, err, category, code)
			}
		}
	})
}

// FuzzProbeRequests reads arbitrary request bytes at the HTTP boundary. The token
// is well formed but was never issued: no input may authenticate it or panic.
func FuzzProbeRequests(f *testing.F) {
	for _, seed := range []string{`{"frontend":"cli"}`, `{"frontend":"mcp"}`, `{}`, `{"frontend":null}`, `{"frontend":"cli","frontend":"mcp"}`, `{"frontend":"cli","extra":1}`} {
		f.Add(seed)
	}
	h := harness.New(f, harness.Options{})
	s, err := agentapi.New(agentapi.Config{Service: h.Engine()})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, body string) {
		r := httptest.NewRequest("POST", "/v1/agent/probe", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+strings.Repeat("A", 43))
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		var out apiwire.Body
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("non-JSON error: %v", err)
		}
		if (w.Code != 400 && w.Code != 401) || out.Error.Code == "" || w.Code != out.Error.Category.HTTPStatus() {
			t.Fatalf("an unissued token got HTTP %d: %s", w.Code, w.Body)
		}
	})
}

// FuzzAgentRequests covers the common HTTP dispatcher and every endpoint-method
// decoder. A well-formed unissued credential must never authorize an operation.
func FuzzAgentRequests(f *testing.F) {
	methods := []string{"me", "declare", "peers", "directory", "send", "reply", "recv", "ack", "status", "control"}
	seeds := []string{`{}`, `{"paused":true}`, `{}`, `{"limit":1}`, `{"to":["address"],"body":"message"}`, `{"message_id":"message","body":"answer"}`, `{"frontend":"cli","wait_s":0}`, `{"lease":"11111111111111111111111111111111"}`, `{"message_id":"message"}`, `{"to":"address","signal":"session.create","preset":"default"}`}
	for i, body := range seeds {
		f.Add(uint8(i), body)
	}
	for _, body := range []string{`null`, `{"frontend":null}`, `{"frontend":"cli","wait_s":86401}`, `{"frontend":"cli","part":-1}`, `{"paused":true,"paused":false}`, `{"items":[]}`, `{} {}`} {
		f.Add(uint8(6), body)
	}
	h := harness.New(f, harness.Options{})
	s, err := agentapi.New(agentapi.Config{Service: h.Engine()})
	if err != nil {
		f.Fatal(err)
	}
	s.EnableMethods()
	f.Fuzz(func(t *testing.T, selector uint8, body string) {
		w := request(s, adapterproto.Token(strings.Repeat("A", 43)), methods[int(selector)%len(methods)], body)
		var out apiwire.Body
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("non-JSON error: %v", err)
		}
		if (w.Code != 400 && w.Code != 401) || out.Error.Code == "" || w.Code != out.Error.Category.HTTPStatus() {
			t.Fatalf("an unissued token got HTTP %d: %s", w.Code, w.Body)
		}
	})
}
