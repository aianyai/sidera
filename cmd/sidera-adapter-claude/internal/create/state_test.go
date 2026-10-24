package create

import (
	"errors"
	"testing"
	"time"
)

func TestNewIntent(t *testing.T) {
	s, err := NewIntent("attempt-1", testSession, testPrompt, testDir)
	if err != nil {
		t.Fatal(err)
	}
	if s.Stage != StageIntent || !s.Process.IsZero() || s.Starts != 0 || s.FailReason != 0 {
		t.Fatalf("intent = %+v", s)
	}
	if s.AttemptID != "attempt-1" || s.SessionID != testSession || s.FirstPromptID != testPrompt || s.ProjectDir != testDir {
		t.Fatalf("intent does not persist what it was given: %+v", s)
	}
	for _, r := range []struct {
		rule   string
		a      AttemptID
		id     SessionID
		prompt PromptID
		dir    string
	}{
		{"zero attempt id", "", testSession, testPrompt, testDir},
		{"zero session id", "a", "", testPrompt, testDir},
		{"zero first prompt id", "a", testSession, "", testDir},
		{"first prompt id in upper case", "a", testSession, "F47AC10B-58CC-4372-A567-0E02B2C3D479", testDir},
		{"zero project directory", "a", testSession, testPrompt, ""},
	} {
		if _, err := NewIntent(r.a, r.id, r.prompt, r.dir); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: err = %v", r.rule, err)
		}
	}
}

func TestProcessSame(t *testing.T) {
	rows := []struct {
		rule string
		a, b Process
		want bool
	}{
		{"same PID and start time", proc(5, time.Second), proc(5, time.Second), true},
		{"start times one second apart", proc(5, time.Second), proc(5, 2*time.Second), false},
		{"different PIDs", proc(5, time.Second), proc(6, time.Second), false},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if got := r.a.Same(r.b); got != r.want {
				t.Fatalf("Same = %v, want %v", got, r.want)
			}
		})
	}
}

func TestStateTransitions(t *testing.T) {
	s, _ := NewIntent("attempt-1", testSession, testPrompt, testDir)

	s1, err := s.Started(proc(100, time.Second))
	if err != nil || s1.Stage != StageStarted || s1.Starts != 1 || s1.Process != proc(100, time.Second) {
		t.Fatalf("started: %+v %v", s1, err)
	}
	if s.Stage != StageIntent {
		t.Fatal("transitions must not change the receiver")
	}
	again, _ := s1.Started(proc(100, time.Second))
	if again.Starts != 1 {
		t.Fatal("recording the recorded process again must not count a start")
	}
	reused, _ := s1.Started(proc(100, 50*time.Second))
	if reused.Starts != 2 {
		t.Fatal("the same PID with another start time is another process")
	}
	next, _ := s1.Started(proc(200, 2*time.Second))
	if next.Starts != 2 || next.Process.PID != 200 {
		t.Fatalf("a later start replaces the recorded process: %+v", next)
	}

	seen, err := s1.UUIDSeen()
	if err != nil || seen.Stage != StageUUIDSeen {
		t.Fatalf("seen: %+v %v", seen, err)
	}
	relaunched, _ := seen.Started(proc(300, 3*time.Second))
	if relaunched.Stage != StageUUIDSeen || relaunched.Starts != 2 {
		t.Fatalf("a start never moves the stage back: %+v", relaunched)
	}
	skipped, err := s.UUIDSeen()
	if err != nil || skipped.Stage != StageUUIDSeen || !skipped.Process.IsZero() {
		t.Fatalf("seen without a recorded process (the process ended before it was found): %+v %v", skipped, err)
	}
	if again, _ := seen.UUIDSeen(); again.Stage != StageUUIDSeen {
		t.Fatal("seen is idempotent")
	}

	ready, err := seen.Ready()
	if err != nil || ready.Stage != StageReady {
		t.Fatalf("ready: %+v %v", ready, err)
	}
	direct, err := s.Ready()
	if err != nil || direct.Stage != StageReady {
		t.Fatalf("ready straight from the intent: %+v %v", direct, err)
	}
	if _, err := ready.Started(proc(400, time.Second)); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("a ready attempt must not record a process: %v", err)
	}
	if _, err := ready.UUIDSeen(); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("a ready attempt must not go back to uuid-seen: %v", err)
	}
	if _, err := ready.Failed(ReasonStartTimedOut); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("a ready attempt must not fail: %v", err)
	}
	for _, p := range []Process{{}, {PID: 5}, {StartUnix: unix(time.Second)}, {PID: -1, StartUnix: unix(time.Second)}} {
		if _, err := s.Started(p); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("Started(%+v): %v", p, err)
		}
	}
	if _, err := (State{}).Ready(); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("zero state: %v", err)
	}
}

