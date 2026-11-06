//go:build !windows

package cli

import (
	"context"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A setup program drives a client in a process group of its own, and the end of the
// command (a time limit, a cancelled context) must reach that client through the
// program: the program is asked to stop with SIGTERM and gets the time to do it. A
// kill would leave the client running.
func TestExecSetupAsksTheProgramToStop(t *testing.T) {
	dir := os.Getenv("SIDERA_SETUP_STOP_HELPER")
	if dir != "" {
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, syscall.SIGTERM)
		if err := os.WriteFile(filepath.Join(dir, "ready"), nil, 0o600); err != nil {
			os.Exit(4)
		}
		<-stop
		if err := os.WriteFile(filepath.Join(dir, "terminated"), nil, 0o600); err != nil {
			os.Exit(5)
		}
		os.Exit(1)
	}
	dir = t.TempDir()
	t.Setenv("SIDERA_SETUP_STOP_HELPER", dir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for {
			if _, err := os.Stat(filepath.Join(dir, "ready")); err == nil {
				cancel()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	if _, err := (execSetup{}).Run(ctx, []string{os.Args[0], "-test.run=^TestExecSetupAsksTheProgramToStop$"}, nil); err == nil {
		t.Error("a cancelled run reported success")
	}
	if _, err := os.Stat(filepath.Join(dir, "terminated")); err != nil {
		t.Errorf("the program was not asked to stop with SIGTERM: %v", err)
	}
}
