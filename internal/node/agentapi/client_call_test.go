package agentapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/agentapi"
	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/ipc"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func responseClient(t testing.TB, handler http.HandlerFunc) *agentapi.Client {
	t.Helper()
	home := socketHome(t)
	addr, err := ipc.Locate(home, ipc.KindAgent)
	if err != nil {
		t.Fatal(err)
	}
	l, err := ipc.Listen(addr, held{}, ipc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s := &http.Server{Handler: handler}
	ended := make(chan error, 1)
	go func() { ended <- s.Serve(l) }()
	t.Cleanup(func() { s.Close(); <-ended })
	c, err := agentapi.NewClient(agentapi.ClientConfig{
		Home: home, Token: adapterproto.Token(strings.Repeat("A", 43)), ProbeToken: adapterproto.Token(strings.Repeat("B", 43)),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func TestCallSelectsTheCredentialAndValidatesEveryResultType(t *testing.T) {
	declaration := `{"paused":false,"refuse_multi_recipient":false,"restrict_senders":false,"accept_from":[],"hidden_from_directory":false}`
	for _, tc := range []struct {
		method, answer string
		result         any
	}{
		{"me", `{"address":"address","node":"node","kind":"session","display_name":"session","declaration":` + declaration + `,"frontends":{}}`, agentapi.MeResult{}},
		{"declare", `{"declaration":` + declaration + `}`, agentapi.DeclareResult{}},
		{"peers", `{"nodes":[]}`, agentapi.PeersResult{}},
		{"directory", `{"endpoints":[],"next_cursor":""}`, agentapi.DirectoryResult{}},
		{"send", `{"message_id":"sent","to":["address"],"replayed":false}`, agentapi.SendResult{}},
		{"reply", `{"message_id":"reply","to":["address"],"replayed":false}`, agentapi.SendResult{}},
		{"recv", "{\"lease\":\"11111111111111111111111111111111\",\"messages\":[],\"more\":0}\n", []byte{}},
		{"ack", `{"items":[]}`, agentapi.AckResult{}},
		{"status", `{"recipients":[]}`, adapterproto.MessageStatusResult{}},
		{"control", `{"message_id":"control","to":"address","replayed":false}`, agentapi.ControlResult{}},
		{"probe", `{"ok":true,"preset":"default","frontend":"cli"}`, agentapi.ProbeResult{}},
	} {
		t.Run(tc.method, func(t *testing.T) {
			c := responseClient(t, func(w http.ResponseWriter, r *http.Request) {
				want := strings.Repeat("A", 43)
				if tc.method == "probe" {
					want = strings.Repeat("B", 43)
				}
				if r.URL.Path != "/v1/agent/"+tc.method || r.Header.Get("Authorization") != "Bearer "+want {
					t.Error("wrong method or credential")
				}
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				if strings.Contains(string(raw), "incomplete") {
					io.WriteString(w, `{}`)
				} else {
					io.WriteString(w, tc.answer)
				}
			})
			got, err := c.Call(context.Background(), tc.method, json.RawMessage(`{}`))
			if err != nil || reflect.TypeOf(got) != reflect.TypeOf(tc.result) {
				t.Fatalf("Call: %T, %v; want %T", got, err, tc.result)
			}
			if raw, ok := got.([]byte); ok && string(raw) != tc.answer {
				t.Fatalf("recv bytes: %q; want %q", raw, tc.answer)
			}
			_, err = c.Call(context.Background(), tc.method, map[string]any{"incomplete": true})
			if e := apiwire.FromError(err); e == nil || e.Category != apiwire.CategoryOutcomeUnknown || e.Code != apiwire.CodeOutcomeUnknown {
				t.Fatalf("incomplete result: %v; want outcome_unknown", err)
			}
		})
	}
}

func TestCallPreservesExplicitEmptyFieldsForTheServer(t *testing.T) {
	h, token, _ := sessionNode(t)
	c := endpointClient(t, h, token)
	empty := ""
	for _, tc := range []struct {
		name, method string
		input        any
	}{
		{"raw control body", "control", json.RawMessage(`{"to":"` + h.RuntimeAddr() + `","signal":"session.create","preset":"default","body":""}`)},
		{"map control body", "control", map[string]any{"to": h.RuntimeAddr(), "signal": "session.create", "preset": "default", "body": ""}},
		{"struct control body", "control", &struct {
			To     string  `json:"to"`
			Signal string  `json:"signal"`
			Preset string  `json:"preset"`
			Body   *string `json:"body,omitempty"`
		}{h.RuntimeAddr(), "session.create", "default", &empty}},
		{"optional integer zero", "directory", map[string]any{"limit": 0}},
		{"optional string empty", "send", map[string]any{"to": []string{h.Addr("creator")}, "body": "message", "content_type": ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := c.Call(h.Ctx(), tc.method, tc.input)
			if e := apiwire.FromError(err); e == nil || e.Category != apiwire.CategoryInvalidArgument || e.Code != "malformed_request" {
				t.Fatalf("explicit empty field: %v; want invalid_argument/malformed_request", err)
			}
		})
	}
	got, err := c.Call(h.Ctx(), "control", map[string]any{"to": h.RuntimeAddr(), "signal": "session.create", "preset": "default"})
	if result, ok := got.(agentapi.ControlResult); err != nil || !ok || result.MessageID == "" {
		t.Fatalf("omitted control body: %+v, %v", got, err)
	}
	if _, err := c.Control(h.Ctx(), agentapi.ControlRequest{To: h.RuntimeAddr(), Signal: adapterproto.SignalSessionCreate, Preset: "default", Body: ""}); err != nil {
		t.Fatalf("typed zero body retains the omission default: %v", err)
	}
	got, err = c.Call(h.Ctx(), "declare", map[string]any{"paused": false, "accept_from": []string{}})
	if result, ok := got.(agentapi.DeclareResult); err != nil || !ok || result.Declaration.Paused || result.Declaration.AcceptFrom == nil || len(result.Declaration.AcceptFrom) != 0 {
		t.Fatalf("legitimate declaration zero values: %+v, %v", got, err)
	}
	if _, err := c.Call(h.Ctx(), "recv", map[string]any{"frontend": "cli", "wait_s": int64(0)}); err != nil {
		t.Fatalf("legitimate zero receive wait: %v", err)
	}
}

func TestCallRejectsUnknownMethodsAndUnsafeDocumentsBeforeDialing(t *testing.T) {
	c, err := agentapi.NewClient(agentapi.ClientConfig{Home: socketHome(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, method := range []string{"", "unknown", "../probe", "probe?other", "/probe"} {
		_, err := c.Call(context.Background(), method, json.RawMessage(`{}`))
		if e := apiwire.FromError(err); e == nil || e.Category != apiwire.CategoryNotFound || e.Code != "method_unknown" {
			t.Fatalf("method %q: %v; want not_found/method_unknown", method, err)
		}
	}
	for _, input := range []any{
		json.RawMessage("{\"body\":\"bad\xfftext\"}"),
		json.RawMessage(`{"body":"\ud800"}`),
		json.RawMessage(`{"body":"a","body":"b"}`),
		json.RawMessage(`{} {}`),
		map[string]any{"bad\xffkey": "value"},
		map[string]any{"nested": []any{json.RawMessage(`{"body":"\udfff"}`)}},
	} {
		_, err := c.Call(context.Background(), "send", input)
		if e := apiwire.FromError(err); e == nil || e.Category != apiwire.CategoryInvalidArgument || e.Code != "malformed_request" {
			t.Fatalf("unsafe JSON: %v; want invalid_argument/malformed_request", err)
		}
	}
}
