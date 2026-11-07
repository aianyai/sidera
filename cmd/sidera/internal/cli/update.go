package cli

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/aianyai/sidera/cmd/sidera/internal/update"
	"github.com/aianyai/sidera/cmd/sidera/internal/update/fetch"
	"github.com/aianyai/sidera/cmd/sidera/internal/update/verify"
	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/manageapi"
	"github.com/aianyai/sidera/sdk/go/termout"
)

type updateFlags struct {
	check, yes bool
	version    string
}

func (o *updateFlags) add(f *flag.FlagSet) {
	f.BoolVar(&o.check, "check", false, "only report the release and whether this program may update to it")
	f.StringVar(&o.version, "version", "", "the release to update to, such as 0.12.0 or v0.12.0 (default: the latest)")
	f.BoolVar(&o.yes, "yes", false, "go ahead without asking")
}

// update is `update`. It builds what the run needs from the process and the data
// directory; the run itself is the update package's.
func (c *command) update(ctx context.Context, args []string) error {
	o := &c.arguments.update
	p, err := parse(c.arguments.flags, args)
	if err != nil {
		return err
	}
	if len(p) != 0 {
		return usage("update takes no arguments")
	}
	want, err := update.NormalizeVersion(o.version)
	if err != nil {
		return usage("--version: " + err.Error())
	}
	if !o.check && !o.yes && !termout.IsTerminal(c.Stdin) {
		return noTerminalToAsk("update")
	}
	keys, err := verify.EmbeddedKeys()
	if err != nil {
		return err
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return &failure{"cannot find the user cache directory to download into: " + err.Error()}
	}
	program, err := c.programPath()
	if err != nil {
		return err
	}
	cfg := update.Config{
		Installed: c.Release, Program: program, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
		Fetch: fetch.New(fetch.UserAgent(c.Release.Version)), Keys: keys,
		CacheRoot: filepath.Join(cacheDir, "sidera-update"), Random: c.random,
	}
	if !o.check {
		home, err := c.dataDir()
		if err != nil {
			return err
		}
		client := &lazyClient{home: home}
		defer client.Close()
		cfg.Node = &managementNode{node: client, random: c.random, home: home,
			setupEnv: setupEnv{home: home, homeIsDefault: c.isDefaultDataDir(home), program: program}, setupExec: execSetup{}}
	}
	return c.runUpdate(ctx, cfg, updateRequest{want: want, check: o.check, yes: o.yes})
}

type updateRequest struct {
	want       string
	check, yes bool
}

// runUpdate runs or checks and writes the outcome: the report on standard output, in
// the CLI's `key value` lines or as one JSON object, and the plan, the question and
// the steps on standard error, so that what a script reads is only the report.
func (c *command) runUpdate(ctx context.Context, cfg update.Config, req updateRequest) error {
	cfg.Step = func(step string) { fmt.Fprintln(c.Stderr, step) }
	if req.check {
		verdict, err := update.Check(ctx, cfg, req.want)
		if err != nil {
			return &failure{err.Error()}
		}
		return c.printCheck(verdict)
	}
	cfg.Confirm = func(p update.Plan) (bool, error) { return c.confirmUpdate(c.Stderr, p, req.yes) }
	rep, err := update.Run(ctx, cfg, req.want)
	if err == nil || rep.Acted() {
		if printErr := c.printUpdate(rep, err); printErr != nil {
			err = errors.Join(err, printErr)
		}
	}
	if err != nil {
		return &failure{err.Error()}
	}
	return nil
}

