package daemon_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/daemon"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/keys"
	"github.com/aianyai/sidera/internal/node/txn"
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
