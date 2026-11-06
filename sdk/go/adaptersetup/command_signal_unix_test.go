//go:build darwin || linux

package adaptersetup_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aianyai/sidera/sdk/go/adapterkit"
	"github.com/aianyai/sidera/sdk/go/adaptersetup"
)

const signalHelperEnv = "ADAPTERSETUP_SIGNAL_HELPER"

// clientPlan is a handler whose plan runs a client that outlives its caller unless
// killed, and writes the client's grandchild's process ID to pidFile.
type clientPlan struct {
	t       *testing.T
	pidFile string
}

func (h clientPlan) Plan(ctx context.Context, _ adaptersetup.Request) ([]adaptersetup.Item, error) {
	_, err := helper(h.t, time.Minute, "grandchild", h.pidFile).Run(ctx)
	return nil, err
}
func (clientPlan) Apply(context.Context, adaptersetup.Request) ([]adaptersetup.Outcome, error) {
	return nil, nil
}
func (clientPlan) Remove(context.Context, adaptersetup.Request) ([]adaptersetup.Item, error) {
	return nil, nil
}
func (clientPlan) Undo(context.Context, adaptersetup.Request) ([]adaptersetup.Outcome, error) {
	return nil, nil
}
func (clientPlan) Refresh(context.Context, adaptersetup.Request) (adaptersetup.Refresh, error) {
	return adaptersetup.Refresh{}, nil
}
func (clientPlan) Check(context.Context, adaptersetup.Request) (adaptersetup.Check, error) {
	return adaptersetup.Check{}, nil
}

// TestSignalHelperProcess is the adapter program the signal test runs: the test
// binary itself, serving one plan request on standard input. It is not a test when
// signalHelperEnv is unset.
func TestSignalHelperProcess(t *testing.T) {
	pidFile := os.Getenv(signalHelperEnv)
	if pidFile == "" {
		return
	}
	code := adaptersetup.Command("prog", clientPlan{t: t, pidFile: pidFile})(nil,
		adapterkit.Streams{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr})
	os.Exit(code)
}

// A client runs in a process group of its own, so a terminal's Ctrl-C reaches the
// adapter program and not the client; the program has to turn the signal into the end
// of its context, or the client is left running after the person gave up.
func TestCommandStopsItsClientOnASignal(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			pidFile := filepath.Join(t.TempDir(), "pid")
			request, err := json.Marshal(map[string]any{"version": adaptersetup.Version, "op": "plan", "state_dir": t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			program := exec.Command(exe, "-test.run=TestSignalHelperProcess")
			program.Env = append(os.Environ(), signalHelperEnv+"="+pidFile, "GORACE=atexit_sleep_ms=0")
			program.Stdin, program.Stderr = bytes.NewReader(request), &stderr
			if err := program.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = program.Process.Kill() })

			// The client's grandchild writes its ID once the client runs.
			var pid int
			for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
				if raw, err := os.ReadFile(pidFile); err == nil {
					if pid, err = strconv.Atoi(string(raw)); err == nil {
						break
					}
				}
				if time.Now().After(deadline) {
					t.Fatal("the client did not start")
				}
			}
			t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

			if err := program.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			err = program.Wait()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != adapterkit.ExitFailed || !strings.Contains(stderr.String(), "stopped") {
				t.Fatalf("program ended with %v, stderr %q; want exit %d, the run stopped", err, stderr.String(), adapterkit.ExitFailed)
			}
			for deadline := time.Now().Add(3 * time.Second); syscall.Kill(pid, 0) == nil; time.Sleep(20 * time.Millisecond) {
				if time.Now().After(deadline) {
					t.Fatalf("process %d, the client's grandchild, is still running after the program was signalled", pid)
				}
			}
		})
	}
}