// confirmUpdate shows what is about to happen and, unless yes, asks.
func (c *command) confirmUpdate(out io.Writer, p update.Plan, yes bool) (bool, error) {
	fmt.Fprintf(out, "current %s\ntarget %s\nrelease %s\n", p.Current, p.Target, p.ReleaseURL)
	if len(p.Replace) > 0 {
		fmt.Fprintf(out, "replace %s\n", strings.Join(p.Replace, ", "))
	}
	switch {
	case p.Restart:
		fmt.Fprintf(out, "restart daemon (running %s)\n", p.DaemonVersion)
	case !p.Daemon:
		fmt.Fprintln(out, "daemon not running: it is not restarted, the adapters are not installed and their setup is not refreshed; run sidera update again once it runs")
	}
	if len(p.Adapters) > 0 {
		fmt.Fprintf(out, "install adapters %s\n", strings.Join(p.Adapters, ", "))
	}
	if len(p.Missing) > 0 {
		fmt.Fprintf(out, "not in this release (left as they are) %s\n", strings.Join(p.Missing, ", "))
	}
	if len(p.Faulted) > 0 {
		fmt.Fprintf(out, "faulted adapters, installed again %s\n", strings.Join(p.Faulted, ", "))
	}
	if len(p.FaultedMissing) > 0 {
		fmt.Fprintf(out, "faulted adapters not in this release (install them again by hand) %s\n", strings.Join(p.FaultedMissing, ", "))
	}
	if p.RefreshSetup {
		fmt.Fprintln(out, "refresh the client setup that adapters recorded, where it is out of date")
	}
	if yes {
		return true, nil
	}
	return askDefaultNo(out, bufio.NewReader(c.Stdin), "Update?")
}

// updateOutcome is what --json writes: the report, and why the run failed when it did.
type updateOutcome struct {
	update.Report
	Error string `json:"error,omitempty"`
}

// printCheck writes what a check found: only the release and whether this program may
// update to it, because that is all a check looks at.
func (c *command) printCheck(v update.Verdict) error {
	if c.json {
		return c.encode(v)
	}
	var b strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	releaseLines(line, v.Release)
	line("update_available %t", v.UpdateAvailable)
	line("admitted %t", v.Admitted)
	if v.Refusal != "" {
		line("refusal %s", v.Refusal)
	}
	_, err := io.WriteString(c.Stdout, b.String())
	return err
}

// releaseLines writes the lines every report starts with.
func releaseLines(line func(string, ...any), r update.Release) {
	line("current %s", r.Current)
	line("target %s", r.Target)
	line("release %s", r.ReleaseURL)
	if r.Floor != "" {
		line("floor %s", r.Floor)
	}
}

func (c *command) printUpdate(rep update.Report, runErr error) error {
	if c.json {
		out := updateOutcome{Report: rep}
		if runErr != nil {
			out.Error = runErr.Error()
		}
		return c.encode(out)
	}
	var b strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	releaseLines(line, rep.Release)
	if rep.AlreadyCurrent {
		line("already_current true")
	}
	if len(rep.Program.Restored) > 0 {
		line("program restored %s", strings.Join(rep.Program.Restored, ", "))
	}
	if rep.Program.Updated {
		line("program updated %s", strings.Join(rep.Program.Replaced, ", "))
		if rep.Program.ConsoleMissing {
			line("sidera-adapter-console not found next to the program: it was not installed")
		}
		line("program previous versions kept as <name>.old, to go back to; the next update that replaces the programs removes them")
	}
	if len(rep.Program.Left) > 0 {
		line("program left %s (a running process still uses them; the next update removes them)", strings.Join(rep.Program.Left, ", "))
	}
	switch {
	case rep.Daemon.Restarted:
		line("daemon restarted version %s", rep.Daemon.Version)
	case rep.Daemon.Running:
		line("daemon running version %s", rep.Daemon.Previous)
	default:
		line("daemon not running: it was not restarted and the adapters were not looked at; start it, then run sidera update again")
	}
	replaced := false
	for _, a := range rep.Adapters {
		switch {
		case a.Error != "":
			line("adapter %s failed %s", a.ID, a.Error)
		case a.Skipped != "":
			line("adapter %s skipped %s", a.ID, a.Skipped)
		case a.Faulted:
			line("adapter %s was faulted, installed again version %s replaced %t", a.ID, a.Version, a.Replaced)
			replaced = replaced || a.Replaced
		default:
			line("adapter %s version %s replaced %t", a.ID, a.Version, a.Replaced)
			replaced = replaced || a.Replaced
		}
	}
	if replaced {
		line("adapters the old version directories of replaced adapters are removed at the next daemon start")
	}
	for _, st := range rep.Setup {
		printSetupReport(line, st)
	}
	_, err := io.WriteString(c.Stdout, b.String())
	return err
}

