package adaptersetup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// waitDelay is how long after the program has exited, or has been killed at its time
// limit, Run waits for the program's output to end. A grandchild the program left
// behind may hold the pipes open; without a limit Run would wait for it.
const waitDelay = 2 * time.Second

// failureTail is how much of a command's standard error a failure message keeps.
const failureTail = 2000

// ClientCommand is one run of a client's command line (`claude mcp get`, `codex plugin
// add`) for an adapter's setup. Run starts it without a shell, with the null device as
// standard input so that a client never waits on a terminal, and for a bounded time.
type ClientCommand struct {
	// Program is the absolute path of the executable; it is never looked up on PATH
	// and never given to a shell.
	Program string
	Args    []string
	// Dir is the working directory, and the caller must name a directory with no
	// project configuration of its own. A client reads the configuration of the
	// project it is started in, so in the caller's directory it could answer, and
	// change, something other than the user's own configuration.
	Dir string
	// Env is the environment the caller chose; nil means the caller's own.
	Env []string
	// Timeout bounds the run.
	Timeout time.Duration
}

// ClientOutput is what a command that ran to an end wrote and how it ended.
type ClientOutput struct {
	Stdout, Stderr []byte
	ExitCode       int
}

// NotStartedError is the error of a program that could not be started, such as a client
// that is not installed or not executable. Err is the operating system's reason, which
// errors.Is can match (os.ErrNotExist, os.ErrPermission).
type NotStartedError struct {
	Program string
	Err     error
}

func (e *NotStartedError) Error() string {
	return fmt.Sprintf("%s could not be started: %v", e.Program, e.Err)
}
func (e *NotStartedError) Unwrap() error { return e.Err }

// Run runs the command and waits for it. A non-zero exit is not an error here: callers
// decide what an exit code means (a client's `get` of a missing server exits non-zero
// and that is the answer), and Failure words the cases that are failures. The errors
// are: a command that is not allowed to run (a relative Program, no Dir, no Timeout), a
// *NotStartedError, a timeout (which is context.DeadlineExceeded and names the program
// and the limit), the end of ctx, and a program that exited while a process it left
// behind held its output open.
func (c ClientCommand) Run(ctx context.Context) (ClientOutput, error) {
	switch {
	case !filepath.IsAbs(c.Program):
		return ClientOutput{}, fmt.Errorf("client program %q is not an absolute path", c.Program)
	case c.Dir == "":
		return ClientOutput{}, fmt.Errorf("%s: no working directory", c.Program)
	case c.Timeout <= 0:
		return ClientOutput{}, fmt.Errorf("%s: no time limit", c.Program)
	}
	runCtx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, c.Program, c.Args...)
	cmd.Dir, cmd.Env, cmd.WaitDelay = c.Dir, c.Env, waitDelay
	// A nil Stdin is the null device on every platform: the program reads end of file.
	cmd.Stdin = nil
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	startErr := cmd.Start()
	var err error
	if startErr == nil {
		err = cmd.Wait()
	}
	// The context is checked before the error: a program killed at its limit ends with
	// an ExitError ("signal: killed"), which would read as an exit code.
	if ctx.Err() != nil {
		return ClientOutput{}, fmt.Errorf("%s: stopped: %w", c.Program, ctx.Err())
	}
	if runCtx.Err() != nil {
		return ClientOutput{}, fmt.Errorf("%s: timed out after %s: %w", c.Program, c.Timeout, runCtx.Err())
	}
	var exit *exec.ExitError
	switch {
	case startErr != nil:
		return ClientOutput{}, &NotStartedError{Program: c.Program, Err: startErr}
	case err == nil:
		return ClientOutput{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}, nil
	case errors.As(err, &exit):
		return ClientOutput{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), ExitCode: exit.ExitCode()}, nil
	default:
		return ClientOutput{}, fmt.Errorf("%s: %w", c.Program, err)
	}
}

// Failure is the error for a command whose result is not what was asked: the command,
// its exit code and the end of its standard error, which is where a client says why.
func (c ClientCommand) Failure(out ClientOutput) error {
	detail := strings.TrimSpace(string(out.Stderr))
	if len(detail) > failureTail {
		detail = "..." + strings.ToValidUTF8(detail[len(detail)-failureTail:], "")
	}
	if detail == "" {
		detail = "no output on standard error"
	}
	return fmt.Errorf("%s %s failed (exit %d): %s", c.Program, strings.Join(c.Args, " "), out.ExitCode, detail)
}
