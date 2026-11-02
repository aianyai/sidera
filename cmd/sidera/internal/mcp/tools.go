package mcp

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"maps"
	"math/big"
	"slices"
	"strconv"
	"strings"

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

// project validates raw against the schema and returns what the Agent interface
// is sent: the advertised properties only. A property the schema does not
// advertise is an error where the schema says additionalProperties is false, which
// every tool's schema does, so that a model that misspells an argument is told
// instead of having it dropped. owner names the object in that message: the tool
// at the root, the path of the value below it.
// Unknown values are scanned for duplicate keys before this function is called.
func (s schema) project(raw json.RawMessage, path, owner string) (json.RawMessage, error) {
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
		if s.AdditionalProperties != nil && !*s.AdditionalProperties {
			if err := s.rejectUnknown(fields, path, owner); err != nil {
				return nil, err
			}
		}
		for _, key := range s.Required {
			if _, ok := fields[key]; !ok {
				return nil, apiwire.Malformed("a required tool argument is missing", join(path, key))
			}
		}
		known := make(map[string]json.RawMessage)
		for key, child := range s.Properties {
			if value, ok := fields[key]; ok {
				at := join(path, key)
				projected, err := child.project(value, at, at)
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
			at := fmt.Sprintf("%s[%d]", path, i)
			projected, err := s.Items.project(value, at, at)
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
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var decoded any
		if decoder.Decode(&decoded) != nil {
			return bad()
		}
		number, ok := decoded.(json.Number)
		if !ok {
			return bad()
		}
		// JSON Schema integers describe values, including decimal and exponent
		// spellings. Keep the value exact before the Agent's int64 decoding.
		value, ok := new(big.Rat).SetString(number.String())
		if !ok || !value.IsInt() || !value.Num().IsInt64() {
			return bad()
		}
		integer := value.Num().Int64()
		if (s.Minimum != nil && integer < *s.Minimum) || (s.Maximum != nil && integer > *s.Maximum) {
			return bad()
		}
		return json.RawMessage(value.Num().String()), nil
	default:
		panic("mcp: unsupported schema type")
	}
	return raw, nil
}

// rejectUnknown fails when fields holds a member the schema does not advertise.
// The message names every such member and lists the accepted ones, so the caller
// can correct the call from the answer alone; the error's path is the first
// unknown member, in name order.
func (s schema) rejectUnknown(fields map[string]json.RawMessage, path, owner string) error {
	var unknown []string
	for key := range fields {
		if _, ok := s.Properties[key]; !ok {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	slices.Sort(unknown)
	accepted := slices.Sorted(maps.Keys(s.Properties))
	accepts := "accepts no arguments"
	if len(accepted) > 0 {
		accepts = "accepts: " + strings.Join(accepted, ", ")
	}
	noun := "argument"
	if len(unknown) > 1 {
		noun = "arguments"
	}
	quoted := make([]string, len(unknown))
	for i, key := range unknown {
		quoted[i] = strconv.Quote(key)
	}
	return apiwire.Malformed(fmt.Sprintf("unknown %s %s; %s %s", noun, strings.Join(quoted, ", "), owner, accepts), join(path, unknown[0]))
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
	projected, err := selected.InputSchema.project(args, "", name)
	if err != nil {
		return s.toolResult(nil, err, modern, "")
	}
	if s.cfg.Agent == nil {
		return s.toolResult(nil, apiwire.New(apiwire.CategoryUnavailable, "daemon_not_running", "the node is not running"), modern, "")
	}
	var notice string
	if s.controller != nil {
		arrival, err := s.controller.Ensure(ctx, codexThread(fields))
		if err != nil {
			return s.toolResult(nil, err, modern, "")
		}
		if arrival != nil {
			notice = arrival.Notice()
		}
	}
	result, err := s.invoke(ctx, name, projected)
	if s.controller != nil {
		s.controller.Rejected(err)
	}
	return s.toolResult(result, err, modern, notice)
}

// codexThread is the thread id Codex puts into the metadata of every tool call, or
// empty when the call carries none. The key is Codex's own and not a protocol
// field, so only this member is read and anything unexpected reads as absent. The
// value is an object or, in some versions, the same object as a JSON string.
func codexThread(fields map[string]json.RawMessage) string {
	var meta map[string]json.RawMessage
	if json.Unmarshal(fields["_meta"], &meta) != nil {
		return ""
	}
	raw := meta[codexTurnMetadataKey]
	var encoded string
	if json.Unmarshal(raw, &encoded) == nil {
		raw = json.RawMessage(encoded)
	}
	var turn struct {
		ThreadID string `json:"thread_id"`
	}
	if json.Unmarshal(raw, &turn) != nil {
		return ""
	}
	return turn.ThreadID
}

// toolResult is the one text item of a call; a notice adds a second item, which
// a controller's model reads on its first use.
func (s *server) toolResult(result any, err error, modern bool, notice string) (any, error) {
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
	content := []map[string]string{{"type": "text", "text": string(text)}}
	if notice != "" {
		content = append(content, map[string]string{"type": "text", "text": notice})
	}
	return s.decorate(map[string]any{"content": content, "isError": isError}, modern, false), nil
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
