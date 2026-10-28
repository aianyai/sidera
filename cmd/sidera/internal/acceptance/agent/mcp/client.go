// Package mcp drives a real MCP process as a test agent, without a server SDK.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
)

// Client implements the scenario agent over an independently encoded MCP wire.
// The caller starts the process and owns waiting for it after Close.
type Client struct {
	input   io.WriteCloser
	output  io.ReadCloser
	era     string
	write   sync.Mutex
	mu      sync.Mutex
	next    int
	pending map[int]chan response
	ended   error
}
type response struct {
	result json.RawMessage
	err    error
}

// New takes the process pipes and negotiates exactly the requested era.
func New(ctx context.Context, input io.WriteCloser, output io.ReadCloser, era string) (*Client, error) {
	if era != "2025-06-18" && era != "2026-07-28" {
		return nil, errors.New("mcp test client: unsupported era")
	}
	c := &Client{input: input, output: output, era: era, pending: make(map[int]chan response)}
	go c.read()
	method := "server/discover"
	params := map[string]any{}
	if era == "2025-06-18" {
		method = "initialize"
		params = map[string]any{"protocolVersion": era, "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "sidera-test", "version": "1"}}
	}
	raw, err := c.request(ctx, method, params)
	if err != nil {
		c.Close()
		return nil, err
	}
	var initialized struct {
		ProtocolVersion   string   `json:"protocolVersion"`
		SupportedVersions []string `json:"supportedVersions"`
	}
	if err := json.Unmarshal(raw, &initialized); err != nil {
		c.Close()
		return nil, err
	}
	if era == "2025-06-18" {
		if initialized.ProtocolVersion != era {
			c.Close()
			return nil, fmt.Errorf("mcp test client: negotiated %q", initialized.ProtocolVersion)
		}
		if err := c.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}); err != nil {
			c.Close()
			return nil, err
		}
	} else if len(initialized.SupportedVersions) != 1 || initialized.SupportedVersions[0] != era {
		c.Close()
		return nil, errors.New("mcp test client: unexpected supported versions")
	}
	return c, nil
}

// Close ends pending calls and closes the process's streams.
func (c *Client) Close() error {
	c.finish(io.EOF)
	return errors.Join(c.input.Close(), c.output.Close())
}
func (c *Client) finish(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ended != nil {
		return
	}
	c.ended = err
	for id, ch := range c.pending {
		ch <- response{err: err}
		delete(c.pending, id)
	}
}
func (c *Client) read() {
	r := bufio.NewReader(c.output)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			c.finish(err)
			return
		}
		var frame struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
			Method string `json:"method"`
		}
		if err := json.Unmarshal(line, &frame); err != nil {
			c.finish(err)
			return
		}
		if frame.Method != "" {
			c.finish(errors.New("mcp test client: server initiated a message"))
			return
		}
		answer := response{result: frame.Result}
		if frame.Error != nil {
			answer.err = fmt.Errorf("MCP %d: %s", frame.Error.Code, frame.Error.Message)
		}
		c.mu.Lock()
		ch := c.pending[frame.ID]
		delete(c.pending, frame.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- answer
		}
	}
}
func (c *Client) send(frame any) error {
	c.write.Lock()
	defer c.write.Unlock()
	enc := json.NewEncoder(c.input)
	enc.SetEscapeHTML(false)
	return enc.Encode(frame)
}
func (c *Client) request(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	if c.era == "2026-07-28" {
		params["_meta"] = map[string]any{"io.modelcontextprotocol/protocolVersion": c.era, "io.modelcontextprotocol/clientCapabilities": map[string]any{}}
	}
	c.mu.Lock()
	if c.ended != nil {
		err := c.ended
		c.mu.Unlock()
		return nil, err
	}
	c.next++
	id := c.next
	ch := make(chan response, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	if err := c.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		c.finish(err)
		return nil, err
	}
	select {
	case answer := <-ch:
		return answer.result, answer.err
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		_ = c.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/cancelled", "params": map[string]any{"requestId": id}})
		return nil, ctx.Err()
	}
}
func (c *Client) call(ctx context.Context, name string, args map[string]any, out any) ([]byte, error) {
	raw, err := c.request(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return nil, err
	}
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError    bool   `json:"isError"`
		ResultType string `json:"resultType"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	if len(result.Content) != 1 || result.Content[0].Type != "text" || (c.era == "2026-07-28" && result.ResultType != "complete") {
		return nil, errors.New("mcp test client: invalid tool result")
	}
	text := []byte(result.Content[0].Text)
	if result.IsError {
		var body struct {
			Error struct {
				Category fakeagent.Category `json:"category"`
				Code     string             `json:"code"`
				Message  string             `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(text, &body); err != nil {
			return nil, err
		}
		return nil, &fakeagent.Error{Category: body.Error.Category, Code: body.Error.Code, Message: body.Error.Message}
	}
	if out != nil {
		if err := json.Unmarshal(text, out); err != nil {
			return nil, err
		}
	}
	return text, nil
}
func optional(args map[string]any, key string, value any, include bool) {
	if include {
		args[key] = value
	}
}