// printSetupReport writes what became of the client setup of one instance, in the
// lines `adapter setup` uses for items and drift.
func printSetupReport(line func(string, ...any), st update.SetupReport) {
	if st.Message != "" {
		line("setup %s %s %s", st.Instance, st.Status, st.Message)
	} else {
		line("setup %s %s", st.Instance, st.Status)
	}
	for _, it := range st.Items {
		if it.Message != "" {
			line("setup %s item %s %s %s", st.Instance, it.ID, it.Status, it.Message)
		} else {
			line("setup %s item %s %s", st.Instance, it.ID, it.Status)
		}
	}
	for _, d := range st.Drift {
		line("setup %s drift %s %s %s", st.Instance, d.ID, d.Problem, d.Summary)
	}
	if st.Fix != "" {
		line("setup %s fix %s", st.Instance, st.Fix)
	}
}

// lazyClient opens the management client at its first call, so that a run that
// stops before it needs the daemon (a build that cannot be updated, a release that
// cannot be believed) does not fail on the data directory first.
type lazyClient struct {
	home   string
	client *manageapi.Client
	err    error
}

func (l *lazyClient) Call(ctx context.Context, method string, in manageapi.Request) (json.RawMessage, error) {
	if l.client == nil && l.err == nil {
		l.client, l.err = manageapi.NewClient(manageapi.ClientConfig{Home: l.home})
	}
	if l.err != nil {
		return nil, l.err
	}
	return l.client.Call(ctx, method, in)
}

func (l *lazyClient) Close() {
	if l.client != nil {
		l.client.Close()
	}
}

// managementNode is the daemon of the data directory, as update sees it, over the
// management interface. It reads the answers loosely: a field it does not know is
// ignored, so that this program can update from and to versions whose answers have
// grown.
type managementNode struct {
	node   caller
	random io.Reader
	home   string
	// setupEnv and setupExec are what the refresh of an instance's setup runs with: the
	// facts every request carries, and the way to start the setup program.
	setupEnv  setupEnv
	setupExec setupExecutor
}

func (n *managementNode) Status(ctx context.Context) (update.NodeStatus, error) {
	raw, err := n.node.Call(ctx, "status", manageapi.Request{})
	if err != nil {
		if apiwire.FromError(err).Code == "daemon_not_running" {
			return update.NodeStatus{}, update.ErrNotRunning
		}
		return update.NodeStatus{}, err
	}
	var out struct {
		Node struct {
			Version string `json:"version"`
		} `json:"node"`
	}
	if err := decodeResult(raw, &out); err != nil {
		return update.NodeStatus{}, err
	}
	return update.NodeStatus{Version: out.Node.Version}, nil
}

func (n *managementNode) Restart(ctx context.Context, expectVersion string) error {
	_, err := restartDaemon(ctx, n.node, n.random, restartRequest{ExpectVersion: expectVersion, Wait: defaultRestartWait, Home: n.home}, nil)
	return err
}

func (n *managementNode) Adapters(ctx context.Context) ([]update.Adapter, error) {
	raw, err := n.node.Call(ctx, "adapters.list", manageapi.Request{})
	if err != nil {
		return nil, err
	}
	// A package fault is the adapter's own, or one an instance of it reports; either
	// way the adapter has to be installed again.
	type fault struct {
		Reason string `json:"reason"`
	}
	var out struct {
		Adapters []struct {
			ID      string `json:"id"`
			Version string `json:"version"`
			Fault   *fault `json:"fault"`
		} `json:"adapters"`
		Instances []struct {
			Adapter string `json:"adapter"`
			Fault   *fault `json:"fault"`
		} `json:"instances"`
	}
	if err := decodeResult(raw, &out); err != nil {
		return nil, err
	}
	faulted := map[string]bool{}
	for _, in := range out.Instances {
		if in.Fault != nil {
			faulted[in.Adapter] = true
		}
	}
	var adapters []update.Adapter
	for _, a := range out.Adapters {
		adapters = append(adapters, update.Adapter{ID: a.ID, Version: a.Version, Faulted: a.Fault != nil || faulted[a.ID]})
	}
	return adapters, nil
}

