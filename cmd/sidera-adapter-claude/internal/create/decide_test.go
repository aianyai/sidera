package create

import (
	"errors"
	"testing"
	"time"
)

func TestDecide(t *testing.T) {
	p := DefaultParams()
	// A state with a process recorded at 10 s, and one without any.
	intent := stateAt(t, StageIntent, false)
	started := stateAt(t, StageStarted, true)
	seenNoProc := stateAt(t, StageUUIDSeen, false)
	seen := stateAt(t, StageUUIDSeen, true)
	ready := stateAt(t, StageReady, true)

	withRecorded := func(o Observations, l Lookup) Observations { o.Recorded = l; return o }
	withRunning := func(o Observations, ps ...Process) Observations { o.Running = ps; return o }
	withRegistry := func(o Observations, es ...RegistryEntry) Observations { o.Registry = es; return o }
	entry := func(pid int, started time.Duration, cur Lookup) RegistryEntry {
		return RegistryEntry{SessionID: testSession, PID: pid, StartTime: at(started), Current: cur}
	}

	rows := []struct {
		rule string
		s    State
		o    Observations
		now  time.Time
		want Decision
	}{
		// Starting: --session-id only when the session file does not exist.
		{"nothing exists and nothing ran: start with --session-id",
			intent, obsRecord(noFile), at(time.Second),
			Decision{Action: ActionStartNew, Reason: ReasonNotStarted}},
		{"a recorded process is gone and left no file: start with --session-id again",
			started, obsRecord(noFile), at(time.Minute),
			Decision{Action: ActionStartNew, Reason: ReasonProcessEndedWithoutSession}},
		{"the session file exists without the uuid and nothing runs: resume, never --session-id",
			started, obsRecord(emptyFile), at(time.Minute),
			Decision{Action: ActionStartResume, Reason: ReasonRecordWithoutFirstPrompt}},
		{"session file exists, uuid already marked, first prompt missing: resume",
			seen, obsRecord(fileWithID), at(time.Minute),
			Decision{Action: ActionStartResume, Reason: ReasonRecordWithoutFirstPrompt}},
		{"session file exists but is empty: resume, because --session-id refuses an existing file",
			intent, obsRecord(emptyFile), at(time.Minute),
			Decision{Action: ActionStartResume, Reason: ReasonRecordWithoutFirstPrompt}},
		{"session file exists, process never recorded, uuid already marked: resume",
			seenNoProc, obsRecord(fileWithID), at(time.Minute),
			Decision{Action: ActionStartResume, Reason: ReasonRecordWithoutFirstPrompt}},

		// Bounded retries.
		{"one start left: still start",
			func() State { s := started; s.Starts = p.MaxStarts - 1; return s }(), obsRecord(noFile), at(time.Minute),
			Decision{Action: ActionStartNew, Reason: ReasonProcessEndedWithoutSession}},
		{"start limit reached without a session file: fail instead of looping",
			func() State { s := started; s.Starts = p.MaxStarts; return s }(), obsRecord(noFile), at(time.Minute),
			Decision{Action: ActionFail, Reason: ReasonStartsExhausted}},
		{"start limit reached with a session file lacking the prompt: fail instead of resuming again",
			func() State { s := seen; s.Starts = p.MaxStarts; return s }(), obsRecord(fileWithID), at(time.Minute),
			Decision{Action: ActionFail, Reason: ReasonStartsExhausted}},

		// Never two processes: the recorded process.
		{"the recorded process is alive with the recorded start time: wait, start nothing",
			started, withRecorded(obsRecord(noFile), alive(10*time.Second)), at(30 * time.Second),
			Decision{Action: ActionWait, Reason: ReasonTurnRunning}},
		{"the recorded process is alive and the file exists without the prompt: wait, never resume",
			seen, withRecorded(obsRecord(fileWithID), alive(10*time.Second)), at(30 * time.Second),
			Decision{Action: ActionWait, Reason: ReasonTurnRunning}},
		{"start times are compared in whole seconds",
			started, withRecorded(obsRecord(noFile), Lookup{Alive: true, StartTime: at(10*time.Second + 400*time.Millisecond)}), at(30 * time.Second),
			Decision{Action: ActionWait, Reason: ReasonTurnRunning}},
		{"a registry entry's start time is compared in whole seconds too",
			intent, withRegistry(obsRecord(noFile), entry(200, 5*time.Second, Lookup{Alive: true, StartTime: at(5*time.Second + 300*time.Millisecond)})), at(10 * time.Second),
			Decision{Action: ActionRecordProcess, Reason: ReasonProcessFoundInRegistry, Process: proc(200, 5*time.Second)}},
		{"the recorded PID is alive with another start time (PID reuse): not running, start again",
			started, withRecorded(obsRecord(noFile), alive(500*time.Second)), at(10 * time.Minute),
			Decision{Action: ActionStartNew, Reason: ReasonProcessEndedWithoutSession}},
		{"PID reuse with the session file present: resume, not wait",
			seen, withRecorded(obsRecord(fileWithID), alive(500*time.Second)), at(10 * time.Minute),
			Decision{Action: ActionStartResume, Reason: ReasonRecordWithoutFirstPrompt}},
		{"the recorded PID is not held by any process: not running",
			started, withRecorded(obsRecord(noFile), Lookup{}), at(time.Minute),
			Decision{Action: ActionStartNew, Reason: ReasonProcessEndedWithoutSession}},

		// Started but the PID was not recorded.
		{"a process with the uuid runs and no PID is recorded: record it",
			intent, withRunning(obsRecord(noFile), proc(200, 5*time.Second)), at(10 * time.Second),
			Decision{Action: ActionRecordProcess, Reason: ReasonProcessFoundByCommandLine, Process: proc(200, 5*time.Second)}},
		{"an unrecorded process is recorded before readiness is taken from the file",
			intent, withRunning(obsRecord(fileWithBoth), proc(200, 5*time.Second)), at(10 * time.Second),
			Decision{Action: ActionRecordProcess, Reason: ReasonProcessFoundByCommandLine, Process: proc(200, 5*time.Second)}},
		{"a running process that is not the recorded one replaces it (a retry crashed before recording)",
			started, withRunning(obsRecord(fileWithID), proc(300, 60*time.Second)), at(70 * time.Second),
			Decision{Action: ActionRecordProcess, Reason: ReasonProcessFoundByCommandLine, Process: proc(300, 60*time.Second)}},
		{"the recorded process found again by command line is recorded already: wait",
			started, withRecorded(withRunning(obsRecord(noFile), proc(100, 10*time.Second)), alive(10*time.Second)), at(20 * time.Second),
			Decision{Action: ActionWait, Reason: ReasonTurnRunning}},
		{"one process reported by command line, registry and recorded lookup counts once",
			started, withRecorded(withRegistry(withRunning(obsRecord(noFile), proc(100, 10*time.Second)),
				entry(100, 10*time.Second, alive(10*time.Second))), alive(10*time.Second)), at(20 * time.Second),
			Decision{Action: ActionWait, Reason: ReasonTurnRunning}},

		// The registry is auxiliary and always confirmed.
		{"a registry entry confirmed by PID and start time counts as a running process",
			intent, withRegistry(obsRecord(noFile), entry(200, 5*time.Second, alive(5*time.Second))), at(10 * time.Second),
			Decision{Action: ActionRecordProcess, Reason: ReasonProcessFoundInRegistry, Process: proc(200, 5*time.Second)}},
		{"a registry entry whose process is dead is stale (left behind by SIGKILL): ignored",
			intent, withRegistry(obsRecord(noFile), entry(200, 5*time.Second, Lookup{})), at(10 * time.Second),
			Decision{Action: ActionStartNew, Reason: ReasonNotStarted}},
		{"a registry entry whose PID is held by another start time (PID reuse): ignored",
			intent, withRegistry(obsRecord(noFile), entry(200, 5*time.Second, alive(900*time.Second))), at(20 * time.Minute),
			Decision{Action: ActionStartNew, Reason: ReasonNotStarted}},
		{"a registry entry of another session is ignored",
			intent, withRegistry(obsRecord(noFile), RegistryEntry{SessionID: otherSession, PID: 200, StartTime: at(5 * time.Second), Current: alive(5 * time.Second)}), at(10 * time.Second),
			Decision{Action: ActionStartNew, Reason: ReasonNotStarted}},
		{"a process found by command line is reported as such even when the registry names it too",
			intent, withRegistry(withRunning(obsRecord(noFile), proc(200, 5*time.Second)),
				entry(200, 5*time.Second, alive(5*time.Second))), at(10 * time.Second),
			Decision{Action: ActionRecordProcess, Reason: ReasonProcessFoundByCommandLine, Process: proc(200, 5*time.Second)}},

		// Several processes.
		{"two different processes carry the uuid: start nothing and wait",
			started, withRunning(obsRecord(noFile), proc(100, 10*time.Second), proc(300, 12*time.Second)), at(20 * time.Second),
			Decision{Action: ActionWait, Reason: ReasonMultipleProcesses}},

		// Readiness.
		{"the first prompt is in the record: ready, from the intent stage",
			intent, obsRecord(fileWithBoth), at(time.Minute),
			Decision{Action: ActionMarkReady, Reason: ReasonFirstPromptRecorded}},
		{"the first prompt is in the record: ready, from the started stage",
			started, obsRecord(fileWithBoth), at(time.Minute),
			Decision{Action: ActionMarkReady, Reason: ReasonFirstPromptRecorded}},
		{"the first prompt is in the record: ready, from the uuid-seen stage",
			seen, obsRecord(fileWithBoth), at(time.Minute),
			Decision{Action: ActionMarkReady, Reason: ReasonFirstPromptRecorded}},
		{"ready does not wait for the turn to end once the prompt is recorded",
			started, withRecorded(obsRecord(fileWithBoth), alive(10*time.Second)), at(20 * time.Second),
			Decision{Action: ActionMarkReady, Reason: ReasonFirstPromptRecorded}},
		{"the uuid alone in the record is not ready: mark it seen",
			started, obsRecord(fileWithID), at(time.Minute),
			Decision{Action: ActionMarkUUIDSeen, Reason: ReasonSessionRecordHasID}},
		{"the uuid is marked seen while the turn still runs",
			started, withRecorded(obsRecord(fileWithID), alive(10*time.Second)), at(20 * time.Second),
			Decision{Action: ActionMarkUUIDSeen, Reason: ReasonSessionRecordHasID}},
		{"a ready attempt has nothing left to do, whatever is observed",
			ready, obsRecord(noFile), at(time.Hour),
			Decision{Action: ActionDone, Reason: ReasonAlreadyReady}},

		// Timeout.
		{"a turn running exactly StartTimeout without the prompt is not yet failed",
			started, withRecorded(obsRecord(noFile), alive(10*time.Second)), at(10*time.Second + p.StartTimeout),
			Decision{Action: ActionWait, Reason: ReasonTurnRunning}},
		{"a turn running longer than StartTimeout without the prompt fails",
			started, withRecorded(obsRecord(noFile), alive(10*time.Second)), at(10*time.Second + p.StartTimeout + time.Nanosecond),
			Decision{Action: ActionFail, Reason: ReasonStartTimedOut}},
		{"a turn past StartTimeout with the uuid in the file but not the prompt fails",
			seen, withRecorded(obsRecord(fileWithID), alive(10*time.Second)), at(10*time.Second + p.StartTimeout + time.Second),
			Decision{Action: ActionFail, Reason: ReasonStartTimedOut}},
		{"the timeout counts from the process's own start, not from when it was recorded",
			intent, withRunning(obsRecord(noFile), proc(200, 5*time.Second)), at(5*time.Second + p.StartTimeout + time.Hour),
			Decision{Action: ActionRecordProcess, Reason: ReasonProcessFoundByCommandLine, Process: proc(200, 5*time.Second)}},
		{"a turn past StartTimeout whose prompt is in the record is ready, not failed",
			started, withRecorded(obsRecord(fileWithBoth), alive(10*time.Second)), at(time.Hour),
			Decision{Action: ActionMarkReady, Reason: ReasonFirstPromptRecorded}},
		{"a process start time in the future (clock skew) does not time out",
			started, withRecorded(obsRecord(noFile), alive(10*time.Second)), at(0),
			Decision{Action: ActionWait, Reason: ReasonTurnRunning}},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			got, err := Decide(r.s, r.o, p, r.now)
			if err != nil {
				t.Fatal(err)
			}
			if got.Action != r.want.Action || got.Reason != r.want.Reason || !got.Process.Same(r.want.Process) {
				t.Fatalf("got %v/%v process %+v, want %v/%v process %+v",
					got.Action, got.Reason, got.Process, r.want.Action, r.want.Reason, r.want.Process)
			}
		})
	}
}

