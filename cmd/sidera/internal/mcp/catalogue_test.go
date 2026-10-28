package mcp_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/aianyai/sidera/cmd/sidera/internal/mcp"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestToolCatalogueUsesCommonSchemasAndVersionedResults(t *testing.T) {
	for _, era := range []string{"legacy", "modern"} {
		t.Run(era, func(t *testing.T) {
			s := start(t, mcp.Config{Version: "test"})
			if era == "legacy" {
				s.exchange(t, `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{}}}`, `{"jsonrpc":"2.0","id":0,"result":{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"sidera","version":"test"},"instructions":"Use me to inspect your endpoint and receive limits. Receive pages must be read before acknowledging them."}}`)
			}
			params := `{}`
			if era == "modern" {
				params = `{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}`
			}
			fmt.Fprintf(s.input, "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/list\",\"params\":%s}\n", params)
			raw, err := s.output.ReadBytes('\n')
			if err != nil {
				t.Fatal(err)
			}
			var response struct {
				Result struct {
					Tools []struct {
						Name        string          `json:"name"`
						Description string          `json:"description"`
						Schema      map[string]any  `json:"inputSchema"`
						Annotations map[string]bool `json:"annotations"`
					} `json:"tools"`
					ResultType string         `json:"resultType"`
					TTL        int            `json:"ttlMs"`
					CacheScope string         `json:"cacheScope"`
					Meta       map[string]any `json:"_meta"`
				} `json:"result"`
			}
			if err := json.Unmarshal(raw, &response); err != nil {
				t.Fatal(err)
			}
			result := response.Result
			if era == "modern" {
				if result.ResultType != "complete" || result.TTL != 3600000 || result.CacheScope != "public" || result.Meta["io.modelcontextprotocol/serverInfo"] == nil {
					t.Fatalf("modern result: %s", raw)
				}
			} else if result.ResultType != "" || result.Meta != nil {
				t.Fatalf("legacy result: %s", raw)
			}
			var names []string
			for _, tool := range result.Tools {
				names = append(names, tool.Name)
				if tool.Description == "" || tool.Schema["$schema"] != nil || tool.Schema["$ref"] != nil || tool.Schema["type"] != "object" || tool.Schema["additionalProperties"] != false {
					t.Fatalf("tool schema: %+v", tool)
				}
				for _, draft := range []*jsonschema.Draft{jsonschema.Draft7, jsonschema.Draft2020} {
					c := jsonschema.NewCompiler()
					c.DefaultDraft(draft)
					if err := c.AddResource("urn:tool", tool.Schema); err != nil {
						t.Fatal(err)
					}
					if _, err := c.Compile("urn:tool"); err != nil {
						t.Fatalf("%s schema: %v", tool.Name, err)
					}
				}
				switch tool.Name {
				case "me", "peers", "directory", "status":
					if !tool.Annotations["readOnlyHint"] {
						t.Fatalf("%s missing read-only hint", tool.Name)
					}
				case "probe":
					if value, ok := tool.Annotations["readOnlyHint"]; !ok || value {
						t.Fatal("probe must explicitly be mutable")
					}
				case "declare", "ack":
					if !tool.Annotations["idempotentHint"] {
						t.Fatalf("%s missing idempotent hint", tool.Name)
					}
				}
				if tool.Name == "send" {
					to := tool.Schema["properties"].(map[string]any)["to"].(map[string]any)
					if _, ok := to["maxItems"]; ok {
						t.Fatal("recipient limit belongs to Agent interface")
					}
				}
			}
			want := []string{"me", "declare", "peers", "directory", "send", "reply", "recv", "ack", "status", "control", "probe"}
			if !reflect.DeepEqual(names, want) {
				t.Fatalf("tools: %v", names)
			}
		})
	}
}
