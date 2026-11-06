package adaptersetup_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/sdk/go/adaptersetup"
)

const helperEnv = "ADAPTERSETUP_CLIENT_HELPER"

// TestHelperProcess is the "client" the tests run: the test binary itself, doing what
// the words after "--" say. It is not a test when helperEnv is unset.
func TestHelperProcess(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	switch args[1] {
	case "exit":
		var code int
		fmt.Sscan(args[2], &code)
		fmt.Fprint(os.Stdout, "out")
		fmt.Fprint(os.Stderr, "err")
		os.Exit(code)
	case "noisy":
		fmt.Fprint(os.Stderr, strings.Repeat("a", 5000)+"END")
		os.Exit(1)
	case "sleep":
		time.Sleep(5 * time.Second)
	case "orphan":
		// A grandchild that keeps the output pipes open after this process is gone.
		child := exec.Command(os.Args[0], "-test.run=TestHelperProcess", "--", "sleep")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(3)
		}
		time.Sleep(5 * time.Second)
	case "pwd":
		wd, _ := os.Getwd()
		fmt.Fprint(os.Stdout, wd)
	case "stdin":
		n, err := os.Stdin.Read(make([]byte, 1))
		fmt.Fprintf(os.Stdout, "%d %v", n, err)
	}
	os.Exit(0)
}

func helper(t *testing.T, timeout time.Duration, args ...string) adaptersetup.ClientCommand {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return adaptersetup.ClientCommand{
		Program: exe,
		Args:    append([]string{"-test.run=TestHelperProcess", "--"}, args...),
		Dir:     t.TempDir(),
		Env:     append(os.Environ(), helperEnv+"=1"),
		Timeout: timeout,
	}
}

func TestClientCommandRun(t *testing.T) {
	for _, tc := range []struct {
		name         string
		args         []string
		stdout       string
		stderr       string
		code         int
		stderrSuffix string
	}{
		{name: "success", args: []string{"exit", "0"}, stdout: "out", stderr: "err"},
		{name: "a non-zero exit is a result, not an error", args: []string{"exit", "7"}, stdout: "out", stderr: "err", code: 7},
		{name: "large stderr is captured whole", args: []string{"noisy"}, code: 1, stderrSuffix: "END"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := helper(t, time.Minute, tc.args...).Run(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if out.ExitCode != tc.code {
				t.Errorf("exit code = %d, want %d", out.ExitCode, tc.code)
			}
			if tc.stdout != "" && string(out.Stdout) != tc.stdout {
				t.Errorf("stdout = %q, want %q", out.Stdout, tc.stdout)
			}
			if tc.stderr != "" && string(out.Stderr) != tc.stderr {
				t.Errorf("stderr = %q, want %q", out.Stderr, tc.stderr)
			}
			if !strings.HasSuffix(string(out.Stderr), tc.stderrSuffix) {
				t.Errorf("stderr ends %q, want %q", out.Stderr[max(0, len(out.Stderr)-10):], tc.stderrSuffix)
			}
		})
	}
}

func TestClientCommandRunsInTheDirectoryAndWithoutInput(t *testing.T) {
	c := helper(t, time.Minute, "pwd")
	out, err := c.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(c.Dir)
	got, _ := filepath.EvalSymlinks(string(out.Stdout))
	if got != want {
		t.Errorf("working directory = %q, want %q", got, want)
	}

	// The child must see end of file at once, not wait on this process's input.
	out, err = helper(t, time.Minute, "stdin").Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(out.Stdout) != "0 EOF" {
		t.Errorf("a read of standard input gave %q, want %q", out.Stdout, "0 EOF")
	}
}

func TestClientCommandTimeout(t *testing.T) {
	for _, mode := range []string{"sleep", "orphan"} {
		t.Run(mode, func(t *testing.T) {
			start := time.Now()
			_, err := helper(t, 200*time.Millisecond, mode).Run(context.Background())
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("error = %v, want one that is context.DeadlineExceeded", err)
			}
			if msg := err.Error(); !strings.Contains(msg, "timed out after 200ms") || !strings.Contains(msg, filepath.Base(os.Args[0])) {
				t.Errorf("error %q does not name the program and the limit", msg)
			}
			// A grandchild that holds the pipes must not hold the caller past the limit
			// by more than the wait delay; it sleeps for five seconds.
			if took := time.Since(start); took > 4*time.Second {
				t.Errorf("returned after %s", took)
			}
		})
	}
}

func TestClientCommandCancelIsNotATimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := helper(t, time.Minute, "sleep").Run(ctx)
	if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "timed out") {
		t.Errorf("error = %v, want a cancellation", err)
	}
}

func TestClientCommandNotStarted(t *testing.T) {
	c := helper(t, time.Minute)
	c.Program = filepath.Join(t.TempDir(), "no-such-client")
	_, err := c.Run(context.Background())
	var notStarted *adaptersetup.NotStartedError
	if !errors.As(err, &notStarted) {
		t.Fatalf("error = %v, want a NotStartedError", err)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("error = %v, want one that is os.ErrNotExist", err)
	}
}

func TestClientCommandRefusesWhatItCannotRunSafely(t *testing.T) {
	for name, edit := range map[string]func(*adaptersetup.ClientCommand){
		"relative program":     func(c *adaptersetup.ClientCommand) { c.Program = "claude" },
		"no working directory": func(c *adaptersetup.ClientCommand) { c.Dir = "" },
		"no timeout":           func(c *adaptersetup.ClientCommand) { c.Timeout = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			c := helper(t, time.Minute, "exit", "0")
			edit(&c)
			_, err := c.Run(context.Background())
			var notStarted *adaptersetup.NotStartedError
			if err == nil || errors.As(err, &notStarted) {
				t.Errorf("error = %v, want a refusal that is not NotStartedError", err)
			}
		})
	}
}

func TestClientCommandFailureClipsStderr(t *testing.T) {
	c := helper(t, time.Minute, "noisy")
	out, err := c.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	msg := c.Failure(out).Error()
	if !strings.Contains(msg, "(exit 1)") || !strings.HasSuffix(msg, "END") || !strings.Contains(msg, "...") {
		t.Errorf("failure %q does not report the exit code and the end of stderr", msg[:min(len(msg), 200)])
	}
	if len(msg) > 2200+len(c.Program)+200 {
		t.Errorf("failure is %d bytes; stderr was not clipped", len(msg))
	}

	out = adaptersetup.ClientOutput{ExitCode: 2}
	if msg := c.Failure(out).Error(); !strings.Contains(msg, "no output") {
		t.Errorf("failure %q for a silent command should say it wrote nothing", msg)
	}
}