// A decision never starts a second process or reuses --session-id, whatever
// the observations say: every start decision requires no running process and
// StartNew additionally requires that the session file does not exist.
func TestDecideStartGuards(t *testing.T) {
	for _, stage := range []Stage{StageIntent, StageStarted, StageUUIDSeen} {
		for _, withProc := range []bool{false, true} {
			if stage == StageStarted && !withProc {
				continue
			}
			s := stateAt(t, stage, withProc)
			for _, rec := range []SessionRecord{noFile, emptyFile, fileWithID, fileWithBoth} {
				for _, nProc := range []int{0, 1, 2} {
					o := obsRecord(rec)
					for i := 0; i < nProc; i++ {
						o.Running = append(o.Running, proc(100+i*100, 10*time.Second))
					}
					d, err := Decide(s, o, DefaultParams(), at(time.Minute))
					if err != nil {
						t.Fatal(err)
					}
					switch d.Action {
					case ActionStartNew:
						if rec.Exists || nProc > 0 {
							t.Errorf("stage %d proc %v record %+v procs %d: StartNew with the file present or a process running", stage, withProc, rec, nProc)
						}
					case ActionStartResume:
						if !rec.Exists || nProc > 0 {
							t.Errorf("stage %d proc %v record %+v procs %d: StartResume without the file or with a process running", stage, withProc, rec, nProc)
						}
					}
				}
			}
		}
	}
}

