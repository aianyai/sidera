//go:build windows && acceptance

package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

const scopeHelperArg = "--sidera-acceptance-process-helper"

// The self-reexec helper must run before TestMain: it neither builds fixture
// binaries nor inherits the outer test's fixture configuration.
func init() {
	if len(os.Args) > 2 && os.Args[1] == scopeHelperArg {
		if err := runScopeHelper(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(91)
		}
		os.Exit(0)
	}
}

func runScopeHelper(args []string) error {
	switch args[0] {
	case "exit":
		input, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		dir, err := os.Getwd()
		if err != nil {
			return err
		}
		if err := json.NewEncoder(os.Stdout).Encode(struct {
			Args              []string
			Dir, Value, Input string
		}{args[2:], dir, os.Getenv("SCOPE_VALUE"), string(input)}); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "helper stderr")
		code, err := strconv.Atoi(args[1])
		if err != nil {
			return err
		}
		os.Exit(code)
	case "wait":
		ready, err := openScopeEvent(args[1])
		if err != nil {
			return err
		}
		defer windows.CloseHandle(ready)
		release, err := openScopeEvent(args[2])
		if err != nil {
			return err
		}
		defer windows.CloseHandle(release)
		if err := windows.SetEvent(ready); err != nil {
			return err
		}
		_, err = windows.WaitForSingleObject(release, windows.INFINITE)
		return err
	case "tree":
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		child := exec.Command(exe, scopeHelperArg, "wait", args[1], args[2])
		child.Env = os.Environ()
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		child.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
		if err := child.Start(); err != nil {
			return err
		}
		if err := os.WriteFile(args[4], []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
			return err
		}
		ready, err := openScopeEvent(args[1])
		if err != nil {
			return err
		}
		defer windows.CloseHandle(ready)
		if _, err := windows.WaitForSingleObject(ready, windows.INFINITE); err != nil {
			return err
		}
		// This barrier confirms a child exists, while the ancestor still has
		// not passed its simulated hello gate.
		return runScopeHelper([]string{"wait", args[3], args[2]})
	}
	return fmt.Errorf("unknown process helper mode %q", args[0])
}

func openScopeEvent(name string) (windows.Handle, error) {
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	return windows.OpenEvent(windows.EVENT_MODIFY_STATE|windows.SYNCHRONIZE, false, n)
}

var scopeEventNumber atomic.Uint64

func scopeEvent(t *testing.T) (windows.Handle, string) {
	t.Helper()
	name := fmt.Sprintf("Local\\SideraAcceptance-%d-%d", os.Getpid(), scopeEventNumber.Add(1))
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateEvent(nil, 1, 0, n)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := windows.CloseHandle(h); err != nil {
			t.Error(err)
		}
	})
	return h, name
}

func waitScopeHandle(t *testing.T, h windows.Handle) {
	t.Helper()
	status, err := windows.WaitForSingleObject(h, 10000)
	if err != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatalf("event/process wait: status=%d err=%v", status, err)
	}
}

func newTestProcessScope(t *testing.T) *processScope {
	t.Helper()
	s, err := newProcessScope()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return s
}

