package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// interrupt starts the helper: the adapter's own executable, detached from any
// console and in the adapter's session. It has to be in the session of the process
// it interrupts, which a process the adapter started is, and it cannot be the
// adapter itself because attaching to another console would take the adapter's
// away. Breakaway is not asked for: the helper is short-lived and has no use for
// outliving the adapter.
func interrupt(ctx context.Context, id Identity) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("process: find the adapter's executable: %w", err)
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, exe, helperArg, "interrupt", strconv.Itoa(id.PID), id.StartKey)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS, HideWindow: true}
	err = cmd.Run()
	if ctx.Err() != nil {
		return fmt.Errorf("process: interrupt process %d: %w", id.PID, ctx.Err())
	}
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit) && exit.ExitCode() == helperNotRunning:
		return ErrNotRunning
	case errors.As(err, &exit):
		return fmt.Errorf("process: interrupt process %d: the helper failed: %s", id.PID, strings.TrimSpace(stderr.String()))
	case err != nil:
		return fmt.Errorf("process: interrupt process %d: start the helper: %w", id.PID, err)
	case strings.TrimSpace(stdout.String()) != helperOK:
		return fmt.Errorf("process: interrupt process %d: the helper did not answer ok: process.HandleHelperInvocation is not the first statement of the adapter's main", id.PID)
	}
	return nil
}
