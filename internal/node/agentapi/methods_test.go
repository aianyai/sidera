package agentapi_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/agentapi"
	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func sessionNode(t testing.TB) (*harness.H, adapterproto.Token, string) {
	t.Helper()
	h := harness.New(t, harness.Options{Adapter: &harness.AdapterOptions{Runtime: true}})
	h.Quiesce()
	h.MustDirect("creator")
	created, err := h.Agent("creator").Control(h.Ctx(), fakeagent.ControlInput{To: h.RuntimeAddr(), Signal: adapterproto.SignalSessionCreate, Preset: "default"})
	if err != nil {
		t.Fatal(err)
	}
	for range 4 {
		h.Quiesce()
		h.Advance(engine.DefaultParams().PullInterval)
	}
	h.Quiesce()
	status, err := h.Agent("creator").Status(h.Ctx(), fakeagent.StatusInput{MessageID: created.MessageID})
	if err != nil || status.ControlReply == nil || status.ControlReply.Create == nil || status.ControlReply.Create.Outcome != adapterproto.CreateReplyCreated {
		t.Fatalf("creation: %+v, %v", status, err)
	}
	endpoint := status.ControlReply.Create.SessionEndpoint
	scan, err := h.Records()
	if err != nil {
		t.Fatal(err)
	}
	for _, ep := range scan.Endpoints {
		if ep.ID.String() == string(endpoint) && ep.Status == store.EndpointOpen {
			return h, adapterproto.Token(base64.RawURLEncoding.EncodeToString(ep.Token)), harness.NodeID().String() + "/" + ep.ID.String()
		}
	}
	t.Fatal("the created session has no open endpoint")
	return nil, "", ""
}

