//go:build darwin || linux

package process

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"
)

// Terminate signals nothing it cannot show to be the process the identity names:
// when the process table does not answer, and when it says the process is gone,
// the process the PID names stays untouched.
func TestTerminateSignalsNothingUnlessTheIdentityIsShown(t *testing.T) {
	tableFailure := errors.New("the process table did not answer")
	for _, tc := range []struct {
		name    string
		running func(Identity) (bool, error)
		want    error
	}{
		{"the table does not answer", func(Identity) (bool, error) { return false, tableFailure }, tableFailure},
		{"another process holds the PID", func(Identity) (bool, error) { return false, nil }, ErrNotRunning},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command("sleep", "60")
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}()
			err := terminateWith(context.Background(), Identity{PID: cmd.Process.Pid, StartKey: "x"}, time.Millisecond, tc.running)
			if !errors.Is(err, tc.want) {
				t.Fatalf("terminate = %v, want %v", err, tc.want)
			}
			time.Sleep(200 * time.Millisecond)
			if p, found, err := lookup(cmd.Process.Pid, false); err != nil || !found {
				t.Fatalf("the process does not run any more: %+v, %v, %v", p, found, err)
			}
		})
	}
}
