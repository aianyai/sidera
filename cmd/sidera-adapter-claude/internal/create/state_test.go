package create

import (
	"errors"
	"testing"
	"time"
)

func TestNewIntent(t *testing.T) {
	s, err := NewIntent("attempt-1", testSession, testDir)
	if err != nil {
		t.Fatal(err)
	}
	if s.Stage != StageIntent || !s.Process.IsZero() || s.Starts != 0 {
		t.Fatalf("intent = %+v", s)
	}
	for _, r := range []struct {
		rule string
		a    AttemptID
		id   SessionID
		dir  string
	}{
		{"zero attempt id", "", testSession, testDir},
		{"zero session id", "a", "", testDir},
		{"zero project directory", "a", testSession, ""},
	} {
		if _, err := NewIntent(r.a, r.id, r.dir); !errors.Is(err, ErrInvalidInput) {
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
		{"start times within the same second", proc(5, time.Second), proc(5, time.Second+400*time.Millisecond), true},
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
	s, _ := NewIntent("attempt-1", testSession, testDir)

	s1, err := s.Started(proc(100, time.Second))
	if err != nil || s1.Stage != StageStarted || s1.Starts != 1 || !s1.Process.Same(proc(100, time.Second)) {
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
	for _, p := range []Process{{}, {PID: 5}, {StartTime: at(1)}, {PID: -1, StartTime: at(1)}} {
		if _, err := s.Started(p); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("Started(%+v): %v", p, err)
		}
	}
	if _, err := (State{}).Ready(); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("zero state: %v", err)
	}
}
