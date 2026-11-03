package process_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/sdk/go/adapterkit/process"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// The test binary is its own child on every platform: it runs as one of the modes
// below when childEnv names it.
const (
	childEnv = "SIDERA_PROCESS_TEST_CHILD"
	markEnv  = "SIDERA_PROCESS_TEST_MARK"

	// childLifetime ends a child nobody stopped.
	childLifetime = 60 * time.Second
	// wait bounds what the tests wait for.
	wait = 30 * time.Second
)

// childModes are the modes of a child, by name; platform tests add their own.
var childModes = map[string]func(){
	// report writes where it runs and what it was given, and ends.
	"report": func() {
		cwd, _ := os.Getwd()
		_ = json.NewEncoder(os.Stdout).Encode(report{Cwd: cwd, Mark: os.Getenv(markEnv), PID: os.Getpid()})
	},
	// hold says it is ready and stays until it is killed.
	"hold": func() {
		println("ready")
		time.Sleep(childLifetime)
	},
	// interruptible says it is ready and ends when it is interrupted.
	"interruptible": func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt)
		println("ready")
		select {
		case <-sig:
			println("interrupted")
		case <-time.After(childLifetime):
			os.Exit(2)
		}
	},
}

type report struct {
	Cwd  string
	Mark string
	PID  int
}

func TestMain(m *testing.M) {
	process.HandleHelperInvocation()
	if mode := os.Getenv(childEnv); mode != "" {
		run, ok := childModes[mode]
		if !ok {
			println("unknown child mode", mode)
			os.Exit(2)
		}
		run()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// childEnvironment is the environment of a child in mode, with extra on top.
func childEnvironment(mode string, extra ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, childEnv+"=") {
			env = append(env, kv)
		}
	}
	return append(append(env, childEnv+"="+mode), extra...)
}

// child is a started test child and the file its output goes to.
type child struct {
	process.Started
	out string
}

// startChild starts the test binary in mode, with its standard error and output in
// one file, and kills it when the test ends. configure may change the spec.
func startChild(t *testing.T, mode string, configure func(*process.Spec)) child {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "out")
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	spec := process.Spec{Argv: []string{exe}, Env: childEnvironment(mode), Stdout: f, Stderr: f}
	if configure != nil {
		configure(&spec)
	}
	started, err := process.Start(spec)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Done delivers once; the cleanup must not wait for what the test took.
	ended := make(chan struct{})
	done := make(chan error, 1)
	taken := started.Done
	go func() {
		done <- <-taken
		close(ended)
	}()
	started.Done = done
	t.Cleanup(func() {
		_ = started.Handle.Kill()
		select {
		case <-ended:
		case <-time.After(wait):
			t.Error("a test child did not end after Kill")
		}
	})
	return child{started, out}
}

