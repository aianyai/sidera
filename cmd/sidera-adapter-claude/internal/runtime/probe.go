package runtime

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/claudeargs"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// RuntimeName is the runtime's name in a probe.
const RuntimeName = "claude-code"

// probeTimeout bounds one tool probe.
const probeTimeout = 30 * time.Second

// Probe judges what the adapter depends on, without a model turn: the version
// of Claude Code, whether it is logged in, whether each preset can be used, and
// whether the sidera tools of each front end reach the daemon. A tool probe
// starts the tool as a session would, with the probe token the daemon sent; only
// the daemon seeing the token arrive proves that the path works, so a probe that
// ran says no more than that.
func (a *App) Probe(ctx context.Context, p adapterproto.ProbeParams) (adapterproto.ProbeResult, error) {
	if err := a.requireStarted(); err != nil {
		return adapterproto.ProbeResult{}, err
	}
	claude, sidera, _ := a.executables()
	res := adapterproto.ProbeResult{
		Auth: adapterproto.AuthUnknown, Dependencies: []adapterproto.Dependency{},
		Presets: []adapterproto.PresetStatus{}, ToolProbes: []adapterproto.ToolProbeResult{},
	}
	var conditions []adapterproto.Availability

	status, statusKnown := claudeStatus{}, false
	if claude == "" {
		res.Dependencies = append(res.Dependencies, adapterproto.Dependency{Name: "claude", State: adapterproto.DependencyMissing, Detail: "claude was not found"})
		conditions = append(conditions, adapterproto.Availability{Condition: adapterproto.ConditionDependencyMissing, Detail: "claude was not found"})
	} else if problem := claudeProblem(claude, hostOS); problem != "" {
		res.Dependencies = append(res.Dependencies, adapterproto.Dependency{Name: "claude", State: adapterproto.DependencyMissing, Detail: clip(problem)})
		conditions = append(conditions, adapterproto.Availability{Condition: adapterproto.ConditionDependencyMissing, Detail: clip(problem)})
	} else {
		version, err := a.claudeVersion(ctx, claude)
		if err != nil {
			res.Dependencies = append(res.Dependencies, adapterproto.Dependency{Name: "claude", State: adapterproto.DependencyMissing, Detail: clip(err.Error())})
			conditions = append(conditions, adapterproto.Availability{Condition: adapterproto.ConditionNativeUnavailable, Detail: clip(err.Error())})
		} else {
			res.Dependencies = append(res.Dependencies, adapterproto.Dependency{Name: "claude", State: adapterproto.DependencyOK})
			res.Runtime = &adapterproto.RuntimeInfo{Name: RuntimeName, Version: version}
			st, err := a.claudeStatus(ctx, claude)
			switch {
			case err != nil:
				a.log.Warn("the login of claude cannot be read", "error", err)
			case st.LoggedIn:
				status, statusKnown = st, true
				res.Auth = adapterproto.AuthValid
				a.log.Info("claude is logged in", "method", st.AuthMethod)
			default:
				status, statusKnown = st, true
				res.Auth = adapterproto.AuthInvalid
				conditions = append(conditions, adapterproto.Availability{Condition: adapterproto.ConditionRuntimeAuth, Detail: "claude is not logged in"})
			}
		}
	}
	if sidera == "" {
		res.Dependencies = append(res.Dependencies, adapterproto.Dependency{Name: "sidera", State: adapterproto.DependencyMissing, Detail: "sidera was not found"})
		conditions = append(conditions, adapterproto.Availability{Condition: adapterproto.ConditionDependencyMissing, Detail: "sidera was not found"})
	} else {
		res.Dependencies = append(res.Dependencies, adapterproto.Dependency{Name: "sidera", State: adapterproto.DependencyOK})
	}

	a.mu.Lock()
	names := make([]string, 0, len(a.presets))
	for name := range a.presets {
		names = append(names, name)
	}
	a.mu.Unlock()
	sort.Strings(names)
	for _, name := range names {
		ps := adapterproto.PresetStatus{Name: name}
		switch {
		case claude == "" || res.Runtime == nil:
			ps.Problems = []string{"claude is not usable"}
		case !statusKnown || !status.LoggedIn:
			ps.Problems = []string{"claude is not logged in"}
		default:
			_, perr := a.resolvePreset(name, status.ProjectsDirectory)
			if perr != nil {
				ps.Problems = perr.problems
			}
		}
		for i := range ps.Problems {
			ps.Problems[i] = clip(ps.Problems[i])
		}
		ps.Usable = len(ps.Problems) == 0
		res.Presets = append(res.Presets, ps)
	}

	for _, req := range p.ToolProbes {
		res.ToolProbes = append(res.ToolProbes, a.probeTool(ctx, req, sidera))
	}

	// The protocol has no place in a probe result for how far a turn escapes the
	// daemon (a dependency is ok, missing or the wrong version), so it is logged.
	a.logDetachment("a turn process", a.o.Launcher.Host(), "when", "probe")

	a.setProbe(ctx, conditions)
	return res, nil
}

// probeTool starts the front end's tool as a session would and reports whether
// it ran.
func (a *App) probeTool(ctx context.Context, req adapterproto.ToolProbeRequest, sidera string) adapterproto.ToolProbeResult {
	out := adapterproto.ToolProbeResult{Preset: req.Preset, Frontend: req.Frontend, Outcome: adapterproto.ProbeNotRun}
	a.mu.Lock()
	_, known := a.presets[req.Preset]
	a.mu.Unlock()
	switch {
	case !known:
		out.Detail = fmt.Sprintf("preset %q is not configured", req.Preset)
		return out
	case sidera == "":
		// The probe applies and cannot be run: that is a failure, not not_run.
		out.Outcome, out.Detail = adapterproto.ProbeFailed, "sidera was not found"
		return out
	}
	env := map[string]string{"SIDERA_PROBE_TOKEN": string(req.Token)}
	for k, v := range req.Env {
		env[k] = v
	}
	environ := claudeargs.Environ(a.o.Environ, env, filepath.Dir(sidera))
	// The tools of a session are not sandboxed by this adapter, so the probe is not.
	out.Method = adapterproto.ProbeUnsandboxed

	var err error
	switch req.Frontend {
	case adapterproto.FrontendCLI:
		var res CommandResult
		res, err = a.o.Runner.Run(ctx, Command{Argv: []string{sidera, "probe"}, Env: environ, Timeout: probeTimeout})
		if err == nil && res.ExitCode != 0 {
			err = fmt.Errorf("sidera probe exited with %d: %s", res.ExitCode, tail(res.Stderr))
		}
	case adapterproto.FrontendMCP:
		err = probeMCP(ctx, sidera, environ, probeTimeout)
	default:
		out.Method = ""
		out.Detail = fmt.Sprintf("front end %q has no probe", req.Frontend)
		return out
	}
	if err != nil {
		out.Outcome, out.Detail = adapterproto.ProbeFailed, clip(strings.ReplaceAll(err.Error(), string(req.Token), "[token]"))
		return out
	}
	out.Outcome = adapterproto.ProbeRan
	return out
}
