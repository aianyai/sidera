package create

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestDecide(t *testing.T) {
	p := DefaultParams()
	timeout := int64(p.StartTimeout / time.Second)
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
		return RegistryEntry{SessionID: testSession, PID: pid, StartUnix: unix(started), Current: cur}
	}
	withStarts := func(s State, n int) State { s.Starts = n; return s }
	wait := func(startedAt time.Duration) Decision {
		return Decision{Action: ActionWait, Reason: ReasonTurnRunning, RecheckBy: unix(startedAt) + timeout}
	}
	record := func(pid int, startedAt time.Duration) Decision {
		return Decision{Action: ActionRecordProcess, Reason: ReasonProcessFoundByCommandLine, Process: proc(pid, startedAt)}
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
			withStarts(started, p.MaxStarts-1), obsRecord(noFile), at(time.Minute),
			Decision{Action: ActionStartNew, Reason: ReasonProcessEndedWithoutSession}},
		{"start limit reached without a session file: fail instead of looping",
			withStarts(started, p.MaxStarts), obsRecord(noFile), at(time.Minute),
			Decision{Action: ActionFail, Reason: ReasonStartsExhausted}},
		{"start limit reached with a session file lacking the prompt: fail instead of resuming again",
			withStarts(seen, p.MaxStarts), obsRecord(fileWithID), at(time.Minute),
			Decision{Action: ActionFail, Reason: ReasonStartsExhausted}},

		// Never two processes: the recorded process.
		{"the recorded process is alive with the recorded start time: wait, start nothing",
			started, withRecorded(obsRecord(noFile), alive(10*time.Second)), at(30 * time.Second),
			wait(10 * time.Second)},
		{"the recorded process is alive and the file exists without the prompt: wait, never resume",
			seen, withRecorded(obsRecord(fileWithID), alive(10*time.Second)), at(30 * time.Second),
			wait(10 * time.Second)},
		{"the recorded PID is alive with another start time (PID reuse): not running, start again",
			started, withRecorded(obsRecord(noFile), alive(500*time.Second)), at(10 * time.Minute),
			Decision{Action: ActionStartNew, Reason: ReasonProcessEndedWithoutSession}},
		{"PID reuse with the session file present: resume, not wait",
			seen, withRecorded(obsRecord(fileWithID), alive(500*time.Second)), at(10 * time.Minute),
			Decision{Action: ActionStartResume, Reason: ReasonRecordWithoutFirstPrompt}},
		{"the recorded PID is not held by any process: not running",
			started, withRecorded(obsRecord(noFile), gone(recordedPID)), at(time.Minute),
			Decision{Action: ActionStartNew, Reason: ReasonProcessEndedWithoutSession}},

		// Started but the PID was not recorded.
		{"a process with the uuid runs and no PID is recorded: record it",
			intent, withRunning(obsRecord(noFile), proc(200, 5*time.Second)), at(10 * time.Second),
			record(200, 5*time.Second)},
		{"an unrecorded process is recorded before readiness is taken from the file",
			intent, withRunning(obsRecord(fileWithBoth), proc(200, 5*time.Second)), at(10 * time.Second),
			record(200, 5*time.Second)},
		{"a running process that is not the recorded one replaces it (a retry crashed before recording)",
			started, withRecorded(withRunning(obsRecord(fileWithID), proc(300, 60*time.Second)), gone(recordedPID)), at(70 * time.Second),
			record(300, 60*time.Second)},
		{"the recorded process found again by command line is recorded already: wait",
			started, withRecorded(withRunning(obsRecord(noFile), proc(recordedPID, 10*time.Second)), alive(10*time.Second)), at(20 * time.Second),
			wait(10 * time.Second)},
		{"one process reported by command line, registry and recorded lookup counts once",
			started, withRecorded(withRegistry(withRunning(obsRecord(noFile), proc(recordedPID, 10*time.Second)),
				entry(recordedPID, 10*time.Second, alive(10*time.Second))), alive(10*time.Second)), at(20 * time.Second),
			wait(10 * time.Second)},

		// The registry is auxiliary and always confirmed.
		{"a registry entry of the recorded process, confirmed, is the recorded process: wait",
			started, withRecorded(withRegistry(obsRecord(noFile), entry(recordedPID, 10*time.Second, alive(10*time.Second))), alive(10*time.Second)), at(20 * time.Second),
			wait(10 * time.Second)},
		{"a registry entry whose process is dead is stale (left behind by SIGKILL): ignored",
			intent, withRegistry(obsRecord(noFile), entry(200, 5*time.Second, gone(200))), at(10 * time.Second),
			Decision{Action: ActionStartNew, Reason: ReasonNotStarted}},
		{"a registry entry whose PID is held by another start time (PID reuse): ignored",
			intent, withRegistry(obsRecord(noFile), entry(200, 5*time.Second, aliveAs(200, 900*time.Second))), at(20 * time.Minute),
			Decision{Action: ActionStartNew, Reason: ReasonNotStarted}},
		{"a registry entry of another session is ignored",
			intent, withRegistry(obsRecord(noFile), RegistryEntry{SessionID: otherSession, PID: 200, StartUnix: unix(5 * time.Second), Current: aliveAs(200, 5*time.Second)}), at(10 * time.Second),
			Decision{Action: ActionStartNew, Reason: ReasonNotStarted}},
		{"a process listed by command line and named by the registry too counts once",
			intent, withRegistry(withRunning(obsRecord(noFile), proc(200, 5*time.Second)),
				entry(200, 5*time.Second, aliveAs(200, 5*time.Second))), at(10 * time.Second),
			record(200, 5*time.Second)},

		// An outside writer: a live process works on the session without being ours.
		{"a registry entry confirmed by the table, not listed by command line and not recorded: an outside writer, never recorded as ours",
			intent, withRegistry(obsRecord(noFile), entry(200, 5*time.Second, aliveAs(200, 5*time.Second))), at(10 * time.Second),
			Decision{Action: ActionControlUnknown, Reason: ReasonOutsideWriter, Processes: []Process{proc(200, 5*time.Second)}}},
		{"an outside writer beside the recorded process: control unknown, both reported",
			started, withRecorded(withRegistry(obsRecord(noFile), entry(200, 12*time.Second, aliveAs(200, 12*time.Second))), alive(10*time.Second)), at(20 * time.Second),
			Decision{Action: ActionControlUnknown, Reason: ReasonOutsideWriter, Processes: []Process{proc(recordedPID, 10*time.Second), proc(200, 12*time.Second)}}},
		{"an outside writer with the first prompt on disk: control unknown comes before readiness",
			seen, withRecorded(withRegistry(obsRecord(fileWithBoth), entry(200, 12*time.Second, aliveAs(200, 12*time.Second))), gone(recordedPID)), at(20 * time.Second),
			Decision{Action: ActionControlUnknown, Reason: ReasonOutsideWriter, Processes: []Process{proc(200, 12*time.Second)}}},
		{"an outside writer when the file lacks the first prompt: nothing is resumed",
			seen, withRecorded(withRegistry(obsRecord(fileWithID), entry(200, 12*time.Second, aliveAs(200, 12*time.Second))), gone(recordedPID)), at(20 * time.Second),
			Decision{Action: ActionControlUnknown, Reason: ReasonOutsideWriter, Processes: []Process{proc(200, 12*time.Second)}}},

		// Several processes.
		{"two different processes carry the uuid: control unknown, nothing is started",
			started, withRecorded(withRunning(obsRecord(noFile), proc(300, 12*time.Second), proc(recordedPID, 10*time.Second)), alive(10*time.Second)), at(20 * time.Second),
			Decision{Action: ActionControlUnknown, Reason: ReasonMultipleProcesses, Processes: []Process{proc(recordedPID, 10*time.Second), proc(300, 12*time.Second)}}},
		{"two processes carry the uuid and the first prompt is on disk: control unknown comes before readiness",
			started, withRecorded(withRunning(obsRecord(fileWithBoth), proc(recordedPID, 10*time.Second), proc(300, 12*time.Second)), alive(10*time.Second)), at(20 * time.Second),
			Decision{Action: ActionControlUnknown, Reason: ReasonMultipleProcesses, Processes: []Process{proc(recordedPID, 10*time.Second), proc(300, 12*time.Second)}}},
		{"two unrecorded processes carry the uuid: control unknown, neither is recorded",
			intent, withRunning(obsRecord(noFile), proc(200, 5*time.Second), proc(300, 6*time.Second)), at(10 * time.Second),
			Decision{Action: ActionControlUnknown, Reason: ReasonMultipleProcesses, Processes: []Process{proc(200, 5*time.Second), proc(300, 6*time.Second)}}},
		{"the recorded process is gone and two others carry the uuid: control unknown",
			started, withRecorded(withRunning(obsRecord(fileWithID), proc(300, 60*time.Second), proc(200, 50*time.Second)), gone(recordedPID)), at(70 * time.Second),
			Decision{Action: ActionControlUnknown, Reason: ReasonMultipleProcesses, Processes: []Process{proc(200, 50*time.Second), proc(300, 60*time.Second)}}},

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
			wait(10 * time.Second)},
		{"a turn running longer than StartTimeout without the prompt fails",
			started, withRecorded(obsRecord(noFile), alive(10*time.Second)), at(10*time.Second + p.StartTimeout + time.Nanosecond),
			Decision{Action: ActionFail, Reason: ReasonStartTimedOut}},
		{"a turn past StartTimeout with the uuid in the file but not the prompt fails",
			seen, withRecorded(obsRecord(fileWithID), alive(10*time.Second)), at(10*time.Second + p.StartTimeout + time.Second),
			Decision{Action: ActionFail, Reason: ReasonStartTimedOut}},
		{"the timeout counts from the process's own start, not from when it was recorded",
			intent, withRunning(obsRecord(noFile), proc(200, 5*time.Second)), at(5*time.Second + p.StartTimeout + time.Hour),
			record(200, 5*time.Second)},
		{"a turn past StartTimeout whose prompt is in the record is ready, not failed",
			started, withRecorded(obsRecord(fileWithBoth), alive(10*time.Second)), at(time.Hour),
			Decision{Action: ActionMarkReady, Reason: ReasonFirstPromptRecorded}},
		{"a process start time in the future (clock skew) does not time out",
			started, withRecorded(obsRecord(noFile), alive(10*time.Second)), at(0),
			wait(10 * time.Second)},

		// A failed attempt stays failed.
		{"a failed attempt decides done with the stored reason",
			failedFrom(t, started, ReasonStartTimedOut), withRecorded(obsRecord(noFile), gone(recordedPID)), at(time.Hour),
			Decision{Action: ActionDone, Reason: ReasonAlreadyFailed, FailReason: ReasonStartTimedOut}},
		{"a failed attempt is never ready, though the timed-out process wrote the first prompt later",
			failedFrom(t, started, ReasonStartTimedOut), withRecorded(obsRecord(fileWithBoth), alive(10*time.Second)), at(time.Hour),
			Decision{Action: ActionDone, Reason: ReasonAlreadyFailed, FailReason: ReasonStartTimedOut}},
		{"a failed attempt never starts a process, though nothing runs and the file lacks the prompt",
			failedFrom(t, seen, ReasonStartsExhausted), withRecorded(obsRecord(fileWithID), gone(recordedPID)), at(time.Hour),
			Decision{Action: ActionDone, Reason: ReasonAlreadyFailed, FailReason: ReasonStartsExhausted}},
		{"a failed attempt never records a process it finds",
			failedFrom(t, intent, ReasonStartTimedOut), withRunning(obsRecord(noFile), proc(200, 5*time.Second)), at(time.Hour),
			Decision{Action: ActionDone, Reason: ReasonAlreadyFailed, FailReason: ReasonStartTimedOut}},
		{"a failed attempt keeps a control-unknown failure when more processes appear",
			failedFrom(t, started, ReasonMultipleProcesses), withRecorded(withRunning(obsRecord(noFile), proc(recordedPID, 10*time.Second), proc(300, 12*time.Second)), alive(10*time.Second)), at(time.Hour),
			Decision{Action: ActionDone, Reason: ReasonAlreadyFailed, FailReason: ReasonMultipleProcesses}},
		{"a failed attempt keeps an outside-writer failure when the outside writer is gone",
			failedFrom(t, intent, ReasonOutsideWriter), obsRecord(noFile), at(time.Hour),
			Decision{Action: ActionDone, Reason: ReasonAlreadyFailed, FailReason: ReasonOutsideWriter}},
		{"a failed attempt does not judge the record: the file it saw may be gone",
			failedFrom(t, seenNoProc, ReasonStartsExhausted), obsRecord(noFile), at(time.Hour),
			Decision{Action: ActionDone, Reason: ReasonAlreadyFailed, FailReason: ReasonStartsExhausted}},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			o := r.o
			// A row that does not say anything about the lookup of the recorded
			// PID means that nobody holds it.
			if !r.s.Process.IsZero() && o.Recorded == (Lookup{}) {
				o.Recorded = gone(r.s.Process.PID)
			}
			got, err := Decide(r.s, o, p, r.now)
			if err != nil {
				t.Fatal(err)
			}
			// Exact comparison: PIDs and start seconds equal field by field.
			if !reflect.DeepEqual(got, r.want) {
				t.Fatalf("got %+v, want %+v", got, r.want)
			}
		})
	}
}