func TestDecideRefusesInvalidInput(t *testing.T) {
	good := stateAt(t, StageStarted, true)
	okObs := obsRecord(noFile)
	p := DefaultParams()
	rows := []struct {
		rule string
		s    State
		o    Observations
		p    Params
		now  time.Time
	}{
		{"zero state", State{}, okObs, p, at(0)},
		{"zero session id", func() State { s := good; s.SessionID = ""; return s }(), okObs, p, at(0)},
		{"session id in upper case", func() State { s := good; s.SessionID = "8612A8ED-5C4A-4920-BF66-37CDAFAFFDA2"; return s }(), okObs, p, at(0)},
		{"session id without dashes", func() State { s := good; s.SessionID = "8612a8ed5c4a492--bf66-37cdafaffda2"; return s }(), okObs, p, at(0)},
		{"zero attempt id", func() State { s := good; s.AttemptID = ""; return s }(), okObs, p, at(0)},
		{"zero project directory", func() State { s := good; s.ProjectDir = ""; return s }(), okObs, p, at(0)},
		{"zero stage", func() State { s := good; s.Stage = 0; return s }(), okObs, p, at(0)},
		{"stage beyond ready", func() State { s := good; s.Stage = StageReady + 1; return s }(), okObs, p, at(0)},
		{"started stage without a recorded process", func() State { s := good; s.Process = Process{}; s.Starts = 0; return s }(), okObs, p, at(0)},
		{"recorded process with zero PID", func() State { s := good; s.Process.PID = 0; return s }(), okObs, p, at(0)},
		{"recorded process with zero start time", func() State { s := good; s.Process.StartTime = time.Time{}; return s }(), okObs, p, at(0)},
		{"recorded process but zero starts", func() State { s := good; s.Starts = 0; return s }(), okObs, p, at(0)},
		{"starts counted without a recorded process", func() State { s := stateAt(t, StageIntent, false); s.Starts = 1; return s }(), okObs, p, at(0)},
		{"negative starts", func() State { s := good; s.Starts = -1; return s }(), okObs, p, at(0)},
		{"session record looked up in another directory", good, Observations{Record: SessionRecord{Dir: "/srv/projects/beta"}}, p, at(0)},
		{"session record without a directory", good, Observations{}, p, at(0)},
		{"uuid in a file that does not exist", good, Observations{Record: SessionRecord{Dir: testDir, HasSessionID: true}}, p, at(0)},
		{"first prompt in a file without the uuid", good, Observations{Record: SessionRecord{Dir: testDir, Exists: true, HasFirstPrompt: true}}, p, at(0)},
		{"running process without a PID", good, func() Observations { o := okObs; o.Running = []Process{{StartTime: at(1)}}; return o }(), p, at(0)},
		{"running process without a start time", good, func() Observations { o := okObs; o.Running = []Process{{PID: 5}}; return o }(), p, at(0)},
		{"alive lookup without a start time", good, func() Observations { o := okObs; o.Recorded = Lookup{Alive: true}; return o }(), p, at(0)},
		{"registry entry without a session id", good, func() Observations {
			o := okObs
			o.Registry = []RegistryEntry{{PID: 5, StartTime: at(1)}}
			return o
		}(), p, at(0)},
		{"registry entry without a start time", good, func() Observations {
			o := okObs
			o.Registry = []RegistryEntry{{SessionID: testSession, PID: 5}}
			return o
		}(), p, at(0)},
		{"one PID with two start times in one snapshot", good, func() Observations {
			o := okObs
			o.Running = []Process{proc(7, 1*time.Second), proc(7, 9*time.Second)}
			return o
		}(), p, at(0)},
		{"zero now", good, okObs, p, time.Time{}},
		{"zero StartTimeout", good, okObs, Params{MaxStarts: 3}, at(0)},
		{"zero MaxStarts", good, okObs, Params{StartTimeout: time.Minute}, at(0)},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			_, err := Decide(r.s, r.o, r.p, r.now)
			if !errors.Is(err, ErrInvalidInput) && !errors.Is(err, ErrInvalidParams) {
				t.Fatalf("err = %v, want ErrInvalidInput or ErrInvalidParams", err)
			}
		})
	}
}

