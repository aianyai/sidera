package keys_test

import (
	"bytes"
	"math/rand/v2"
	"reflect"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/keys"
	"github.com/aianyai/sidera/internal/node/txn"
)

// capture runs the transaction and returns the disk a crash would leave before
// each of its points, in every kind of crash: the process dying, the worst power
// loss and seeded ones. It also returns how many points the transaction has, so
// a test can assert that it saw them all.
func capture(r *rig, rng *rand.Rand, seeded int, run func() error) (states []crashState, points int, err error) {
	r.obs.on = func(p fsx.Point) {
		points++
		for _, s := range crashStates(r.mem, rng, seeded) {
			s.name += " before " + p.String()
			states = append(states, s)
		}
	}
	err = run()
	r.obs.on = nil
	return
}

// repaired asserts the one state a repaired node is in: it loads, no renewal is
// interrupted, nothing is left for recovery, and the certificate is the one the
// serial file names.
func repaired(t *testing.T, where string, r *rig) *keys.Keys {
	t.Helper()
	k, err := keys.Load(r.w)
	if err != nil {
		t.Fatalf("%s: Load: %v", where, err)
	}
	if k.RenewalInterrupted() {
		t.Errorf("%s: the renewal is still interrupted", where)
	}
	if work, err := keys.RecoveryWork(r.mem); err != nil || len(work) != 0 {
		t.Errorf("%s: RecoveryWork = %v, %v; want nothing", where, work, err)
	}
	if k.SerialSeen().Serial != k.Certificate().Payload().Serial || k.SerialSeen().Digest != k.Certificate().Digest() {
		t.Errorf("%s: the serial file (%d) and the certificate (%d) disagree", where, k.SerialSeen().Serial, k.Certificate().Payload().Serial)
	}
	return k
}

// Every crash state of a renewal, restarted: the keys load, the interruption is
// reported exactly when the serial is ahead of the certificate, the recovery
// predicate agrees, and the repair, a renewal, ends in a consistent state with a
// serial above everything that was reserved. The repair is crashed at each of its
// own points as well, and the keys that restart from those states are repaired
// again.
func TestRenewalReplaysToAConsistentStateFromEveryCrashState(t *testing.T) {
	rng := rand.New(rand.NewPCG(31, 32))
	base := newRig(t)
	base.mustInit(1, t0)
	if err := base.mustLoad().Renew(t0.Add(time.Hour)); err != nil { // serial 2: the starting point has history
		t.Fatal(err)
	}

	k := open(t, base.mem.Clone()).mustLoad()
	r := open(t, base.mem.Clone())
	k = r.mustLoad()
	states, points, err := capture(r, rng, 4, func() error { return k.Renew(t0.Add(2 * time.Hour)) })
	if err != nil {
		t.Fatal(err)
	}
	if want := 11 * 6; len(states) != want || points != 11 {
		t.Fatalf("captured %d states at %d points, want %d at 11", len(states), points, want)
	}

	const acked = 2 // the last serial whose renewal was acknowledged
	interrupted, innerStates := 0, 0
	for _, s := range states {
		r1 := open(t, s.mem)
		k1, err := keys.Load(r1.w)
		if err != nil {
			t.Fatalf("%s: Load after the crash: %v", s.name, err)
		}
		cert, seen := k1.Certificate().Payload().Serial, k1.SerialSeen().Serial
		if (cert != 2 && cert != 3) || (seen != 2 && seen != 3) || seen < cert || (cert == 3 && seen != 3) {
			t.Fatalf("%s: certificate serial %d, serial seen %d: a state no step of a renewal leaves", s.name, cert, seen)
		}
		work, err := keys.RecoveryWork(r1.mem)
		if err != nil {
			t.Fatalf("%s: RecoveryWork: %v", s.name, err)
		}
		if k1.RenewalInterrupted() != (seen == 3 && cert == 2) || (len(work) == 1) != k1.RenewalInterrupted() || len(work) > 1 {
			t.Errorf("%s: interrupted=%v for certificate %d, serial seen %d, work %v", s.name, k1.RenewalInterrupted(), cert, seen, work)
		}
		if !k1.RenewalInterrupted() {
			repaired(t, s.name, r1)
			continue
		}
		interrupted++

		// the repair, crashed at each of its points
		inner, _, err := capture(r1, rng, 1, func() error { return k1.Renew(t0.Add(3 * time.Hour)) })
		if err != nil {
			t.Fatalf("%s: the repair: %v", s.name, err)
		}
		fin := repaired(t, s.name+": repair", r1)
		if got := fin.Certificate().Payload().Serial; got <= seen {
			t.Errorf("%s: the repair issued serial %d, at most the reserved %d", s.name, got, seen)
		}
		for _, is := range inner {
			innerStates++
			r2 := open(t, is.mem)
			k2, err := keys.Load(r2.w)
			if err != nil {
				t.Fatalf("%s, then %s: Load: %v", s.name, is.name, err)
			}
			// the serial reserved by the first crash was never durable when the
			// repair crashed, so it may be gone; what was acknowledged never is
			if k2.SerialSeen().Serial < acked {
				t.Errorf("%s, then %s: the serial went back to %d, below the acknowledged %d", s.name, is.name, k2.SerialSeen().Serial, acked)
			}
			loaded := k2.SerialSeen().Serial
			if k2.RenewalInterrupted() {
				if err := k2.Renew(t0.Add(4 * time.Hour)); err != nil {
					t.Fatalf("%s, then %s: second repair: %v", s.name, is.name, err)
				}
			}
			fin2 := repaired(t, s.name+", then "+is.name, r2)
			if fin2.Certificate().Payload().Serial < loaded {
				t.Errorf("%s, then %s: final serial %d is below the %d that was loaded", s.name, is.name, fin2.Certificate().Payload().Serial, loaded)
			}
		}
	}
	if interrupted < 5 || innerStates < 50 {
		t.Errorf("only %d interrupted states and %d crashes during repair were exercised", interrupted, innerStates)
	}
}

