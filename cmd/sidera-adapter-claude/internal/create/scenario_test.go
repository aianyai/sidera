package create

import (
	"testing"
	"time"
)

// step applies Decide and the matching state transition the way a caller does,
// and returns the decision.
func step(t *testing.T, s *State, o Observations, now time.Time) Decision {
	t.Helper()
	d, err := Decide(*s, o, DefaultParams(), now)
	if err != nil {
		t.Fatal(err)
	}
	var next State
	switch d.Action {
	case ActionRecordProcess:
		next, err = s.Started(d.Process)
	case ActionMarkUUIDSeen:
		next, err = s.UUIDSeen()
	case ActionMarkReady:
		next, err = s.Ready()
	default:
		return d
	}
	if err != nil {
		t.Fatal(err)
	}
	*s = next
	return d
}

func expect(t *testing.T, d Decision, a Action, r Reason) {
	t.Helper()
	if d.Action != a || d.Reason != r {
		t.Fatalf("got %v/%v, want %v/%v", d.Action, d.Reason, a, r)
	}
}

// The adapter starts the turn process with --session-id and is killed before
// the PID is recorded. After the restart there is one process and one
// session: the process is found by the uuid on its command line and recorded
// instead of starting a second one, and once the process has written the
// session file nothing is started with --session-id.
func TestCrashAfterStartBeforePIDRecordedKeepsOneProcessAndOneSession(t *testing.T) {
	t.Run("the process still runs after the restart: it is found and waited for", func(t *testing.T) {
		s, _ := NewIntent("attempt-1", testSession, testDir)
		expect(t, step(t, &s, obsRecord(noFile), at(time.Second)), ActionStartNew, ReasonNotStarted)
		// The adapter starts process 200 at 2 s and dies; the intent is all it committed.

		o := obsRecord(noFile)
		o.Running = []Process{proc(200, 2*time.Second)}
		expect(t, step(t, &s, o, at(10*time.Second)), ActionRecordProcess, ReasonProcessFoundByCommandLine)
		if !s.Process.Same(proc(200, 2*time.Second)) || s.Stage != StageStarted {
			t.Fatalf("state after recording: %+v", s)
		}

		o = obsRecord(fileWithID)
		o.Running = []Process{proc(200, 2*time.Second)}
		o.Recorded = alive(2 * time.Second)
		expect(t, step(t, &s, o, at(11*time.Second)), ActionMarkUUIDSeen, ReasonSessionRecordHasID)
		expect(t, step(t, &s, o, at(12*time.Second)), ActionWait, ReasonTurnRunning)

		o = obsRecord(fileWithBoth)
		o.Recorded = Lookup{}
		expect(t, step(t, &s, o, at(20*time.Second)), ActionMarkReady, ReasonFirstPromptRecorded)
		expect(t, step(t, &s, o, at(21*time.Second)), ActionDone, ReasonAlreadyReady)
	})

	t.Run("the process finished while the adapter was down: the file is read, nothing is started", func(t *testing.T) {
		s, _ := NewIntent("attempt-1", testSession, testDir)
		d := step(t, &s, obsRecord(fileWithBoth), at(time.Minute))
		expect(t, d, ActionMarkReady, ReasonFirstPromptRecorded)
	})

	t.Run("the process died after creating the file but before the prompt: resume, not --session-id", func(t *testing.T) {
		s, _ := NewIntent("attempt-1", testSession, testDir)
		d := step(t, &s, obsRecord(emptyFile), at(time.Minute))
		expect(t, d, ActionStartResume, ReasonRecordWithoutFirstPrompt)
	})

	t.Run("the process table lists nothing readable but the registry names the live process", func(t *testing.T) {
		s, _ := NewIntent("attempt-1", testSession, testDir)
		o := obsRecord(noFile)
		o.Registry = []RegistryEntry{{SessionID: testSession, PID: 200, StartTime: at(2 * time.Second), Current: alive(2 * time.Second)}}
		expect(t, step(t, &s, o, at(10*time.Second)), ActionRecordProcess, ReasonProcessFoundInRegistry)
	})
}

// A retry after the crash window may itself crash before recording its PID,
// and the process it started must be found as well.
func TestCrashDuringTheResumeRetryIsFoundToo(t *testing.T) {
	s := stateAt(t, StageUUIDSeen, true)
	// The first process (PID 100) is gone; the retry started PID 300 with --resume and the adapter died.
	o := obsRecord(fileWithID)
	o.Recorded = Lookup{}
	o.Running = []Process{proc(300, 90*time.Second)}
	d := step(t, &s, o, at(100*time.Second))
	expect(t, d, ActionRecordProcess, ReasonProcessFoundByCommandLine)
	if s.Process.PID != 300 || s.Starts != 2 {
		t.Fatalf("state: %+v", s)
	}
}

// PID reuse: the recorded PID now belongs to an unrelated process with another
// start time. It is neither "alive" (which would wait forever) nor a reason to
// skip the file check.
func TestRecordedPIDReusedByAnotherProcessIsNotAlive(t *testing.T) {
	s := stateAt(t, StageStarted, true)
	o := obsRecord(noFile)
	o.Recorded = alive(4000 * time.Second)
	expect(t, step(t, &s, o, at(time.Hour)), ActionStartNew, ReasonProcessEndedWithoutSession)

	o = obsRecord(fileWithID)
	o.Recorded = alive(4000 * time.Second)
	expect(t, step(t, &s, o, at(time.Hour)), ActionMarkUUIDSeen, ReasonSessionRecordHasID)
	expect(t, step(t, &s, o, at(time.Hour)), ActionStartResume, ReasonRecordWithoutFirstPrompt)
}

// Open questions the design does not answer. Each is a skipped test named after
// the question, to be unskipped with the answer.

func TestTwoProcessesCarryTheUUIDWhatShouldHappen(t *testing.T) {
	t.Skip("open question: two distinct live processes carry the uuid. Decide waits (ReasonMultipleProcesses) and starts nothing, " +
		"which keeps 'never two writers' but never settles the attempt while both live. Should it wait, fail terminally, " +
		"or record one of them and mark the session's control as unknown?")
}
