package daemon_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/clock/clocktest"
	"github.com/aianyai/sidera/internal/node/daemon"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
)

// relauncher is the process around the daemon, as far as the daemon asks it.
type relauncher struct {
	program, version string
	err              error
}

func (r relauncher) CheckRestart(context.Context) (string, string, error) {
	return r.program, r.version, r.err
}

func restartRequest(id byte, params map[string]any) map[string]any {
	if params == nil {
		params = map[string]any{}
	}
	return map[string]any{"operation_id": strings.Repeat("0", 30) + string("0123456789abcdef"[id>>4]) + string("0123456789abcdef"[id&15]), "params": params}
}

type restartResult struct {
	Replayed bool `json:"replayed"`
	Result   struct {
		Restarting bool   `json:"restarting"`
		Program    string `json:"program"`
		Version    string `json:"version"`
		StartID    string `json:"start_id"`
	} `json:"result"`
}

func statusStartID(t *testing.T, home string) string {
	t.Helper()
	status, body := manage(t, home, "status", map[string]any{})
	var st struct {
		Result struct {
			Daemon struct {
				StartID string `json:"start_id"`
			} `json:"daemon"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &st); status != 200 || err != nil {
		t.Fatalf("status answered %d %s (%v)", status, body, err)
	}
	return st.Result.Daemon.StartID
}

// A restart is accepted, answered with the program, its version and the start the
// client is to see replaced, and the daemon then shuts down in the normal way and
// reports that it is to be started again. The same request sent again, to the
// daemon that took its place, is answered with the stored result and restarts
// nothing, where a stop would be asked for again.
func TestARestartEndsTheDaemonWithARestartAndItsResendDoesNotRestartAgain(t *testing.T) {
	noLeaks(t)
	c, _ := initialized(t)
	c.Relauncher = relauncher{program: "/opt/sidera/sidera", version: "0.12.0"}
	d, err := daemon.Start(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	startID := statusStartID(t, c.Home)
	req := restartRequest(1, map[string]any{"expect_version": "0.12.0"})
	status, body := manage(t, c.Home, "daemon.restart", req)
	var got restartResult
	if err := json.Unmarshal(body, &got); status != 200 || err != nil {
		t.Fatalf("the restart answered %d %s (%v)", status, body, err)
	}
	if !got.Result.Restarting || got.Result.Program != "/opt/sidera/sidera" || got.Result.Version != "0.12.0" || got.Result.StartID != startID || got.Replayed {
		t.Errorf("the restart answered %s; want the program, its version and start id %s", body, startID)
	}
	waitDone(t, d)
	if err := d.Wait(); err != nil {
		t.Fatalf("Wait = %v", err)
	}
	if d.Ending() != daemon.EndRestart {
		t.Error("a restart ended the daemon as a stop")
	}

	// the daemon that took its place
	d2, err := daemon.Start(context.Background(), c)
	if err != nil {
		t.Fatalf("the start after the restart: %v", err)
	}
	if again := statusStartID(t, c.Home); again == startID {
		t.Errorf("the start id %s did not change with the start", again)
	}
	status, body = manage(t, c.Home, "daemon.restart", req)
	var replay restartResult
	if err := json.Unmarshal(body, &replay); status != 200 || err != nil || !replay.Replayed || replay.Result.StartID != startID {
		t.Fatalf("the resend answered %d %s (%v); want the stored result", status, body, err)
	}
	select {
	case <-d2.Done():
		t.Error("the resend of a restart restarted the daemon again")
	case <-time.After(300 * time.Millisecond):
	}
	d2.RequestStop("test over")
	if err := d2.Wait(); err != nil || d2.Ending() != daemon.EndStopped {
		t.Errorf("the second daemon ended with %v, %v", err, d2.Ending())
	}
}

// A restart that is refused shuts nothing down and stores nothing, and the
// refusal reaches the owner in the words the interface gives it (the engine hands
// the error of the check on as it is). A daemon with no relauncher refuses too, and
// says why in its log, which the words of the wire send the owner to read. The
// words of each refusal are shown once, where they are made, in `manageapi`.
func TestARefusedRestartLeavesTheDaemonRunning(t *testing.T) {
	for _, tc := range []struct {
		name string
		rl   daemon.Relauncher
		code string
	}{
		{"no relauncher", nil, "restart_unavailable"},
		{"another version", relauncher{program: "/p", version: "0.11.0"}, "restart_version_mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			noLeaks(t)
			c, _ := initialized(t)
			c.Relauncher = tc.rl
			d := started(t, c)
			status, body := manage(t, c.Home, "daemon.restart", restartRequest(1, map[string]any{"expect_version": "0.12.0"}))
			if status == 200 || !strings.Contains(string(body), tc.code) {
				t.Errorf("answered %d %s; want %s", status, body, tc.code)
			}
			select {
			case <-d.Done():
				t.Error("a refused restart shut the daemon down")
			case <-time.After(300 * time.Millisecond):
			}
			if status, _ := manage(t, c.Home, "operation.result", map[string]any{"params": map[string]any{"operation_id": restartRequest(1, nil)["operation_id"]}}); status != 404 {
				t.Errorf("a refused restart stored a result (operation.result answered %d)", status)
			}
			if tc.rl == nil {
				log, err := os.ReadFile(filepath.Join(c.Home, "logs", "daemon.log"))
				if err != nil || !strings.Contains(string(log), "a restart was refused") {
					t.Errorf("the log does not say why the restart was refused: %v\n%s", err, log)
				}
			}
		})
	}
}

// A signal still stops the daemon while a restart is shutting it down: SIGTERM from
// a service manager or Ctrl-C is an order to end, and a daemon that went on to start
// the program again would ignore it. A second management request does not override
// the first (the next test); a signal does.
func TestASignalDuringTheShutdownOfARestartStopsTheDaemon(t *testing.T) {
	// the signal package starts one goroutine for the process, the first time it is
	// used and for good; it is not the daemon's
	_, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt)
	stopSignals()
	noLeaks(t)
	c, _ := initialized(t)
	c.Relauncher = relauncher{program: "/p", version: "0.12.0"}
	self, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Skipf("this platform cannot name the process: %v", err)
	}
	restarted := make(chan int, 1)
	c.Attach.AgentServe = func(context.Context, *daemon.Env) error {
		// the start is over only after this hook, so the request is made beside it
		go func() {
			deadline := time.Now().Add(30 * time.Second)
			for time.Now().Before(deadline) {
				if status, _, err := doManage(c.Home, "daemon.restart", restartRequest(1, nil)); err == nil && status == 200 {
					restarted <- status
					return
				}
				time.Sleep(20 * time.Millisecond)
			}
			restarted <- 0
		}()
		return nil
	}
	var signalErr error
	c.Attach.StopInterfaces = func(context.Context) error {
		// the shutdown is that of the restart; the signal comes in the middle of it
		if signalErr = self.Signal(syscall.SIGTERM); signalErr == nil {
			time.Sleep(time.Second)
		}
		return nil
	}
	ending, err := daemon.Run(context.Background(), c)
	if signalErr != nil {
		t.Skipf("this platform cannot send itself a signal: %v", signalErr)
	}
	if status := <-restarted; status != 200 {
		t.Fatalf("the restart was not accepted (%d)", status)
	}
	if err != nil || ending != daemon.EndStopped {
		t.Errorf("Run = %v, %v; want a stop", ending, err)
	}
}

// The first request decides how the daemon ends, and what must end the process
// does so whatever was asked for.
func TestOnlyAnOrderlyShutdownAfterARestartRequestEndsInARestart(t *testing.T) {
	t.Run("restart then stop", func(t *testing.T) {
		noLeaks(t)
		c, _ := initialized(t)
		d, err := daemon.Start(context.Background(), c)
		if err != nil {
			t.Fatal(err)
		}
		d.RequestRestart("test")
		d.RequestStop("test")
		waitDone(t, d)
		if err := d.Wait(); err != nil || d.Ending() != daemon.EndRestart {
			t.Errorf("ended with %v, %v; want a restart", err, d.Ending())
		}
	})
	t.Run("stop then restart", func(t *testing.T) {
		noLeaks(t)
		c, _ := initialized(t)
		d, err := daemon.Start(context.Background(), c)
		if err != nil {
			t.Fatal(err)
		}
		d.RequestStop("test")
		d.RequestRestart("test")
		waitDone(t, d)
		if err := d.Wait(); err != nil || d.Ending() != daemon.EndStopped {
			t.Errorf("ended with %v, %v; want a stop", err, d.Ending())
		}
	})
	t.Run("the engine has not stopped", func(t *testing.T) {
		noLeaks(t)
		n := newMemNode(t)
		release := make(chan struct{})
		running := make(chan struct{}, 1)
		c := memConfig(n.home, n.clk, n.mem)
		c.ShutdownGrace = 300 * time.Millisecond
		c.Attach.Periodic = []daemon.PeriodicTask{{Name: "stuck", Every: time.Minute, Run: func(engine.TaskInput) error {
			select {
			case running <- struct{}{}:
			default:
			}
			<-release
			return nil
		}}}
		d, err := daemon.Start(context.Background(), c)
		if err != nil {
			t.Fatal(err)
		}
		n.clk.Advance(time.Minute)
		select {
		case <-running:
		case <-time.After(30 * time.Second):
			t.Fatal("the task of the loop never ran")
		}
		d.RequestRestart("test")
		waitDone(t, d)
		if err := d.Wait(); !errors.Is(err, daemon.ErrEngineNotStopped) || d.Ending() != daemon.EndStopped {
			t.Errorf("ended with %v, %v; want ErrEngineNotStopped and a stop", err, d.Ending())
		}
		close(release)
		<-d.Engine().Done()
	})
	t.Run("a fatal failure of the disk", func(t *testing.T) {
		noLeaks(t)
		clk := clocktest.NewFake(t0)
		home := newHome(t)
		mem := fsxtest.NewMem()
		mkSocketDirs(t, home)
		if _, err := daemon.Init(context.Background(), memConfig(home, clk, mem)); err != nil {
			t.Fatal(err)
		}
		faulty := fsxtest.NewFaulty(t, mem)
		faulty.Fail(fsxtest.Match{Tx: "daemon-stop", Step: fsx.StepSyncDir}, errors.New("input/output error"))
		c := memConfig(home, clk, faulty)
		c.Relauncher = relauncher{program: "/p", version: "0.12.0"}
		d, err := daemon.Start(context.Background(), c)
		if err != nil {
			t.Fatal(err)
		}
		if status, body := manage(t, home, "daemon.restart", restartRequest(1, nil)); status != 500 || !strings.Contains(string(body), "storage_fatal") {
			t.Errorf("the request that met the failure answered %d %s", status, body)
		}
		waitDone(t, d)
		var fatal *daemon.FatalError
		if err := d.Wait(); !errors.As(err, &fatal) || d.Ending() != daemon.EndStopped {
			t.Errorf("ended with %v, %v; want a fatal failure and a stop", err, d.Ending())
		}
	})
}
