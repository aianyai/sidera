package daemon_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/node/clock/clocktest"
	"github.com/aianyai/sidera/internal/node/daemon"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/keys"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/txn"
)

var errIO = errors.New("input/output error")

// memNode is an initialized data directory in memory, with the sockets' directories
// on the real disk, and the clock the test moves.
type memNode struct {
	home string
	mem  *fsxtest.Mem
	clk  *clocktest.Fake
}

func newMemNode(t testing.TB) *memNode {
	t.Helper()
	n := &memNode{home: newHome(t), mem: fsxtest.NewMem(), clk: clocktest.NewFake(t0)}
	mkSocketDirs(t, n.home)
	if _, err := daemon.Init(context.Background(), memConfig(n.home, n.clk, n.mem)); err != nil {
		t.Fatal(err)
	}
	return n
}

// mustHit fails the test when the injected fault was never hit, after it has
// stopped the daemon that went through the start instead.
func mustHit(t testing.TB, rule *fsxtest.Rule, d *daemon.Daemon) {
	t.Helper()
	if rule.Hits() > 0 {
		return
	}
	if d != nil {
		d.RequestStop("test over")
		d.Wait()
	}
	t.Fatal("the injected fault was never hit")
}

// mustEndFatally checks what every start that met a fatal failure of the disk owes:
// the error is a *daemon.FatalError and the exit code 74 and, when cause says the
// failure was an I/O error, it still names it, nothing is left running, and the lock is let go so that the next start scans the disk again.
func mustEndFatally(t testing.TB, n *memNode, d *daemon.Daemon, err error, where string, cause bool) {
	t.Helper()
	if err == nil {
		d.RequestStop("test over")
		d.Wait()
		t.Fatalf("%s: the start went through", where)
	}
	var fatal *daemon.FatalError
	if !errors.As(err, &fatal) || daemon.ExitCode(err) != daemon.ExitFatal {
		t.Errorf("%s: the start ended with %v (exit code %d); want a *daemon.FatalError and exit code 74", where, err, daemon.ExitCode(err))
	}
	if cause && !strings.Contains(err.Error(), errIO.Error()) {
		t.Errorf("%s: the cause is lost from the error: %v", where, err)
	}
	d2, err2 := daemon.Start(context.Background(), memConfig(n.home, n.clk, n.mem))
	if err2 != nil {
		t.Errorf("%s: the lock was not let go or the next start fails: %v", where, err2)
		return
	}
	d2.RequestStop("test over")
	if err := d2.Wait(); err != nil {
		t.Errorf("%s: the next start ended with %v", where, err)
	}
}

// A failure of the disk that leaves memory and disk in doubt ends a start in any
// of its steps as it ends a running daemon: the exit code is the one of an I/O
// error, so that the next start scans the disk again, and not the code of an
// ordinary refusal that a supervisor would not tell from a bad data directory.
// Step 2: the directory of the layout the owner removed cannot be made durable.
func TestAFatalStorageErrorInTheLayoutRuleEndsTheStartWithTheExitCodeOfAnIOError(t *testing.T) {
	noLeaks(t)
	n := newMemNode(t)
	if err := fsx.RemoveAll(n.mem, fsx.Tag{Tx: "setup", Role: "r"}, "state"); err != nil {
		t.Fatal(err)
	}
	faulty := fsxtest.NewFaulty(t, n.mem)
	rule := faulty.Fail(fsxtest.Match{Tx: "layout", Step: fsx.StepSyncDir}, errIO)
	d, err := daemon.Start(context.Background(), memConfig(n.home, n.clk, faulty))
	mustHit(t, rule, d)
	if err != nil && !strings.Contains(err.Error(), "making the layout again") {
		t.Errorf("the failure was not the layout rule's: %v", err)
	}
	mustEndFatally(t, n, d, err, "layout", true)
}

