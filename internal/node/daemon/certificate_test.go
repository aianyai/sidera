package daemon_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/daemon"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/keys"
	"github.com/aianyai/sidera/internal/node/txn"
	"github.com/aianyai/sidera/internal/record"
)

// certificateInMemory is the serial of the certificate of a node on a disk in
// memory, read with the record package alone.
func certificateInMemory(t testing.TB, n *memNode) uint64 {
	t.Helper()
	data, err := n.mem.ReadFile("keys/network.cert")
	if err != nil {
		t.Fatal(err)
	}
	serial, _ := certificateOf(t, data)
	return serial
}

// The start renews a certificate that is due and the disk cannot take the renewal.
// Before the commit point (the serial reserved) the failure is an ordinary one: the
// start is refused with the error, what had begun is unwound, the lock is let go and
// the certificate is as it was; after it the failure is fatal, as for any write of a
// transaction past its commit point, and ends the start with the exit code of an I/O
// error so that the next start reads the disk again.
func TestAFailureRenewingTheCertificateAtTheStartEndsTheStartAsAnyFailureOfTheDiskDoes(t *testing.T) {
	t.Run("a write that fails before the commit point", func(t *testing.T) {
		noLeaks(t)
		n := newMemNode(t)
		before := certificateInMemory(t, n)
		n.clk.Advance(25 * 24 * time.Hour)
		faulty := fsxtest.NewFaulty(t, n.mem)
		rule := faulty.Fail(fsxtest.Match{Tx: keys.TxCertRenew, Role: keys.RoleSerial, Step: fsx.StepWrite, Hit: 1}, fsxtest.DiskFull)
		d, err := daemon.Start(context.Background(), memConfig(n.home, n.clk, faulty))
		mustHit(t, rule, d)
		if err == nil || !strings.Contains(err.Error(), "startup step 5") || daemon.ExitCode(err) != 1 {
			t.Fatalf("the start ended with %v (exit code %d); want an ordinary refusal at step 5", err, daemon.ExitCode(err))
		}
		if got := certificateInMemory(t, n); got != before {
			t.Errorf("the certificate changed to serial %d after a renewal that failed", got)
		}
		// the lock was let go: the next start, on a disk with room, renews
		d2, err := daemon.Start(context.Background(), memConfig(n.home, n.clk, n.mem))
		if err != nil {
			t.Fatalf("the start after the failure: %v", err)
		}
		d2.RequestStop("test over")
		if err := d2.Wait(); err != nil {
			t.Fatal(err)
		}
		if got := certificateInMemory(t, n); got != before+1 {
			t.Errorf("serial %d after the start that could write, want %d", got, before+1)
		}
	})
	t.Run("a directory that cannot be made durable after the commit point", func(t *testing.T) {
		noLeaks(t)
		n := newMemNode(t)
		n.clk.Advance(25 * 24 * time.Hour)
		faulty := fsxtest.NewFaulty(t, n.mem)
		rule := faulty.Fail(fsxtest.Match{Tx: keys.TxCertRenew, Step: fsx.StepSyncDir}, errIO)
		d, err := daemon.Start(context.Background(), memConfig(n.home, n.clk, faulty))
		mustHit(t, rule, d)
		if err != nil && !strings.Contains(err.Error(), "startup step 5") {
			t.Errorf("the failure was not at step 5: %v", err)
		}
		mustEndFatally(t, n, d, err, "certificate", true)
	})
}

