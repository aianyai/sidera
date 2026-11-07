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
			if e := apiwire.FromError(err); e == nil || e.Category != apiwire.CategoryInternal || e.Code != apiwire.CodeAnswerUnreadable {
				t.Fatalf("a successful answer that lacks the members of the result: %v; want internal/answer_unreadable", err)
			}
		})
	}
}

// A client reads the answer of a node that is newer than itself: a member it does
// not know is ignored in a successful result at any depth, a page of recv
// included, which is returned as it came. The page rows that were refused before
// (an unknown member of the page, of a part) are accepted on purpose: one rule
// reads every result.
func TestCallIgnoresUnknownMembersOfASuccessfulResult(t *testing.T) {
	const lease = `"lease":"11111111111111111111111111111111"`
	declaration := `{"paused":false,"refuse_multi_recipient":false,"restrict_senders":false,"accept_from":[],"hidden_from_directory":false`
	message := func(extra string) string {
		return `{"id":"m1","from":"node/ep","body":"hello"` + extra + `}`
	}
	page := func(messages, extra string) string {
		return "{" + lease + `,"messages":[` + messages + `],"more":0` + extra + "}\n"
	}
	for _, tc := range []struct {
		name, method, answer string
	}{
		{"me, the result", "me", `{"address":"a","node":"n","kind":"session","display_name":"s","declaration":` + declaration + `},"frontends":{},"future":[1]}`},
		{"me, the declaration", "me", `{"address":"a","node":"n","kind":"session","display_name":"s","declaration":` + declaration + `,"future":1},"frontends":{}}`},
		{"me, a front end and its budget", "me", `{"address":"a","node":"n","kind":"session","display_name":"s","declaration":` + declaration + `},"frontends":{"cli":{"recv_wait_max_s":5,"future":1,"budget":{"unit":"chars","limit":9,"future":2}}}}`},
		{"declare, the declaration", "declare", `{"declaration":` + declaration + `,"future":1}}`},
		{"peers, the result", "peers", `{"nodes":[],"future":1}`},
		{"peers, a node", "peers", `{"nodes":[{"node":"n","self":true,"trust":"self","connected":true,"future":1}]}`},
		{"peers, a profile", "peers", `{"nodes":[{"node":"n","self":true,"trust":"self","connected":true,"profile":{"name":"n","as_of":1,"future":{"a":[1]}}}]}`},
		{"directory, the result", "directory", `{"endpoints":[],"next_cursor":"","future":1}`},
		{"directory, an entry", "directory", `{"endpoints":[{"address":"a","kind":"session","display_name":"s","description":"","accepts":[],"future":1}],"next_cursor":""}`},
		{"directory, an adapter", "directory", `{"endpoints":[{"address":"a","kind":"session","display_name":"s","description":"","accepts":[],"adapter":{"instance":"i","id":"x","future":1}}],"next_cursor":""}`},
		{"send, the result", "send", `{"message_id":"m1","to":["a"],"replayed":false,"future":1}`},
		{"reply, the result", "reply", `{"message_id":"m1","to":["a"],"replayed":false,"future":1}`},
		{"ack, the result", "ack", `{"items":[],"future":1}`},
		{"status, the result", "status", `{"recipients":[],"future":1}`},
		{"control, the result", "control", `{"message_id":"m1","to":"a","replayed":false,"future":1}`},
		{"probe, the result", "probe", `{"ok":true,"preset":"default","frontend":"cli","future":1}`},
		{"recv, a message", "recv", page(message(`,"future":{"a":[1]}`), "")},
		{"recv, every message", "recv", page(message(`,"future":1`)+","+message(`,"other":"x"`), "")},
		{"recv, the page", "recv", page(message(""), `,"future":1`)},
		{"recv, a part", "recv", page(message(`,"part":{"index":1,"total":2,"label":"part 1/2","future":1,"next":{"msg":"m1","part":2}}`), "")},
		{"recv, the pointer to the next part", "recv", page(message(`,"part":{"index":1,"total":2,"label":"part 1/2","next":{"msg":"m1","part":2,"future":1}}`), "")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := responseClient(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, tc.answer) })
			got, err := c.Call(context.Background(), tc.method, json.RawMessage(`{}`))
			if err != nil {
				t.Fatalf("Call: %v; want the unknown member ignored", err)
			}
			if raw, isBytes := got.([]byte); isBytes != (tc.method == "recv") || (isBytes && string(raw) != tc.answer) {
				t.Fatalf("Call returned %T %q; for recv, want the page as it came", got, got)
			}
		})
	}
}