func TestStateFailed(t *testing.T) {
	intent := stateAt(t, StageIntent, false)
	started := stateAt(t, StageStarted, true)
	seen := stateAt(t, StageUUIDSeen, true)

	for _, from := range []State{intent, started, seen} {
		for _, r := range failReasons {
			f, err := from.Failed(r)
			if err != nil {
				t.Fatalf("stage %d reason %v: %v", from.Stage, r, err)
			}
			if f.Stage != StageFailed || f.FailReason != r || f.Process != from.Process || f.Starts != from.Starts {
				t.Errorf("failed state = %+v", f)
			}
			if from.Stage == StageIntent && from.FailReason != 0 {
				t.Error("transitions must not change the receiver")
			}
		}
	}

	f := failedFrom(t, started, ReasonStartTimedOut)
	t.Run("failing again for the same reason changes nothing", func(t *testing.T) {
		again, err := f.Failed(ReasonStartTimedOut)
		if err != nil || again != f {
			t.Fatalf("%+v %v", again, err)
		}
	})
	t.Run("a failed attempt keeps its first reason", func(t *testing.T) {
		if _, err := f.Failed(ReasonStartsExhausted); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a failed attempt records no process", func(t *testing.T) {
		if _, err := f.Started(proc(400, time.Second)); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a failed attempt does not see the uuid", func(t *testing.T) {
		if _, err := f.UUIDSeen(); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a failed attempt is never ready", func(t *testing.T) {
		if _, err := f.Ready(); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a reason that does not fail an attempt is refused", func(t *testing.T) {
		for _, r := range []Reason{0, ReasonTurnRunning, ReasonAlreadyReady, ReasonAlreadyFailed, ReasonProcessFoundByCommandLine, Reason(99)} {
			if _, err := started.Failed(r); !errors.Is(err, ErrInvalidInput) {
				t.Errorf("Failed(%v): %v", r, err)
			}
		}
	})
	t.Run("the zero state cannot fail", func(t *testing.T) {
		if _, err := (State{}).Failed(ReasonStartTimedOut); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("err = %v", err)
		}
	})
}

// The reasons an attempt fails with are persisted as numbers in the
// checkpoint; they must never be renumbered. The numbers are written out here.
func TestFailureReasonNumbersAreStable(t *testing.T) {
	for _, r := range []struct {
		reason Reason
		number int
	}{
		{ReasonMultipleProcesses, 5},
		{ReasonStartTimedOut, 10},
		{ReasonStartsExhausted, 11},
		{ReasonOutsideWriter, 12},
	} {
		if int(r.reason) != r.number {
			t.Errorf("%v = %d, persisted checkpoints hold %d", r.reason, int(r.reason), r.number)
		}
	}
}

func TestReasonControlUnknown(t *testing.T) {
	for _, r := range []struct {
		reason Reason
		want   bool
	}{
		{ReasonMultipleProcesses, true},
		{ReasonOutsideWriter, true},
		{ReasonStartTimedOut, true},
		{ReasonStartsExhausted, false},
		{ReasonTurnRunning, false},
	} {
		if got := r.reason.ControlUnknown(); got != r.want {
			t.Errorf("%v.ControlUnknown() = %v, want %v", r.reason, got, r.want)
		}
	}
}
