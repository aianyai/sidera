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
	"strconv"
	"strings"

	"github.com/aianyai/sidera/internal/node/agentapi"
	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/daemon"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// Config supplies the process streams and environment. Run borrows them.
type Config struct {
	Stdin   io.ReadCloser
	Stdout  io.WriteCloser
	Stderr  io.Writer
	Getenv  func(string) string
	Version string
}

type command struct {
	Config
	home      string
	json      bool
	help      bool
	arguments *commandArguments
}
type usageError struct{ message string }

func (e *usageError) Error() string { return e.message }
func usage(message string) error    { return &usageError{message} }

// Run dispatches argv, including the executable name, and returns its exit code.
// Only main ends the process, so clients and daemon resources are closed first.
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
			err = c.run(ctx, args)
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
	if c.json {
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
	if c.home == "" {
		c.home = c.Getenv("SIDERA_HOME")
	}
	if c.home == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		c.home = filepath.Join(home, ".sidera")
	}
	abs, err := filepath.Abs(c.home)
	c.home = abs
	return out, err
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

func (c *command) run(ctx context.Context, args []string) error {
	if args[0] == "init" || (args[0] == "daemon" && (len(args) == 1 || args[1] != "stop")) {
		return c.local(ctx, args)
	}
	switch args[0] {
	case "daemon", "alerts", "preset", "adapter", "endpoint", "node", "identity", "clock":
		return c.manage(ctx, args)
	case "status":
		if len(args) == 1 {
			return c.manage(ctx, args)
		}
	}
	client, err := agentapi.NewClient(agentapi.ClientConfig{Home: c.home, Token: adapterproto.Token(c.Getenv("SIDERA_TOKEN")), ProbeToken: adapterproto.Token(c.Getenv("SIDERA_PROBE_TOKEN"))})
	if err != nil {
		return err
	}
	defer client.Close()
	switch args[0] {
	case "status":
		p, err := parse(c.arguments.flags, args[1:])
		if err != nil {
			return err
		}
		if len(p) != 1 {
			return usage("status needs a message identifier")
		}
		out, err := client.Call(ctx, "status", map[string]any{"message_id": p[0]})
		if err != nil {
			return err
		}
		return c.result(out)
	case "me", "peers", "directory", "declare", "probe":
		return c.query(ctx, client, args)
	case "send", "reply", "control":
		return c.message(ctx, client, args)
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
		out, err := client.Call(ctx, "recv", in)
		if err != nil {
			return err
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
		out, err := client.Call(ctx, "ack", in)
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