// Every crash state of init, restarted: whatever the state, the rerun of init
// ends in a complete set, and the keys that existed are untouched (the protected
// files are compared byte for byte). The rerun is crashed at each of its points
// too and run a third time.
func TestInitReplaysToAConsistentStateFromEveryCrashState(t *testing.T) {
	rng := rand.New(rand.NewPCG(41, 42))
	golden := newRig(t)
	states, points, err := capture(golden, rng, 4, func() error {
		_, err := golden.runInit(newSeeded(1), t0)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := points * 6; len(states) != want || points < 40 {
		t.Fatalf("captured %d states at %d points", len(states), points)
	}

	protected := []string{keys.IdentityKeyPath, keys.NetworkKeyPath, keys.SelfRevocationPath, keys.ClockPath}
	snapProtected := func(m interface{ ReadFile(string) ([]byte, error) }) map[string][]byte {
		out := map[string][]byte{}
		for _, p := range protected {
			if b, err := m.ReadFile(p); err == nil {
				out[p] = b
			}
		}
		return out
	}
	completed := func(where string, r *rig, before map[string][]byte, seed byte) {
		t.Helper()
		if done, _ := keys.Initialized(r.mem); !done {
			if _, err := r.runInit(newSeeded(seed), t0.Add(time.Minute)); err != nil {
				t.Fatalf("%s: rerun of init: %v", where, err)
			}
		}
		repaired(t, where, r)
		for p, want := range before {
			if got := r.read(p); !bytes.Equal(got, want) {
				t.Errorf("%s: %s changed", where, p)
			}
		}
	}

	rerunCrashes := 0
	for i, s := range states {
		r := open(t, s.mem)
		before := snapProtected(r.mem)
		outer := s.mem.Clone()
		completed(s.name, r, before, 2)

		// crash the rerun itself, for the process crash of every point (the
		// number of nested runs stays small), in the two crashes that need no
		// seed, and run init a third time
		if i%6 != 0 {
			continue
		}
		rr := open(t, outer.Clone())
		if done, _ := keys.Initialized(rr.mem); done {
			continue
		}
		inner, _, err := capture(rr, rng, 0, func() error {
			_, err := rr.runInit(newSeeded(2), t0.Add(time.Minute))
			return err
		})
		if err != nil {
			t.Fatalf("%s: rerun: %v", s.name, err)
		}
		for _, is := range inner {
			rerunCrashes++
			r3 := open(t, is.mem)
			completed(s.name+", then "+is.name, r3, snapProtected(r3.mem), 3)
		}
	}
	if rerunCrashes < 1000 {
		t.Errorf("only %d crashes during a rerun of init were exercised", rerunCrashes)
	}
}

// A periodic write of the clock record is one replacement: after any crash the
// record is the old state or the new one, whole.
func TestClockHighWaterIsOldOrNewAfterEveryCrashState(t *testing.T) {
	rng := rand.New(rand.NewPCG(51, 52))
	base := newRig(t)
	base.mustInit(1, t0)
	old := keys.ClockState{HighWater: t0.Add(time.Minute)}
	if err := base.mustLoad().SaveClock(old); err != nil {
		t.Fatal(err)
	}
	next := keys.ClockState{HighWater: t0.Add(2 * time.Minute), RollbackPausedSince: t0.Add(90 * time.Second)}

	r := open(t, base.mem.Clone())
	k := r.mustLoad()
	states, points, err := capture(r, rng, 6, func() error { return k.SaveClock(next) })
	if err != nil || points != 6 || len(states) != 6*8 {
		t.Fatalf("captured %d states at %d points: %v", len(states), points, err)
	}
	seenOld, seenNew := 0, 0
	for _, s := range states {
		r1 := open(t, s.mem)
		got, err := keys.ReadClock(r1.mem)
		if err != nil {
			t.Fatalf("%s: ReadClock: %v", s.name, err)
		}
		switch got {
		case old:
			seenOld++
		case next:
			seenNew++
		default:
			t.Errorf("%s: clock state %+v is neither the old nor the new one", s.name, got)
		}
		r1.mustLoad()
	}
	if seenOld == 0 || seenNew == 0 {
		t.Errorf("old seen %d times and new %d times: the crash states did not cover both", seenOld, seenNew)
	}
}

// The clear of the rollback is a transaction of its composer that holds this
// package's write: after any crash the pause is still in force or cleared whole,
// and the owner repeating the operation reaches the cleared state.
func TestClockClearIsPausedOrClearedAfterEveryCrashState(t *testing.T) {
	rng := rand.New(rand.NewPCG(61, 62))
	base := newRig(t)
	base.mustInit(1, t0)
	paused := keys.ClockState{HighWater: t0.Add(time.Hour), RollbackPausedSince: t0.Add(30 * time.Minute)}
	if err := base.mustLoad().SaveClock(paused); err != nil {
		t.Fatal(err)
	}
	now := t0.Add(10 * time.Minute)
	clear := func(r *rig) error {
		tx := r.w.Begin(clearDecl)
		defer tx.Close()
		if err := keys.PutClockCleared(tx, now); err != nil {
			return err
		}
		if err := tx.Replace("result", "config/result-stand-in", []byte("{}"), fsx.ModePrivate); err != nil {
			return err
		}
		return tx.Commit()
	}

	r := open(t, base.mem.Clone())
	states, points, err := capture(r, rng, 6, func() error { return clear(r) })
	if err != nil || points != 11 {
		t.Fatalf("captured %d points: %v", points, err)
	}
	cleared := keys.ClockState{HighWater: now}
	seenPaused, seenCleared := 0, 0
	for _, s := range states {
		r1 := open(t, s.mem)
		got, err := keys.ReadClock(r1.mem)
		if err != nil {
			t.Fatalf("%s: ReadClock: %v", s.name, err)
		}
		switch got {
		case paused:
			seenPaused++
			if err := r1.mustLoad().Renew(t0.Add(2 * time.Hour)); err == nil {
				t.Errorf("%s: a renewal was signed while the pause was in force", s.name)
			}
			if err := clear(r1); err != nil { // the owner repeats the operation
				t.Fatalf("%s: repeating the clear: %v", s.name, err)
			}
		case cleared:
			seenCleared++
		default:
			t.Errorf("%s: clock state %+v is neither paused nor cleared", s.name, got)
		}
		if got, _ := keys.ReadClock(r1.mem); got != cleared {
			t.Errorf("%s: after the clear the state is %+v", s.name, got)
		}
	}
	if seenPaused == 0 || seenCleared == 0 {
		t.Errorf("paused seen %d times, cleared %d: the crash states did not cover both", seenPaused, seenCleared)
	}
}

// Every key of every transaction this package declares, and of the composite
// transactions that hold its writes, is reached by the scenarios of this
// package: nothing is declared that no test exercises.
func TestEveryDeclaredCrashPointOfKeysIsCovered(t *testing.T) {
	r := newRig(t)
	r.mustInit(1, t0)
	k := r.mustLoad()
	if err := k.Renew(t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := k.SaveClock(keys.ClockState{HighWater: t0.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	tx := r.w.Begin(clearDecl)
	if err := keys.PutClockCleared(tx, t0.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Replace("result", "config/result-stand-in", []byte("{}"), fsx.ModePrivate); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	tx.Close()
	// init without the marker, on a second disk with no directory: the
	// declaration of the keys-only init and the directory creation
	r2 := newRig(t)
	if _, err := r2.runInitKeysOnly(newSeeded(2), t0); err != nil {
		t.Fatal(err)
	}

	hits := map[string]int{}
	for _, w := range []*txn.Writer{r.w, r2.w} {
		for _, c := range w.Coverage() {
			hits[c.Key.String()] += c.Hits
		}
	}
	owned := map[string]bool{"cert-renew": true, "clock-high-water": true, "init": true, "init-keys": true, "clock-clear": true}
	for _, c := range r.w.Coverage() {
		if !owned[c.Key.Tag.Tx] || c.Key.Tag.Role == "result" {
			continue
		}
		if hits[c.Key.String()] == 0 {
			t.Errorf("crash point %s of %s is never reached by the tests of this package", c.Key, c.Key.Tag.Tx)
		}
	}
	if reflect.DeepEqual(hits, map[string]int{}) {
		t.Fatal("no coverage was recorded")
	}
}
