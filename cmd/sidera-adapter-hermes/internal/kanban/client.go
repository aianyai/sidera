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
	"runtime"
	"strings"
	"time"
)

// MaxArgBytes bounds every command-line argument the client passes to hermes.
// A Linux argument is limited to 128 KiB and a Windows command line to about 32K
// characters. Card bodies go through standard input; what can only be an argument
// (a summary, a result, metadata, a comment, a block reason, a title) is refused
// above this bound without running anything, so a caller puts long text in an
// attachment. An argument is always text the caller wrote, never text another
// agent sent: see the reference for why Hermes makes that a safety rule.
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
	// goos is the operating system the executable rule is applied for; it is
	// runtime.GOOS except in tests, which run the rule for another one.
	goos string
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
	return &Client{cfg: cfg, goos: runtime.GOOS}, nil
}

// ChildEnv is the environment of any hermes child of the adapter, the client's and
// `setup`'s alike: the parent's, without the variables that would redirect or scope
// the call (a Kanban worker's task and run, a board override, the profile pin), plus
// the explicit HERMES_HOME and HERMES_KANBAN_HOME of cfg. The busy timeout is the one
// Kanban variable that stays: it is an operator's tuning knob, not routing.
func ChildEnv(parent []string, cfg Config) []string {
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

// resolve finds the program the client runs and refuses one that must not be run.
// It is the one place hermes is located, so that what is checked is what is run.
func (c *Client) resolve(op string) (string, error) {
	resolved, err := exec.LookPath(c.cfg.Executable)
	if err != nil {
		return "", &Error{Op: op, Class: Transient, ExitCode: -1, Err: fmt.Errorf("hermes executable: %w", err)}
	}
	if err := CheckExecutable(c.goos, resolved); err != nil {
		return "", &Error{Op: op, Class: Permanent, ExitCode: -1, Err: err}
	}
	return resolved, nil
}

// run executes hermes with argv, without a shell. stdin may be nil. Every start of
// hermes passes through here, so the rules about what may be started and with
// which arguments are enforced once.
func (c *Client) run(ctx context.Context, op string, stdin []byte, argv []string) ([]byte, error) {
	for _, a := range argv {
		switch {
		case len(a) > MaxArgBytes:
			return nil, &Error{Op: op, Class: Permanent, ExitCode: -1, Err: fmt.Errorf(
				"%w: %d bytes against a bound of %d; long text belongs in an attachment",
				ErrArgTooLong, len(a), MaxArgBytes)}
		case strings.IndexByte(a, 0) >= 0:
			return nil, &Error{Op: op, Class: Permanent, ExitCode: -1, Err: fmt.Errorf(
				"%w: it holds a NUL byte; text that is not the caller's own belongs in standard input or an attachment",
				ErrArgInvalid)}
		}
	}
	executable, err := c.resolve(op)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, executable, argv...)
	cmd.Env = ChildEnv(os.Environ(), c.cfg)
	cmd.WaitDelay = killDelay
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
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
