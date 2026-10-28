package mcp_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/aianyai/sidera/cmd/sidera/internal/mcp"
)

type session struct {
	input  *io.PipeWriter
	output *bufio.Reader
	done   chan error
}

func start(t *testing.T, cfg mcp.Config) *session {
	t.Helper()
	in, input := io.Pipe()
	output, out := io.Pipe()
	cfg.Stdin, cfg.Stdout = in, out
	if cfg.Stderr == nil {
		cfg.Stderr = io.Discard
	}
	s := &session{input, bufio.NewReader(output), make(chan error, 1)}
	go func() { s.done <- mcp.Run(context.Background(), cfg) }()
	t.Cleanup(func() {
		input.Close()
		output.Close()
		select {
		case <-s.done:
		case <-time.After(5 * time.Second):
			t.Error("MCP did not stop")
		}
	})
	return s
}
func (s *session) exchange(t *testing.T, request, want string) {
	t.Helper()
	fmt.Fprintln(s.input, request)
	got, err := s.output.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	var a, b any
	if err := json.Unmarshal([]byte(got), &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &b); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("response:\n%s\nwant:\n%s", got, want)
	}
}
func TestLegacyInitializeGolden(t *testing.T) {
	s := start(t, mcp.Config{Version: "test"})
	s.exchange(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"sidera","version":"test"},"instructions":"Use me to inspect your endpoint and receive limits. Receive pages must be read before acknowledging them."}}`)
	s.exchange(t, `{"jsonrpc":"2.0","id":2,"method":"ping"}`, `{"jsonrpc":"2.0","id":2,"result":{}}`)
	s.exchange(t, `{"jsonrpc":"2.0","id":3,"method":"server/discover"}`, `{"jsonrpc":"2.0","id":3,"error":{"code":-32601,"message":"method not found"}}`)
	s.exchange(t, `{"jsonrpc":"2.0","id":4,"method":"unknown"}`, `{"jsonrpc":"2.0","id":4,"error":{"code":-32601,"message":"method not found"}}`)
}

func TestModernDiscoveryGolden(t *testing.T) {
	s := start(t, mcp.Config{Version: "test"})
	s.exchange(t, `{"jsonrpc":"2.0","id":"before","method":"server/discover"}`, `{"jsonrpc":"2.0","id":"before","error":{"code":-32602,"message":"send initialize first, or carry io.modelcontextprotocol/protocolVersion in _meta"}}`)
	s.exchange(t, `{"jsonrpc":"2.0","id":"discover","method":"server/discover","extension":true,"params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{},"extension":[1]},"extension":false}}`, `{"jsonrpc":"2.0","id":"discover","result":{"resultType":"complete","supportedVersions":["2026-07-28"],"capabilities":{"tools":{}},"instructions":"Use me to inspect your endpoint and receive limits. Receive pages must be read before acknowledging them.","ttlMs":3600000,"cacheScope":"public","_meta":{"io.modelcontextprotocol/serverInfo":{"name":"sidera","version":"test"}}}}`)
	s.exchange(t, `{"jsonrpc":"2.0","id":"after","method":"server/discover"}`, `{"jsonrpc":"2.0","id":"after","error":{"code":-32602,"message":"send initialize first, or carry io.modelcontextprotocol/protocolVersion in _meta"}}`)
}