// output waits until the child's output holds want and returns it.
func (c child) waitOutput(t *testing.T, want string) string {
	t.Helper()
	deadline := time.Now().Add(wait)
	for {
		raw, _ := os.ReadFile(c.out)
		if strings.Contains(string(raw), want) {
			return string(raw)
		}
		if time.Now().After(deadline) {
			t.Fatalf("the child did not write %q; it wrote %q", want, raw)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitDone(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(wait):
		t.Fatal("the process did not end")
		return nil
	}
}

func TestLookupAndListFindThisProcess(t *testing.T) {
	self, found, err := process.Lookup(os.Getpid())
	if err != nil || !found {
		t.Fatalf("Lookup(self) = %+v, %v, %v", self, found, err)
	}
	if n := len(self.StartKey); n < 1 || n > 64 {
		t.Fatalf("start key %q is %d bytes, want 1 to 64", self.StartKey, n)
	}
	if self.NativeStart == "" || self.StartTime.IsZero() {
		t.Fatalf("Lookup(self) = %+v lacks a start", self)
	}
	if running, err := process.Running(self.Identity); err != nil || !running {
		t.Fatalf("Running(self) = %v, %v", running, err)
	}
	if running, err := process.Running(process.Identity{PID: self.PID, StartKey: self.StartKey + "x"}); err != nil || running {
		t.Fatalf("Running with a wrong key = %v, %v", running, err)
	}

	all, err := process.List()
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(all, func(p process.Process) bool { return p.PID == self.PID })
	if i < 0 {
		t.Fatal("List lacks this process")
	}
	if all[i].StartKey != self.StartKey || !slices.Equal(all[i].Args, os.Args) {
		t.Fatalf("List has %+v for this process, want key %q and args %q", all[i], self.StartKey, os.Args)
	}

	// No PID of any kind may name a process that is not there; zero and negative
	// PIDs would mean a group or every process to a signal.
	for _, pid := range []int{1 << 22, 0, -1} {
		if p, found, err := process.Lookup(pid); err != nil || found {
			t.Fatalf("Lookup(%d) = %+v, %v, %v, want not found", pid, p, found, err)
		}
	}
}

func TestStartRunsTheProcessAsAsked(t *testing.T) {
	for _, interruptible := range []bool{false, true} {
		t.Run("interruptible="+strconv.FormatBool(interruptible), func(t *testing.T) {
			dir := t.TempDir()
			c := startChild(t, "report", func(s *process.Spec) {
				s.Env = childEnvironment("report", markEnv+"=marked")
				s.Dir = dir
				s.Interruptible = interruptible
			})
			if err := waitDone(t, c.Done); err != nil {
				t.Fatalf("Done = %v, want a clean exit", err)
			}
			var got report
			if err := json.Unmarshal([]byte(c.waitOutput(t, "\n")), &got); err != nil {
				t.Fatal(err)
			}
			if got.Mark != "marked" || got.PID != c.PID {
				t.Fatalf("the child reports %+v, want mark %q and pid %d", got, "marked", c.PID)
			}
			want, _ := os.Stat(dir)
			ran, err := os.Stat(got.Cwd)
			if err != nil || !os.SameFile(want, ran) {
				t.Fatalf("the child ran in %q, want %q (%v)", got.Cwd, dir, err)
			}
		})
	}
}

func TestStartIdentifiesTheProcess(t *testing.T) {
	for _, interruptible := range []bool{false, true} {
		t.Run("interruptible="+strconv.FormatBool(interruptible), func(t *testing.T) {
			c := startChild(t, "hold", func(s *process.Spec) { s.Interruptible = interruptible })
			c.waitOutput(t, "ready")

			found, ok, err := process.Lookup(c.PID)
			if err != nil || !ok {
				t.Fatalf("Lookup = %+v, %v, %v", found, ok, err)
			}
			if found.Identity != c.Identity || found.NativeStart != c.NativeStart {
				t.Fatalf("Start says %+v, Lookup says %+v", c.Process, found)
			}
			if c.Args != nil {
				t.Fatalf("Start set Args %q", c.Args)
			}
			if d := time.Since(c.StartTime); d < -time.Minute || d > time.Minute {
				t.Fatalf("StartTime %v is not about now", c.StartTime)
			}
			if err := c.Container.Validate(); err != nil {
				t.Fatalf("Container %+v: %v", c.Container, err)
			}
			checkContainer(t, c)

			if err := c.Handle.Kill(); err != nil {
				t.Fatal(err)
			}
			if err := waitDone(t, c.Done); err == nil {
				t.Fatal("a killed process reported a clean exit")
			}
			if running, err := process.Running(c.Identity); err != nil || running {
				t.Fatalf("Running after the end = %v, %v", running, err)
			}
		})
	}
}

func TestStartOfAMissingExecutableFailsWithoutStarting(t *testing.T) {
	_, err := process.Start(process.Spec{Argv: []string{filepath.Join(t.TempDir(), "missing")}})
	if err == nil || errors.Is(err, process.ErrStartedUnidentified) {
		t.Fatalf("Start = %v, want an error that is not ErrStartedUnidentified", err)
	}
	if _, err := process.Start(process.Spec{}); err == nil {
		t.Fatal("Start with no command did not fail")
	}
}

func TestHostDetachmentIsStable(t *testing.T) {
	d := process.HostDetachment()
	t.Logf("host detachment: %+v", d)
	if again := process.HostDetachment(); again != d {
		t.Fatalf("HostDetachment changed from %+v to %+v", d, again)
	}
}

func TestInterruptStopsTheProcess(t *testing.T) {
	c := startChild(t, "interruptible", func(s *process.Spec) { s.Interruptible = true })
	c.waitOutput(t, "ready")

	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	if err := process.Interrupt(ctx, c.Identity); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	if err := waitDone(t, c.Done); err != nil {
		t.Fatalf("the interrupted process ended with %v, want a clean exit", err)
	}
	c.waitOutput(t, "interrupted")

	if err := process.Interrupt(ctx, c.Identity); !errors.Is(err, process.ErrNotRunning) {
		t.Fatalf("Interrupt after the end = %v, want ErrNotRunning", err)
	}
}

func TestInterruptOfAnotherProcessWithTheSamePIDIsRefused(t *testing.T) {
	c := startChild(t, "interruptible", func(s *process.Spec) { s.Interruptible = true })
	c.waitOutput(t, "ready")

	stale := process.Identity{PID: c.PID, StartKey: c.StartKey + "0"}
	if err := process.Interrupt(context.Background(), stale); !errors.Is(err, process.ErrNotRunning) {
		t.Fatalf("Interrupt with a stale key = %v, want ErrNotRunning", err)
	}
	if running, _ := process.Running(c.Identity); !running {
		t.Fatal("the process was interrupted by an identity that is not its own")
	}
}

// checkContainer checks what Start says about where the process runs against the
// platform.
func checkContainer(t *testing.T, c child) {
	t.Helper()
	switch c.Container.Kind {
	case adapterproto.ContainerProcessGroup:
		if c.Container.Name != strconv.Itoa(c.PID) {
			t.Fatalf("Container %+v, want the group named by the PID %d", c.Container, c.PID)
		}
		checkGroupLeader(t, c.PID)
	case adapterproto.ContainerSystemdUnit:
		checkScope(t, c)
	case adapterproto.ContainerNone:
		if c.Container.Name != "" {
			t.Fatalf("Container %+v, want no name", c.Container)
		}
	default:
		t.Fatalf("unexpected Container %+v", c.Container)
	}
	if want := wantContainerKinds(); !slices.Contains(want, c.Container.Kind) {
		t.Fatalf("Container kind %q, want one of %v on this platform", c.Container.Kind, want)
	}
}
