package adapter

import (
	"context"
	"path/filepath"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/appserver"
	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/codexcfg"
	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/server"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
)

// Start takes the adapter on: it reads the configuration, reaches or starts the
// App Server and resumes every session the daemon lists, each with its own
// credentials. The server and the sessions are taken in the background and the
// answer waits for them only as long as the request allows (see restore): a session
// not resumed by then is reported unreachable. A server that cannot be reached is
// not a failure of the request either: the sessions are reported unreachable, the
// availability says why and the adapter keeps trying to reach it.
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
		var s *session
		if sa.Attached {
			s = newAttachedSession(sa.Endpoint, string(sa.NativeRef))
		} else {
			s = newSession(sa.Endpoint, sa.Preset, string(sa.NativeRef), sa.ToolAccess.Env)
		}
		// The server has not loaded the thread in this adapter's books until the restore
		// has: whatever an earlier connection held is loaded again, with the
		// credentials of this start.
		s.status = appserver.ThreadNotLoaded
		sessions[sa.Endpoint], byThread[s.threadID] = s, s
		order = append(order, s)
	}

	a.mu.Lock()
	// adapter.start is a full re-attachment: the readings of the sessions of an
	// earlier one end with them.
	for _, old := range a.sessions {
		if old.watchCancel != nil {
			old.watchCancel()
			old.watchCancel = nil
		}
	}
	if a.endRestore != nil {
		a.endRestore() // the loading of an earlier start's sessions
	}
	rctx, endRestore := context.WithCancel(a.bgCtx)
	a.endRestore = endRestore
	a.started = true
	a.runtimeEP, a.instance = p.RuntimeEndpoint, inst
	a.codexPath, a.sideraPath, a.codexVer, a.missing = codexPath, sideraPath, version, missing
	a.presets, a.sessions, a.byThread = presets, sessions, byThread
	a.srv = a.opts.NewServer(server.Config{
		Program: codexPath, StateDir: a.inst.StateDir, Environ: a.opts.Environ, Logger: a.log,
	})
	a.mu.Unlock()

	// The sessions are loaded in the background and answered when the time adapter.start
	// has is used up; the ones not loaded by then are unreachable for now, which is what
	// they are when the server cannot be reached, and are reported when they are.
	rest := newRestore(order)
	go rest.run(rctx, a, order)
	var timeout <-chan time.Time
	if wait, ok := a.answerWait(ctx); ok {
		t := time.NewTimer(wait)
		defer t.Stop()
		timeout = t.C
	}
	select {
	case <-rest.finished:
	case <-timeout:
	case <-ctx.Done():
	}
	states := rest.answer()
	a.publishAvailability(true)
	return adapterproto.StartResult{Sessions: states}, nil
}