// The hourly check meets the same failures in the loop. A failure that a retry may
// cure is tried again a minute later (the engine's backoff, on the injected clock),
// the certificate is not touched meanwhile and the node runs on; a fatal one ends the
// daemon with the exit code of an I/O error.
func TestAFailureRenewingTheCertificateInTheLoopIsRetriedOrEndsTheDaemon(t *testing.T) {
	t.Run("a failure that a retry may cure", func(t *testing.T) {
		noLeaks(t)
		n := newMemNode(t)
		before := certificateInMemory(t, n)
		faulty := fsxtest.NewFaulty(t, n.mem)
		rule := faulty.Fail(fsxtest.Match{Tx: keys.TxCertRenew, Role: keys.RoleSerial, Step: fsx.StepWrite, Hit: 1}, fsxtest.DiskFull)
		d, err := daemon.Start(context.Background(), memConfig(n.home, n.clk, faulty))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { d.RequestStop("test over"); d.Wait() })
		n.clk.Advance(25 * 24 * time.Hour)
		settled(t, d)
		if rule.Hits() == 0 {
			t.Fatal("the injected fault was never hit")
		}
		if got := certificateInMemory(t, n); got != before {
			t.Fatalf("serial %d after a renewal that failed", got)
		}
		if status := tryManage(n.home, "status"); status != 200 {
			t.Errorf("the node does not answer after a failed renewal: %d", status)
		}
		n.clk.Advance(time.Minute)
		settled(t, d)
		if got := certificateInMemory(t, n); got != before+1 {
			t.Errorf("serial %d a minute after the failure, want %d (retried)", got, before+1)
		}
	})
	t.Run("a fatal failure", func(t *testing.T) {
		noLeaks(t)
		n := newMemNode(t)
		faulty := fsxtest.NewFaulty(t, n.mem)
		rule := faulty.Fail(fsxtest.Match{Tx: keys.TxCertRenew, Step: fsx.StepSyncDir}, errIO)
		d, err := daemon.Start(context.Background(), memConfig(n.home, n.clk, faulty))
		if err != nil {
			t.Fatal(err)
		}
		n.clk.Advance(25 * 24 * time.Hour)
		select {
		case <-d.Done():
		case <-time.After(30 * time.Second):
			t.Fatal("the daemon did not end after a fatal failure of the disk")
		}
		if rule.Hits() == 0 {
			t.Fatal("the injected fault was never hit")
		}
		err = d.Wait()
		var fatal *daemon.FatalError
		if !errors.As(err, &fatal) || daemon.ExitCode(err) != daemon.ExitFatal {
			t.Errorf("the daemon ended with %v (exit code %d); want a *daemon.FatalError", err, daemon.ExitCode(err))
		}
	})
}

