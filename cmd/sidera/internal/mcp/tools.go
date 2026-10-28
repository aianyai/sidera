package mcp

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/sdk/go/peer"
	"github.com/aianyai/sidera/sdk/go/strictjson"
)

// The advertised schemas are also the argument validator's only rules.
//
//go:embed tools.json
var catalogueJSON []byte

type schema struct {
	Type                 string            `json:"type"`
	Properties           map[string]schema `json:"properties,omitempty"`
	Required             []string          `json:"required,omitempty"`
	Items                *schema           `json:"items,omitempty"`
	Enum                 []string          `json:"enum,omitempty"`
	Minimum              *int64            `json:"minimum,omitempty"`
	Maximum              *int64            `json:"maximum,omitempty"`
	MinItems             int               `json:"minItems,omitempty"`
	AdditionalProperties *bool             `json:"additionalProperties,omitempty"`
}
type tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema schema          `json:"inputSchema"`
	Annotations map[string]bool `json:"annotations,omitempty"`
}

var catalogue = func() []tool {
	var tools []tool
	if err := json.Unmarshal(catalogueJSON, &tools); err != nil {
		panic(err)
	}
	return tools
}()

// project applies only advertised properties, keeping future extensions opaque.
// Unknown values are scanned for duplicate keys before this function is called.
func (s schema) project(raw json.RawMessage, path string) (json.RawMessage, error) {
	bad := func() (json.RawMessage, error) {
		return nil, apiwire.Malformed("tool arguments do not match the input schema", path)
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return bad()
	}
	switch s.Type {
	case "object":
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil || fields == nil {
			return bad()
		}
		for _, key := range s.Required {
			if _, ok := fields[key]; !ok {
				return nil, apiwire.Malformed("a required tool argument is missing", join(path, key))
			}
		}
		known := make(map[string]json.RawMessage)
		for key, child := range s.Properties {
			if value, ok := fields[key]; ok {
				projected, err := child.project(value, join(path, key))
				if err != nil {
					return nil, err
				}
				known[key] = projected
			}
		}
		return json.Marshal(known)
	case "array":
		var values []json.RawMessage
		if json.Unmarshal(raw, &values) != nil || values == nil || len(values) < s.MinItems {
			return bad()
		}
		for i, value := range values {
			projected, err := s.Items.project(value, fmt.Sprintf("%s[%d]", path, i))
			if err != nil {
				return nil, err
			}
			values[i] = projected
		}
		return json.Marshal(values)
	case "string":
		var value string
		if json.Unmarshal(raw, &value) != nil {
			return bad()
		}
		if len(s.Enum) > 0 {
			found := false
			for _, choice := range s.Enum {
				found = found || value == choice
			}
			if !found {
				return bad()
			}
		}
	case "boolean":
		var value bool
		if json.Unmarshal(raw, &value) != nil {
			return bad()
		}
	case "integer":
		var value int64
		if json.Unmarshal(raw, &value) != nil || (s.Minimum != nil && value < *s.Minimum) || (s.Maximum != nil && value > *s.Maximum) {
			return bad()
		}
	default:
		panic("mcp: unsupported schema type")
	}
	return raw, nil
}
func join(parent, key string) string {
	if parent == "" {
		return key
	}
	return parent + "." + key
}

func (s *server) callTool(ctx context.Context, fields map[string]json.RawMessage, modern bool) (any, error) {
	var name string
	if strictjson.Decode(fields["name"], &name) != nil {
		return nil, peer.NewError(-32602, "tool name must be a string")
	}
	var selected *tool
	for i := range catalogue {
		if catalogue[i].Name == name {
			selected = &catalogue[i]
			break
		}
	}
	if selected == nil {
		return nil, peer.NewError(-32602, "unknown tool")
	}
	args, ok := fields["arguments"]
	if !ok {
		args = json.RawMessage("{}")
	}
	projected, err := selected.InputSchema.project(args, "")
	if err != nil {
		return s.toolResult(nil, err, modern)
	}
	if s.cfg.Agent == nil {
		return s.toolResult(nil, apiwire.New(apiwire.CategoryUnavailable, "daemon_not_running", "the node is not running"), modern)
	}
	result, err := s.invoke(ctx, name, projected)
	return s.toolResult(result, err, modern)
}
func (s *server) toolResult(result any, err error, modern bool) (any, error) {
	var text []byte
	isError := err != nil
	if isError {
		result = apiwire.Body{Error: *apiwire.FromError(err)}
	}
	if raw, ok := result.([]byte); ok {
		text = raw
	} else {
		var out bytes.Buffer
		enc := json.NewEncoder(&out)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(result); err != nil {
			return nil, err
		}
		text = out.Bytes()
	}
	return s.decorate(map[string]any{"content": []map[string]string{{"type": "text", "text": string(text)}}, "isError": isError}, modern, false), nil
}
func (s *server) invoke(ctx context.Context, name string, args json.RawMessage) (any, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(args, &fields); err != nil {
		return nil, err
	}
	if name == "recv" || name == "probe" {
		fields["frontend"] = json.RawMessage(`"mcp"`)
	}
	// Keep explicit empty values present. The Agent client owns encoding,
	// credentials, deadlines and successful result validation.
	return s.cfg.Agent.Call(ctx, name, fields)
}
