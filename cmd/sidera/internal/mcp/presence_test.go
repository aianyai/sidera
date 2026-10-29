package mcp_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/aianyai/sidera/cmd/sidera/internal/mcp"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

const emptyStatusArguments = `{"message_id":""}`

func TestExplicitInvalidAcknowledgementCannotConfirmThePage(t *testing.T) {
	h, c, address := agentNode(t)
	sent, err := h.Agent("creator").Send(h.Ctx(), fakeagent.SendInput{To: []string{address}, Body: "must remain unread after an invalid acknowledgement"})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Quiesce(); err != nil {
		t.Fatal(err)
	}
	s := start(t, mcp.Config{Agent: c, Version: "test"})
	for i, items := range []any{[]any{}, nil, []any{map[string]any{"message_id": sent.MessageID, "part": 1}, map[string]any{"message_id": sent.MessageID, "part": 0}}} {
		text, isError := s.call(t, 10*i+1, "recv", map[string]any{})
		var page adapterproto.RecvPage
		if err := json.Unmarshal([]byte(text), &page); err != nil || isError || len(page.Messages) != 1 || page.Messages[0].ID != sent.MessageID {
			t.Fatalf("receive after invalid ack: %s, %v", text, err)
		}
		text, isError = s.call(t, 10*i+2, "ack", map[string]any{"lease": page.Lease, "items": items})
		requireToolError(t, text, isError, "invalid_argument", "malformed_request")
	}
	// A subsequent receive must still expose exactly the unacknowledged message.
	text, isError := s.call(t, 40, "recv", map[string]any{})
	var page adapterproto.RecvPage
	if err := json.Unmarshal([]byte(text), &page); err != nil || isError || len(page.Messages) != 1 || page.Messages[0].ID != sent.MessageID || !page.Messages[0].PossiblyDuplicate {
		t.Fatalf("message disappeared or was confirmed: %s %v", text, err)
	}
	text, isError = s.call(t, 41, "ack", map[string]any{"lease": page.Lease})
	var ack struct {
		Items []adapterproto.PartAck `json:"items"`
	}
	if err := json.Unmarshal([]byte(text), &ack); err != nil || isError || len(ack.Items) != 1 {
		t.Fatalf("whole page ack: %s %v", text, err)
	}
}

func TestExplicitEmptyToolMembersReachAgentValidation(t *testing.T) {
	statuses := make(chan int, 1)
	h, c, _, _, _ := agentNodeDetails(t, func(_ string, status int) { statuses <- status })
	s := start(t, mcp.Config{Agent: c, Version: "test"})
	for i, tc := range []struct {
		name string
		args any
	}{
		{"control", map[string]any{"to": h.RuntimeAddr(), "signal": "session.create", "preset": "default", "body": ""}},
		{"directory", map[string]any{"node": ""}},
		{"send", map[string]any{"to": []string{h.Addr("creator")}, "body": "body", "idempotency_key": ""}},
		{"recv", map[string]any{"msg": ""}},
		{"status", json.RawMessage(emptyStatusArguments)},
	} {
		text, isError := s.call(t, i+1, tc.name, tc.args)
		requireToolError(t, text, isError, "invalid_argument", "malformed_request")
		select {
		case status := <-statuses:
			if status != http.StatusBadRequest {
				t.Fatalf("%s Agent HTTP status: got %d, want 400", tc.name, status)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s did not reach Agent validation", tc.name)
		}
	}
}

func requireToolError(t *testing.T, text string, isError bool, category, code string) {
	t.Helper()
	var body struct {
		Error struct {
			Category string `json:"category"`
			Code     string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(text), &body); err != nil || !isError || body.Error.Category != category || body.Error.Code != code {
		t.Fatalf("tool error: %s isError=%v decode=%v; want %s/%s", text, isError, err, category, code)
	}
}
