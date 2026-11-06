package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/manageapi"
	"github.com/aianyai/sidera/sdk/go/adaptersetup"
)

const (
	// setupCallTimeout bounds one run of a setup program: it drives a client's own
	// command line, and one that hangs must not hold `sidera update` for good.
	setupCallTimeout = 5 * time.Minute
	// setupStderrTail is how much of a failed setup program's standard error is quoted.
	setupStderrTail = 2048
)

type adapterSetupFlags struct{ check, remove, refresh, yes bool }

func (o *adapterSetupFlags) add(f *flag.FlagSet) {
	f.BoolVar(&o.check, "check", false, "only report what has drifted from the record: exit 0 with none, 1 with some")
	f.BoolVar(&o.remove, "remove", false, "undo what setup recorded, asking about each item")
	f.BoolVar(&o.refresh, "refresh", false, "bring the recorded items up to date without asking, as sidera update does")
	f.BoolVar(&o.yes, "yes", false, "accept every item without asking")
}

// setupMode is what `adapter setup` does with the instance.
type setupMode int

const (
	modeSetup setupMode = iota
	modeRemove
	modeCheck
	modeRefresh
)

var (
	// errNoSetup is the answer for an instance whose adapter declares no setup.
	errNoSetup = errors.New("the adapter declares no setup")
	// errDaemonOlder is the answer of a daemon that has no adapters.setup_info.
	errDaemonOlder = errors.New("the daemon is older than this command and cannot tell how an adapter's setup runs (it has no adapters.setup_info): update the daemon to this version (sidera update) and run the command again")
)

// setupEnv is what every request of one command has in common.
type setupEnv struct {
	home          string
	homeIsDefault bool
	// program is the absolute path of the running sidera program.
	program string
}

// setupExecutor runs a setup program: argv exec'd directly, request on its standard
// input, the standard output returned. An error says how the program failed and ends
// with the tail of its standard error.
type setupExecutor interface {
	Run(ctx context.Context, argv []string, request []byte) ([]byte, error)
}

// setupSession is the setup of one instance: what the daemon said about it and how to
// run its program.
type setupSession struct {
	instance string
	argv     []string
	base     adaptersetup.Request
	exec     setupExecutor
}

// openSetupSession asks the daemon how the instance's setup runs. It returns
// errNoSetup when the adapter declares none, and errDaemonOlder when the daemon is
// older than this program.
func openSetupSession(ctx context.Context, client caller, exec setupExecutor, instance string, env setupEnv, options []string) (*setupSession, error) {
	raw, err := client.Call(ctx, "adapters.setup_info", manageapi.Request{Params: map[string]any{"instance": instance}})
	if err != nil {
		if apiwire.FromError(err).Code == "method_unknown" {
			return nil, errDaemonOlder
		}
		return nil, err
	}
	var info struct {
		StateDir  string                     `json:"state_dir"`
		Config    map[string]json.RawMessage `json:"config"`
		SetupArgv []string                   `json:"setup_argv"`
	}
	if err := decodeResult(raw, &info); err != nil {
		return nil, err
	}
	if len(info.SetupArgv) == 0 {
		return nil, errNoSetup
	}
	if options == nil {
		options = []string{}
	}
	return &setupSession{
		instance: instance, argv: info.SetupArgv, exec: exec,
		base: adaptersetup.Request{
			Version: adaptersetup.Version, Home: env.home, HomeIsDefault: env.homeIsDefault, Instance: instance,
			StateDir: info.StateDir, Config: info.Config, SideraProgram: env.program, Options: options,
		},
	}, nil
}

// call runs one op of the setup and returns its result, which it has checked.
func (s *setupSession) call(ctx context.Context, op adaptersetup.Op, accepted []string, undo bool) (adaptersetup.Result, error) {
	req := s.base
	req.Op, req.Accepted, req.Undo = op, accepted, undo
	if req.Accepted == nil {
		req.Accepted = []string{}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return adaptersetup.Result{}, err
	}
	out, err := s.exec.Run(ctx, s.argv, body)
	if err != nil {
		return adaptersetup.Result{}, &failure{fmt.Sprintf("the setup of %s failed (%s %s): %v", s.instance, s.argv[0], op, err)}
	}
	var res adaptersetup.Result
	if err := json.Unmarshal(out, &res); err != nil {
		return adaptersetup.Result{}, &failure{fmt.Sprintf("the setup of %s answered %s with something that is not a result: %v", s.instance, op, err)}
	}
	if err := res.Validate(op, req.Version); err != nil {
		return adaptersetup.Result{}, &failure{fmt.Sprintf("the setup of %s answered %s with a result this program cannot use: %v", s.instance, op, err)}
	}
	return res, nil
}