// A decision never starts a second process or reuses --session-id, whatever
// the observations say: every start decision requires no running process and
// StartNew additionally requires that the session file does not exist. Two
// processes end the attempt without a start, and a failed attempt never starts
// anything or becomes ready.
func TestDecideStartGuards(t *testing.T) {
	var states []State
	for _, stage := range []Stage{StageIntent, StageStarted, StageUUIDSeen} {
		for _, withProc := range []bool{false, true} {
			if stage == StageStarted && !withProc {
				continue
			}
			s := stateAt(t, stage, withProc)
			states = append(states, s)
			for _, r := range failReasons {
				states = append(states, failedFrom(t, s, r))
			}
		}
	}
	registryOnly := func(pid int) RegistryEntry {
		return RegistryEntry{SessionID: testSession, PID: pid, StartUnix: unix(30 * time.Second), Current: aliveAs(pid, 30*time.Second)}
	}
	for _, s := range states {
		for _, rec := range []SessionRecord{noFile, emptyFile, fileWithID, fileWithBoth} {
			if s.Stage == StageUUIDSeen && !rec.Exists {
				continue // contradicts the state
			}
			for _, nProc := range []int{0, 1, 2} {
				for _, outsider := range []bool{false, true} {
					o := obsRecord(rec)
					if !s.Process.IsZero() {
						o.Recorded = gone(s.Process.PID)
					}
					for i := 0; i < nProc; i++ {
						o.Running = append(o.Running, proc(500+i*100, 10*time.Second))
					}
					if outsider {
						o.Registry = []RegistryEntry{registryOnly(900)}
					}
					d, err := Decide(s, o, DefaultParams(), at(time.Minute))
					if err != nil {
						t.Fatal(err)
					}
					desc := func() string {
						return fmt.Sprintf("stage %d fail %v proc %v record %+v procs %d outsider %v", s.Stage, s.FailReason, !s.Process.IsZero(), rec, nProc, outsider)
					}
					running := nProc > 0 || outsider
					switch d.Action {
					case ActionStartNew:
						if rec.Exists || running || s.Stage == StageFailed {
							t.Errorf("%s: StartNew with the file present, a process running or a failed attempt", desc())
						}
					case ActionStartResume:
						if !rec.Exists || running || s.Stage == StageFailed {
							t.Errorf("%s: StartResume without the file, with a process running or a failed attempt", desc())
						}
					case ActionMarkReady, ActionRecordProcess, ActionMarkUUIDSeen, ActionWait, ActionFail:
						if s.Stage == StageFailed {
							t.Errorf("%s: %v for a failed attempt", desc(), d.Action)
						}
					}
					if (outsider || nProc > 1) && s.Stage != StageFailed && d.Action != ActionControlUnknown {
						t.Errorf("%s: more than one process, decision %v", desc(), d.Action)
					}
					if s.Stage == StageFailed && (d.Action != ActionDone || d.FailReason != s.FailReason) {
						t.Errorf("%s: failed attempt decided %+v", desc(), d)
					}
				}
			}
		}
	}
}

