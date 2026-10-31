package runtime

import (
	"context"
	"path/filepath"

	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/claudeargs"
	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/record"
	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/runtime/proc"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// Start takes the adapter on: it keeps the configuration, writes the MCP
// configuration file, and takes back the sessions the daemon lists. For each
// session it reads the checkpoint and looks at the files and the process table:
//
//   - a session file that is gone is lost (Claude Code removes the records of
//     sessions that were idle for longer than its cleanup period, 30 days by
//     default);
//   - a session without a usable checkpoint is unreachable: the adapter does not
//     know where its files are;
//   - the others are attached, with the turn active exactly when a turn process
//     of the session runs. The turns they left behind are followed, and what was
//     lost with the adapter is reported after the answer.
func (a *App) Start(_ context.Context, p adapterproto.StartParams) (adapterproto.StartResult, error) {
	cfg := parseInstanceConfig(p.Config)
	claude := a.findExecutable(cfg.claudePath, "claude")
	sidera := a.findExecutable(cfg.siderPath, "sidera")
	mcpConfig := ""
	if sidera != "" {
		content, err := claudeargs.MCPConfig(sidera)
		if err != nil {
			return adapterproto.StartResult{}, err
		}
		mcpConfig = a.mcpConfigPath()
		if err := a.o.Files.WriteFile(mcpConfig, content); err != nil {
			return adapterproto.StartResult{}, err
		}
	}
	if claude == "" {
		a.log.Warn("claude was not found: sessions cannot be created or prompted", "configured", cfg.claudePath)
	}
	if sidera == "" {
		a.log.Warn("sidera was not found: sessions cannot be created or prompted", "configured", cfg.siderPath)
	}

	a.mu.Lock()
	a.claude, a.sidera, a.mcpConfig = claude, sidera, mcpConfig
	a.presets = map[string]adapterproto.ConfigValues{}
	for _, preset := range p.Presets {
		a.presets[preset.Name] = preset.Config
	}
	a.started = true
	a.mu.Unlock()

	res := adapterproto.StartResult{Sessions: make([]adapterproto.SessionState, 0, len(p.Sessions))}
	if len(p.Sessions) == 0 {
		return res, nil
	}
	snap, snapErr := a.o.Table.Snapshot()
	if snapErr != nil {
		a.log.Error("the process table cannot be read: the sessions cannot be attached", "error", snapErr)
	}
	for _, attach := range p.Sessions {
		state := adapterproto.SessionState{Endpoint: attach.Endpoint, State: adapterproto.AttachUnreachable}
		if s := a.attach(attach, snap, snapErr, &state); s != nil {
			a.mu.Lock()
			a.sessions[s.endpoint] = s
			a.mu.Unlock()
			// What the adapter lost while it was away is reported after the answer.
			go a.resume(s)
		}
		res.Sessions = append(res.Sessions, state)
	}
	return res, nil
}

func (a *App) mcpConfigPath() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return filepath.Join(a.instance.StateDir, "mcp.json")
}

// resume follows the session's turn: the first look reports what was lost.
func (a *App) resume(s *session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a.tick(a.life, s)
	a.watch(s)
}

// attach reads one session of adapter.start into a session of the table, and
// fills state with the answer. It returns nil for a session that is not attached.
func (a *App) attach(at adapterproto.SessionAttach, snap proc.Snapshot, snapErr error, state *adapterproto.SessionState) *session {
	if at.Checkpoint == nil {
		a.log.Warn("a session has no checkpoint: it cannot be attached", "endpoint", at.Endpoint)
		return nil
	}
	cp, err := decodeSessionCheckpoint(at.Checkpoint)
	if err != nil || cp.Session != string(at.NativeRef) {
		a.log.Warn("a session's checkpoint is not usable: it cannot be attached", "endpoint", at.Endpoint, "error", err)
		return nil
	}
	if exists, err := a.fileExists(record.File(cp.RecordDir, cp.Session)); err != nil {
		a.log.Warn("a session file cannot be opened: the session cannot be attached", "endpoint", at.Endpoint, "error", err)
		return nil
	} else if !exists {
		state.State = adapterproto.AttachLost
		return nil
	}
	if snapErr != nil {
		return nil
	}

	s := &session{
		endpoint: at.Endpoint, id: cp.Session, preset: at.Preset, cwd: cp.Cwd, recordDir: cp.RecordDir,
		toolEnv: at.ToolAccess.Env, turn: cp.Turn,
	}
	running := a.turnProcesses(snap, s.id)
	if len(running) > 1 {
		a.log.Error("several turn processes write one session: it cannot be attached", "endpoint", at.Endpoint, "count", len(running))
		return nil
	}
	if len(running) == 1 && (s.turn == nil || s.turn.Process != processOf(running[0])) {
		// The adapter died between starting a process and recording it.
		s.turn = adoptUnknown(processOf(running[0]))
	}

	state.State = adapterproto.AttachAttached
	if s.turn != nil {
		info := snap.Lookup(s.turn.Process.PID)
		if info.Alive && info.StartKey() == s.turn.Process.StartKey {
			state.TurnActive = true
			state.Process = identity(s.turn.Process)
		}
	}
	return s
}
