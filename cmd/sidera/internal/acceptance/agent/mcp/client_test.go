package mcp_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"testing"

	"github.com/aianyai/sidera/cmd/sidera/internal/acceptance/agent/mcp"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
)

func TestIndependentExchangesPreservePagesAndToolErrors(t *testing.T) {
	for _, era := range []string{"2025-06-18", "2026-07-28"} {
		t.Run(era, func(t *testing.T) {
			in, input := io.Pipe()
			output, out := io.Pipe()
			t.Cleanup(func() { input.Close(); output.Close(); in.Close(); out.Close() })
			serverDone := make(chan struct{})
			go func() {
				defer close(serverDone)
				reader := bufio.NewReader(in)
				read := func(want string) {
					raw, err := reader.ReadBytes('\n')
					if err != nil {
						t.Error(err)
						return
					}
					var a, b any
					json.Unmarshal(raw, &a)
					json.Unmarshal([]byte(want), &b)
					if !reflect.DeepEqual(a, b) {
						t.Errorf("request:\n%s\nwant:\n%s", raw, want)
					}
				}
				if era == "2025-06-18" {
					read(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"sidera-test","version":"1"}}}`)
					fmt.Fprintln(out, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"sidera","version":"test"}}}`)
					read(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
					read(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"recv","arguments":{"wait_s":0}}}`)
					fmt.Fprintln(out, `{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"{ \"lease\":\"a-lease\", \"messages\":[], \"more\":0 }\n"}],"isError":false}}`)
					read(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"status","arguments":{"message_id":""}}}`)
					fmt.Fprintln(out, `{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"{\"error\":{\"category\":\"not_found\",\"code\":\"message_unknown\",\"message\":\"unknown message\"}}\n"}],"isError":true}}`)
				} else {
					read(`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`)
					fmt.Fprintln(out, `{"jsonrpc":"2.0","id":1,"result":{"resultType":"complete","supportedVersions":["2026-07-28"],"capabilities":{"tools":{}},"ttlMs":3600000,"cacheScope":"public"}}`)
					read(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"recv","arguments":{"wait_s":0},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`)
					fmt.Fprintln(out, `{"jsonrpc":"2.0","id":2,"result":{"resultType":"complete","content":[{"type":"text","text":"{ \"lease\":\"a-lease\", \"messages\":[], \"more\":0 }\n"}],"isError":false}}`)
					read(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"status","arguments":{"message_id":""},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`)
					fmt.Fprintln(out, `{"jsonrpc":"2.0","id":3,"result":{"resultType":"complete","content":[{"type":"text","text":"{\"error\":{\"category\":\"not_found\",\"code\":\"message_unknown\",\"message\":\"unknown message\"}}\n"}],"isError":true}}`)
				}
			}()
			client, err := mcp.New(context.Background(), input, output, era)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			page, err := client.Recv(context.Background(), fakeagent.RecvInput{})
			if err != nil || string(page.Raw) != "{ \"lease\":\"a-lease\", \"messages\":[], \"more\":0 }\n" {
				t.Fatalf("receive: %q %v", page.Raw, err)
			}
			_, err = client.Status(context.Background(), fakeagent.StatusInput{})
			if fakeagent.CategoryOf(err) != fakeagent.CategoryNotFound || fakeagent.CodeOf(err) != "message_unknown" {
				t.Fatalf("status: %v", err)
			}
			<-serverDone
		})
	}
}
