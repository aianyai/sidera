// Package cli is the command-line front end of the node's public interfaces.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/aianyai/sidera/cmd/sidera/internal/controller"
	"github.com/aianyai/sidera/cmd/sidera/internal/launch"
	"github.com/aianyai/sidera/cmd/sidera/internal/mcp"
	"github.com/aianyai/sidera/internal/datahome"
	"github.com/aianyai/sidera/internal/node/agentapi"
	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/daemon"
	"github.com/aianyai/sidera/internal/node/manageapi"
	"github.com/aianyai/sidera/internal/node/txn"
	"github.com/aianyai/sidera/internal/release"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// Config supplies the process streams and environment. Run borrows them.
type Config struct {
	Stdin  io.ReadCloser
	Stdout io.WriteCloser
	Stderr io.Writer
	Getenv func(string) string
	// Executable is the path of the running program (os.Executable).
	Executable func() (string, error)
	// Release is what the program was built from.
	Release release.Info
	// Launch is how the program was started, captured before anything changed the
	// working directory. LaunchErr is why it could not be captured; Launch is then
	// not to be used.
	Launch    launch.Launch
	LaunchErr error
}

type command struct {
	Config
	home       string // the value of --home, as given
	json       bool
	help       bool
	random     io.Reader
	tempRandom io.Reader
	observer   txn.Observer
	afterRecv  func()
	arguments  *commandArguments
	// restart is set when the foreground daemon ended because it was asked to restart.
	restart bool
}
type usageError struct{ message string }

func (e *usageError) Error() string { return e.message }
func usage(message string) error    { return &usageError{message} }

// failure is a command that ran and did not get what it waited for, with the words
// for the owner. It is a plain failure: exit code 1, no category of the wire.
type failure struct{ message string }

func (e *failure) Error() string { return e.message }

// Run dispatches argv, including the executable name, and returns its exit code.
// Only main ends the process, so clients and daemon resources are closed first. A
// daemon that was restarted is the exception that proves it: once everything it held
// is released, Run starts the program again, which on Unix replaces the process and
// returns only when that failed.
func Run(ctx context.Context, argv []string, cfg Config) int {
	c := command{Config: cfg}
	args := argv[1:]
	if len(argv) > 0 && strings.TrimSuffix(filepath.Base(argv[0]), ".exe") == "siderad" {
		args = append([]string{"daemon"}, args...)
	}
	c.arguments = arguments(args)
	args, err := c.globals(args, c.arguments.flags)
	if err == nil {
		if c.help {
			err = c.printHelp(args)
		} else if len(args) == 0 {
			err = usage("a command is required; use --help")
		} else {
			role := "cli"
			if runsLocally(args) {
				role = args[0]
			}
			close, setupErr := c.configureTestProcess(role)
			if setupErr != nil {
				err = setupErr
			} else {
				err = c.run(ctx, args)
				err = errors.Join(err, close())
				if err == nil && c.restart {
					// Everything the daemon and this command held is released: the
					// instance lock, the logs, the sockets and the test records.
					if replaceErr := c.Launch.Replace(); replaceErr != nil {
						err = &failure{fmt.Sprintf("the daemon stopped but could not start %s again: %v", c.Launch.Program, replaceErr)}
					}
				}
			}
		}
	}
	if err == nil {
		return 0
	}
	var u *usageError
	if errors.As(err, &u) {
		fmt.Fprintln(c.Stderr, "error:", u.message)
		return apiwire.ExitUsage
	}
	var fail *failure
	if errors.As(err, &fail) {
		fmt.Fprintln(c.Stderr, "error:", fail.message)
		return 1
	}
	var d *daemonError
	if errors.As(err, &d) {
		if c.json {
			_ = c.encode(apiwire.Body{Error: *apiwire.FromError(err)})
		} else {
			fmt.Fprintln(c.Stderr, "error:", d.err)
		}
		return daemon.ExitCode(d.err)
	}
	e := apiwire.FromError(err)
	if c.json && c.arguments.flags.Name() != "mcp" {
		_ = c.encode(apiwire.Body{Error: *e})
	} else {
		fmt.Fprintln(c.Stderr, "error:", e)
		if id, ok := e.Details["operation_id"].(string); ok && id != "" {
			fmt.Fprintln(c.Stderr, "operation_id", id)
		}
	}
	return e.Category.ExitCode()
}

