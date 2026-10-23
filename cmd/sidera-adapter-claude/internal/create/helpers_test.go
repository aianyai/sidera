package create

import (
	"testing"
	"time"
)

const (
	testSession  SessionID = "8612a8ed-5c4a-4920-bf66-37cdafaffda2"
	otherSession SessionID = "0b6c1f2e-1d3a-4c5b-9e7f-112233445566"
	testPrompt   PromptID  = "f47ac10b-58cc-4372-a567-0e02b2c3d479"
	testDir                = "/srv/projects/alpha"

	// recordedPID is the PID of the process that stateAt records.
	recordedPID = 100
)

var t0 = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

func at(d time.Duration) time.Time { return t0.Add(d) }

// unix is the Unix second d after t0.
func unix(d time.Duration) int64 { return t0.Unix() + int64(d/time.Second) }

func proc(pid int, started time.Duration) Process {
	return Process{PID: pid, StartUnix: unix(started)}
}

// aliveAs is the process table's answer for a process that holds the PID.
func aliveAs(pid int, started time.Duration) Lookup {
	return Lookup{PID: pid, Alive: true, StartUnix: unix(started)}
}

// alive is the answer for the PID of the process stateAt records.
func alive(started time.Duration) Lookup { return aliveAs(recordedPID, started) }

// gone is the answer for a PID no process holds.
func gone(pid int) Lookup { return Lookup{PID: pid} }

// stateAt builds a valid state in the given stage. Stage Started and the
// stages after it carry the process 100 started at 10 seconds, except where
// withProcess is false (a UUIDSeen or Ready state whose process was never
// recorded).
func stateAt(t *testing.T, stage Stage, withProcess bool) State {
	t.Helper()
	s, err := NewIntent("attempt-1", testSession, testPrompt, testDir)
	if err != nil {
		t.Fatal(err)
	}
	if withProcess || stage == StageStarted {
		if s, err = s.Started(proc(recordedPID, 10*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if stage >= StageUUIDSeen {
		if s, err = s.UUIDSeen(); err != nil {
			t.Fatal(err)
		}
	}
	if stage >= StageReady {
		if s, err = s.Ready(); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func obsRecord(r SessionRecord) Observations {
	r.Dir = testDir
	return Observations{Record: r}
}

// failedFrom returns s failed with the reason.
func failedFrom(t *testing.T, s State, reason Reason) State {
	t.Helper()
	f, err := s.Failed(reason)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// failReasons are the reasons that end an attempt, written out by hand.
var failReasons = []Reason{ReasonStartTimedOut, ReasonStartsExhausted, ReasonMultipleProcesses, ReasonOutsideWriter}

var (
	noFile       = SessionRecord{}
	emptyFile    = SessionRecord{Exists: true}
	fileWithID   = SessionRecord{Exists: true, HasSessionID: true}
	fileWithBoth = SessionRecord{Exists: true, HasSessionID: true, HasFirstPrompt: true}
)