func TestSessionIDValid(t *testing.T) {
	rows := []struct {
		rule string
		id   SessionID
		want bool
	}{
		{"canonical lower-case uuid", testSession, true},
		{"empty", "", false},
		{"upper case", "8612A8ED-5C4A-4920-BF66-37CDAFAFFDA2", false},
		{"too short", "8612a8ed-5c4a-4920-bf66-37cdafaffda", false},
		{"too long", "8612a8ed-5c4a-4920-bf66-37cdafaffda22", false},
		{"dash in the wrong place", "8612a8ed5-c4a-4920-bf66-37cdafaffda2", false},
		{"non-hex digit", "8612a8ed-5c4a-4920-bf66-37cdafaffdag", false},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if got := r.id.Valid(); got != r.want {
				t.Fatalf("Valid(%q) = %v, want %v", r.id, got, r.want)
			}
		})
	}
}

func TestDefaultParams(t *testing.T) {
	p := DefaultParams()
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if p.StartTimeout != 2*time.Minute || p.MaxStarts != 3 {
		t.Fatalf("defaults = %+v", p)
	}
}

func TestActionAndReasonNames(t *testing.T) {
	if ActionStartResume.String() != "StartResume" || ReasonStartsExhausted.String() != "StartsExhausted" {
		t.Fatal("names are wrong")
	}
	if Action(99).String() != "Action(99)" || Reason(99).String() != "Reason(99)" {
		t.Fatal("unknown values are not marked")
	}
	for a := ActionStartNew; a <= ActionFail; a++ {
		if _, ok := actionNames[a]; !ok {
			t.Errorf("action %d has no name", a)
		}
	}
	for r := ReasonNotStarted; r <= ReasonStartsExhausted; r++ {
		if _, ok := reasonNames[r]; !ok {
			t.Errorf("reason %d has no name", r)
		}
	}
}
