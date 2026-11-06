package adaptersetup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
// Where the platform has process groups the client runs in a group of its own, and the
// end of the time (or of the caller's context) kills the whole group.
type ClientCommand struct {
	// Program is the absolute path of the executable; it is never looked up on PATH
	// and never given to a shell.
	Program string
	// Args are the arguments. Failure quotes them into its message, which ends up in
	// reports and logs, so a secret is never passed as an argument.
	Args []string
	// Dir is the working directory; it must exist. The caller must name a directory
	// with no project configuration of its own. A client reads the configuration of the
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
// errors.Is can match (os.ErrPermission, for one). Whether the program is simply not
// there is NotInstalled's to say, not errors.Is(err, os.ErrNotExist)'s: see there.
type NotStartedError struct {
	Program string
	Err     error
}

func (e *NotStartedError) Error() string {
	return fmt.Sprintf("%s could not be started: %v", e.Program, e.Err)
}
func (e *NotStartedError) Unwrap() error { return e.Err }

// NotInstalled reports whether err is a *NotStartedError because there is no such
// program. Every caller that reads "the client is not installed" from a failed start
// asks this and not errors.Is(err, os.ErrNotExist): on Windows a missing program given
// without an executable extension (an absolute path to "codex", say) is reported by
// os/exec as exec.ErrNotFound, which does not match os.ErrNotExist, while the same path
// with ".exe" is os.ErrNotExist.
func NotInstalled(err error) bool {
	var notStarted *NotStartedError
	return errors.As(err, &notStarted) &&
		(errors.Is(err, os.ErrNotExist) || errors.Is(err, exec.ErrNotFound))
}

// Run runs the command and waits for it. A non-zero exit is not an error here: callers
// decide what an exit code means (a client's `get` of a missing server exits non-zero
// and that is the answer), and Failure words the cases that are failures. The errors
// are: a command that is not allowed to run (a relative Program, a Dir that is not a
// directory, no Timeout), a *NotStartedError, a timeout (which is
// context.DeadlineExceeded and names the program and the limit) and the end of ctx.
//
// A program that exited by itself, with a process it left behind still holding its
// output open, has finished: once waitDelay has passed Go stops waiting for the output
// and reports exec.ErrWaitDelay in place of a result that was a success, and Run
// returns what the program wrote so far as that success. Only a run that ends in an
// error can be an end of ctx or a timeout: a program that already exited 0 is a result,
// even when ctx ended just after.
func (c ClientCommand) Run(ctx context.Context) (ClientOutput, error) {
	switch {
	case !filepath.IsAbs(c.Program):
		return ClientOutput{}, fmt.Errorf("client program %q is not an absolute path", c.Program)
	case c.Dir == "":
		return ClientOutput{}, fmt.Errorf("%s: no working directory", c.Program)
	case c.Timeout <= 0:
		return ClientOutput{}, fmt.Errorf("%s: no time limit", c.Program)
	}
	// Checked here because Go reports a missing working directory as the program's
	// own os.ErrNotExist, which the callers of Run read as "the client is not
	// installed". The reason is worded, not wrapped, so errors.Is cannot say so either.
	if info, err := os.Stat(c.Dir); err != nil || !info.IsDir() {
		reason := "it is not a directory"
		if err != nil {
			reason = err.Error()
		}
		return ClientOutput{}, fmt.Errorf("%s: the working directory %s cannot be used: %s", c.Program, c.Dir, reason)
	}
	runCtx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, c.Program, c.Args...)
	cmd.Dir, cmd.Env, cmd.WaitDelay = c.Dir, c.Env, waitDelay
	killGroupOnCancel(cmd)
	// A nil Stdin is the null device on every platform: the program reads end of file.
	cmd.Stdin = nil
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		// Start fails with the context's error when it ended before the program began.
		if ended := c.endOfRun(ctx, runCtx); ended != nil {
			return ClientOutput{}, ended
		}
		return ClientOutput{}, &NotStartedError{Program: c.Program, Err: err}
	}
	err := cmd.Wait()
	result := ClientOutput{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if err == nil || errors.Is(err, exec.ErrWaitDelay) {
		return result, nil
	}
	if ended := c.endOfRun(ctx, runCtx); ended != nil {
		return ClientOutput{}, ended
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		result.ExitCode = exit.ExitCode()
		return result, nil
	}
	return ClientOutput{}, fmt.Errorf("%s: %w", c.Program, err)
}

// endOfRun is the error for a run that the end of ctx or of the time limit cut short,
// or nil. It is asked before the exit status is read: a program killed at its limit
// ends with an ExitError ("signal: killed"), which would read as an exit code.
func (c ClientCommand) endOfRun(ctx, runCtx context.Context) error {
	if ctx.Err() != nil {
		return fmt.Errorf("%s: stopped: %w", c.Program, ctx.Err())
	}
	if runCtx.Err() != nil {
		return fmt.Errorf("%s: timed out after %s: %w", c.Program, c.Timeout, runCtx.Err())
	}
	return nil
}

// Failure is the error for a command whose result is not what was asked: the command
// with each argument quoted, so that an argument with a space or a quote reads as one,
// its exit code and the end of its standard error, which is where a client says why. The
// message names the arguments, so a caller never passes a secret as one.
func (c ClientCommand) Failure(out ClientOutput) error {
	detail := strings.TrimSpace(string(out.Stderr))
	if len(detail) > failureTail {
		detail = "..." + strings.ToValidUTF8(detail[len(detail)-failureTail:], "")
	}
	if detail == "" {
		detail = "no output on standard error"
	}
	quoted := make([]string, len(c.Args))
	for i, a := range c.Args {
		quoted[i] = strconv.Quote(a)
	}
	return fmt.Errorf("%s %s failed (exit %d): %s", c.Program, strings.Join(quoted, " "), out.ExitCode, detail)
}
