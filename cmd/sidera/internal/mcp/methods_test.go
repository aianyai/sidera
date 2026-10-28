package mcp_test

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"testing"

	"github.com/aianyai/sidera/cmd/sidera/internal/mcp"
)

func TestEveryToolProjectsItsArgumentsAndUsesTheAgentCredential(t *testing.T) {
	const declaration = `{"paused":false,"refuse_multi_recipient":false,"restrict_senders":false,"accept_from":[],"hidden_from_directory":false}`
	const accepted = `{"message_id":"9fcd5192c24b6ff0b06924563af0c837","to":["recipient"],"replayed":false}`
	for _, tc := range []struct{ name, args, request, result string }{
		{"me", `{"future":true}`, `{}`, `{"address":"address","node":"node","kind":"session","display_name":"test","declaration":` + declaration + `,"frontends":{}}`},
		{"declare", `{"paused":false,"accept_from":[],"future":1}`, `{"paused":false,"accept_from":[]}`, `{"declaration":` + declaration + `}`},
		{"peers", `{}`, `{}`, `{"nodes":[{"node":"node","self":true}]}`},
		{"directory", `{"node":"node","cursor":"cursor","limit":1}`, `{"node":"node","cursor":"cursor","limit":1}`, `{"endpoints":[],"next_cursor":""}`},
		{"send", `{"to":["recipient"],"body":"中文 <body>","content_type":"text/plain","thread":"thread","disclose_recipients":false,"accept_before_s":1,"idempotency_key":"key"}`, `{"to":["recipient"],"body":"中文 <body>","content_type":"text/plain","thread":"thread","disclose_recipients":false,"accept_before_s":1,"idempotency_key":"key"}`, accepted},
		{"reply", `{"message_id":"9fcd5192c24b6ff0b06924563af0c837","body":"reply","content_type":"text/plain","thread":"thread","idempotency_key":"key"}`, `{"message_id":"9fcd5192c24b6ff0b06924563af0c837","body":"reply","content_type":"text/plain","thread":"thread","idempotency_key":"key"}`, accepted},
		{"recv", `{"wait_s":0,"ack_lease":"previous","frontend":"cli"}`, `{"wait_s":0,"ack_lease":"previous","frontend":"mcp"}`, `{"lease":"lease_1234567890123456","messages":[],"more":0}`},
		{"ack", `{"lease":"lease_1234567890123456","items":[{"message_id":"9fcd5192c24b6ff0b06924563af0c837","part":1,"future":true}]}`, `{"lease":"lease_1234567890123456","items":[{"message_id":"9fcd5192c24b6ff0b06924563af0c837","part":1}]}`, `{"items":[]}`},
		{"status", `{"message_id":"9fcd5192c24b6ff0b06924563af0c837"}`, `{"message_id":"9fcd5192c24b6ff0b06924563af0c837"}`, `{"recipients":[{"to":"recipient","state":"delivered"}]}`},
		{"control", `{"to":"runtime","signal":"session.create","preset":"default","body":"initial message","idempotency_key":"key"}`, `{"to":"runtime","signal":"session.create","preset":"default","body":"initial message","idempotency_key":"key"}`, `{"message_id":"9fcd5192c24b6ff0b06924563af0c837","to":"session","replayed":false}`},
		{"probe", `{"frontend":"cli","future":true}`, `{"frontend":"mcp"}`, `{"ok":true,"preset":"default","frontend":"mcp"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := httpAgent(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/agent/"+tc.name {
					t.Errorf("Agent route: %s", r.URL.Path)
				}
				token := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
				if tc.name == "probe" {
					token = "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBA"
				}
				if r.Header.Get("Authorization") != "Bearer "+token {
					t.Error("tool used the wrong credential")
				}
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				var got, want any
				if err := json.Unmarshal(raw, &got); err != nil {
					t.Error(err)
				}
				if err := json.Unmarshal([]byte(tc.request), &want); err != nil {
					t.Error(err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("Agent request: %s; want %s", raw, tc.request)
				}
				io.WriteString(w, tc.result+"\n")
			}))
			s := start(t, mcp.Config{Version: "test", Agent: c})
			text, isError := s.call(t, 1, tc.name, json.RawMessage(tc.args))
			if isError {
				t.Fatal(text)
			}
			var got, want any
			if err := json.Unmarshal([]byte(text), &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(tc.result), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("tool result: %s; want %s", text, tc.result)
			}
		})
	}
}
