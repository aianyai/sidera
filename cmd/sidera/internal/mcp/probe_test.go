package mcp_test

import (
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/aianyai/sidera/cmd/sidera/internal/mcp"
)

func TestFirstCompliantRequestProbesWithTheSeparateCredential(t *testing.T) {
	for _, tc := range []struct{ name, request, response string }{
		{"legacy initialize", `{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{}}}`, `{"jsonrpc":"2.0","id":2,"result":{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"sidera","version":"test"},"instructions":"Use me to inspect your endpoint and receive limits. Receive pages must be read before acknowledging them."}}`},
		{"modern discovery", `{"jsonrpc":"2.0","id":2,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`, `{"jsonrpc":"2.0","id":2,"result":{"resultType":"complete","supportedVersions":["2026-07-28"],"capabilities":{"tools":{}},"instructions":"Use me to inspect your endpoint and receive limits. Receive pages must be read before acknowledging them.","ttlMs":3600000,"cacheScope":"public","_meta":{"io.modelcontextprotocol/serverInfo":{"name":"sidera","version":"test"}}}}`},
		{"modern ping", `{"jsonrpc":"2.0","id":2,"method":"ping","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`, `{"jsonrpc":"2.0","id":2,"result":{}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reached := make(chan struct{}, 2)
			c := httpAgent(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/agent/probe" || r.Header.Get("Authorization") != "Bearer BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBA" {
					t.Errorf("wrong probe credential or method: %s", r.URL.Path)
				}
				raw, _ := io.ReadAll(r.Body)
				if string(raw) != "{\"frontend\":\"mcp\"}\n" {
					t.Errorf("probe: %s", raw)
				}
				io.WriteString(w, `{"ok":true,"preset":"default","frontend":"mcp"}`)
				reached <- struct{}{}
			}))
			s := start(t, mcp.Config{Version: "test", Agent: c, ProbeOnConnect: true})
			s.exchange(t, `{"jsonrpc":"2.0","id":1,"method":"ping"}`, `{"jsonrpc":"2.0","id":1,"result":{}}`)
			select {
			case <-reached:
				t.Fatal("uninitialized legacy ping consumed the probe credential")
			case <-time.After(100 * time.Millisecond):
			}
			s.exchange(t, tc.request, tc.response)
			select {
			case <-reached:
			case <-time.After(5 * time.Second):
				t.Fatal("first compliant request did not probe")
			}
			s.exchange(t, tc.request, tc.response)
			select {
			case <-reached:
				t.Fatal("later request started a second automatic probe")
			case <-time.After(100 * time.Millisecond):
			}
		})
	}
}