func TestDecideRefusesInvalidInput(t *testing.T) {
	good := stateAt(t, StageStarted, true)
	okObs := obsRecord(noFile)
	okObs.Recorded = gone(recordedPID)
	p := DefaultParams()
	mutState := func(s State, f func(*State)) State { f(&s); return s }
	mutObs := func(o Observations, f func(*Observations)) Observations { f(&o); return o }
	seenWithProc := stateAt(t, StageUUIDSeen, true)
	rows := []struct {
		rule string
		s    State
		o    Observations
		p    Params
		now  time.Time
	}{
		{"zero state", State{}, okObs, p, at(0)},
		{"zero session id", mutState(good, func(s *State) { s.SessionID = "" }), okObs, p, at(0)},
		{"session id in upper case", mutState(good, func(s *State) { s.SessionID = "8612A8ED-5C4A-4920-BF66-37CDAFAFFDA2" }), okObs, p, at(0)},
		{"session id without dashes", mutState(good, func(s *State) { s.SessionID = "8612a8ed5c4a492--bf66-37cdafaffda2" }), okObs, p, at(0)},
		{"zero first prompt id", mutState(good, func(s *State) { s.FirstPromptID = "" }), okObs, p, at(0)},
		{"first prompt id in upper case", mutState(good, func(s *State) { s.FirstPromptID = "F47AC10B-58CC-4372-A567-0E02B2C3D479" }), okObs, p, at(0)},
		{"zero attempt id", mutState(good, func(s *State) { s.AttemptID = "" }), okObs, p, at(0)},
		{"zero project directory", mutState(good, func(s *State) { s.ProjectDir = "" }), okObs, p, at(0)},
		{"zero stage", mutState(good, func(s *State) { s.Stage = 0 }), okObs, p, at(0)},
		{"stage beyond failed", mutState(good, func(s *State) { s.Stage = StageFailed + 1 }), okObs, p, at(0)},
		{"started stage without a recorded process", mutState(good, func(s *State) { s.Process = Process{}; s.Starts = 0 }), okObs, p, at(0)},
		{"intent stage with a recorded process", mutState(stateAt(t, StageIntent, false), func(s *State) { s.Process = proc(100, time.Second); s.Starts = 1 }), okObs, p, at(0)},
		{"recorded process with zero PID", mutState(good, func(s *State) { s.Process.PID = 0 }), okObs, p, at(0)},
		{"recorded process with zero start time", mutState(good, func(s *State) { s.Process.StartUnix = 0 }), okObs, p, at(0)},
		{"recorded process but zero starts", mutState(good, func(s *State) { s.Starts = 0 }), okObs, p, at(0)},
		{"starts counted without a recorded process", mutState(stateAt(t, StageIntent, false), func(s *State) { s.Starts = 1 }), okObs, p, at(0)},
		{"negative starts", mutState(good, func(s *State) { s.Starts = -1 }), okObs, p, at(0)},
		{"failed stage without a reason", mutState(good, func(s *State) { s.Stage = StageFailed }), okObs, p, at(0)},
		{"failure reason on a stage that has not failed", mutState(good, func(s *State) { s.FailReason = ReasonStartTimedOut }), okObs, p, at(0)},
		{"failed stage with a reason that does not fail an attempt", mutState(good, func(s *State) { s.Stage = StageFailed; s.FailReason = ReasonTurnRunning }), okObs, p, at(0)},
		{"session record looked up in another directory", good, Observations{Recorded: gone(recordedPID), Record: SessionRecord{Dir: "/srv/projects/beta"}}, p, at(0)},
		{"session record without a directory", good, Observations{Recorded: gone(recordedPID)}, p, at(0)},
		{"uuid in a file that does not exist", good, Observations{Recorded: gone(recordedPID), Record: SessionRecord{Dir: testDir, HasSessionID: true}}, p, at(0)},
		{"first prompt in a file without the uuid", good, Observations{Recorded: gone(recordedPID), Record: SessionRecord{Dir: testDir, Exists: true, HasFirstPrompt: true}}, p, at(0)},
		{"uuid seen earlier but no session file now", seenWithProc, mutObs(okObs, func(o *Observations) { o.Record = SessionRecord{Dir: testDir} }), p, at(0)},
		{"running process without a PID", good, mutObs(okObs, func(o *Observations) { o.Running = []Process{{StartUnix: unix(1)}} }), p, at(0)},
		{"running process without a start time", good, mutObs(okObs, func(o *Observations) { o.Running = []Process{{PID: 5}} }), p, at(0)},
		{"zero lookup although the state records a process", good, mutObs(okObs, func(o *Observations) { o.Recorded = Lookup{} }), p, at(0)},
		{"lookup of another PID than the recorded one", good, mutObs(okObs, func(o *Observations) { o.Recorded = gone(recordedPID + 1) }), p, at(0)},
		{"alive lookup of another PID than the recorded one", good, mutObs(okObs, func(o *Observations) { o.Recorded = aliveAs(recordedPID+1, 10*time.Second) }), p, at(0)},
		{"alive lookup without a start time", good, mutObs(okObs, func(o *Observations) { o.Recorded = Lookup{PID: recordedPID, Alive: true} }), p, at(0)},
		{"a lookup although the state records no process (alive)", stateAt(t, StageIntent, false), mutObs(okObs, func(o *Observations) { o.Recorded = aliveAs(recordedPID, 10*time.Second) }), p, at(0)},
		{"a lookup although the state records no process (not alive)", stateAt(t, StageIntent, false), mutObs(okObs, func(o *Observations) { o.Recorded = gone(recordedPID) }), p, at(0)},
		{"registry entry without a session id", good, mutObs(okObs, func(o *Observations) {
			o.Registry = []RegistryEntry{{PID: 5, StartUnix: unix(1), Current: gone(5)}}
		}), p, at(0)},
		{"registry entry without a start time", good, mutObs(okObs, func(o *Observations) {
			o.Registry = []RegistryEntry{{SessionID: testSession, PID: 5, Current: gone(5)}}
		}), p, at(0)},
		{"registry entry whose lookup is for another PID", good, mutObs(okObs, func(o *Observations) {
			o.Registry = []RegistryEntry{{SessionID: testSession, PID: 5, StartUnix: unix(1), Current: gone(6)}}
		}), p, at(0)},
		{"one PID with two start times in one snapshot", good, mutObs(okObs, func(o *Observations) {
			o.Running = []Process{proc(7, 1*time.Second), proc(7, 9*time.Second)}
		}), p, at(0)},
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

func TestPromptIDValid(t *testing.T) {
	if !testPrompt.Valid() || PromptID("").Valid() || PromptID("F47AC10B-58CC-4372-A567-0E02B2C3D479").Valid() ||
		PromptID("f47ac10b-58cc-4372-a567-0e02b2c3d47").Valid() || PromptID("f47ac10b58cc-4372-a567-0e02b2c3d479-").Valid() {
		t.Fatal("PromptID.Valid does not follow the canonical lower-case form")
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
	for a := ActionStartNew; a <= ActionControlUnknown; a++ {
		if _, ok := actionNames[a]; !ok {
			t.Errorf("action %d has no name", a)
		}
	}
	for r := ReasonNotStarted; r <= ReasonAlreadyFailed; r++ {
		if _, ok := reasonNames[r]; !ok {
			t.Errorf("reason %d has no name", r)
		}
	}
}
