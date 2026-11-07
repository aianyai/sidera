// Package console is the console bridge adapter: a direct endpoint on the node,
// `owner`, that the node's owner uses from a terminal to message agents and read
// their replies. One program has two roles. The adapter role (`serve`) is what the
// daemon starts; it registers the endpoint and serves a local socket. The client
// role (every other command) talks to that socket, one request per connection.
package console

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"

	"github.com/aianyai/sidera/internal/release"
	"github.com/aianyai/sidera/sdk/go/adapterkit"
)

const (
	// AdapterID is the adapter's id in its manifest and in initialize, and the
	// instance name the client looks for unless it is told another.
	AdapterID = "console"
	// ProgramName is the program of the package, without an extension.
	ProgramName = "sidera-adapter-console"

	// endpointKey is the key the adapter registers its endpoint under; the same key
	// is the same endpoint, so the address survives every restart.
	endpointKey = "owner"
	// maxEndpoints is what the adapter declares; it registers one.
	maxEndpoints = 8
)

// Env is what the program is given by its process.
type Env struct {
	Getenv func(string) string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// Exit codes of the client commands.
const (
	exitOK     = adapterkit.ExitOK
	exitFailed = adapterkit.ExitFailed // the daemon, the adapter or the target refused or failed
	exitUsage  = adapterkit.ExitUsage
	// exitUnsettled: nothing is wrong yet, but what happened is not known: see the message.
	exitUnsettled = adapterkit.ExitUnknownOutcome
)

// help is what `help` and `--help` print, besides the commands the kit runs.
var help = adapterkit.Help{
	Usage: ProgramName + " <command> [flags]",
	Groups: []adapterkit.HelpGroup{
		{Title: "Messages", Entries: []adapterkit.HelpEntry{
			{Name: "send", Arguments: "--to <address>[,<address>...] (--body <text> | --body-file <file>)",
				Purpose: "Send a message from the owner endpoint."},
			{Name: "reply", Arguments: "<message-id> [--to <address>] (--body <text> | --body-file <file>)",
				Purpose: "Reply to a message the owner endpoint received."},
			{Name: "recv", Arguments: "[--wait <seconds>]",
				Purpose: "Print the messages waiting for the owner endpoint, then acknowledge them."},
			{Name: "status", Arguments: "<message-id>",
				Purpose: "Show the delivery state of a message sent from the owner endpoint."},
		}},
		{Title: "Sessions", Entries: []adapterkit.HelpEntry{
			{Name: "new", Arguments: "<runtime-address> --preset <name> [--body <text> | --body-file <file>] [--timeout <seconds>, default 120]",
				Purpose: "Start a session on a runtime endpoint; prints the new session's address."},
		}},
		{Title: "Where things are", Entries: []adapterkit.HelpEntry{
			{Name: "whoami", Purpose: "Show the address of the owner endpoint."},
			{Name: "peers", Purpose: "List the nodes the owner endpoint can address."},
			{Name: "directory", Arguments: "[--node <id>] [--limit <n>] [--cursor <c>]",
				Purpose: "List the endpoints a node shows to the owner endpoint (--node names a node from `peers`)."},
		}},
		{Title: "Flags of every client command", Entries: []adapterkit.HelpEntry{
			{Name: "--home <directory>", Purpose: "The node's data directory (default: $SIDERA_HOME, then ~/.sidera, on Windows %LOCALAPPDATA%\\Sidera)."},
			{Name: "--instance <name>", Purpose: "The adapter instance (default: console)."},
		}},
	},
	Footer: "Exit codes: 0 done, 1 failed, 2 usage, 10 outcome not known yet (the same codes as sidera).",
}

// clientCommands are the commands of the client role, which talks to the adapter's
// socket (see client.go).
var clientCommands = []string{"new", "recv", "reply", "send", "directory", "peers", "status", "whoami"}

// Main runs the program with its arguments after the program name and returns the
// exit code. It is the only entry the program has.
func Main(args []string, env Env) int {
	return program(env).Run(args, env.streams())
}

// program is the command line of the executable: the client commands run the client
// role with env, and `serve` the adapter role.
func program(env Env) adapterkit.Program {
	commands := make(map[string]adapterkit.Command, len(clientCommands))
	for _, name := range clientCommands {
		commands[name] = func(rest []string, _ adapterkit.Streams) int {
			return runClient(append([]string{name}, rest...), env)
		}
	}
	return adapterkit.Program{
		Name:     ProgramName,
		Help:     help,
		Serve:    runServe,
		Package:  writePackage,
		Build:    release.Read(),
		Commands: commands,
	}
}

// streams are the streams of env for the kit.
func (e Env) streams() adapterkit.Streams {
	return adapterkit.Streams{Stdin: e.Stdin, Stdout: e.Stdout, Stderr: e.Stderr, Getenv: e.Getenv}
}

func runServe(s adapterkit.Streams) int {
	log := slog.New(slog.NewTextHandler(s.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	out, ok := s.Stdout.(io.WriteCloser)
	if !ok {
		out = nopCloser{s.Stdout}
	}
	if err := serve(ctx, s.Stdin, out, log); err != nil {
		fmt.Fprintln(s.Stderr, "sidera-adapter-console:", err)
		return exitFailed
	}
	return exitOK
}

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
