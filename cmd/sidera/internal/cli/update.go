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
// the layout of the other commands or as one JSON object, and the plan, the question and
// the steps on standard error, so that what a script reads is only the report.
func (c *command) runUpdate(ctx context.Context, cfg update.Config, req updateRequest) error {
	cfg.Step = func(step string) { fmt.Fprintln(c.errOut, step) }
	if req.check {
		verdict, err := update.Check(ctx, cfg, req.want)
		if err != nil {
			return &failure{err.Error()}
		}
		return c.printCheck(verdict)
	}
	cfg.Confirm = func(p update.Plan) (bool, error) { return c.confirmUpdate(p, req.yes) }
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

// confirmUpdate shows what is about to happen and, unless yes, asks. The plan and the
// question go to standard error.
func (c *command) confirmUpdate(p update.Plan, yes bool) (bool, error) {
	if _, err := io.WriteString(c.errOut, renderUpdatePlan(c.errOut, p)); err != nil {
		return false, err
	}
	if yes {
		return true, nil
	}
	return askDefaultNo(c.errOut, bufio.NewReader(c.Stdin), "Update?")
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
	_, err := io.WriteString(c.out, renderUpdateCheck(c.out, v))
	return err
}

func (c *command) printUpdate(rep update.Report, runErr error) error {
	if c.json {
		out := updateOutcome{Report: rep}
		if runErr != nil {
			out.Error = runErr.Error()
		}
		return c.encode(out)
	}
	_, err := io.WriteString(c.out, renderUpdateReport(c.out, rep, runErr != nil))
	return err
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

// setupFix is the command that settles the drift of an instance's setup.
func (n *managementNode) setupFix(instance string) string {
	return setupFixCommand(n.setupEnv, instance)
}
