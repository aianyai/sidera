// Package kanban is the client of the Hermes Kanban board used by the Hermes
// adapter. It drives the `hermes kanban ... --json` command line and nothing
// else: it never opens kanban.db and never calls the dashboard's REST API, so the
// board's tables and endpoints can change between Hermes releases without
// touching this code, while the command line and its JSON are what a version
// guard and recorded samples can pin.
package kanban

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// MaxArgBytes bounds every command-line argument the client passes to hermes.
// Remote text must not travel on a command line: a Linux argument is limited to
// 128 KiB, a Windows command line to about 32K characters, and a Windows batch
// shim would let cmd.exe interpret metacharacters in it. Card bodies go through
// standard input; what can only be an argument (a summary, a result, metadata, a
// comment, a block reason, a title) is refused above this bound without running
// anything, so a caller puts long text in an attachment.
const MaxArgBytes = 8 << 10

// defaultAuthor labels the comments and attachments the client writes, in place
// of Hermes' default, the name of the profile that owns HERMES_HOME.
const defaultAuthor = "sidera"

// Config says which Hermes and which board a Client talks to.
type Config struct {
	// Executable is the path of the hermes program (or a name resolved on PATH).
	Executable string
	// Home is HERMES_HOME. It is always set explicitly in the child environment: the
	// adapter inherits the daemon's service environment, not the owner's shell.
	Home string
	// KanbanHome is HERMES_KANBAN_HOME, where the boards live. Empty leaves Hermes
	// its default, the Hermes root directory.
	KanbanHome string
	// Board is the slug of the board. Every command names it with --board; the
	// board that Hermes considers current is never used.
	Board string
	// Author labels comments and attachments. Empty means "sidera".
	Author string
}

// Client runs hermes kanban commands against one board. It holds no state and is
// safe for concurrent use.
type Client struct {
	cfg Config
}

// New checks cfg and returns a Client.
func New(cfg Config) (*Client, error) {
	switch {
	case cfg.Executable == "":
		return nil, errors.New("kanban: the hermes executable is not set")
	case cfg.Home == "":
		return nil, errors.New("kanban: HERMES_HOME is not set")
	case cfg.Board == "":
		return nil, errors.New("kanban: the board is not set")
	}
	if cfg.Author == "" {
		cfg.Author = defaultAuthor
	}
	return &Client{cfg: cfg}, nil
}

// childEnv is the environment of a hermes child: the parent's, without the
// variables that would redirect or scope the call (a Kanban worker's task and run,
// a board override, the profile pin), plus the explicit HERMES_HOME and
// HERMES_KANBAN_HOME. The busy timeout is the one Kanban variable that stays: it
// is an operator's tuning knob, not routing.
func childEnv(parent []string, cfg Config) []string {
	env := make([]string, 0, len(parent)+2)
	for _, kv := range parent {
		name, _, _ := strings.Cut(kv, "=")
		switch u := strings.ToUpper(name); {
		case u == "HERMES_HOME", u == "HERMES_PROFILE", u == "HERMES_PROFILE_NAME":
			continue
		case strings.HasPrefix(u, "HERMES_KANBAN_") && u != "HERMES_KANBAN_BUSY_TIMEOUT_MS":
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "HERMES_HOME="+cfg.Home)
	if cfg.KanbanHome != "" {
		env = append(env, "HERMES_KANBAN_HOME="+cfg.KanbanHome)
	}
	return env
}

// killDelay is how long the client waits for hermes' pipes to close after
// killing it on a cancelled context.
const killDelay = 5 * time.Second

// kanban runs `hermes kanban --board <slug> <args...>`. --board is an option of
// the kanban parent, so it precedes the subcommand.
func (c *Client) kanban(ctx context.Context, op string, stdin []byte, args ...string) ([]byte, error) {
	argv := append([]string{"kanban", "--board", c.cfg.Board}, args...)
	return c.run(ctx, op, stdin, argv)
}

// run executes hermes with argv, without a shell. stdin may be nil.
func (c *Client) run(ctx context.Context, op string, stdin []byte, argv []string) ([]byte, error) {
	for _, a := range argv {
		if len(a) > MaxArgBytes {
			return nil, &Error{Op: op, Class: Permanent, ExitCode: -1, Err: fmt.Errorf(
				"%w: %d bytes against a bound of %d; long text belongs in an attachment",
				ErrArgTooLong, len(a), MaxArgBytes)}
		}
	}
	cmd := exec.CommandContext(ctx, c.cfg.Executable, argv...)
	cmd.Env = childEnv(os.Environ(), c.cfg)
	cmd.WaitDelay = killDelay
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err == nil {
		return stdout.Bytes(), nil
	}
	// A killed child reports an exit error; the context is the cause.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, &Error{Op: op, Class: Transient, ExitCode: -1, Err: ctxErr}
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		text := stderr.String()
		return nil, &Error{Op: op, Class: classify(exitErr.ExitCode(), text),
			ExitCode: exitErr.ExitCode(), Stderr: boundedStderr(text)}
	}
	return nil, &Error{Op: op, Class: Transient, ExitCode: -1, Err: err}
}
