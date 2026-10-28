// Package mcp serves the Agent tools over MCP stdio.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/aianyai/sidera/internal/node/agentapi"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
	"github.com/aianyai/sidera/sdk/go/strictjson"
)

const legacyVersion = "2025-06-18"
const modernVersion = "2026-07-28"
const versionKey = "io.modelcontextprotocol/protocolVersion"
const capabilitiesKey = "io.modelcontextprotocol/clientCapabilities"
const instructions = "Use me to inspect your endpoint and receive limits. Receive pages must be read before acknowledging them."

// Config borrows Agent and Stderr. Run owns and closes Stdin and Stdout.
// Its caller resolves environment, credentials and the node's home.
type Config struct {
	Stdin          io.ReadCloser
	Stdout         io.WriteCloser
	Stderr         io.Writer
	Agent          *agentapi.Client
	Version        string
	ProbeOnConnect bool
}

type server struct {
	cfg         Config
	initialized atomic.Bool
	probe       sync.Once
	ctx         context.Context
	log         *slog.Logger
}

// Run serves until stdin closes, the context ends or a transport fails.
// Tool and protocol failures are replies, not process failures.
func Run(ctx context.Context, cfg Config) error {
	if cfg.Stdin == nil || cfg.Stdout == nil || cfg.Stderr == nil {
		return errors.New("mcp: streams are required")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	logger := slog.New(slog.NewTextHandler(cfg.Stderr, nil))
	s := &server{cfg: cfg, ctx: ctx, log: logger}
	p, err := peer.New(cfg.Stdin, cfg.Stdout, peer.Config{
		Logger: logger,
		Dialect: peer.Dialect{
			RejectDuplicateKeysAndInvalidUnicode: true,
			CancelMethod:                         "notifications/cancelled",
			CancelTarget:                         cancelTarget,
			SilentOnCancel:                       true,
			OverloadedCode:                       -32603,
			LineTooLongCode:                      -32600,
		},
	})
	if err != nil {
		return err
	}
	defer p.Close()
	p.HandleDefault(s.handle)
	p.HandleNotification("notifications/initialized", func(context.Context, json.RawMessage) {})
	p.Start()
	select {
	case <-ctx.Done():
		p.Close()
		return ctx.Err()
	case <-p.Done():
		if errors.Is(p.Err(), peer.ErrRemoteClosed) {
			return nil
		}
		return p.Err()
	}
}

func (s *server) handle(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	modern, fields, err := s.era(method, raw)
	if err != nil {
		return nil, err
	}
	if method == "initialize" && modern {
		return nil, peer.NewError(-32601, "method not found")
	}
	if method == "initialize" {
		var in struct {
			ProtocolVersion string                     `json:"protocolVersion"`
			Capabilities    map[string]json.RawMessage `json:"capabilities"`
			ClientInfo      map[string]json.RawMessage `json:"clientInfo"`
		}
		if err := strictjson.DecodeOpen(raw, &in); err != nil {
			return nil, peer.NewError(-32602, "invalid initialize parameters")
		}
		s.initialized.Store(true)
	}
	if method == "initialize" || modern {
		s.probe.Do(func() {
			if s.cfg.ProbeOnConnect && s.cfg.Agent != nil {
				go func() {
					result, err := s.cfg.Agent.Probe(s.ctx, adapterproto.FrontendMCP)
					if err != nil {
						s.log.Warn("MCP probe failed", "error", err)
						return
					}
					s.log.Info("MCP probe succeeded", "preset", result.Preset, "frontend", result.Frontend)
				}()
			}
		})
	}
	switch method {
	case "ping":
		return struct{}{}, nil
	case "initialize":
		return map[string]any{"protocolVersion": legacyVersion, "capabilities": map[string]any{"tools": struct{}{}}, "serverInfo": s.info(), "instructions": instructions}, nil
	case "server/discover":
		if !modern {
			return nil, peer.NewError(-32601, "method not found")
		}
		return s.decorate(map[string]any{"supportedVersions": []string{modernVersion}, "capabilities": map[string]any{"tools": struct{}{}}, "instructions": instructions}, true, true), nil
	}
	if method == "tools/list" {
		return s.decorate(map[string]any{"tools": catalogue}, modern, true), nil
	}
	if method == "tools/call" {
		return s.callTool(ctx, fields, modern)
	}
	return nil, peer.NewError(-32601, "method not found")
}

func (s *server) info() map[string]string {
	return map[string]string{"name": "sidera", "version": s.cfg.Version}
}
func (s *server) decorate(result map[string]any, modern, cache bool) map[string]any {
	if modern {
		result["resultType"] = "complete"
		result["_meta"] = map[string]any{"io.modelcontextprotocol/serverInfo": s.info()}
		if cache {
			result["ttlMs"] = 3600000
			result["cacheScope"] = "public"
		}
	}
	return result
}

// Read the parameter object without imposing a schema on unknown values.
// The peer has already checked the whole frame for ambiguous JSON.
func (s *server) era(method string, raw json.RawMessage) (bool, map[string]json.RawMessage, error) {
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	var fields map[string]json.RawMessage
	if err := strictjson.Decode(raw, &fields); err != nil || fields == nil {
		return false, nil, peer.NewError(-32602, "parameters must be an unambiguous object")
	}
	var meta map[string]json.RawMessage
	if value, ok := fields["_meta"]; ok {
		if err := strictjson.Decode(value, &meta); err != nil || meta == nil {
			return false, nil, peer.NewError(-32602, "_meta must be an object")
		}
	}
	if version, ok := meta[versionKey]; ok {
		var requested string
		if err := strictjson.Decode(version, &requested); err != nil {
			return false, nil, peer.NewError(-32602, "protocol version must be a string")
		}
		if requested != modernVersion {
			return false, nil, peer.NewError(-32022, "unsupported protocol version").WithData(map[string]any{"supported": []string{modernVersion}, "requested": requested})
		}
		var capabilities map[string]json.RawMessage
		if err := strictjson.Decode(meta[capabilitiesKey], &capabilities); err != nil || capabilities == nil {
			return false, nil, peer.NewError(-32602, "client capabilities must be an object")
		}
		return true, fields, nil
	}
	if method == "initialize" || method == "ping" || s.initialized.Load() {
		return false, fields, nil
	}
	return false, nil, peer.NewError(-32602, "send initialize first, or carry io.modelcontextprotocol/protocolVersion in _meta")
}

func cancelTarget(raw json.RawMessage) (peer.RequestID, bool) {
	var in struct {
		RequestID peer.RequestID `json:"requestId"`
	}
	err := strictjson.DecodeOpen(raw, &in)
	return in.RequestID, err == nil
}