// execSetup is the setupExecutor that starts a process.
type execSetup struct{}

func (execSetup) Run(ctx context.Context, argv []string, request []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, setupCallTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var stdout bytes.Buffer
	stderr := &tailWriter{max: setupStderrTail}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = bytes.NewReader(request), &stdout, stderr
	// A process the setup program started may keep the pipes open after it is gone.
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Run(); err != nil {
		message := err.Error()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			message = fmt.Sprintf("did not finish within %s", setupCallTimeout)
		}
		if tail := strings.TrimSpace(stderr.String()); tail != "" {
			message += "; its standard error ends: " + tail
		}
		return nil, errors.New(message)
	}
	return stdout.Bytes(), nil
}

// tailWriter keeps the last max bytes written to it.
type tailWriter struct {
	max int
	buf []byte
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	if len(w.buf) > w.max {
		w.buf = w.buf[len(w.buf)-w.max:]
	}
	return len(p), nil
}

func (w *tailWriter) String() string { return string(w.buf) }

// setupDialog is how the command asks: the plan and the questions go to out (standard
// error, so that standard output carries only the report), the answers come from one
// reader.
type setupDialog struct {
	out     io.Writer
	answers *bufio.Reader
	// yes accepts every item without asking.
	yes bool
}

// choose shows the items and returns the ids of the ones to do: every item that
// needs no consent, and the others as the person answers, one by one. An item whose
// required item is not going ahead is neither asked nor done, and the listing says so.
func (d setupDialog) choose(heading string, items []adaptersetup.Item) ([]string, error) {
	fmt.Fprintln(d.out, heading)
	for _, it := range items {
		note := ""
		if !it.Consent {
			note = " (no question: it stays inside the adapter's own state)"
		}
		fmt.Fprintf(d.out, "  %s %s [%s %s]%s\n", it.Action, it.Summary, it.Kind, it.Target, note)
	}
	accepted := []string{}
	for _, it := range items {
		if missing := slices.IndexFunc(it.Requires, func(id string) bool { return !slices.Contains(accepted, id) }); missing >= 0 {
			fmt.Fprintf(d.out, "left alone: %s (it needs %s, which is not going ahead)\n", it.Summary, it.Requires[missing])
			continue
		}
		if it.Consent && !d.yes {
			ok, err := askYes(d.out, d.answers, it.Summary+"?")
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
		}
		accepted = append(accepted, it.ID)
	}
	return accepted, nil
}

// applyInStages applies the accepted items of a listing. Items that name no required
// item go in one call; an item whose required item has to be done first goes in a
// later call, and is not sent at all when the required item did not complete, which
// is a skipped outcome of the command's own, so that deleting the source of a plugin
// cannot run after the plugin failed to uninstall. This is the one place that honours
// Requires after the questions. The result is the first call's, with the outcomes of
// all of them, and an error status when any item did not complete without being
// skipped (an outcome status this program does not know counts as a failure).
func applyInStages(ctx context.Context, s *setupSession, items []adaptersetup.Item, accepted []string, undo bool) (adaptersetup.Result, error) {
	var pending []adaptersetup.Item
	for _, it := range items {
		if slices.Contains(accepted, it.ID) {
			pending = append(pending, it)
		}
	}
	attempted := map[string]bool{} // id: whether it was done
	var merged adaptersetup.Result
	calls := 0
	for len(pending) > 0 {
		var now, later []adaptersetup.Item
		var skipped []adaptersetup.Outcome
		for _, it := range pending {
			waiting, unmet := false, ""
			for _, req := range it.Requires {
				if done, tried := attempted[req]; !tried {
					waiting = true
				} else if !done {
					unmet = req
				}
			}
			switch {
			case unmet != "":
				skipped = append(skipped, adaptersetup.Outcome{ID: it.ID, Kind: it.Kind, Target: it.Target, Status: adaptersetup.OutcomeSkipped,
					Message: "left alone: it needs " + unmet + ", which did not complete"})
				attempted[it.ID] = false
			case waiting:
				later = append(later, it)
			default:
				now = append(now, it)
			}
		}
		if len(now) == 0 && len(skipped) == 0 {
			// Not reachable: Validate admits only requirements on earlier items, and
			// choose accepts an item only after the items it requires.
			return adaptersetup.Result{}, &failure{fmt.Sprintf("the setup of %s lists items that wait for each other; nothing was applied for them", s.instance)}
		}
		if len(now) > 0 {
			ids := make([]string, len(now))
			for i, it := range now {
				ids[i] = it.ID
			}
			res, err := s.call(ctx, adaptersetup.OpApply, ids, undo)
			if err != nil {
				return adaptersetup.Result{}, err
			}
			for _, o := range res.Outcomes {
				attempted[o.ID] = o.Status == adaptersetup.OutcomeDone
			}
			if calls == 0 {
				merged = res
			} else {
				merged.Outcomes = append(merged.Outcomes, res.Outcomes...)
			}
			calls++
		}
		if len(skipped) > 0 {
			merged.Outcomes = append(merged.Outcomes, skipped...)
		}
		pending = later
	}
	merged.Status = adaptersetup.StatusOK
	for _, o := range merged.Outcomes {
		if o.Failed() {
			merged.Status = adaptersetup.StatusError
		}
	}
	return merged, nil
}