func (c *command) globals(args []string, local *flag.FlagSet) ([]string, error) {
	var out []string
	// A value belongs to its preceding local flag, even when it happens to be
	// spelled like a global flag (for example --body --json).
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			out = append(out, args[i:]...)
			break
		}
		option, _ := globalOption(a)
		switch option.name {
		case "help":
			c.help = true
		case "home":
			if option.takesNext {
				i++
				if i == len(args) {
					return nil, usage("--home needs a directory")
				}
				option.value = args[i]
			}
			c.home = option.value
		case "json":
			b, err := strconv.ParseBool(option.value)
			if err != nil {
				return nil, usage("--json needs a boolean")
			}
			c.json = b
		default:
			out = append(out, a)
			name, _, has := strings.Cut(strings.TrimLeft(a, "-"), "=")
			if v := local.Lookup(name); strings.HasPrefix(a, "-") && !has && v != nil && takesValue(v) && i+1 < len(args) {
				i++
				out = append(out, args[i])
			}
		}
	}
	return out, nil
}

// dataDir is the data directory of the command: --home, then SIDERA_HOME, then
// the default of the platform, made absolute. It is resolved by the commands that
// use it and not before, so that version and help work on a machine whose default
// is refused (see datahome.Resolve).
func (c *command) dataDir() (string, error) {
	home, err := datahome.Resolve(c.home, datahome.Host{OS: runtime.GOOS, Getenv: c.Getenv, UserHomeDir: os.UserHomeDir, Lstat: os.Lstat})
	if err != nil {
		return "", &failure{err.Error()} // the words are for the owner, as they are
	}
	return home, nil
}

func flags(name string) *flag.FlagSet {
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(io.Discard)
	return f
}

// parse accepts flags on either side of positional arguments, as the command
// examples promise, while leaving flag values and -- literals untouched.
func parse(f *flag.FlagSet, args []string) ([]string, error) {
	var options, positionals []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			positionals = append(positionals, a)
			continue
		}
		name, _, has := strings.Cut(strings.TrimLeft(a, "-"), "=")
		v := f.Lookup(name)
		if v == nil {
			return nil, usage("unknown flag: " + name)
		}
		options = append(options, a)
		if !has && takesValue(v) {
			i++
			if i == len(args) {
				return nil, usage("flag needs a value: " + name)
			}
			options = append(options, args[i])
		}
	}
	if err := f.Parse(options); err != nil {
		return nil, usage(err.Error())
	}
	return positionals, nil
}

