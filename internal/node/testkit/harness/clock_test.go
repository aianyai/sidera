package harness_test

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/keys"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent/engineagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
)

func clockOnDisk(t *testing.T, h *harness.H) keys.ClockState {
	t.Helper()
	st, err := keys.ReadClock(h.Disk())
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func requireClockPause(t *testing.T, h *harness.H, want bool, since time.Time) {
	t.Helper()
	status, err := h.Engine().Status(bg())
	if err != nil {
		t.Fatal(err)
	}
	if status.Clock.RollbackPaused != want || !status.Clock.PausedSince.Equal(since) {
		t.Fatalf("clock status = %+v, want paused=%v since=%v", status.Clock, want, since)
	}
}

func refuseClockWork(t *testing.T, h *harness.H) {
	t.Helper()
	_, err := h.Agent("a").Send(bg(), fakeagent.SendInput{To: []string{h.Addr("b")}, Body: "paused work"})
	if fakeagent.CodeOf(err) != "clock_rollback" {
		t.Fatalf("send while paused: %v", err)
	}
}

func clearHarnessClock(t *testing.T, h *harness.H) {
	t.Helper()
	var id admin.OperationID
	id[0] = 231
	_, err := h.Engine().Execute(bg(), engine.ManagementCommand{
		Key: admin.KeyFor(admin.LocalOwner(), id), Fingerprint: sha256.Sum256([]byte("clear")), Name: "clock.clear_rollback",
		Operation: engine.ClearClockRollbackOp(func(c engine.ClockClear) ([]byte, error) { return json.Marshal(c) }),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestClockPauseSurvivesNormalWallTimeAndRestart(t *testing.T) {
	h := harness.New(t, harness.Options{})
	h.MustDirect("a")
	h.MustDirect("b")
	mark := h.Clock().Now()
	since := mark.Add(-time.Hour)
	h.Set(since)
	refuseClockWork(t, h)
	st := clockOnDisk(t, h)
	if !st.HighWater.Equal(mark) || !st.RollbackPausedSince.Equal(since) {
		t.Fatalf("rollback was not durable at refusal: %+v", st)
	}
	h.Set(mark.Add(time.Hour))
	h.Restart()
	refuseClockWork(t, h)
	requireClockPause(t, h, true, since)
	if got := clockOnDisk(t, h); !got.RollbackPausedSince.Equal(since) {
		t.Fatalf("restart moved pause start: %+v", got)
	}
}

func TestClearedClockSurvivesRestartAndPeriodicSaveThenPausesAnew(t *testing.T) {
	h := harness.New(t, harness.Options{})
	h.MustDirect("a")
	h.MustDirect("b")
	h.Set(h.Clock().Now().Add(-time.Hour))
	refuseClockWork(t, h)
	clearHarnessClock(t, h)
	mark := h.Clock().Now()
	h.Restart()
	requireClockPause(t, h, false, time.Time{})
	if st := clockOnDisk(t, h); st.Paused() || !st.HighWater.Equal(mark) {
		t.Fatalf("clear lost across restart: %+v", st)
	}
	h.Advance(time.Minute)
	if err := h.Quiesce(); err != nil {
		t.Fatal(err)
	}
	if st := clockOnDisk(t, h); st.Paused() || !st.HighWater.Equal(h.Clock().Now()) {
		t.Fatalf("periodic task restored old state: %+v", st)
	}
	since := h.Clock().Now().Add(-30 * time.Minute)
	h.Set(since)
	refuseClockWork(t, h)
	requireClockPause(t, h, true, since)
	if st := clockOnDisk(t, h); !st.RollbackPausedSince.Equal(since) {
		t.Fatalf("new pause retained old since: %+v", st)
	}
}

func TestForcedClockPauseAppliesToEveryStartAndKeepsExistingSince(t *testing.T) {
	h := harness.New(t, harness.Options{RollbackPaused: true})
	first := h.Clock().Now()
	requireClockPause(t, h, true, first)
	if st := clockOnDisk(t, h); !st.RollbackPausedSince.Equal(first) {
		t.Fatalf("forced pause not persisted: %+v", st)
	}
	h.Advance(time.Minute)
	h.Restart()
	requireClockPause(t, h, true, first)
	clearHarnessClock(t, h)
	requireClockPause(t, h, false, time.Time{})
	h.Restart()
	since := h.Clock().Now()
	requireClockPause(t, h, true, since)
	if st := clockOnDisk(t, h); !st.RollbackPausedSince.Equal(since) {
		t.Fatalf("forced restart pause not persisted: %+v", st)
	}
	h.Advance(time.Minute)
	h.Restart()
	requireClockPause(t, h, true, since)
}

func TestPeriodicClockMarkDeterminesRollbackAfterRestart(t *testing.T) {
	h := harness.New(t, harness.Options{})
	h.MustDirect("a")
	h.MustDirect("b")
	h.Advance(10 * time.Minute)
	if err := h.Quiesce(); err != nil {
		t.Fatal(err)
	}
	mark := h.Clock().Now()
	if st := clockOnDisk(t, h); st.Paused() || !st.HighWater.Equal(mark) {
		t.Fatalf("minute task did not persist mark: %+v", st)
	}
	h.Stop()
	since := mark.Add(-5 * time.Minute)
	h.Set(since)
	h.Start()
	refuseClockWork(t, h)
	requireClockPause(t, h, true, since)
	if st := clockOnDisk(t, h); !st.HighWater.Equal(mark) || !st.RollbackPausedSince.Equal(since) {
		t.Fatalf("startup rollback did not keep minute mark: %+v", st)
	}
}

func TestForcedClockPauseWriteFailureAbortsStart(t *testing.T) {
	h := harness.NewStopped(t, harness.Options{RollbackPaused: true})
	want := errors.New("forced pause write failed")
	crashed, err := h.TryStart(bg(), harness.StartOptions{Arm: func(f *fsxtest.Faulty) {
		f.Fail(fsxtest.Match{Tx: "clock-high-water", Step: fsx.StepCreate}, want)
	}})
	if crashed || !errors.Is(err, want) {
		t.Fatalf("forced pause start = %v, %v", crashed, err)
	}
	if err := h.Quiesce(); !errors.Is(err, engineagent.ErrNodeNotRunning) {
		t.Fatalf("failed startup published an engine: %v", err)
	}
	h.Start()
	requireClockPause(t, h, true, h.Clock().Now())
}
