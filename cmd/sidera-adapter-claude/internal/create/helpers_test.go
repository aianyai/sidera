package create

import (
	"testing"
	"time"
)

const (
	testSession  SessionID = "8612a8ed-5c4a-4920-bf66-37cdafaffda2"
	otherSession SessionID = "0b6c1f2e-1d3a-4c5b-9e7f-112233445566"
	testDir                = "/srv/projects/alpha"
)

var t0 = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

func at(d time.Duration) time.Time { return t0.Add(d) }

func proc(pid int, started time.Duration) Process {
	return Process{PID: pid, StartTime: at(started)}
}

// alive is the process table's answer for a process that holds the PID.
func alive(started time.Duration) Lookup { return Lookup{Alive: true, StartTime: at(started)} }

// stateAt builds a valid state in the given stage. Stage Started and the
// stages after it carry the process 100 started at 10 seconds, except where
// withProcess is false (a UUIDSeen or Ready state whose process was never
// recorded).
func stateAt(t *testing.T, stage Stage, withProcess bool) State {
	t.Helper()
	s, err := NewIntent("attempt-1", testSession, testDir)
	if err != nil {
		t.Fatal(err)
	}
	if withProcess || stage == StageStarted {
		if s, err = s.Started(proc(100, 10*time.Second)); err != nil {
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

var (
	noFile       = SessionRecord{}
	emptyFile    = SessionRecord{Exists: true}
	fileWithID   = SessionRecord{Exists: true, HasSessionID: true}
	fileWithBoth = SessionRecord{Exists: true, HasSessionID: true, HasFirstPrompt: true}
)
