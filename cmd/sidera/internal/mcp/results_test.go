package mcp_test

import (
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/aianyai/sidera/cmd/sidera/internal/mcp"
)

func TestToolResultGoldens(t *testing.T) {
	for _, era := range []string{"legacy", "modern"} {
		t.Run(era, func(t *testing.T) {
			c := httpAgent(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				switch r.URL.Path {
				case "/v1/agent/peers":
					io.WriteString(w, "{\"nodes\":[]}\n")
				case "/v1/agent/recv":
					io.WriteString(w, "{ \"lease\":\"lease_1234567890123456\", \"messages\":[], \"more\":0 }\n")
				default:
					t.Errorf("unexpected Agent route %s", r.URL.Path)
				}
			}))
			s := start(t, mcp.Config{Version: "test", Agent: c})
			meta := ""
			if era == "legacy" {
				s.exchange(t, `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{}}}`, `{"jsonrpc":"2.0","id":0,"result":{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"sidera","version":"test"},"instructions":"Use me to inspect your endpoint and receive limits. Receive pages must be read before acknowledging them."}}`)
			} else {
				meta = `,"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}`
			}
			for _, tc := range []struct {
				id                         int
				name, args, legacy, modern string
			}{
				{1, "peers", `{}`, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{\"nodes\":[]}\n"}],"isError":false}}`, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{\"nodes\":[]}\n"}],"isError":false,"resultType":"complete","_meta":{"io.modelcontextprotocol/serverInfo":{"name":"sidera","version":"test"}}}}`},
				{2, "directory", `{"limit":0}`, `{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"{\"error\":{\"category\":\"invalid_argument\",\"code\":\"malformed_request\",\"message\":\"tool arguments do not match the input schema\",\"details\":{\"path\":\"limit\"}}}\n"}],"isError":true}}`, `{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"{\"error\":{\"category\":\"invalid_argument\",\"code\":\"malformed_request\",\"message\":\"tool arguments do not match the input schema\",\"details\":{\"path\":\"limit\"}}}\n"}],"isError":true,"resultType":"complete","_meta":{"io.modelcontextprotocol/serverInfo":{"name":"sidera","version":"test"}}}}`},
				{3, "recv", `{}`, `{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"{ \"lease\":\"lease_1234567890123456\", \"messages\":[], \"more\":0 }\n"}],"isError":false}}`, `{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"{ \"lease\":\"lease_1234567890123456\", \"messages\":[], \"more\":0 }\n"}],"isError":false,"resultType":"complete","_meta":{"io.modelcontextprotocol/serverInfo":{"name":"sidera","version":"test"}}}}`},
			} {
				request := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":%q,"arguments":%s%s}}`, tc.id, tc.name, tc.args, meta)
				want := tc.legacy
				if era == "modern" {
					want = tc.modern
				}
				s.exchange(t, request, want)
			}
		})
	}
}
