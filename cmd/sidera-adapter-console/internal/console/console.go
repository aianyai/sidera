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
	exitOK        = 0
	exitFailed    = 1 // the daemon, the adapter or the target refused or failed
	exitUsage     = 2
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
  status <message-id>    delivery state of a message sent from the owner endpoint
  whoami                 the address of the owner endpoint
  version                the version, the commit and the Go version of this program
  package --goos <os> --goarch <arch> --binary <file> --out <directory>
                         write an adapter package for 'sidera adapter install'
  serve                  the adapter role; the daemon starts it, a person does not

flags of every client command:
  --home <directory>     the node's data directory (default: $SIDERA_HOME, then ~/.sidera)
  --instance <name>      the adapter instance (default: console)

exit codes: 0 done, 1 failed, 2 usage, 3 not settled yet
`

// Main runs the program with its arguments after the program name and returns the
// exit code. It is the only entry the program has.
func Main(args []string, env Env) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(env.Stderr, usageText)
		if len(args) == 0 {
			return exitUsage
		}
		return exitOK
	}
	switch args[0] {
	case "serve":
		return runServe(env)
	case "package":
		return runPackage(args[1:], env)
	case "version":
		fmt.Fprintln(env.Stdout, release.Read().Describe(ProgramName))
		return exitOK
	}
	return runClient(args, env)
}

func runServe(env Env) int {
	log := slog.New(slog.NewTextHandler(env.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	out, ok := env.Stdout.(io.WriteCloser)
	if !ok {
		out = nopCloser{env.Stdout}
	}
	if err := serve(ctx, env.Stdin, out, log); err != nil {
		fmt.Fprintln(env.Stderr, "sidera-adapter-console:", err)
		return exitFailed
	}
	return exitOK
}

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