// runSetup carries out a mode on the session and returns the last result the program
// gave, and whether the items were applied (the last result is then that of the
// apply; else it is the plan, which is what a person who declined everything, or an
// instance with nothing to do, is shown).
func runSetup(ctx context.Context, s *setupSession, mode setupMode, d setupDialog) (res adaptersetup.Result, applied bool, err error) {
	switch mode {
	case modeCheck:
		res, err = s.call(ctx, adaptersetup.OpCheck, nil, false)
		return res, false, err
	case modeRefresh:
		res, err = s.call(ctx, adaptersetup.OpRefresh, nil, false)
		return res, false, err
	}
	op, heading, undo := adaptersetup.OpPlan, "Setup of "+s.instance+" would:", false
	if mode == modeRemove {
		op, heading, undo = adaptersetup.OpRemove, "Removing the setup of "+s.instance+" would:", true
	}
	listing, err := s.call(ctx, op, nil, false)
	if err != nil || len(listing.Items) == 0 {
		return listing, false, err
	}
	accepted, err := d.choose(heading, listing.Items)
	if err != nil || len(accepted) == 0 {
		return listing, false, err
	}
	res, err = applyInStages(ctx, s, listing.Items, accepted, undo)
	return res, err == nil, err
}

// setupJSON is what --json writes: the last result of the program, and the instance.
type setupJSON struct {
	Instance string `json:"instance"`
	adaptersetup.Result
}

// printSetup writes the report on standard output: `key value` lines, or one JSON
// object with --json.
func (c *command) printSetup(instance string, res adaptersetup.Result, applied bool) error {
	if c.json {
		return c.encode(setupJSON{Instance: instance, Result: res})
	}
	var b strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	line("instance %s", instance)
	line("status %s", res.Status)
	if res.Recorded != nil {
		line("recorded %t", *res.Recorded)
	}
	if (res.Op == adaptersetup.OpPlan || res.Op == adaptersetup.OpRemove) && !applied {
		if len(res.Items) == 0 {
			line("nothing to do")
		} else {
			line("nothing applied")
		}
	}
	for _, o := range res.Outcomes {
		status, message := o.Status, o.Message
		if o.Failed() && o.Status != adaptersetup.OutcomeFailed {
			// A status of a later release: counted as a failure, with the adapter's words.
			status, message = adaptersetup.OutcomeFailed, strings.TrimSpace(fmt.Sprintf("(its status %q is not known to this program) %s", o.Status, o.Message))
		}
		if message != "" {
			line("item %s %s %s", o.ID, status, message)
		} else {
			line("item %s %s", o.ID, status)
		}
	}
	for _, d := range res.Drift {
		line("drift %s %s %s", d.ID, d.Problem, d.Summary)
	}
	_, err := io.WriteString(c.Stdout, b.String())
	return err
}

// setupVerdict turns a result into the exit of the command: items that failed, and
// drift that a check or a refresh found, are exit 1; a record that does not exist is
// not drift.
func setupVerdict(instance string, res adaptersetup.Result) error {
	failed := 0
	for _, o := range res.Outcomes {
		if o.Failed() {
			failed++
		}
	}
	switch {
	case res.Status == adaptersetup.StatusError || failed > 0:
		return &failure{fmt.Sprintf("the setup of %s did not complete: %d item(s) failed (see above)", instance, failed)}
	case res.Status == adaptersetup.StatusDrift:
		return &failure{fmt.Sprintf("%s has drifted from its setup: %d difference(s) (see above)", instance, len(res.Drift))}
	}
	return nil
}

// splitOptions separates the words after the first `--`, which belong to the adapter,
// from the command's own arguments.
func splitOptions(args []string) (own, options []string) {
	for i, a := range args {
		if a == "--" {
			return args[:i], args[i+1:]
		}
	}
	return args, nil
}

