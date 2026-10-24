package keys_test

import (
	"math/rand/v2"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/keys"
)

// The serial of a certificate a node issued never goes back: not across a
// restart, not across a crash at any step of a renewal, not across a power
// loss. A peer accepts only the highest serial it has seen, so a node that
// reissued a lower or an equal serial would be unverifiable to peers that had
// seen the higher one.
//
// The oracle is a model written in the test: the serials of the renewals whose
// Commit returned nil are acknowledged, and an acknowledged serial is durable
// and strictly above every earlier one; whatever the disk looks like after any
// crash, the next load and the next renewal stay above all of them.
func TestSerialNeverGoesBackAcrossRestartsAndCrashes(t *testing.T) {
	const renewPoints = 11 // 5 steps of the serial, 5 of the certificate, the commit point
	rng := rand.New(rand.NewPCG(21, 22))
	r := newRig(t)
	r.mustInit(1, t0)
	now := t0

	maxAcked := uint64(1) // the certificate init issued
	stats := map[string]int{}
	pointCrashes := make([]int, renewPoints+1)
	crashes := 0

	for step := 0; step < 400; step++ {
		now = now.Add(time.Hour)
		k := r.mustLoad()

		seen, cert := k.SerialSeen().Serial, k.Certificate().Payload().Serial
		if cert < maxAcked || seen < maxAcked || seen < cert {
			t.Fatalf("step %d: after a restart seen=%d cert=%d, acknowledged up to %d", step, seen, cert, maxAcked)
		}
		if node, serial, _ := oracleSerial(t, r.read(keys.SerialPath)); node != goldenNodeOf(k) || serial != seen {
			t.Fatalf("step %d: the serial file says %s/%d, Load says %d", step, node, serial, seen)
		}
		if k.RenewalInterrupted() {
			stats["interrupted-at-load"]++
		}

		switch rng.IntN(5) {
		case 0, 1: // an acknowledged renewal
			if err := k.Renew(now); err != nil {
				t.Fatalf("step %d: Renew: %v", step, err)
			}
			got := k.Certificate().Payload().Serial
			if got <= maxAcked {
				t.Fatalf("step %d: renewal issued serial %d, acknowledged %d before", step, got, maxAcked)
			}
			if k.SerialSeen().Serial != got || k.RenewalInterrupted() {
				t.Fatalf("step %d: after a renewal seen=%d cert=%d interrupted=%v", step, k.SerialSeen().Serial, got, k.RenewalInterrupted())
			}
			maxAcked = got
			stats["renewal"]++
		case 2: // a renewal during which the machine dies at one of its points
			target := crashes%renewPoints + 1
			crashes++
			var crashed *fsxtest.Mem
			n := 0
			r.obs.on = func(p fsx.Point) {
				if n++; n == target {
					all := crashStates(r.mem, rng, 2)
					crashed = all[rng.IntN(len(all))].mem
				}
			}
			err := k.Renew(now)
			r.obs.on = nil
			if err != nil {
				t.Fatalf("step %d: Renew: %v", step, err)
			}
			if crashed == nil {
				t.Fatalf("step %d: the renewal did not reach point %d", step, target)
			}
			pointCrashes[target]++
			r = open(t, crashed)
			stats["crash-in-renewal"]++
		case 3:
			r = open(t, r.mem.CrashCloneWorst())
			stats["power-loss"]++
		case 4:
			r = open(t, r.mem.Clone())
			stats["process-crash"]++
		}
	}

	for p := 1; p <= renewPoints; p++ {
		if pointCrashes[p] < 3 {
			t.Errorf("point %d of the renewal was crashed at %d times, want at least 3", p, pointCrashes[p])
		}
	}
	for name, min := range map[string]int{"renewal": 60, "crash-in-renewal": 50, "power-loss": 30, "process-crash": 30, "interrupted-at-load": 5} {
		if stats[name] < min {
			t.Errorf("%s happened %d times, want at least %d (all: %v)", name, stats[name], min, stats)
		}
	}
}

// After a renewal was interrupted the next one skips the serial that was
// reserved: the serial after the interruption is the one after the highest
// ever written to the serial file, never the one after the certificate.
func TestRenewalAfterAnInterruptionIssuesTheSerialAfterTheReservedOne(t *testing.T) {
	r := newRig(t)
	r.mustInit(1, t0)

	var crashed *fsxtest.Mem
	r.obs.on = func(p fsx.Point) {
		if p.String() == "cert-renew/cert.create#1" { // the serial is durable, the certificate is not written
			crashed = r.mem.Clone()
		}
	}
	if err := r.mustLoad().Renew(t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if crashed == nil {
		t.Fatal("the crash point was not reached")
	}

	r2 := open(t, crashed)
	k := r2.mustLoad()
	if got := k.SerialSeen().Serial; got != 2 {
		t.Fatalf("seen serial after the interruption = %d, want 2", got)
	}
	if got := k.Certificate().Payload().Serial; got != 1 {
		t.Fatalf("certificate serial after the interruption = %d, want the old 1", got)
	}
	if err := k.Renew(t0.Add(2 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := k.Certificate().Payload().Serial; got != 3 {
		t.Errorf("serial of the repair = %d, want 3: serial 2 was reserved by the interrupted renewal", got)
	}
}