// What a client does not understand of a successful answer is not an unknown
// outcome, whatever the cause: a known member of the wrong type, a repeated member,
// a member a page message needs. It says so in words that name the program as the
// likely cause, and for a method that changes state also that the change was
// accepted. A request whose answer was cut off stays outcome_unknown, which
// TestAProbeWithAnIncompleteAnswerHasAnUnknownOutcomeAndIsNotRetried holds.
func TestCallReportsAnUnreadableSuccessfulAnswer(t *testing.T) {
	const lease = `"lease":"11111111111111111111111111111111"`
	for _, tc := range []struct {
		name, method, answer string
		changes              bool
	}{
		{"me, a known member of the wrong type", "me", `{"address":1,"node":"n","kind":"s","display_name":"s","declaration":{},"frontends":{}}`, false},
		{"peers, a known member of the wrong type", "peers", `{"nodes":[{"node":"n","self":"yes","trust":"self","connected":true}]}`, false},
		{"peers, a wrong type below an unknown member's neighbour", "peers", `{"nodes":[{"node":"n","self":true,"trust":"self","connected":true,"profile":{"name":3,"as_of":1}}],"future":1}`, false},
		{"directory, a repeated member", "directory", `{"endpoints":[],"next_cursor":"","next_cursor":""}`, false},
		{"status, null for a known member", "status", `{"recipients":null}`, false},
		{"send", "send", `{"message_id":7,"to":["a"],"replayed":false}`, true},
		{"reply", "reply", `{"message_id":"m1","to":"a","replayed":false}`, true},
		{"ack", "ack", `{"items":{}}`, true},
		{"declare", "declare", `{"declaration":[]}`, true},
		{"control", "control", `{"to":"a","replayed":false}`, true},
		{"recv, a message without a body", "recv", "{" + lease + `,"messages":[{"id":"m1","from":"n/e"}],"more":0}`, false},
		{"recv, a repeated member of a message", "recv", "{" + lease + `,"messages":[{"id":"m1","from":"n/e","body":"a","body":"b"}],"more":0}`, false},
		{"recv, a page without messages", "recv", "{" + lease + `,"more":0}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := responseClient(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, tc.answer) })
			_, err := c.Call(context.Background(), tc.method, json.RawMessage(`{}`))
			e := apiwire.FromError(err)
			if e == nil || e.Category != apiwire.CategoryInternal || e.Code != apiwire.CodeAnswerUnreadable {
				t.Fatalf("Call: %v; want internal/answer_unreadable", err)
			}
			if told := strings.Contains(e.Message, "the change was accepted"); told != tc.changes {
				t.Fatalf("message %q: says the change was accepted = %v, want %v", e.Message, told, tc.changes)
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

func TestSetTokenReplacesTheEndpointCredentialForLaterCalls(t *testing.T) {
	var seen []string
	c := responseClient(t, func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		io.WriteString(w, `{"nodes":[]}`)
	})
	later := adapterproto.Token(strings.Repeat("C", 43))
	for _, token := range []adapterproto.Token{"", later} {
		c.SetToken(token)
		if _, err := c.Peers(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if want := []string{"", "Bearer " + string(later)}; !reflect.DeepEqual(seen, want) {
		t.Fatalf("credentials: %q; want %q", seen, want)
	}
}