func (c *command) encode(v any) error {
	enc := json.NewEncoder(c.Stdout)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// runsLocally says whether the command is carried out by this process on the data
// directory, and not by asking the daemon: `init` and the foreground `daemon`.
// `daemon stop` and `daemon restart` ask the daemon that runs.
func runsLocally(args []string) bool {
	if args[0] == "init" {
		return true
	}
	return args[0] == "daemon" && (len(args) == 1 || (args[1] != "stop" && args[1] != "restart"))
}

func (c *command) run(ctx context.Context, args []string) error {
	if runsLocally(args) {
		return c.local(ctx, args)
	}
	switch args[0] {
	case "version":
		return c.version(args[1:])
	case "setup":
		return c.setup(args[1:])
	case "daemon", "alerts", "preset", "adapter", "endpoint", "node", "identity", "clock", "network", "trust":
		return c.manage(ctx, args)
	case "status":
		if len(args) == 1 {
			return c.manage(ctx, args)
		}
	}
	probeToken := adapterproto.Token(c.Getenv("SIDERA_PROBE_TOKEN"))
	home, err := c.dataDir()
	if err != nil {
		return err
	}
	client, err := agentapi.NewClient(agentapi.ClientConfig{Home: home, Token: adapterproto.Token(c.Getenv("SIDERA_TOKEN")), ProbeToken: probeToken})
	if err != nil {
		return err
	}
	defer client.Close()
	agent := &attachingClient{Client: client, attach: func(ctx context.Context) error { return c.attachController(ctx, home, client) }}
	switch args[0] {
	case "mcp":
		p, err := parse(c.arguments.flags, args[1:])
		if err != nil {
			return err
		}
		if len(p) != 0 {
			return usage("mcp takes no positional arguments")
		}
		cfg := mcp.Config{
			Stdin: c.Stdin, Stdout: c.Stdout, Stderr: c.Stderr,
			Agent: client, Version: c.Release.Version, ProbeOnConnect: probeToken != "",
		}
		if controller.Wanted(c.Getenv) {
			manage, err := manageapi.NewClient(manageapi.ClientConfig{Home: home})
			if err != nil {
				return err
			}
			defer manage.Close()
			cfg.Controller = &controller.Config{Manage: manage, Getenv: c.Getenv, Getwd: os.Getwd}
		}
		return mcp.Run(ctx, cfg)
	case "status":
		p, err := parse(c.arguments.flags, args[1:])
		if err != nil {
			return err
		}
		if len(p) != 1 {
			return usage("status needs a message identifier")
		}
		out, err := agent.Call(ctx, "status", map[string]any{"message_id": p[0]})
		if err != nil {
			return err
		}
		return c.result(out)
	case "me", "peers", "directory", "declare", "probe":
		return c.query(ctx, agent, args)
	case "send", "reply", "control":
		return c.message(ctx, agent, args)
	case "recv":
		f := c.arguments.flags
		in := map[string]any{"frontend": "cli"}
		p, err := parse(f, args[1:])
		if err != nil {
			return err
		}
		if len(p) != 0 {
			return usage("recv takes no positional arguments")
		}
		addPresent(f, in, "wait", "wait_s", c.arguments.recv.wait)
		addPresent(f, in, "msg", "msg", c.arguments.recv.msg)
		addPresent(f, in, "part", "part", c.arguments.recv.part)
		addPresent(f, in, "ack", "ack_lease", c.arguments.recv.ack)
		out, err := agent.Call(ctx, "recv", in)
		if err != nil {
			return err
		}
		if c.afterRecv != nil {
			c.afterRecv()
		}
		_, err = c.Stdout.Write(out.([]byte))
		return err
	case "ack":
		p, err := parse(c.arguments.flags, args[1:])
		if err != nil {
			return err
		}
		if len(p) < 1 {
			return usage("ack needs a lease")
		}
		in := map[string]any{"lease": p[0]}
		var items []adapterproto.PartRef
		for _, value := range p[1:] {
			id, part, ok := strings.Cut(value, ":")
			k, err := strconv.Atoi(part)
			if !ok || err != nil {
				return usage("ack items are message-id:part")
			}
			items = append(items, adapterproto.PartRef{MessageID: adapterproto.MessageID(id), Part: k})
		}
		if len(items) > 0 {
			in["items"] = items
		}
		out, err := agent.Call(ctx, "ack", in)
		if err != nil {
			return err
		}
		return c.result(out)
	}
	return usage("unknown command: " + args[0])
}

type receiveFlags struct {
	wait int64
	msg  string
	part int
	ack  string
}

func (r *receiveFlags) add(f *flag.FlagSet) {
	f.Int64Var(&r.wait, "wait", 0, "seconds to wait")
	f.StringVar(&r.msg, "msg", "", "message from page next")
	f.IntVar(&r.part, "part", 0, "part from page next")
	f.StringVar(&r.ack, "ack", "", "earlier lease to acknowledge")
}
