//go:build realcodex

// These tests run against the Codex installed on this machine, with a CODEX_HOME of
// their own, and run no model turn. They are opt-in twice: the build tag, and
// SIDERA_REALCODEX=1 in the environment.
//
//	SIDERA_REALCODEX=1 go test -tags realcodex -count=1 ./cmd/sidera-adapter-codex/...
package clientsetup

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aianyai/sidera/sdk/go/adaptersetup"
)

// TestRealCodex runs the whole setup against the installed Codex: install, check,
// refresh, a marketplace of somebody else's that Codex cannot read, a hiding server of
// the person's, undo. It runs no model turn, and gives Codex a home of its own, so
// nothing of the person's is read or written.
func TestRealCodex(t *testing.T) {
	if os.Getenv("SIDERA_REALCODEX") != "1" {
		t.Skip("set SIDERA_REALCODEX=1 to run against the installed Codex")
	}
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatalf("codex is not installed: %v", err)
	}
	t.Setenv("CODEX_HOME", t.TempDir())
	root := t.TempDir()
	program := filepath.Join(root, "sidera")
	if err := os.WriteFile(program, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	h := New("1.0.0", os.Environ())
	run := func(req adaptersetup.Request) adaptersetup.Result {
		t.Helper()
		req.Version, req.Instance, req.StateDir, req.SideraProgram, req.HomeIsDefault = adaptersetup.Version, "codex", state, program, true
		res, err := adaptersetup.Handle(context.Background(), h, req)
		if err != nil {
			t.Fatalf("%s: %v", req.Op, err)
		}
		return res
	}
	c := cli{program: codex, dir: state}
	ctx := context.Background()

	plan := run(adaptersetup.Request{Op: adaptersetup.OpPlan})
	res := run(adaptersetup.Request{Op: adaptersetup.OpApply, Accepted: itemIDs(plan.Items)})
	if res.Status != adaptersetup.StatusOK {
		t.Fatalf("apply = %+v", res)
	}
	s, err := c.Server(ctx, serverName)
	if err != nil || !(serverDef{Command: program, Args: []string{"mcp"}}).matches(s) {
		t.Fatalf("the server Codex offers = %+v, %v", s, err)
	}
	if res := run(adaptersetup.Request{Op: adaptersetup.OpCheck}); res.Status != adaptersetup.StatusOK {
		t.Fatalf("check = %+v", res)
	}

	// A new version of the adapter: the refresh installs it again.
	h.Version = "1.0.1"
	res = run(adaptersetup.Request{Op: adaptersetup.OpRefresh})
	if res.Status != adaptersetup.StatusRefreshed || len(res.Outcomes) != 2 {
		t.Fatalf("refresh = %+v", res)
	}
	if p, err := c.Plugin(ctx); err != nil || p.Version != "1.0.1" || !p.Enabled {
		t.Fatalf("plugin = %+v, %v", p, err)
	}

	// The source files vanish: Codex cannot list, and check says so while the plugin
	// keeps running from the cache.
	if err := os.Rename(filepath.Join(state, "setup", "codex"), filepath.Join(root, "moved")); err != nil {
		t.Fatal(err)
	}
	res = run(adaptersetup.Request{Op: adaptersetup.OpCheck})
	if got := driftIDs(res.Drift); len(got) != 2 || got[0] != sourceID+":missing" || got[1] != marketplID+":broken" {
		t.Fatalf("check without the source = %v", got)
	}
	if err := os.Rename(filepath.Join(root, "moved"), filepath.Join(state, "setup", "codex")); err != nil {
		t.Fatal(err)
	}

	// A marketplace of somebody else's that Codex cannot read stops every listing, and is
	// not our source gone.
	foo := filepath.Join(root, "foo")
	if err := os.MkdirAll(filepath.Join(foo, ".agents", "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(foo, marketplaceFile), []byte(`{"name":"foo","plugins":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runCodex(codex, state, "plugin", "marketplace", "add", foo); err != nil {
		t.Fatalf("codex plugin marketplace add: %v\n%s", err, out)
	}
	if err := os.Remove(filepath.Join(foo, marketplaceFile)); err != nil {
		t.Fatal(err)
	}
	res = run(adaptersetup.Request{Op: adaptersetup.OpCheck})
	if got := driftIDs(res.Drift); len(got) != 1 || got[0] != clientID+":broken" || !strings.Contains(res.Drift[0].Summary, "foo") {
		t.Fatalf("check with a marketplace of somebody else's unreadable = %+v", res.Drift)
	}
	h.Version = "1.0.2"
	res = run(adaptersetup.Request{Op: adaptersetup.OpRefresh})
	if got := outcomeIDs(res.Outcomes); len(got) != 2 || got[1] != pluginItem+":skipped" {
		t.Fatalf("refresh with a marketplace of somebody else's unreadable = %+v", res.Outcomes)
	}
	if out, err := runCodex(codex, state, "plugin", "marketplace", "remove", "foo"); err != nil {
		t.Fatalf("codex plugin marketplace remove: %v\n%s", err, out)
	}
	if res = run(adaptersetup.Request{Op: adaptersetup.OpRefresh}); res.Status != adaptersetup.StatusRefreshed {
		t.Fatalf("refresh after it was removed = %+v", res)
	}

	// The person's own server hides the plugin's.
	if out, err := runCodex(codex, state, "mcp", "add", serverName, "--", program, "mcp"); err != nil {
		t.Fatalf("codex mcp add: %v\n%s", err, out)
	}
	plan = run(adaptersetup.Request{Op: adaptersetup.OpPlan})
	if got := itemIDs(plan.Items); len(got) != 1 || got[0] != mcpServerID {
		t.Fatalf("plan = %v", got)
	}
	run(adaptersetup.Request{Op: adaptersetup.OpApply, Accepted: []string{mcpServerID}})
	if res := run(adaptersetup.Request{Op: adaptersetup.OpCheck}); res.Status != adaptersetup.StatusOK {
		t.Fatalf("check after removing the hiding server = %+v", res)
	}

	res = run(adaptersetup.Request{Op: adaptersetup.OpApply, Undo: true, Accepted: []string{pluginItem, marketplID, sourceID}})
	if res.Status != adaptersetup.StatusOK {
		t.Fatalf("undo = %+v", res)
	}
	if p, err := c.Plugin(ctx); err != nil || p.Installed {
		t.Errorf("plugin after undo = %+v, %v", p, err)
	}
	if mp, err := c.Marketplace(ctx, marketplaceName); err != nil || mp.Registered {
		t.Errorf("marketplace after undo = %+v, %v", mp, err)
	}
	if s, err := c.Server(ctx, serverName); err != nil || s.Found {
		t.Errorf("server after undo = %+v, %v", s, err)
	}
}

// TestRealCodexUndoActsOnTheHomeSetupRecorded sets up in one CODEX_HOME and undoes from an
// environment that names another: the plugin and marketplace of the first are removed, and
// the second, which never had them, is left alone. Nothing outside the two temporary
// homes is read or written.
func TestRealCodexUndoActsOnTheHomeSetupRecorded(t *testing.T) {
	if os.Getenv("SIDERA_REALCODEX") != "1" {
		t.Skip("set SIDERA_REALCODEX=1 to run against the installed Codex")
	}
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatalf("codex is not installed: %v", err)
	}
	home1, home2 := t.TempDir(), t.TempDir()
	t.Setenv("CODEX_HOME", home1)
	root := t.TempDir()
	program := filepath.Join(root, "sidera")
	if err := os.WriteFile(program, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	run := func(home string, req adaptersetup.Request) (adaptersetup.Result, error) {
		req.Version, req.Instance, req.StateDir, req.SideraProgram, req.HomeIsDefault = adaptersetup.Version, "codex", state, program, true
		return adaptersetup.Handle(context.Background(), New("1.0.0", append(os.Environ(), "CODEX_HOME="+home)), req)
	}
	inHome := func(home string) cli {
		return cli{program: codex, dir: state, env: currentHome([]string{"CODEX_HOME=" + home}).environ(os.Environ())}
	}
	plan, err := run(home1, adaptersetup.Request{Op: adaptersetup.OpPlan})
	if err != nil {
		t.Fatal(err)
	}
	if res, err := run(home1, adaptersetup.Request{Op: adaptersetup.OpApply, Accepted: itemIDs(plan.Items)}); err != nil || res.Status != adaptersetup.StatusOK {
		t.Fatalf("apply = %+v, %v", res, err)
	}
	if _, err := run(home2, adaptersetup.Request{Op: adaptersetup.OpPlan}); err == nil || !strings.Contains(err.Error(), "CODEX_HOME="+home1) {
		t.Fatalf("a plan in another home = %v, want a refusal that names the recorded home", err)
	}
	res, err := run(home2, adaptersetup.Request{Op: adaptersetup.OpApply, Undo: true, Accepted: []string{pluginItem, marketplID, sourceID}})
	if err != nil || res.Status != adaptersetup.StatusOK {
		t.Fatalf("undo from the other home = %+v, %v", res, err)
	}
	ctx := context.Background()
	if p, err := inHome(home1).Plugin(ctx); err != nil || p.Installed {
		t.Errorf("plugin in the recorded home after undo = %+v, %v", p, err)
	}
	if mp, err := inHome(home1).Marketplace(ctx, marketplaceName); err != nil || mp.Registered {
		t.Errorf("marketplace in the recorded home after undo = %+v, %v", mp, err)
	}
}

// runCodex runs the installed Codex in a directory with no project configuration.
func runCodex(codex, dir string, args ...string) (string, error) {
	cmd := exec.Command(codex, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}
