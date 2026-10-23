package create

import (
	"reflect"
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
	case ActionFail, ActionControlUnknown:
		next, err = s.Failed(d.Reason)
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
		s, _ := NewIntent("attempt-1", testSession, testPrompt, testDir)
		expect(t, step(t, &s, obsRecord(noFile), at(time.Second)), ActionStartNew, ReasonNotStarted)
		// The adapter starts process 200 at 2 s and dies; the intent is all it committed.

		o := obsRecord(noFile)
		o.Running = []Process{proc(200, 2*time.Second)}
		expect(t, step(t, &s, o, at(10*time.Second)), ActionRecordProcess, ReasonProcessFoundByCommandLine)
		if s.Process.PID != 200 || s.Process.StartUnix != unix(2*time.Second) || s.Stage != StageStarted {
			t.Fatalf("state after recording: %+v", s)
		}

		o = obsRecord(fileWithID)
		o.Running = []Process{proc(200, 2*time.Second)}
		o.Recorded = aliveAs(200, 2*time.Second)
		expect(t, step(t, &s, o, at(11*time.Second)), ActionMarkUUIDSeen, ReasonSessionRecordHasID)
		expect(t, step(t, &s, o, at(12*time.Second)), ActionWait, ReasonTurnRunning)

		o = obsRecord(fileWithBoth)
		o.Recorded = gone(200)
		expect(t, step(t, &s, o, at(20*time.Second)), ActionMarkReady, ReasonFirstPromptRecorded)
		expect(t, step(t, &s, o, at(21*time.Second)), ActionDone, ReasonAlreadyReady)
	})

	t.Run("the process finished while the adapter was down: the file is read, nothing is started", func(t *testing.T) {
		s, _ := NewIntent("attempt-1", testSession, testPrompt, testDir)
		d := step(t, &s, obsRecord(fileWithBoth), at(time.Minute))
		expect(t, d, ActionMarkReady, ReasonFirstPromptRecorded)
	})

	t.Run("the process died after creating the file but before the prompt: resume, not --session-id", func(t *testing.T) {
		s, _ := NewIntent("attempt-1", testSession, testPrompt, testDir)
		d := step(t, &s, obsRecord(emptyFile), at(time.Minute))
		expect(t, d, ActionStartResume, ReasonRecordWithoutFirstPrompt)
	})
}

// A retry after the crash window may itself crash before recording its PID,
// and the process it started must be found as well.
func TestCrashDuringTheResumeRetryIsFoundToo(t *testing.T) {
	s := stateAt(t, StageUUIDSeen, true)
	// The first process (PID 100) is gone; the retry started PID 300 with --resume and the adapter died.
	o := obsRecord(fileWithID)
	o.Recorded = gone(recordedPID)
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

// Two live processes carry the session uuid, for example because the user
// resumed the session by hand while the adapter's turn runs. Two writers on one
// record interleave it into branches, so the adapter's control of the session
// is unknown: the attempt ends, nothing is started, no process is touched and
// nothing advances afterwards, not even readiness.
func TestTwoProcessesCarryingTheUUIDEndTheAttemptAsControlUnknown(t *testing.T) {
	s := stateAt(t, StageStarted, true)
	o := obsRecord(fileWithID)
	o.Recorded = alive(10 * time.Second)
	o.Running = []Process{proc(recordedPID, 10*time.Second), proc(300, 20*time.Second)}
	d := step(t, &s, o, at(30*time.Second))
	expect(t, d, ActionControlUnknown, ReasonMultipleProcesses)
	if !reflect.DeepEqual(d.Processes, []Process{proc(recordedPID, 10*time.Second), proc(300, 20*time.Second)}) {
		t.Fatalf("processes seen: %+v", d.Processes)
	}
	if s.Stage != StageFailed || s.FailReason != ReasonMultipleProcesses || !s.FailReason.ControlUnknown() {
		t.Fatalf("state after the decision: %+v", s)
	}

	// The world goes on: one writer leaves, the other writes the first prompt.
	o = obsRecord(fileWithBoth)
	o.Recorded = alive(10 * time.Second)
	o.Running = []Process{proc(recordedPID, 10*time.Second)}
	d = step(t, &s, o, at(40*time.Second))
	expect(t, d, ActionDone, ReasonAlreadyFailed)
	if d.FailReason != ReasonMultipleProcesses {
		t.Fatalf("stored reason: %v", d.FailReason)
	}
}

// A user who resumed the session in an interactive Claude has a process whose
// command line carries no uuid, so only the runtime's registry shows it. It is
// an outside writer: never recorded as the adapter's process, and the attempt
// ends as control unknown even though the first prompt is on disk.
func TestARegistryOnlyProcessIsAnOutsideWriter(t *testing.T) {
	s, _ := NewIntent("attempt-1", testSession, testPrompt, testDir)
	o := obsRecord(fileWithBoth)
	o.Registry = []RegistryEntry{{SessionID: testSession, PID: 777, StartUnix: unix(5 * time.Second), Current: aliveAs(777, 5*time.Second)}}
	d := step(t, &s, o, at(10*time.Second))
	expect(t, d, ActionControlUnknown, ReasonOutsideWriter)
	if !reflect.DeepEqual(d.Processes, []Process{proc(777, 5*time.Second)}) {
		t.Fatalf("processes seen: %+v", d.Processes)
	}
	if !s.Process.IsZero() || s.Stage != StageFailed || s.FailReason != ReasonOutsideWriter {
		t.Fatalf("the outside writer was recorded or the attempt did not fail: %+v", s)
	}
}

// A turn that timed out may still write the first prompt: the attempt was
// reported as failed already, so it stays failed and no second process is ever
// started for the uuid.
func TestFailureIsTerminalEvenWhenTheTimedOutProcessWritesLater(t *testing.T) {
	s := stateAt(t, StageUUIDSeen, true)
	o := obsRecord(fileWithID)
	o.Recorded = alive(10 * time.Second)
	expect(t, step(t, &s, o, at(10*time.Second+DefaultParams().StartTimeout+time.Second)), ActionFail, ReasonStartTimedOut)

	for _, rec := range []SessionRecord{fileWithID, fileWithBoth} {
		for _, l := range []Lookup{alive(10 * time.Second), gone(recordedPID)} {
			o = obsRecord(rec)
			o.Recorded = l
			expect(t, step(t, &s, o, at(time.Hour)), ActionDone, ReasonAlreadyFailed)
		}
	}
	o = obsRecord(fileWithID)
	o.Recorded = gone(recordedPID)
	if d := step(t, &s, o, at(2*time.Hour)); d.FailReason != ReasonStartTimedOut {
		t.Fatalf("stored reason: %v", d.FailReason)
	}
}