func request(s *agentapi.Server, token adapterproto.Token, method, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/v1/agent/"+method, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+string(token))
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestEnabledMeShowsTheAuthenticatedSessionAndFrontendLimits(t *testing.T) {
	h, token, address := sessionNode(t)
	s, err := agentapi.New(agentapi.Config{Service: h.Engine()})
	if err != nil {
		t.Fatal(err)
	}
	if w := request(s, token, "me", `{}`); w.Code != 503 {
		t.Fatalf("before enabling: HTTP %d, %s", w.Code, w.Body)
	}
	s.EnableMethods()
	s.EnableMethods() // Enabling is one-way and safe to repeat.
	w := request(s, token, "me", `{}`)
	var got struct {
		Address     string         `json:"address"`
		Node        string         `json:"node"`
		Kind        string         `json:"kind"`
		Declaration map[string]any `json:"declaration"`
		Frontends   map[string]struct {
			RecvWaitMaxS int `json:"recv_wait_max_s"`
			Budget       struct {
				Unit  string `json:"unit"`
				Limit int    `json:"limit"`
			} `json:"budget"`
		} `json:"frontends"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || got.Address != address || got.Node != harness.NodeID().String() || got.Kind != "session" || len(got.Declaration) != 5 || got.Frontends["cli"].RecvWaitMaxS != 20 || got.Frontends["mcp"].RecvWaitMaxS != 45 || got.Frontends["cli"].Budget.Unit != "chars" {
		t.Fatalf("HTTP %d, %s; want own session, snake-case declaration and declared limits", w.Code, w.Body)
	}
	if _, err := h.Engine().Me(context.Background(), engine.ByToken(token)); err != nil {
		t.Fatal(err)
	}
}

func TestDeclarationPatchPreservesOmittedFieldsAndReplacesSenders(t *testing.T) {
	h, token, address := sessionNode(t)
	s, _ := agentapi.New(agentapi.Config{Service: h.Engine()})
	s.EnableMethods()
	for _, body := range []string{`{"paused":true,"restrict_senders":true,"accept_from":["` + address + `"]}`, `{"paused":false,"hidden_from_directory":true}`, `{"accept_from":[]}`} {
		w := request(s, token, "declare", body)
		if w.Code != 200 {
			t.Fatalf("declare %s: HTTP %d, %s", body, w.Code, w.Body)
		}
	}
	w := request(s, token, "declare", `{"refuse_multi_recipient":true}`)
	want := "{\"declaration\":{\"paused\":false,\"refuse_multi_recipient\":true,\"restrict_senders\":true,\"accept_from\":[],\"hidden_from_directory\":true}}\n"
	if w.Code != 200 || w.Body.String() != want {
		t.Fatalf("HTTP %d, %s; want %s", w.Code, w.Body, want)
	}
	w = request(s, token, "declare", `{}`)
	if w.Code != 400 || !strings.Contains(w.Body.String(), `"code":"empty_patch"`) {
		t.Fatalf("empty declaration: HTTP %d, %s", w.Code, w.Body)
	}
}

func TestSendUsesOneRecipientAndExplicitIdempotency(t *testing.T) {
	h, token, _ := sessionNode(t)
	s, _ := agentapi.New(agentapi.Config{Service: h.Engine()})
	s.EnableMethods()
	to := h.Addr("creator")
	body := `{"to":["` + to + `"],"body":"hello <world>","idempotency_key":"same-request","accept_before_s":600}`
	sentAt := h.Clock().Now()
	w := request(s, token, "send", body)
	var first struct {
		MessageID string   `json:"message_id"`
		To        []string `json:"to"`
		Replayed  bool     `json:"replayed"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || first.MessageID == "" || len(first.To) != 1 || first.To[0] != to || first.Replayed {
		t.Fatalf("HTTP %d: %s", w.Code, w.Body)
	}
	h.Quiesce()
	scan, err := h.Records()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, out := range scan.Out {
		if string(out.Key.Message) == first.MessageID {
			found = true
			if got := out.Outbound.AcceptBefore.Sub(sentAt); got != 600*time.Second {
				t.Fatalf("delivery deadline = %v; want 600 seconds", got)
			}
		}
	}
	if !found {
		t.Fatal("the accepted message has no durable outbound record")
	}
	w = request(s, token, "send", body)
	var replay struct {
		MessageID string `json:"message_id"`
		Replayed  bool   `json:"replayed"`
	}
	json.Unmarshal(w.Body.Bytes(), &replay)
	if w.Code != 200 || replay.MessageID != first.MessageID || !replay.Replayed {
		t.Fatalf("replay: HTTP %d, %s", w.Code, w.Body)
	}
	for _, recipients := range []string{`[]`, `["` + to + `","` + to + `"]`} {
		w := request(s, token, "send", `{"to":`+recipients+`,"body":"hello"}`)
		if w.Code != 400 || !strings.Contains(w.Body.String(), `"code":"single_recipient_only"`) {
			t.Fatalf("recipients %s: HTTP %d, %s", recipients, w.Code, w.Body)
		}
	}
}

func TestRecvReturnsTheUnwrappedPageAndAcknowledgesTheEarlierLease(t *testing.T) {
	h, token, address := sessionNode(t)
	s, _ := agentapi.New(agentapi.Config{Service: h.Engine()})
	s.EnableMethods()
	sent, err := h.Agent("creator").Send(h.Ctx(), fakeagent.SendInput{To: []string{address}, Body: "read this exact text <&>"})
	if err != nil {
		t.Fatal(err)
	}
	h.Quiesce()
	w := request(s, token, "recv", `{"frontend":"cli","wait_s":0}`)
	var page adapterproto.RecvPage
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || len(page.Messages) != 1 || page.Messages[0].ID != sent.MessageID || page.Messages[0].Body != "read this exact text <&>" || !strings.HasSuffix(w.Body.String(), "\n") || !strings.HasPrefix(w.Body.String(), `{"lease":`) {
		t.Fatalf("HTTP %d, %s; want the page without a wrapper", w.Code, w.Body)
	}
	w = request(s, token, "recv", `{"frontend":"mcp","ack_lease":"`+string(page.Lease)+`"}`)
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || len(page.Messages) != 0 {
		t.Fatalf("after ack_lease: HTTP %d, %s", w.Code, w.Body)
	}
	w = request(s, token, "recv", `{"frontend":"cli","ack_lease":"11111111111111111111111111111111"}`)
	if w.Code != 200 {
		t.Fatalf("a failed earlier acknowledgement blocked recv: HTTP %d, %s", w.Code, w.Body)
	}
}

func TestControlReturnsTheAcceptedSignalAndAllowsAnOmittedInitialMessage(t *testing.T) {
	h, token, _ := sessionNode(t)
	s, _ := agentapi.New(agentapi.Config{Service: h.Engine()})
	s.EnableMethods()
	to := h.RuntimeAddr()
	body := `{"to":"` + to + `","signal":"session.create","preset":"default","idempotency_key":"create-another"}`
	w := request(s, token, "control", body)
	var first struct {
		MessageID string `json:"message_id"`
		To        string `json:"to"`
		Replayed  bool   `json:"replayed"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || first.MessageID == "" || first.To != to || first.Replayed || strings.Contains(w.Body.String(), "control_reply") {
		t.Fatalf("HTTP %d, %s", w.Code, w.Body)
	}
	w = request(s, token, "control", body)
	var replay struct {
		MessageID string `json:"message_id"`
		Replayed  bool   `json:"replayed"`
	}
	json.Unmarshal(w.Body.Bytes(), &replay)
	if w.Code != 200 || replay.MessageID != first.MessageID || !replay.Replayed {
		t.Fatalf("replayed control: HTTP %d, %s", w.Code, w.Body)
	}
	w = request(s, token, "control", `{"to":"`+to+`","signal":"session.create","preset":"default","body":""}`)
	if w.Code != 400 {
		t.Fatalf("explicit empty initial message: HTTP %d, %s", w.Code, w.Body)
	}
}

func TestStatusCarriesTheEventualControlReply(t *testing.T) {
	h, token, _ := sessionNode(t)
	s, _ := agentapi.New(agentapi.Config{Service: h.Engine()})
	s.EnableMethods()
	w := request(s, token, "control", `{"to":"`+h.RuntimeAddr()+`","signal":"session.create","preset":"default"}`)
	var sent struct {
		MessageID string `json:"message_id"`
	}
	json.Unmarshal(w.Body.Bytes(), &sent)
	if w.Code != 200 {
		t.Fatalf("create: HTTP %d, %s", w.Code, w.Body)
	}
	for range 4 {
		h.Quiesce()
		h.Advance(engine.DefaultParams().PullInterval)
	}
	h.Quiesce()
	w = request(s, token, "status", `{"message_id":"`+sent.MessageID+`"}`)
	var got adapterproto.MessageStatusResult
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || len(got.Recipients) != 1 || got.ControlReply == nil || got.ControlReply.Create == nil || got.ControlReply.Create.Outcome != adapterproto.CreateReplyCreated {
		t.Fatalf("HTTP %d, %s; want the asynchronous creation reply", w.Code, w.Body)
	}
}

func TestReplyGetsItsRecipientFromTheReceivedMessage(t *testing.T) {
	h, token, address := sessionNode(t)
	s, _ := agentapi.New(agentapi.Config{Service: h.Engine()})
	s.EnableMethods()
	sent, err := h.Agent("creator").Send(h.Ctx(), fakeagent.SendInput{To: []string{address}, Body: "a question"})
	if err != nil {
		t.Fatal(err)
	}
	h.Quiesce()
	w := request(s, token, "reply", `{"message_id":"`+string(sent.MessageID)+`","body":"an answer","idempotency_key":"answer"}`)
	var got struct {
		MessageID string   `json:"message_id"`
		To        []string `json:"to"`
	}
	json.Unmarshal(w.Body.Bytes(), &got)
	if w.Code != 200 || got.MessageID == "" || len(got.To) != 1 || got.To[0] != h.Addr("creator") {
		t.Fatalf("HTTP %d, %s; want the original sender", w.Code, w.Body)
	}
}

func TestAckReturnsPartAcknowledgementsAndRejectsAnUnknownLease(t *testing.T) {
	h, token, address := sessionNode(t)
	s, _ := agentapi.New(agentapi.Config{Service: h.Engine()})
	s.EnableMethods()
	_, err := h.Agent("creator").Send(h.Ctx(), fakeagent.SendInput{To: []string{address}, Body: "acknowledge this"})
	if err != nil {
		t.Fatal(err)
	}
	h.Quiesce()
	w := request(s, token, "recv", `{"frontend":"cli"}`)
	var page adapterproto.RecvPage
	json.Unmarshal(w.Body.Bytes(), &page)
	if w.Code != 200 || len(page.Messages) != 1 {
		t.Fatalf("recv: HTTP %d, %s", w.Code, w.Body)
	}
	w = request(s, token, "ack", `{"lease":"`+string(page.Lease)+`"}`)
	var ack adapterproto.AckResult
	json.Unmarshal(w.Body.Bytes(), &ack)
	if w.Code != 200 || len(ack.Items) != 1 || ack.Items[0].MessageID != page.Messages[0].ID || ack.Items[0].Ack != adapterproto.ItemCompleted {
		t.Fatalf("HTTP %d, %s", w.Code, w.Body)
	}
	w = request(s, token, "ack", `{"lease":"11111111111111111111111111111111"}`)
	if w.Code != 404 || !strings.Contains(w.Body.String(), `"code":"lease_unknown"`) {
		t.Fatalf("foreign lease: HTTP %d, %s", w.Code, w.Body)
	}
}

func TestDirectoryReturnsLivePagesWithActionNames(t *testing.T) {
	h, token, address := sessionNode(t)
	s, _ := agentapi.New(agentapi.Config{Service: h.Engine()})
	s.EnableMethods()
	var cursor string
	found := false
	for range 4 {
		body := `{"limit":1}`
		if cursor != "" {
			body = `{"limit":1,"cursor":"` + cursor + `"}`
		}
		w := request(s, token, "directory", body)
		var got struct {
			Endpoints []struct {
				Address string   `json:"address"`
				Kind    string   `json:"kind"`
				Accepts []string `json:"accepts"`
			} `json:"endpoints"`
			NextCursor string `json:"next_cursor"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || len(got.Endpoints) != 1 || len(got.Endpoints[0].Accepts) == 0 {
			t.Fatalf("HTTP %d, %s", w.Code, w.Body)
		}
		if got.Endpoints[0].Address == address {
			found = true
			if got.Endpoints[0].Kind != "session" || got.Endpoints[0].Accepts[0] != "message.send" {
				t.Fatalf("session entry: %s", w.Body)
			}
		}
		cursor = got.NextCursor
		if cursor == "" {
			break
		}
	}
	if !found || cursor != "" {
		t.Fatalf("session found=%v, unfinished cursor=%q", found, cursor)
	}
}

func TestPeersIdentifiesThisNode(t *testing.T) {
	h, token, _ := sessionNode(t)
	s, _ := agentapi.New(agentapi.Config{Service: h.Engine()})
	s.EnableMethods()
	w := request(s, token, "peers", `{}`)
	want := `{"nodes":[{"node":"` + harness.NodeID().String() + `","self":true}]}` + "\n"
	if w.Code != 200 || w.Body.String() != want {
		t.Fatalf("HTTP %d, %s; want %s", w.Code, w.Body, want)
	}
}

func TestMethodInputIsStrictAndReceiveNumbersKeepTheirMeaning(t *testing.T) {
	h, token, _ := sessionNode(t)
	s, _ := agentapi.New(agentapi.Config{Service: h.Engine()})
	s.EnableMethods()
	for _, tc := range []struct{ name, method, body, path string }{
		{"unknown member", "me", `{"unknown":true}`, "unknown"},
		{"repeated member", "declare", `{"paused":true,"paused":false}`, "paused"},
		{"wrong case", "recv", `{"Frontend":"cli"}`, "Frontend"},
		{"missing required field", "status", `{}`, "message_id"},
		{"null", "declare", `{"paused":null}`, "paused"},
		{"trailing document", "peers", `{} {}`, ""},
		{"wrong type", "directory", `{"limit":"1"}`, "limit"},
		{"negative wait", "recv", `{"frontend":"cli","wait_s":-1}`, "wait_s"},
		{"fractional wait", "recv", `{"frontend":"cli","wait_s":1.5}`, "wait_s"},
		{"wait overflow", "recv", `{"frontend":"cli","wait_s":9223372036854775807}`, "wait_s"},
		{"part with explicit zero wait", "recv", `{"frontend":"cli","msg":"message","part":1,"wait_s":0}`, "wait_s"},
		{"negative part", "recv", `{"frontend":"cli","msg":"message","part":-1}`, "part"},
		{"unpaired part", "recv", `{"frontend":"cli","part":1}`, "part"},
		{"empty explicit items", "ack", `{"lease":"11111111111111111111111111111111","items":[]}`, "items"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := request(s, token, tc.method, tc.body)
			var got apiwire.Body
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if w.Code != 400 || got.Error.Category != apiwire.CategoryInvalidArgument || got.Error.Code != apiwire.CodeMalformedRequest || (tc.path != "" && got.Error.Details["path"] != tc.path) {
				t.Fatalf("HTTP %d, %s; want invalid_argument/malformed_request with path %q", w.Code, w.Body, tc.path)
			}
		})
	}
	items := make([]adapterproto.PartRef, adapterproto.MaxAckItems+1)
	for i := range items {
		items[i] = adapterproto.PartRef{MessageID: "message", Part: 1}
	}
	body, _ := json.Marshal(struct {
		Lease string                 `json:"lease"`
		Items []adapterproto.PartRef `json:"items"`
	}{"11111111111111111111111111111111", items})
	w := request(s, token, "ack", string(body))
	if w.Code != 400 {
		t.Fatalf("oversized acknowledgement: HTTP %d, %s", w.Code, w.Body)
	}
}

func TestMethodsRejectUnissuedProbeAndRevokedEndpointTokens(t *testing.T) {
	h, token, address := sessionNode(t)
	s, _ := agentapi.New(agentapi.Config{Service: h.Engine()})
	s.EnableMethods()
	probes, err := h.Engine().ProbeTokens(h.Ctx(), harness.Instance)
	if err != nil || len(probes) == 0 {
		t.Fatalf("probe tokens: %v", err)
	}
	methods := []struct{ name, body string }{
		{"me", `{}`}, {"declare", `{"paused":true}`}, {"peers", `{}`}, {"directory", `{}`},
		{"send", `{"to":["` + h.Addr("creator") + `"],"body":"private text"}`},
		{"reply", `{"message_id":"message","body":"private text"}`}, {"recv", `{"frontend":"cli"}`},
		{"ack", `{"lease":"11111111111111111111111111111111"}`}, {"status", `{"message_id":"message"}`},
		{"control", `{"to":"` + h.RuntimeAddr() + `","signal":"endpoint.status"}`},
	}
	check := func(credential adapterproto.Token) {
		for _, m := range methods {
			w := request(s, credential, m.name, m.body)
			if w.Code != 401 || strings.Contains(w.Body.String(), "private text") || strings.Contains(w.Body.String(), string(credential)) {
				t.Fatalf("%s: HTTP %d, %s; want unauthenticated without secrets", m.name, w.Code, w.Body)
			}
		}
	}
	check(adapterproto.Token(strings.Repeat("A", 43)))
	check(probes[0].Token)
	a, err := ident.ParseAddress(address)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Engine().CloseEndpoint(h.Ctx(), engine.CloseRequest{Endpoint: a.Endpoint}); err != nil {
		t.Fatal(err)
	}
	check(token)
}
