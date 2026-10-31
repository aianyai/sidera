//go:build acceptance && diagnostic && windows

package acceptance

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

const diagnosticWCLChildArgument = "--sidera-diagnostic-wcl-child"

// This subprocess is only the calibration's own file holder. Closing the file
// and exiting are separate acknowledged operations so disappearance cannot be
// explained by process exit.
func diagnosticWCLHelperProcess() bool {
	if len(os.Args) != 3 || os.Args[1] != diagnosticWCLChildArgument || os.Getenv("SIDERA_CI_DIAGNOSTIC_WCL_OBSERVER") != "1" {
		return false
	}
	path, err := windows.UTF16PtrFromString(os.Args[2])
	if err != nil {
		os.Exit(2)
	}
	h, err := windows.CreateFile(path, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		os.Exit(2)
	}
	defer func() {
		if h != 0 {
			_ = windows.CloseHandle(h)
		}
	}()
	if _, err = io.WriteString(os.Stdout, "ready\n"); err != nil {
		os.Exit(2)
	}
	var command [6]byte
	if _, err = io.ReadFull(os.Stdin, command[:]); err != nil || string(command[:]) != "close\n" {
		os.Exit(2)
	}
	if err = windows.CloseHandle(h); err != nil {
		os.Exit(2)
	}
	// The original handle is closed exactly once, even on protocol failure.
	h = 0
	if _, err = io.WriteString(os.Stdout, "closed\n"); err != nil {
		os.Exit(2)
	}
	var exit [5]byte
	if _, err = io.ReadFull(os.Stdin, exit[:]); err != nil || string(exit[:]) != "exit\n" {
		os.Exit(2)
	}
	return true
}

func TestDiagnosticWCLCalibration(t *testing.T) {
	if os.Getenv("SIDERA_CI_DIAGNOSTIC_WCL_OBSERVER") != "1" {
		t.Skip("private Windows observation is disabled")
	}
	root := runRoot(t)
	path := filepath.Join(root, "calibration.dat")
	if err := os.WriteFile(path, []byte{0}, 0o600); err != nil {
		t.Fatal("cannot create owned calibration file")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], diagnosticWCLChildArgument, path)
	cmd.WaitDelay = time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal("cannot create calibration child input")
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		t.Fatal("cannot create calibration child output")
	}
	if err = cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		t.Fatal("cannot start owned calibration child")
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	finished := false
	defer func() {
		_ = stdin.Close()
		if !finished {
			// Abort only the child started above; no external PID is touched.
			cancel()
			<-done
		}
	}()
	readAck := func(want string) {
		t.Helper()
		result := make(chan bool, 1)
		go func() {
			got := make([]byte, len(want))
			_, err := io.ReadFull(stdout, got)
			result <- err == nil && string(got) == want
		}()
		select {
		case ok := <-result:
			if !ok {
				t.Fatal("calibration child acknowledgement failed")
			}
		case <-ctx.Done():
			t.Fatal("calibration child acknowledgement timed out")
		}
	}
	readAck("ready\n")
	owned, status, err := diagnosticWCLOpen(uint32(cmd.Process.Pid))
	r := diagnosticWCLRecord{Phase: "calibration_identity", Status: status, Errno: diagnosticWCLNativeErrno(err)}
	if err == nil {
		r.Identity = &owned.identity
	}
	diagnosticWCLEmit(r)
	if err != nil {
		t.Fatal("cannot identify owned calibration child")
	}
	defer windows.CloseHandle(owned.handle)
	if err := os.Remove(path); !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		diagnosticWCLEmit(diagnosticWCLRecord{Phase: "calibration_remove", Status: "expected_sharing_violation_missing", Errno: diagnosticWCLNativeErrno(err)})
		t.Fatal("calibration did not refuse deletion with a sharing violation")
	}
	check := func(phase string, wantPresent bool) {
		t.Helper()
		r := diagnosticWCLQuery(path, "calibration.dat", phase)
		diagnosticWCLEmit(r)
		if (r.Status != "query_success" && r.Status != "query_success_empty") || r.EndErrno != 0 {
			t.Fatal("calibration resource query was incomplete")
		}
		present := false
		for _, identity := range r.ResourceUsers {
			present = present || identity == owned.identity
		}
		if present != wantPresent {
			t.Fatal("calibration resource users did not track the exact child identity")
		}
	}
	check("calibration_open", true)
	if _, err := io.WriteString(stdin, "close\n"); err != nil {
		t.Fatal("cannot request calibration file close")
	}
	readAck("closed\n")
	if status, err := windows.WaitForSingleObject(owned.handle, 0); err != nil || status != uint32(windows.WAIT_TIMEOUT) {
		t.Fatal("calibration child did not remain alive after closing the file")
	}
	check("calibration_closed", false)
	if _, err := io.WriteString(stdin, "exit\n"); err != nil {
		t.Fatal("cannot request normal calibration child exit")
	}
	_ = stdin.Close()
	if err := <-done; err != nil {
		finished = true
		t.Fatal("calibration child did not exit normally")
	}
	finished = true
	diagnosticWCLEmit(diagnosticWCLRecord{Phase: "calibration", Status: "calibration_passed"})
}