func (n *managementNode) Install(ctx context.Context, dir string) (update.Installed, error) {
	random := n.random
	if random == nil {
		random = rand.Reader
	}
	id, err := admin.NewOperationID(random)
	if err != nil {
		return update.Installed{}, err
	}
	raw, err := callResending(ctx, n.node, "adapters.install", manageapi.Request{OperationID: id, Params: map[string]any{"source": dir}})
	if err != nil {
		return update.Installed{}, err
	}
	var out struct {
		Adapter struct {
			ID      string `json:"id"`
			Version string `json:"version"`
		} `json:"adapter"`
		AlreadyCurrent bool `json:"already_current"`
		Replaced       bool `json:"replaced"`
	}
	if err := decodeResult(raw, &out); err != nil {
		return update.Installed{}, err
	}
	return update.Installed{ID: out.Adapter.ID, Version: out.Adapter.Version, AlreadyCurrent: out.AlreadyCurrent, Replaced: out.Replaced}, nil
}

func (n *managementNode) Instances(ctx context.Context) ([]update.Instance, error) {
	raw, err := n.node.Call(ctx, "adapters.list", manageapi.Request{})
	if err != nil {
		return nil, err
	}
	var out struct {
		Instances []struct {
			Instance string `json:"instance"`
			Adapter  string `json:"adapter"`
		} `json:"instances"`
	}
	if err := decodeResult(raw, &out); err != nil {
		return nil, err
	}
	instances := make([]update.Instance, len(out.Instances))
	for i, in := range out.Instances {
		instances[i] = update.Instance{ID: in.Instance, Adapter: in.Adapter}
	}
	return instances, nil
}

// RefreshSetup is `adapter setup <instance> --refresh`, run through the same session
// and mode, with its result in the update's words. An instance that has no setup to
// refresh is skipped, with the reason: its adapter declares none, the daemon has no
// adapters.setup_info, the adapter's package cannot be loaded (installing it reported
// that as a note of its own, and a condition the update already reports must not fail
// every later update) or the adapter is not installed. Every other way the refresh
// cannot be made is an error. The report carries the command that settles drift.
func (n *managementNode) RefreshSetup(ctx context.Context, instance string) (update.SetupReport, error) {
	s, err := openSetupSession(ctx, n.node, n.setupExec, instance, n.setupEnv, nil)
	switch {
	case errors.Is(err, errNoSetup):
		return update.SetupReport{Status: update.SetupSkipped, Message: "the adapter declares no setup"}, nil
	case errors.Is(err, errDaemonOlder):
		return update.SetupReport{Status: update.SetupSkipped, Message: "the daemon has no adapter setup"}, nil
	case err != nil:
		switch apiwire.FromError(err).Code {
		case "package_invalid":
			return update.SetupReport{Status: update.SetupSkipped, Message: "the adapter's package cannot be loaded; see its adapter line"}, nil
		case "adapter_unknown":
			return update.SetupReport{Status: update.SetupSkipped, Message: "the adapter is not installed"}, nil
		}
		return update.SetupReport{}, err
	}
	res, _, err := runSetup(ctx, s, modeRefresh, setupDialog{})
	if err != nil {
		return update.SetupReport{}, err
	}
	rep := update.SetupReport{Status: update.SetupStatus(res.Status)}
	for _, o := range res.Outcomes {
		status, message := outcomeWords(o)
		rep.Items = append(rep.Items, update.SetupItem{ID: o.ID, Status: string(status), Message: message})
	}
	for _, d := range res.Drift {
		rep.Drift = append(rep.Drift, update.SetupDifference{ID: d.ID, Problem: string(d.Problem), Summary: d.Summary})
	}
	if why := setupFailure(res); why != "" {
		rep.Status, rep.Message = update.SetupError, why
	}
	if len(rep.Drift) > 0 {
		rep.Fix = n.setupFix(instance)
	}
	return rep, nil
}

// setupFix is the command that settles the drift of an instance's setup, as a person
// types it: a node whose data directory is not the default is named by --home, as the
// command a client starts for the MCP server names it.
func (n *managementNode) setupFix(instance string) string {
	words := []string{"sidera"}
	if !n.setupEnv.homeIsDefault {
		words = append(words, "--home", shellWord(runtime.GOOS, n.setupEnv.home))
	}
	return strings.Join(append(words, "adapter", "setup", instance), " ")
}