// A certificate that is due while the node starts with the clock in doubt is not
// signed: the start goes on, with the node paused, and the certificate waits for the
// owner to clear the pause. A certificate signed on a clock that was set back would
// start before what the node has already issued.
func TestACertificateThatIsDueWhileTheClockIsInDoubtIsLeftAloneAtTheStart(t *testing.T) {
	noLeaks(t)
	n := newMemNode(t)
	before := certificateInMemory(t, n)
	tab, err := daemon.Table()
	if err != nil {
		t.Fatal(err)
	}
	w := txn.New(n.mem, tab, nil)
	if err := w.Recover(); err != nil {
		t.Fatal(err)
	}
	k, err := keys.Load(w)
	if err != nil {
		t.Fatal(err)
	}
	// the node last ran at day 30 by a clock that has since been set back to day 25,
	// when the certificate of 30 days has 5 left
	if err := k.SaveClock(keys.ClockState{HighWater: t0.Add(30 * 24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	n.clk.Set(t0.Add(25 * 24 * time.Hour))
	d, err := daemon.Start(context.Background(), memConfig(n.home, n.clk, n.mem))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.RequestStop("test over"); d.Wait() })
	if got := certificateInMemory(t, n); got != before {
		t.Errorf("serial %d after a start on a clock that was set back, want %d: nothing is signed then", got, before)
	}
	if status := tryManage(n.home, "status"); status != 200 {
		t.Errorf("the node does not answer: %d", status)
	}
}

// Wherever the node dies while it renews the certificate, the next start loads the
// keys (a serial reserved and a certificate not written is an interrupted renewal,
// not a refusal), finishes the renewal before anything else signs, and the serial it
// ends on is above every serial that was reserved: a serial is never issued twice.
func TestACrashAnywhereInTheRenewalLeavesKeysTheNextStartAcceptsAndRepairs(t *testing.T) {
	noLeaks(t)
	n := newMemNode(t)
	before := certificateInMemory(t, n)
	n.clk.Advance(25 * 24 * time.Hour)
	var states []*fsxtest.Mem
	c := memConfig(n.home, n.clk, n.mem)
	c.Observer = observerFunc(func(p fsx.Point, _ txn.Kind) {
		if p.Tag.Tx == keys.TxCertRenew {
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
		t.Fatal("no crash state of the renewal was captured")
	}
	interrupted := 0
	for i, s := range states {
		node := &memNode{home: n.home, mem: s, clk: n.clk}
		d, err := daemon.Start(context.Background(), memConfig(node.home, node.clk, node.mem))
		if err != nil {
			t.Errorf("state %d: the start over the crash state: %v", i, err)
			continue
		}
		d.RequestStop("test over")
		if err := d.Wait(); err != nil {
			t.Errorf("state %d: %v", i, err)
		}
		serial := certificateInMemory(t, node)
		if serial <= before {
			t.Errorf("state %d: the certificate has serial %d after the start, want a renewal above %d", i, serial, before)
		}
		if serial > before+2 {
			t.Errorf("state %d: serial %d, want at most %d (one reserved and lost, one issued)", i, serial, before+2)
		}
		if serial == before+2 {
			interrupted++
		}
	}
	if interrupted == 0 {
		t.Error("no crash state was an interrupted renewal: the serial was never reserved and the certificate lost")
	}
}

// certificateWindowInMemory is the window of the certificate of a node on a disk in
// memory, read with the record package alone.
func certificateWindowInMemory(t testing.TB, n *memNode) record.NetworkKeyCertificate {
	t.Helper()
	data, err := n.mem.ReadFile("keys/network.cert")
	if err != nil {
		t.Fatal(err)
	}
	rec, err := record.Decode(data, record.DefaultMaxInput)
	if err != nil {
		t.Fatal(err)
	}
	v, err := record.Verify(record.NetworkKeyCertificateDescriptor, rec)
	if err != nil {
		t.Fatal(err)
	}
	return v.Payload()
}

// A wall clock that jumped forward by more than the 20 days after which a certificate
// looks due makes the node renew on the fast clock, so the certificate starts in the
// future of the real time. The node is stopped at once, the clock is put right, and
// the next start finds it behind the mark and pauses, because the renewal raised the
// mark with the certificate (it would not have if the node had been stopped within a
// minute of the renewal, before the periodic write of the mark). The owner then
// clears the pause, and in that same pass of the loop the periodic checks run on the
// clock the owner accepted: the certificate is not yet valid, so it is due, and it
// is renewed and valid for the real time, with no wait of the days the fast clock
// had scheduled the check for. A periodic task of the composition root runs then
// too.
func TestAClockThatJumpedForwardAndWasPutRightLeavesACertificateTheClearRenews(t *testing.T) {
	noLeaks(t)
	n := newMemNode(t)
	real := n.clk.Now()
	var mu sync.Mutex
	var attached []time.Time
	cfg := func() daemon.Config {
		c := memConfig(n.home, n.clk, n.mem)
		c.Attach.Periodic = []daemon.PeriodicTask{{Name: "attached", Every: time.Hour, Run: func(in engine.TaskInput) error {
			mu.Lock()
			defer mu.Unlock()
			attached = append(attached, in.Now)
			return nil
		}}}
		return c
	}

	n.clk.Set(real.Add(25 * 24 * time.Hour)) // the certificate looks due at the start
	d, err := daemon.Start(context.Background(), cfg())
	if err != nil {
		t.Fatal(err)
	}
	d.RequestStop("the clock is wrong")
	if err := d.Wait(); err != nil {
		t.Fatal(err)
	}
	fast := certificateWindowInMemory(t, n)
	if fast.Serial != 2 || !fast.NotBefore.After(real.Add(24*24*time.Hour)) {
		t.Fatalf("setting up: the certificate is serial %d from %v, want the renewal made on the fast clock", fast.Serial, fast.NotBefore)
	}

	n.clk.Set(real.Add(time.Hour)) // the clock is put right
	d, err = daemon.Start(context.Background(), cfg())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { d.RequestStop("test over"); d.Wait() }()
	status, body := manage(t, n.home, "status", map[string]any{})
	if status != 200 || !strings.Contains(string(body), `"rollback_paused":true`) {
		t.Fatalf("status after the clock was put right: %d %s; want the pause, because the mark follows the certificate", status, body)
	}

	status, body = manage(t, n.home, "clock.clear_rollback", map[string]any{"operation_id": opID(1), "params": map[string]any{}})
	if answerOf(t, status, body); !strings.Contains(string(body), `"cleared":true`) {
		t.Fatalf("the clear: %d %s", status, body)
	}
	settled(t, d) // the same pass: the clock has not moved since the clear

	now := n.clk.Now()
	c := certificateWindowInMemory(t, n)
	if c.Serial != 3 || c.ValidityAt(now) != record.Valid || !c.NotAfter.Equal(now.Truncate(time.Second).Add(30*24*time.Hour)) {
		t.Errorf("after the clear the certificate is serial %d, %v at now, until %v; want serial 3, valid, thirty days from now", c.Serial, c.ValidityAt(now), c.NotAfter)
	}
	status, body = manage(t, n.home, "status", map[string]any{})
	var st struct {
		Result struct {
			Certificate struct {
				RemainingS int64 `json:"remaining_s"`
			} `json:"certificate"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &st); err != nil || status != 200 || st.Result.Certificate.RemainingS != 30*24*3600 {
		t.Errorf("status after the clear: %d %s (%v); want thirty days of certificate", status, body, err)
	}
	mu.Lock()
	ran := append([]time.Time(nil), attached...)
	mu.Unlock()
	if len(ran) == 0 || !ran[len(ran)-1].Equal(now) {
		t.Errorf("the attached periodic task ran at %v; want a run at the clock the owner accepted, %v", ran, now)
	}
}
