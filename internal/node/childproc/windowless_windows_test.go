package childproc_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"testing"

	"github.com/aianyai/sidera/internal/node/childproc"
	"golang.org/x/sys/windows"
)

// The test binary is its own helper: started with childproc_helper set it takes
// one of the roles below and never runs a test.
const helperEnv = "childproc_helper"

const (
	// roleIntermediate stands for the daemon a restart starts: it has no console. It
	// starts the child with Windowless.
	roleIntermediate = "intermediate"
	// roleChild stands for an adapter. It reports, then starts the grandchild.
	roleChild = "child"
	// roleGrandchild stands for what an adapter starts, with no creation flag.
	roleGrandchild = "grandchild"
)

// consoleReport is what a process says about its console.
type consoleReport struct {
	ConsoleCP     uint32 // 0 when the process has no console
	ConsoleWindow uintptr
}

func TestMain(m *testing.M) {
	var err error
	switch os.Getenv(helperEnv) {
	case "":
		os.Exit(m.Run())
	case roleIntermediate:
		err = runIntermediate()
	case roleChild:
		err = runChild()
	case roleGrandchild:
		report()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, os.Getenv(helperEnv)+":", err)
		os.Exit(1)
	}
	os.Exit(0)
}

func report() {
	var r consoleReport
	r.ConsoleCP, _ = windows.GetConsoleCP()
	r.ConsoleWindow, _, _ = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleWindow").Call()
	_ = json.NewEncoder(os.Stdout).Encode(r)
}

// helper returns a command that runs this binary in the given role. Standard
// error is left to the caller: a command with none gets the null device, which
// would hide why a role failed.
func helper(role string) (*exec.Cmd, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), helperEnv+"="+role)
	return cmd, nil
}

// runIntermediate reports its own console, then starts the child as the daemon
// starts an adapter, and passes the reports of the child and the grandchild on.
func runIntermediate() error {
	report()
	cmd, err := helper(roleChild)
	if err != nil {
		return err
	}
	childproc.Windowless(cmd)
	cmd.Stderr = os.Stderr
	// The pipes for standard input and output are those the daemon gives an adapter.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	if _, err := io.Copy(os.Stdout, stdout); err != nil {
		return err
	}
	_ = stdin.Close()
	return cmd.Wait()
}

func runChild() error {
	report()
	// The grandchild is started the way any program does, with no creation flag.
	cmd, err := helper(roleGrandchild)
	if err != nil {
		return err
	}
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

// A program started with Windowless has a console and no window for it, and so has
// a program that program starts with a plain exec.Command. This holds whatever
// console the test process has: the test starts an intermediate with no console,
// as the daemon a restart starts has none, and the intermediate starts the child.
// Without the flag the child would get a new visible console, so its window
// handle would not be zero, and the grandchild would share that console.
func TestWindowlessChildAndGrandchildHaveAConsoleWithoutAWindow(t *testing.T) {
	cmd, err := helper(roleIntermediate)
	if err != nil {
		t.Fatal(err)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	reports := json.NewDecoder(stdout)
	var got []consoleReport
	var readErr error
	for range 3 {
		var r consoleReport
		if readErr = reports.Decode(&r); readErr != nil {
			break
		}
		got = append(got, r)
	}
	_, _ = io.Copy(io.Discard, stdout)
	waitErr := cmd.Wait()
	if readErr != nil || waitErr != nil {
		t.Fatalf("read report %d: %v; wait: %v; standard error:\n%s", len(got)+1, readErr, waitErr, stderr.String())
	}
	if got[0].ConsoleCP != 0 {
		t.Fatalf("the intermediate has a console (code page %d), so the test does not reproduce a daemon without one", got[0].ConsoleCP)
	}
	for i, who := range []string{"child", "grandchild"} {
		r := got[i+1]
		if r.ConsoleCP == 0 {
			t.Errorf("%s has no console, want a console without a window", who)
		}
		if r.ConsoleWindow != 0 {
			t.Errorf("%s has a console window %#x, want none", who, r.ConsoleWindow)
		}
	}
	if stderr.Len() != 0 {
		t.Logf("standard error:\n%s", stderr.String())
	}
}
