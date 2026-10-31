package claudeargs

import (
	"bytes"
	"encoding/json"
	"errors"
)

// UserLine returns the one line of stream-json input a turn process reads: a
// user message with the given uuid. Claude Code writes the uuid into the
// session record as the uuid of the user record, which is how the prompt is
// found there. The line ends with a newline.
func UserLine(uuid, text string) ([]byte, error) {
	if uuid == "" || text == "" {
		return nil, errors.New("claudeargs: a user message needs a uuid and a text")
	}
	type block struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	type message struct {
		Role    string  `json:"role"`
		Content []block `json:"content"`
	}
	line := struct {
		Type            string  `json:"type"`
		UUID            string  `json:"uuid"`
		SessionID       string  `json:"session_id"`
		ParentToolUseID *string `json:"parent_tool_use_id"`
		Message         message `json:"message"`
	}{Type: "user", UUID: uuid, Message: message{Role: "user", Content: []block{{Type: "text", Text: text}}}}
	return encode(line)
}

// MCPConfig returns the content of the MCP configuration file of an instance:
// the sidera server, started as `<sidera> mcp`. The file holds no token: the
// server inherits the environment of the turn process, which has it.
func MCPConfig(siderPath string) ([]byte, error) {
	if siderPath == "" {
		return nil, errors.New("claudeargs: no sidera executable")
	}
	type server struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	cfg := struct {
		MCPServers map[string]server `json:"mcpServers"`
	}{map[string]server{"sidera": {Command: siderPath, Args: []string{"mcp"}}}}
	return encode(cfg)
}

// encode writes v as one JSON line, leaving <, > and & as they are.
func encode(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
