package process

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"golang.org/x/sys/windows"
)

// Exit codes of the helper; the sender maps them back.
const (
	helperFailed     = 1
	helperUsage      = 2
	helperNotRunning = 3
)

// helperOK is what the helper prints on success. A sender that gets anything else
// has started an executable that did not call HandleHelperInvocation.
const helperOK = "ok"

// ctrlCSettle is how long the helper stays attached after sending the event. The
// sequence that was measured to work keeps the console for this long, and a
// console detached at once could take the event with it.
const ctrlCSettle = 300 * time.Millisecond

// consoleGrace is how long after its start a process may still be without the
// console it was created with. A process gets its console while it starts, not at
// the moment CreateProcess returns, and until then attaching to it fails with
// ERROR_INVALID_HANDLE exactly as it does for a process that has none. The start of
// a program takes milliseconds on an idle machine and seconds on a loaded one that
// scans the file first.
const consoleGrace = 5 * time.Second

// consolePoll is how often the helper tries again within consoleGrace.
const consolePoll = 20 * time.Millisecond

// runHelper sends CTRL_C_EVENT to the console of the process named by args
// ("interrupt", pid, start key). It has no console of its own, as Interrupt starts
// it detached, and attaches to the target's for the moment of the event. Ctrl-C
// goes to every process on that console, and the helper ignores it.
func runHelper(args []string, stdout, stderr io.Writer) int {
	if len(args) != 3 || args[0] != "interrupt" {
		fmt.Fprintln(stderr, "usage: interrupt <pid> <start key>")
		return helperUsage
	}
	pid, err := strconv.Atoi(args[1])
	if err != nil {
		fmt.Fprintln(stderr, "bad pid:", err)
		return helperUsage
	}
	// The handle stays open until the event is sent: while it is, the PID names this
	// process object even if the process ends, so the console attached to cannot be
	// that of a process that took the PID over after the check.
	h, err := windows.OpenProcess(openAccess, false, uint32(pid))
	switch classifyOpen(err) {
	case absent:
		return helperNotRunning
	case refused, failed:
		fmt.Fprintln(stderr, err)
		return helperFailed
	}
	defer windows.CloseHandle(h)
	alive, err := running(h)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return helperFailed
	}
	if !alive {
		return helperNotRunning
	}
	p, err := describe(h, pid)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return helperFailed
	}
	if p.StartKey != args[2] {
		return helperNotRunning
	}
	if err := sendCtrlC(h, p); err != nil {
		if errors.Is(err, errEnded) {
			return helperNotRunning
		}
		fmt.Fprintln(stderr, err)
		return helperFailed
	}
	fmt.Fprintln(stdout, helperOK)
	return 0
}

// errEnded is sendCtrlC's answer for a process that ended while it waited for
// the console.
var errEnded = errors.New("the process ended")

// sendCtrlC attaches to the console of the process behind the open handle h and
// sends the event. A process younger than consoleGrace that cannot be attached to
// is given until then to get its console; an older one has none.
func sendCtrlC(h windows.Handle, p Process) error {
	_ = freeConsole() // there is none unless the helper was not started detached
	for {
		err := attachConsole(uint32(p.PID))
		if err == nil {
			break
		}
		if !errors.Is(err, windows.ERROR_INVALID_HANDLE) || time.Since(p.StartTime) > consoleGrace {
			return err
		}
		if alive, runErr := running(h); runErr != nil {
			return runErr
		} else if !alive {
			return errEnded
		}
		time.Sleep(consolePoll)
	}
	defer freeConsole()
	if err := setConsoleCtrlHandler(true); err != nil {
		return err
	}
	if err := windows.GenerateConsoleCtrlEvent(windows.CTRL_C_EVENT, 0); err != nil {
		return fmt.Errorf("GenerateConsoleCtrlEvent: %w", err)
	}
	time.Sleep(ctrlCSettle)
	return nil
}
