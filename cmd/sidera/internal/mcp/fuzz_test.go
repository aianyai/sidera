package mcp_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/aianyai/sidera/cmd/sidera/internal/mcp"
)

func FuzzMCPInputs(f *testing.F) {
	for i, raw := range []string{`{}`, `{"paused":false}`, `{"wait_s":0}`, `{"to":["endpoint"],"body":"中文 English"}`, `{"items":[]}`, `{"future":{"key":1,"key":2}}`, `{"requestId":1,"reason":"done"}`, `{"requestId":null}`, `{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}`} {
		f.Add([]byte(raw), uint8(i))
	}
	f.Fuzz(func(t *testing.T, raw []byte, choice uint8) {
		if len(raw) > 8192 || !json.Valid(raw) {
			t.Skip()
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, raw); err != nil {
			t.Fatal(err)
		}
		s := start(t, mcp.Config{Version: "fuzz"})
		switch choice % 3 {
		case 0:
			names := []string{"me", "declare", "peers", "directory", "send", "reply", "recv", "ack", "status", "control", "probe"}
			fmt.Fprintf(s.input, "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{\"name\":%q,\"arguments\":%s,\"_meta\":{\"io.modelcontextprotocol/protocolVersion\":\"2026-07-28\",\"io.modelcontextprotocol/clientCapabilities\":{}}}}\n", names[int(choice)%len(names)], compact.Bytes())
		case 1:
			fmt.Fprintf(s.input, "{\"jsonrpc\":\"2.0\",\"method\":\"notifications/cancelled\",\"params\":%s}\n", compact.Bytes())
			fmt.Fprintln(s.input, `{"jsonrpc":"2.0","id":1,"method":"ping"}`)
		case 2:
			fmt.Fprintf(s.input, "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"server/discover\",\"params\":%s}\n", compact.Bytes())
		}
		line, err := s.output.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if !json.Valid([]byte(line)) || strings.Contains(line, `"method":`) {
			t.Fatalf("invalid server reply: %s", line)
		}
	})
}
