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
	exitOK        = adapterkit.ExitOK
	exitFailed    = adapterkit.ExitFailed // the daemon, the adapter or the target refused or failed
	exitUsage     = adapterkit.ExitUsage
	exitUnsettled = 3 // nothing is wrong yet, but the outcome is not known: see the message
)

const usageText = `usage: sidera-adapter-console <command> [flags]

commands:
  new <runtime-address> --preset <name> [--body <text> | --body-file <file>] [--timeout <seconds>]
                         start a session on a runtime endpoint; prints the new session's address
  recv [--wait <seconds>]
                         print the messages waiting for the owner endpoint, then acknowledge them
  reply <message-id> [--to <address>] (--body <text> | --body-file <file>)
  send --to <address>[,<address>...] (--body <text> | --body-file <file>)
  directory [--node <id>] [--limit <n>] [--cursor <c>]
                         list the endpoints a node shows to the owner endpoint (--node names a node from 'peers')
  peers                  list the nodes the owner endpoint can address
  status <message-id>    delivery state of a message sent from the owner endpoint
  whoami                 the address of the owner endpoint
  version                the version, the commit and the Go version of this program
  package --goos <os> --goarch <arch> --binary <file> --out <directory>
                         write an adapter package for 'sidera adapter install'
  serve                  the adapter role; the daemon starts it, a person does not

flags of every client command:
  --home <directory>     the node's data directory (default: $SIDERA_HOME, then ~/.sidera, on Windows %LOCALAPPDATA%\Sidera)
  --instance <name>      the adapter instance (default: console)

exit codes: 0 done, 1 failed, 2 usage, 3 not settled yet
`

// clientCommands are the commands of the client role, which talks to the adapter's
// socket (see client.go).
var clientCommands = []string{"new", "recv", "reply", "send", "directory", "peers", "status", "whoami"}

// Main runs the program with its arguments after the program name and returns the
// exit code. It is the only entry the program has.
func Main(args []string, env Env) int {
	commands := make(map[string]adapterkit.Command, len(clientCommands))
	for _, name := range clientCommands {
		commands[name] = func(rest []string, _ adapterkit.Streams) int {
			return runClient(append([]string{name}, rest...), env)
		}
	}
	return adapterkit.Program{
		Name:     ProgramName,
		Usage:    usageText,
		Serve:    runServe,
		Package:  writePackage,
		Build:    release.Read(),
		Commands: commands,
	}.Run(args, adapterkit.Streams{Stdin: env.Stdin, Stdout: env.Stdout, Stderr: env.Stderr})
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
