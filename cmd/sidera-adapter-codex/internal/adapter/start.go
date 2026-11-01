package adapter

import (
	"context"
	"path/filepath"

	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/codexcfg"
	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/server"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
)

// Start takes the adapter on: it reads the configuration, reaches or starts the
// App Server and resumes every session the daemon lists, each with its own
// credentials. A server that cannot be reached is not a failure of the request:
// the sessions are reported unreachable, the availability says why and the adapter
// keeps trying to reach it.
func (a *App) Start(ctx context.Context, p adapterproto.StartParams) (adapterproto.StartResult, error) {
	inst, err := codexcfg.ParseInstance(p.Config)
	if err != nil {
		return adapterproto.StartResult{}, peer.NewError(peer.CodeInvalidParams, err.Error())
	}

	var missing missingPrograms
	codexPath, codexErr := a.opts.LookPath(inst.CodexProgram)
	if codexErr != nil {
		missing = append(missing, missingProgram{"codex", codexErr.Error()})
		codexPath = inst.CodexProgram
	}
	sideraPath, err := a.opts.LookPath(inst.SideraProgram)
	if err != nil {
		missing = append(missing, missingProgram{"sidera", err.Error()})
		sideraPath = inst.SideraProgram
	} else if abs, err := filepath.Abs(sideraPath); err == nil {
		// The thread's MCP server is started by Codex from its own working
		// directory, so the program is named by a path that holds there.
		sideraPath = abs
	}
	version := ""
	if codexErr == nil {
		if v, err := a.opts.CodexVersion(ctx, codexPath); err != nil {
			a.log.Warn("cannot read the version of Codex", "error", err)
		} else {
			version = v
		}
	}

	if len(missing) > 0 {
		a.log.Warn("a program the adapter needs was not found", "missing", missing.String())
	}

	presets := make(map[string]presetEntry, len(p.Presets))
	for _, pc := range p.Presets {
		preset, problems := codexcfg.ParsePreset(pc.Name, pc.Config)
		presets[pc.Name] = presetEntry{preset: preset, configured: len(pc.Config) > 0, problems: problems}
	}

	sessions := make(map[adapterproto.EndpointRef]*session, len(p.Sessions))
	byThread := make(map[string]*session, len(p.Sessions))
	order := make([]*session, 0, len(p.Sessions))
	for _, sa := range p.Sessions {
		s := newSession(sa.Endpoint, sa.Preset, string(sa.NativeRef), sa.ToolAccess.Env)
		sessions[sa.Endpoint], byThread[s.threadID] = s, s
		order = append(order, s)
	}

	a.mu.Lock()
	a.started = true
	a.runtimeEP, a.instance = p.RuntimeEndpoint, inst
	a.codexPath, a.sideraPath, a.codexVer, a.missing = codexPath, sideraPath, version, missing
	a.presets, a.sessions, a.byThread = presets, sessions, byThread
	a.srv = a.opts.NewServer(server.Config{
		Program: codexPath, StateDir: a.inst.StateDir, Environ: a.opts.Environ, Logger: a.log,
	})
	a.mu.Unlock()

	result := adapterproto.StartResult{Sessions: make([]adapterproto.SessionState, 0, len(order))}
	c, cerr := a.ensureClient(ctx)
	if cerr != nil {
		a.log.Warn("the App Server cannot be reached", "error", cerr)
		a.startReconnect()
	}
	for _, s := range order {
		state, active := adapterproto.AttachUnreachable, false
		if c != nil {
			state, active = a.attach(ctx, c, s, false)
		}
		result.Sessions = append(result.Sessions, adapterproto.SessionState{Endpoint: s.endpoint, State: state, TurnActive: active})
	}
	a.publishAvailability(true)
	return result, nil
}
