package mcp_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/cmd/sidera/internal/mcp"
)

func TestProtocolFailureGoldens(t *testing.T) {
	for _, tc := range []struct{ name, request, response string }{
		{"uninitialized list", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"send initialize first, or carry io.modelcontextprotocol/protocolVersion in _meta"}}`},
		{"missing capabilities", `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`, `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"client capabilities must be an object"}}`},
		{"unsupported version", `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2099-01-01","io.modelcontextprotocol/clientCapabilities":{}}}}`, `{"jsonrpc":"2.0","id":1,"error":{"code":-32022,"message":"unsupported protocol version","data":{"supported":["2026-07-28"],"requested":"2099-01-01"}}}`},
		{"wrong version type", `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":12,"io.modelcontextprotocol/clientCapabilities":{}}}}`, `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"protocol version must be a string"}}`},
		{"uninitialized unknown method", `{"jsonrpc":"2.0","id":1,"method":"unknown"}`, `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"send initialize first, or carry io.modelcontextprotocol/protocolVersion in _meta"}}`},
		{"unknown method unsupported version", `{"jsonrpc":"2.0","id":1,"method":"unknown","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2099-01-01","io.modelcontextprotocol/clientCapabilities":{}}}}`, `{"jsonrpc":"2.0","id":1,"error":{"code":-32022,"message":"unsupported protocol version","data":{"supported":["2026-07-28"],"requested":"2099-01-01"}}}`},
		{"unknown method missing capabilities", `{"jsonrpc":"2.0","id":1,"method":"unknown","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`, `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"client capabilities must be an object"}}`},
		{"unknown modern method", `{"jsonrpc":"2.0","id":1,"method":"unknown","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`, `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"method not found"}}`},
		{"duplicate frame key", `{"jsonrpc":"2.0","id":1,"id":2,"method":"ping"}`, `{"jsonrpc":"2.0","id":null,"error":{"code":-32600,"message":"duplicate member or invalid Unicode"}}`},
		{"duplicate ignored key", `{"jsonrpc":"2.0","id":1,"method":"ping","params":{"future":{"value":1,"value":2}}}`, `{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"duplicate member or invalid Unicode"}}`},
		{"ordinary parameter shape error", `{"jsonrpc":"2.0","id":1,"method":"ping","params":[]}`, `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"parameters must be an unambiguous object"}}`},
		{"unknown tool", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"unknown","_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`, `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"unknown tool"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) { s := start(t, mcp.Config{Version: "test"}); s.exchange(t, tc.request, tc.response) })
	}
}

func TestCancellationAndEOFEndTheAgentRequest(t *testing.T) {
	for _, end := range []string{"cancel notification", "stdin EOF", "Run context"} {
		t.Run(end, func(t *testing.T) {
			entered, cancelled := make(chan struct{}), make(chan struct{})
			c := httpAgent(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				close(entered)
				<-r.Context().Done()
				close(cancelled)
			}))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			in, input := io.Pipe()
			output, out := io.Pipe()
			done := make(chan error, 1)
			go func() {
				done <- mcp.Run(ctx, mcp.Config{Stdin: in, Stdout: out, Stderr: io.Discard, Agent: c, Version: "test"})
			}()
			t.Cleanup(func() { cancel(); input.Close(); output.Close() })
			// Read all output while writes proceed; silent cancellation must leave it empty.
			frames := make(chan []byte, 1)
			go func() { raw, _ := io.ReadAll(output); frames <- raw }()
			fmt.Fprintln(input, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"recv","arguments":{"wait_s":30},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`)
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("Agent request did not start")
			}
			switch end {
			case "cancel notification":
				// A malformed or unknown cancellation is ignored; the valid one targets the request.
				fmt.Fprintln(input, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":99}}`)
				fmt.Fprintln(input, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1,"reason":"finished","_meta":{"future":true}}}`)
			case "stdin EOF":
				input.Close()
			case "Run context":
				cancel()
			}
			select {
			case <-cancelled:
			case <-time.After(5 * time.Second):
				t.Fatal("Agent request context was not cancelled")
			}
			if end == "cancel notification" {
				input.Close()
			}
			select {
			case err := <-done:
				if end != "Run context" && err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Run did not end")
			}
			if raw := <-frames; len(raw) != 0 {
				t.Fatalf("cancelled request was answered: %s", raw)
			}
		})
	}
}

func TestOversizedInputUsesTheMCPErrorAndCloses(t *testing.T) {
	s := start(t, mcp.Config{Version: "test"})
	// This checks only the MCP dialect's selected code and closure, not framing's matrix.
	go fmt.Fprintln(s.input, strings.Repeat("x", 4*1024*1024+1))
	line, err := s.output.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(line, `"id":null`) || !strings.Contains(line, `"code":-32600`) {
		t.Fatalf("oversized input: %s", line)
	}
	if _, err := s.output.ReadByte(); err != io.EOF {
		t.Fatalf("after oversized input: %v", err)
	}
}