// adapterSetup is `adapter setup <instance>`.
func (c *command) adapterSetup(ctx context.Context, args []string) error {
	o := &c.arguments.adapterSetup
	own, options := splitOptions(args)
	p, err := parse(c.arguments.flags, own)
	if err != nil {
		return err
	}
	if len(p) != 1 {
		return usage("adapter setup needs exactly one instance; the adapter's own arguments go after --")
	}
	mode, err := o.mode()
	if err != nil {
		return err
	}
	asks := mode == modeSetup || mode == modeRemove
	if o.yes && !asks {
		return usage("--yes answers the questions of setup and of --remove; --check and --refresh ask none")
	}
	if asks && !o.yes && !isTerminal(c.Stdin) {
		return noTerminalToAsk("adapter setup")
	}
	home, err := c.dataDir()
	if err != nil {
		return err
	}
	exe, err := c.programPath()
	if err != nil {
		return err
	}
	if mode == modeSetup || mode == modeRefresh { // the modes that write the path into a client's configuration
		c.warnIfTemporary(exe, "the path kept in a client's configuration")
	}
	client, err := manageapi.NewClient(manageapi.ClientConfig{Home: home})
	if err != nil {
		return err
	}
	defer client.Close()
	env := setupEnv{home: home, homeIsDefault: c.isDefaultDataDir(home), program: exe}
	s, err := openSetupSession(ctx, client, execSetup{}, p[0], env, options)
	if errors.Is(err, errNoSetup) {
		return usage("the adapter of " + p[0] + " has no setup")
	}
	if errors.Is(err, errDaemonOlder) {
		return &failure{err.Error()}
	}
	if err != nil {
		return err
	}
	d := setupDialog{out: c.Stderr, answers: bufio.NewReader(c.Stdin), yes: o.yes}
	res, applied, err := runSetup(ctx, s, mode, d)
	if err != nil {
		return err
	}
	if err := c.printSetup(p[0], res, applied); err != nil {
		return err
	}
	return setupVerdict(p[0], res)
}

// mode says which mode the flags select; they are exclusive.
func (o *adapterSetupFlags) mode() (setupMode, error) {
	chosen := []setupMode{}
	for _, m := range []struct {
		on   bool
		mode setupMode
	}{{o.check, modeCheck}, {o.remove, modeRemove}, {o.refresh, modeRefresh}} {
		if m.on {
			chosen = append(chosen, m.mode)
		}
	}
	switch len(chosen) {
	case 0:
		return modeSetup, nil
	case 1:
		return chosen[0], nil
	}
	return 0, usage("--check, --remove and --refresh are different modes: use one")
}

// refuseRemoveWhileSetUp is the guard of `adapter remove`: an instance whose adapter
// has recorded a setup is not removed before it is undone. Removing the instance
// deletes its state directory, and with it the source of what setup installed in the
// client (a plugin whose source is there vanishes from the client without a word).
// An instance or adapter the daemon does not know, and a daemon that cannot say, have
// nothing to protect and are left to adapters.remove to answer; any other failure to
// find out refuses the removal. A refusal because the check itself failed says how
// --force removes the instance anyway, since an instance whose setup cannot run must not
// be stuck; the refusal because a setup is recorded does not, because --force would leave
// the client pointing at the directory the removal deletes, which is what the guard is for.
func refuseRemoveWhileSetUp(ctx context.Context, client caller, exec setupExecutor, instance string, env setupEnv) error {
	notRemoved := "the instance was not removed, because "
	forceHint := fmt.Sprintf(". `sidera adapter remove %s --force` removes it without this check and leaves whatever setup installed in the client in place (see \"Cleaning up an adapter's client by hand\" in the adapters guide)", instance)
	s, err := openSetupSession(ctx, client, exec, instance, env, nil)
	if err != nil {
		switch apiwire.FromError(err).Code {
		case "instance_unknown", "adapter_unknown":
			return nil
		}
		if errors.Is(err, errNoSetup) || errors.Is(err, errDaemonOlder) {
			return nil
		}
		return &failure{notRemoved + "it cannot be found out whether its adapter has a setup: " + err.Error() + forceHint}
	}
	res, err := s.call(ctx, adaptersetup.OpCheck, nil, false)
	if err != nil {
		return &failure{notRemoved + "it cannot be told whether its setup has recorded anything: " + err.Error() + forceHint}
	}
	if *res.Recorded { // Validate requires the member of a check
		return &failure{fmt.Sprintf("the instance was not removed: the setup of %s has recorded what it installed in a client, and removing the instance would delete the files that installation depends on. Run `sidera adapter setup %s --remove` first", instance, instance)}
	}
	return nil
}

// guardAdapterRemove is refuseRemoveWhileSetUp for the running program.
func (c *command) guardAdapterRemove(ctx context.Context, client caller, home, instance string) error {
	exe, err := c.programPath()
	if err != nil {
		return err
	}
	return refuseRemoveWhileSetUp(ctx, client, execSetup{}, instance, setupEnv{home: home, homeIsDefault: c.isDefaultDataDir(home), program: exe})
}
