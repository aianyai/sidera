package adapter

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/appserver"
	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/declare"
	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/server"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// Probe judges what the adapter depends on: the version of Codex, whether it is
// signed in, the programs and the server, each preset, and whether each front end
// of each preset reaches the daemon.
func (a *App) Probe(ctx context.Context, p adapterproto.ProbeParams) (adapterproto.ProbeResult, error) {
	a.mu.Lock()
	started := a.started
	codexPath, sideraPath, version := a.codexPath, a.sideraPath, a.codexVer
	missing := append(missingPrograms(nil), a.missing...)
	serverVer := a.serverVer
	a.mu.Unlock()
	if !started {
		return adapterproto.ProbeResult{}, errNotStarted
	}

	res := adapterproto.ProbeResult{
		Auth: adapterproto.AuthUnknown, Dependencies: []adapterproto.Dependency{},
		Presets: []adapterproto.PresetStatus{}, ToolProbes: []adapterproto.ToolProbeResult{},
	}
	// codexProblem says why Codex cannot be used, or is empty. The daemon judges a
	// probe by its tool probes alone, so a program that is not there or does not
	// run must fail them: a server left running by an earlier start would answer
	// otherwise, and the probe would pass while no session can be created.
	codexProblem := ""
	if why, gone := missing.find("codex"); gone {
		codexProblem = "codex was not found: " + why
	} else if version == "" {
		if v, err := a.opts.CodexVersion(ctx, codexPath); err != nil {
			codexProblem = "codex cannot be run: " + shorten(err.Error())
		} else {
			version = v
			a.mu.Lock()
			a.codexVer = v
			a.mu.Unlock()
		}
	}
	if version != "" {
		res.Runtime = &adapterproto.RuntimeInfo{Name: declare.RuntimeName, Version: version}
	}

	c, err := a.ensureClient(ctx)
	if err != nil {
		a.startReconnect()
	} else if acct, err := c.AccountRead(ctx); err == nil {
		res.Auth = adapterproto.AuthInvalid
		if acct.SignedIn() {
			res.Auth = adapterproto.AuthValid
		}
		a.mu.Lock()
		a.signedIn = acct.SignedIn()
		a.mu.Unlock()
		a.publishAvailability(false)
	}

	res.Dependencies = append(res.Dependencies,
		codexDependency(codexProblem, version),
		programDependency("sidera", missing, ""),
		serverDependency(c != nil, serverVer, version),
	)

	a.mu.Lock()
	for name, e := range a.presets {
		st := adapterproto.PresetStatus{Name: name, Usable: len(e.problems) == 0, Problems: append([]string(nil), e.problems...)}
		if st.Usable {
			if fi, err := os.Stat(e.preset.CWD); err != nil || !fi.IsDir() {
				st.Usable, st.Problems = false, []string{"cwd: the project directory does not exist"}
			}
		}
		res.Presets = append(res.Presets, st)
	}
	a.mu.Unlock()

	for _, req := range p.ToolProbes {
		res.ToolProbes = append(res.ToolProbes, a.toolProbe(ctx, c, sideraPath, codexProblem, req))
	}
	return res, nil
}

func programDependency(name string, missing missingPrograms, version string) adapterproto.Dependency {
	if why, gone := missing.find(name); gone {
		return adapterproto.Dependency{Name: name, State: adapterproto.DependencyMissing, Detail: cut("not found: "+why, adapterproto.MaxDetailLen)}
	}
	return adapterproto.Dependency{Name: name, State: adapterproto.DependencyOK, Detail: version}
}

func codexDependency(problem, version string) adapterproto.Dependency {
	if problem != "" {
		return adapterproto.Dependency{Name: "codex", State: adapterproto.DependencyMissing, Detail: cut(problem, adapterproto.MaxDetailLen)}
	}
	return adapterproto.Dependency{Name: "codex", State: adapterproto.DependencyOK, Detail: version}
}

func serverDependency(reachable bool, serverVer, installed string) adapterproto.Dependency {
	d := adapterproto.Dependency{Name: "app-server", State: adapterproto.DependencyOK, Detail: serverVer}
	switch {
	case !reachable:
		d.State, d.Detail = adapterproto.DependencyMissing, "the App Server cannot be reached"
	case serverVer != "" && installed != "" && serverVer != installed:
		// A server left running by an earlier adapter serves the version that was
		// installed then. The adapter does not end a server it did not start.
		d.State, d.Detail = adapterproto.DependencyWrongVersion, fmt.Sprintf("serving %s, installed %s: restart it", serverVer, installed)
	}
	return d
}