func scopeSpec(t *testing.T, scope *processScope, args ...string) processSpec {
	t.Helper()
	root := runRoot(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	out, err := os.Create(filepath.Join(root, "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	errout, err := os.Create(filepath.Join(root, "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { out.Close(); errout.Close() })
	if scope != nil {
		t.Cleanup(func() {
			if err := scope.close(context.Background()); err != nil {
				t.Error(err)
			}
		})
	}
	env := []string{}
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if !strings.HasPrefix(strings.ToUpper(key), "SIDERA_") {
			env = append(env, value)
		}
	}
	return processSpec{exe: exe, args: append([]string{scopeHelperArg}, args...), env: env, dir: root, stdout: out, stderr: errout}
}

func TestProcessScopeExitAndLaunchInputs(t *testing.T) {
	for _, code := range []int{0, 7} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			s := newTestProcessScope(t)
			args := []string{"space value", "", `quote"value`, `trailing slash\`, "中文"}
			spec := scopeSpec(t, s, append([]string{"exit", strconv.Itoa(code)}, args...)...)
			spec.env = append(spec.env, "SCOPE_VALUE=space 中文")
			p, err := s.start(spec)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			err = p.wait(ctx)
			if code == 0 && err != nil {
				t.Fatal(err)
			}
			if code != 0 {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != code {
					t.Fatalf("exit error = %v", err)
				}
			}
			if again := p.wait(ctx); !errors.Is(again, err) {
				t.Fatalf("wait result changed: %v -> %v", err, again)
			}
			output, err := os.ReadFile(spec.stdout.Name())
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Args              []string
				Dir, Value, Input string
			}
			if err := json.Unmarshal(output, &got); err != nil {
				t.Fatalf("output %q: %v", output, err)
			}
			if fmt.Sprint(got.Args) != fmt.Sprint(args) || got.Dir != spec.dir || got.Value != "space 中文" || got.Input != "" {
				t.Fatalf("launch inputs: %+v", got)
			}
			stderr, err := os.ReadFile(spec.stderr.Name())
			if err != nil || string(stderr) != "helper stderr\n" {
				t.Fatalf("stderr = %q, %v", stderr, err)
			}
		})
	}
}

func TestProcessScopeSuspendedHalfStart(t *testing.T) {
	ready, name := scopeEvent(t)
	_, release := scopeEvent(t)
	spec := scopeSpec(t, nil, "wait", name, release)
	pi, err := createSuspended(spec)
	if err != nil {
		t.Fatal(err)
	}
	var held windows.Handle
	if err := windows.DuplicateHandle(windows.CurrentProcess(), pi.Process, windows.CurrentProcess(), &held, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		_ = terminateStarted(pi.Process)
		windows.CloseHandle(pi.Thread)
		windows.CloseHandle(pi.Process)
		t.Fatal(err)
	}
	defer windows.CloseHandle(held)
	status, err := windows.WaitForSingleObject(ready, 0)
	if err != nil || status != uint32(windows.WAIT_TIMEOUT) {
		t.Fatalf("suspended helper ran: %d, %v", status, err)
	}
	// Refusing admission exercises the same failure owner as scope.start,
	// while this duplicate pins the exact process for independent exit proof.
	if _, err := (&processScope{}).resumeSuspended(pi); err == nil {
		t.Fatal("invalid job accepted a suspended process")
	}
	waitScopeHandle(t, held)
	status, err = windows.WaitForSingleObject(ready, 0)
	if err != nil || status != uint32(windows.WAIT_TIMEOUT) {
		t.Fatalf("half-start helper ran: %d, %v", status, err)
	}
}

func TestProcessScopeContainsChildBeforeHelloAcrossGenerations(t *testing.T) {
	s := newTestProcessScope(t)
	_, childReady := scopeEvent(t)
	_, release := scopeEvent(t)
	ready, treeReady := scopeEvent(t)
	spec := scopeSpec(t, s, "tree", childReady, release, treeReady, "unused")
	pidFile := filepath.Join(spec.dir, "child.pid")
	spec.args[len(spec.args)-1] = pidFile
	ancestor, err := s.start(spec)
	if err != nil {
		t.Fatal(err)
	}
	waitScopeHandle(t, ready)
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatal(err)
	}
	child, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(child)
	if killed, err := ancestor.killAndWait(); err != nil || !killed {
		t.Fatalf("ancestor kill: %v, %v", killed, err)
	}
	status, err := windows.WaitForSingleObject(child, 0)
	if err != nil || status != uint32(windows.WAIT_TIMEOUT) {
		t.Fatalf("child did not survive ancestor: %d, %v", status, err)
	}
	// A replacement joins the same scope while the previous native survives.
	replacementReady, replacementName := scopeEvent(t)
	replacement, err := s.start(scopeSpec(t, s, "wait", replacementName, release))
	if err != nil {
		t.Fatal(err)
	}
	waitScopeHandle(t, replacementReady)
	var job windows.Handle
	if err := windows.DuplicateHandle(windows.CurrentProcess(), s.job, windows.CurrentProcess(), &job, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(job)
	if err := s.close(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitScopeHandle(t, child)
	if !replacement.exited() {
		t.Fatal("close returned before replacement wait")
	}
	active, err := (&processScope{job: job}).activeProcesses()
	if err != nil || active != 0 {
		t.Fatalf("closed job active=%d err=%v", active, err)
	}
	if _, err := s.start(spec); err == nil {
		t.Fatal("closed scope accepted a start")
	}
}

func TestProcessScopeCancelledCloseStillCleansOnlyOwnedJob(t *testing.T) {
	s := newTestProcessScope(t)
	other := newTestProcessScope(t)
	_, release := scopeEvent(t)
	ready, name := scopeEvent(t)
	p, err := s.start(scopeSpec(t, s, "wait", name, release))
	if err != nil {
		t.Fatal(err)
	}
	waitScopeHandle(t, ready)
	otherReady, otherName := scopeEvent(t)
	outside, err := other.start(scopeSpec(t, other, "wait", otherName, release))
	if err != nil {
		t.Fatal(err)
	}
	waitScopeHandle(t, otherReady)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled close = %v", err)
	}
	if !p.exited() {
		t.Fatal("cancelled close abandoned process wait")
	}
	active, err := other.activeProcesses()
	if err != nil || active != 1 || outside.exited() {
		t.Fatalf("other job affected: active=%d exited=%v err=%v", active, outside.exited(), err)
	}
	if err := s.close(context.Background()); err != nil {
		t.Fatalf("cleanup failed: %v", err)
	}
}
