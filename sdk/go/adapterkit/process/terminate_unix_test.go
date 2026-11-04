//go:build unix

package process_test

import (
	"context"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aianyai/sidera/sdk/go/adapterkit/process"
)

func init() {
	// polite says it is ready and ends cleanly when it is asked to terminate.
	childModes["polite"] = func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM)
		println("ready")
		select {
		case <-sig:
			println("terminated")
		case <-time.After(childLifetime):
			os.Exit(2)
		}
	}
	// stubborn says it is ready and goes on when it is asked to terminate.
	childModes["stubborn"] = func() {
		signal.Ignore(syscall.SIGTERM)
		println("ready")
		time.Sleep(childLifetime)
	}
	// leader starts a process in its own group, says its PID and stays.
	childModes["leader"] = func() {
		exe, err := os.Executable()
		if err != nil {
			os.Exit(2)
		}
		cmd := exec.Command(exe)
		cmd.Env = childEnvironment("hold")
		if err := cmd.Start(); err != nil {
			os.Exit(2)
		}
		println("member", cmd.Process.Pid)
		_ = cmd.Wait()
	}
	// politeleader starts a process in its own group that ignores SIGTERM, says its
	// PID, and ends itself when it is asked to terminate.
	childModes["politeleader"] = func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM)
		exe, err := os.Executable()
		if err != nil {
			os.Exit(2)
		}
		cmd := exec.Command(exe)
		cmd.Env = childEnvironment("stubborn")
		if err := cmd.Start(); err != nil {
			os.Exit(2)
		}
		println("member", cmd.Process.Pid)
		select {
		case <-sig:
		case <-time.After(childLifetime):
			os.Exit(2)
		}
	}
}

// The first step is the polite one: a process that handles SIGTERM ends by itself,
// long before the grace period.
func TestTerminateAsksPolitelyFirst(t *testing.T) {
	c := startChild(t, "polite", nil)
	c.waitOutput(t, "ready")

	started := time.Now()
	if err := process.Terminate(context.Background(), c.Identity, wait); err != nil {
		t.Fatalf("Terminate: %v", err)
	}
	if d := time.Since(started); d > wait/2 {
		t.Fatalf("Terminate took %v for a process that ends when asked", d)
	}
	if err := waitDone(t, c.Done); err != nil {
		t.Fatalf("the process ended with %v, want a clean exit", err)
	}
	c.waitOutput(t, "terminated")
}

func TestTerminateKillsWhatIgnoresTheRequestAfterTheGrace(t *testing.T) {
	c := startChild(t, "stubborn", nil)
	c.waitOutput(t, "ready")

	const grace = 400 * time.Millisecond
	started := time.Now()
	if err := process.Terminate(context.Background(), c.Identity, grace); err != nil {
		t.Fatalf("Terminate: %v", err)
	}
	if d := time.Since(started); d < grace {
		t.Fatalf("Terminate returned after %v, before the grace period of %v", d, grace)
	}
	if err := waitDone(t, c.Done); err == nil {
		t.Fatal("a killed process reported a clean exit")
	}
}

// The process Start makes leads a group of its own; whatever it started in that
// group ends with it.
func TestTerminateEndsTheProcessGroup(t *testing.T) {
	terminateEndsTheGroup(t, "leader", wait)
}

// A leader that ends when asked does not end the group: a member that ignores the
// request is forced.
func TestTerminateForcesAMemberOfTheGroupThatIgnoresTheRequest(t *testing.T) {
	terminateEndsTheGroup(t, "politeleader", 500*time.Millisecond)
}

func terminateEndsTheGroup(t *testing.T, leaderMode string, grace time.Duration) {
	c := startChild(t, leaderMode, nil)
	out := c.waitOutput(t, "member ")
	_, after, _ := strings.Cut(out, "member ")
	pid, err := strconv.Atoi(strings.TrimSpace(after))
	if err != nil {
		t.Fatalf("the leader wrote %q: %v", out, err)
	}
	member, found, err := process.Lookup(pid)
	if err != nil || !found {
		t.Fatalf("Lookup(member) = %+v, %v, %v", member, found, err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

	if err := process.Terminate(context.Background(), c.Identity, grace); err != nil {
		t.Fatalf("Terminate: %v", err)
	}
	for deadline := time.Now().Add(wait); ; time.Sleep(20 * time.Millisecond) {
		running, err := process.Running(member.Identity)
		if err != nil {
			t.Fatal(err)
		}
		if !running {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the member of the group is still running")
		}
	}
}