// Step 2: the recovery of the transaction layer completes a transaction that was
// committed and not finished, and the directory it has to make durable cannot be.
// The disks are those a crash leaves during the layout rule; the fault is a failed
// fsync of a directory on every start over them, and at least one of them is
// recovered by the transaction layer, whose error says so.
func TestAFatalStorageErrorInTheRecoveryOfTheTransactionLayerEndsTheStartWithTheExitCodeOfAnIOError(t *testing.T) {
	noLeaks(t)
	n := newMemNode(t)
	if err := fsx.RemoveAll(n.mem, fsx.Tag{Tx: "setup", Role: "r"}, "state"); err != nil {
		t.Fatal(err)
	}
	var states []*fsxtest.Mem
	c := memConfig(n.home, n.clk, n.mem)
	c.Observer = observerFunc(func(p fsx.Point, _ txn.Kind) {
		if p.Tag.Tx == "layout" {
			states = append(states, n.mem.Clone(), n.mem.CrashCloneWorst())
		}
	})
	d, err := daemon.Start(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	d.RequestStop("test over")
	if err := d.Wait(); err != nil {
		t.Fatal(err)
	}
	if len(states) == 0 {
		t.Fatal("no crash state of the layout rule was captured")
	}
	recovered := 0
	for i, s := range states {
		node := &memNode{home: n.home, mem: s, clk: n.clk}
		faulty := fsxtest.NewFaulty(t, s.Clone())
		rule := faulty.Fail(fsxtest.Match{Step: fsx.StepSyncDir}, errIO)
		d, err := daemon.Start(context.Background(), memConfig(n.home, n.clk, faulty))
		if rule.Hits() == 0 {
			// nothing to make durable in this state: the start went through
			if err != nil {
				t.Errorf("state %d: no fsync was asked for and the start failed with %v", i, err)
			} else {
				d.RequestStop("test over")
				d.Wait()
			}
			continue
		}
		if err != nil && strings.Contains(err.Error(), "recovering the data directory") {
			recovered++
		}
		mustEndFatally(t, node, d, err, "recovery, state "+itoa(i), true)
	}
	if recovered == 0 {
		t.Error("no crash state made the recovery of the transaction layer ask for an fsync of a directory")
	}
}

// Step 4: the wall clock was set back, and the pause cannot be made durable after
// the commit point of its transaction: whether the pause is on disk is not known.
func TestAFatalStorageErrorSavingThePauseOfAClockSetBackEndsTheStartWithTheExitCodeOfAnIOError(t *testing.T) {
	noLeaks(t)
	n := newMemNode(t)
	cfg := memConfig(n.home, n.clk, n.mem)
	d, err := daemon.Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	n.clk.Advance(2 * time.Hour)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := d.Engine().Quiesce(ctx, engine.Expect{}); err != nil {
		t.Fatal(err)
	}
	d.RequestStop("test")
	if err := d.Wait(); err != nil {
		t.Fatal(err)
	}
	n.clk.Set(t0.Add(-3 * time.Hour))
	faulty := fsxtest.NewFaulty(t, n.mem)
	rule := faulty.Fail(fsxtest.Match{Tx: keys.TxClockHighWater, Step: fsx.StepSyncDir}, errIO)
	d, err = daemon.Start(context.Background(), memConfig(n.home, n.clk, faulty))
	mustHit(t, rule, d)
	if err != nil && !strings.Contains(err.Error(), "making the pause durable") {
		t.Errorf("the failure was not at the saving of the pause: %v", err)
	}
	n.clk.Set(t0.Add(3 * time.Hour))
	mustEndFatally(t, n, d, err, "clock", true)
}

// Step 6: the scan of state/ writes (an endpoint of an instance nobody enabled is
// closed), and the directory of that write cannot be made durable.
func TestAFatalStorageErrorInTheScanOfStateEndsTheStartWithTheExitCodeOfAnIOError(t *testing.T) {
	noLeaks(t)
	n := newMemNode(t)
	tab, err := daemon.Table()
	if err != nil {
		t.Fatal(err)
	}
	w := txn.New(n.mem, tab, nil)
	if err := w.Recover(); err != nil {
		t.Fatal(err)
	}
	rec := store.EndpointRecord{ID: endpointID(t, 1), Kind: adapterreg.KindSession, Instance: "chat", Status: store.EndpointOpen,
		Token: []byte(strings.Repeat("\x5a", store.TokenLen)), Session: &store.SessionInfo{Preset: "default"}}
	if err := store.New(w).EndpointUpdate(rec); err != nil {
		t.Fatal(err)
	}
	faulty := fsxtest.NewFaulty(t, n.mem)
	rule := faulty.Fail(fsxtest.Match{Tx: "instance-drain", Step: fsx.StepSyncDir}, errIO)
	d, err := daemon.Start(context.Background(), memConfig(n.home, n.clk, faulty))
	mustHit(t, rule, d)
	if err != nil && !strings.Contains(err.Error(), "startup step 6") {
		t.Errorf("the failure was not in the scan: %v", err)
	}
	mustEndFatally(t, n, d, err, "scan", true)
}

// A command that a hook gave the engine met a fatal storage error and killed the
// loop; the hook returns whatever it makes of that. In every shape the start ends
// as a fatal failure: the error the hook returns is the one of the failed write,
// the refusal the engine gives every command after it (which says only that the
// failure was fatal), or an error of the hook's own made after the engine had died.
func TestAHookThatMeetsTheDeathOfTheEngineEndsTheStartWithTheExitCodeOfAnIOError(t *testing.T) {
	stopCommand := func(id byte) engine.ManagementCommand {
		return engine.ManagementCommand{
			Key:         admin.KeyFor(admin.LocalOwner(), admin.OperationID{15: id}),
			Fingerprint: admin.Fingerprint{0: id},
			Name:        "daemon.stop",
			Operation:   engine.StopDaemonOp(func() ([]byte, error) { return []byte(`{"stopping":true}`), nil }),
		}
	}
	for _, tc := range []struct {
		name   string
		inject bool // the disk fails for the stop command; otherwise the engine stays whole
		cause  bool // the error the start ends with still names the I/O error
		hook   func(context.Context, *daemon.Env) error
	}{
		{"the error of the failed write", true, true, func(ctx context.Context, env *daemon.Env) error {
			_, err := env.Engine.Execute(ctx, stopCommand(1))
			return err
		}},
		{"the refusal the engine gives after its death, with the engine whole", false, false, func(context.Context, *daemon.Env) error {
			return &engine.Refusal{Reason: engine.ReasonStorageFatal}
		}},
		{"the refusal the engine gives after its death", true, false, func(ctx context.Context, env *daemon.Env) error {
			_, _ = env.Engine.Execute(ctx, stopCommand(1))
			<-env.Engine.Done()
			return &engine.Refusal{Reason: engine.ReasonStorageFatal}
		}},
		{"an error of the hook's own after the engine had died", true, false, func(ctx context.Context, env *daemon.Env) error {
			_, _ = env.Engine.Execute(ctx, stopCommand(1))
			<-env.Engine.Done()
			return errors.New("the Agent interface lost the engine")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			noLeaks(t)
			n := newMemNode(t)
			var fsys fsx.FS = n.mem
			var rule *fsxtest.Rule
			if tc.inject {
				faulty := fsxtest.NewFaulty(t, n.mem)
				rule = faulty.Fail(fsxtest.Match{Tx: "daemon-stop", Step: fsx.StepSyncDir}, errIO)
				fsys = faulty
			}
			c := memConfig(n.home, n.clk, fsys)
			c.Attach.AgentServe = tc.hook
			d, err := daemon.Start(context.Background(), c)
			if tc.inject {
				mustHit(t, rule, d)
			}
			mustEndFatally(t, n, d, err, tc.name, tc.cause)
		})
	}
}