// toolProbe runs one probe of a front end. The MCP front end is started by Codex
// outside the sandbox, so its probe is the program run the same way. The command
// line front end runs inside the preset's sandbox, so its probe goes through the
// server under that sandbox, which says truthfully whether the preset lets a
// command reach the daemon; it costs no model turn.
func (a *App) toolProbe(ctx context.Context, c *appserver.Client, sideraPath, codexProblem string, req adapterproto.ToolProbeRequest) adapterproto.ToolProbeResult {
	out := adapterproto.ToolProbeResult{Preset: req.Preset, Frontend: req.Frontend}
	entry, known := a.presetEntryOf(req.Preset)
	switch {
	case !known || !entry.configured:
		// Nothing was configured for this adapter: the probe does not apply.
		out.Outcome, out.Detail = adapterproto.ProbeNotRun, "the preset has no section for this adapter"
		return out
	case len(entry.problems) > 0:
		// The probe applies and cannot be run: that is a failure, not not_run.
		out.Outcome, out.Detail = adapterproto.ProbeFailed, cut("the preset is not usable: "+strings.Join(entry.problems, "; "), adapterproto.MaxDetailLen)
		return out
	case codexProblem != "":
		out.Outcome, out.Detail = adapterproto.ProbeFailed, cut(codexProblem, adapterproto.MaxDetailLen)
		return out
	}
	preset := entry.preset
	env := probeEnv(req)
	switch req.Frontend {
	case adapterproto.FrontendMCP:
		out.Method = adapterproto.ProbeUnsandboxed
		pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := a.opts.MCPProbe(pctx, sideraPath, mapToEnviron(a.baseEnviron(), env)); err != nil {
			out.Outcome, out.Detail = adapterproto.ProbeFailed, err.Error()
		} else {
			out.Outcome = adapterproto.ProbeRan
		}
	case adapterproto.FrontendCLI:
		out.Method = adapterproto.ProbeSandboxed
		if c == nil {
			// The probe applies and cannot be run: that is a failure, not not_run.
			out.Outcome, out.Detail, out.Method = adapterproto.ProbeFailed, "the App Server cannot be reached", ""
			return out
		}
		r, err := c.CommandExec(ctx, appserver.CommandExecParams{
			Command: []string{sideraPath, "probe"}, CWD: preset.CWD, Env: env,
			SandboxPolicy: preset.SandboxPolicy(), TimeoutMs: 20000,
		})
		switch {
		case err != nil:
			out.Outcome, out.Detail = adapterproto.ProbeFailed, "the command could not be run: "+shorten(err.Error())
		case r.ExitCode != 0:
			out.Outcome, out.Detail = adapterproto.ProbeFailed, fmt.Sprintf("sidera probe exited %d: %s", r.ExitCode, shorten(r.Stderr))
		default:
			out.Outcome = adapterproto.ProbeRan
		}
	}
	return out
}

// probeEnv is the environment of a probed command: what the request gives, and the
// probe token, which is a credential of its own that grants nothing else.
func probeEnv(req adapterproto.ToolProbeRequest) map[string]string {
	env := make(map[string]string, len(req.Env)+1)
	for k, v := range req.Env {
		env[k] = v
	}
	env["SIDERA_PROBE_TOKEN"] = string(req.Token)
	return env
}

func (a *App) baseEnviron() []string {
	if a.opts.Environ != nil {
		return server.Environ(a.opts.Environ)
	}
	return server.Environ(os.Environ())
}

// mapToEnviron adds variables to an environment, replacing the ones it has.
func mapToEnviron(base []string, add map[string]string) []string {
	out := make([]string, 0, len(base)+len(add))
	for _, kv := range base {
		name, _, _ := strings.Cut(kv, "=")
		if _, ok := add[name]; !ok {
			out = append(out, kv)
		}
	}
	for k, v := range add {
		out = append(out, k+"="+v)
	}
	return out
}

// shorten cuts a diagnostic to a line the protocol accepts as a detail.
func shorten(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return strings.ToValidUTF8(s, "?")
}

// cut shortens a detail to at most n bytes, dropping a rune the cut broke.
func cut(s string, n int) string {
	if len(s) > n {
		s = s[:n]
	}
	return strings.ToValidUTF8(s, "")
}

// probeGrace is how long the MCP probe waits for the server to say how its own probe
// went, after the server answered initialize.
var probeGrace = 10 * time.Second

// runMCPProbe runs the MCP server of the channel as Codex would, with the probe
// token in its environment, and speaks to it as a client: the server probes the
// daemon itself when the client first initializes it, and says on its standard
// error how that went. The probe ran if the server answered initialize; that it
// also reached the daemon is for the daemon to see, which is the only proof.
func runMCPProbe(ctx context.Context, program string, env []string) error {
	cmd := exec.CommandContext(ctx, program, "mcp")
	cmd.Env = env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("cannot start %s mcp: %w", program, err)
	}
	probeDone := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			line := sc.Text()
			if strings.Contains(line, "MCP probe succeeded") || strings.Contains(line, "MCP probe failed") {
				probeDone <- line
				break
			}
		}
		_, _ = io.Copy(io.Discard, stderr)
	}()
	defer func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	}()

	init, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "sidera-adapter-codex", "version": ClientVersion()},
	}})
	if _, err := stdin.Write(append(init, '\n')); err != nil {
		return fmt.Errorf("cannot talk to %s mcp: %w", program, err)
	}
	answered := make(chan error, 1)
	go func() {
		line, err := bufio.NewReader(stdout).ReadBytes('\n')
		if err != nil {
			answered <- errors.New("the MCP server ended without answering initialize")
			return
		}
		var r struct {
			Error *struct{} `json:"error"`
		}
		if json.Unmarshal(line, &r) != nil || r.Error != nil {
			answered <- errors.New("the MCP server refused initialize")
			return
		}
		answered <- nil
	}()
	select {
	case err := <-answered:
		if err != nil {
			return err
		}
	case <-ctx.Done():
		return errors.New("the MCP server did not answer initialize in time")
	}
	// Give the server's own probe time to finish before its input is closed, which
	// cancels it.
	select {
	case line := <-probeDone:
		if strings.Contains(line, "MCP probe failed") {
			return errors.New("the MCP server could not reach the daemon")
		}
	case <-time.After(probeGrace):
	case <-ctx.Done():
	}
	return nil
}
