package fakeadapter

import (
	"context"
	"os"
	"os/exec"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// ToolProbeConfig selects a static argv for each requested frontend. Commands
// run without a shell, with the request's opaque environment overriding inherited
// values. They must contain no credentials: only executable paths and public args.
// A missing or empty command does not run that frontend's probe.
type ToolProbeConfig struct {
	Commands map[adapterproto.Frontend][]string `json:"commands"`
}

func (a *adapter) runToolProbe(ctx context.Context, request adapterproto.ToolProbeRequest) adapterproto.ToolProbeResult {
	result := adapterproto.ToolProbeResult{Preset: request.Preset, Frontend: request.Frontend, Outcome: adapterproto.ProbeRan, Method: adapterproto.ProbeUnsandboxed}
	if a.cfg.ToolProbe == nil {
		return result
	}
	argv := a.cfg.ToolProbe.Commands[request.Frontend]
	if len(argv) == 0 || argv[0] == "" {
		result.Outcome, result.Method = adapterproto.ProbeNotRun, ""
		return result
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = os.Environ()
	for key, value := range request.Env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	// Nil output streams discard both outputs. Neither command output nor an
	// error string is safe to put in a result or log: either can contain tokens.
	if err := cmd.Run(); err != nil {
		result.Outcome, result.Method = adapterproto.ProbeFailed, ""
		result.Detail = "tool probe command failed"
	}
	return result
}

// stopToolProbes closes admission before waiting, so a handler that races with
// shutdown cannot start a child after its owner has finished waiting.
func (a *adapter) stopToolProbes() {
	a.mu.Lock()
	a.probesClosed = true
	a.cancelProbes()
	a.mu.Unlock()
	a.probes.Wait()
}
