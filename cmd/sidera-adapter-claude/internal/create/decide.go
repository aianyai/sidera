package create

import (
	"fmt"
	"time"
)

// Action is the next step of the attempt.
type Action int

const (
	// ActionStartNew: start a turn process with --session-id <uuid> and give it
	// the first prompt. Chosen only when no process runs and the session file
	// does not exist.
	ActionStartNew Action = iota + 1
	// ActionStartResume: start a turn process with --resume <uuid> and give it
	// the first prompt. Chosen when the session file exists, because
	// --session-id fails on an existing session.
	ActionStartResume
	// ActionWait: a turn process of the session runs; start nothing and decide
	// again when it ends or on the next observation.
	ActionWait
	// ActionRecordProcess: a turn process was found whose PID was never
	// recorded. Commit Decision.Process with State.Started.
	ActionRecordProcess
	// ActionMarkUUIDSeen: commit State.UUIDSeen.
	ActionMarkUUIDSeen
	// ActionMarkReady: commit State.Ready together with the control reply.
	ActionMarkReady
	// ActionDone: the attempt is ready and its reply was committed; nothing to do.
	ActionDone
	// ActionFail: the attempt failed for good; report the reason in the
	// control reply. Whatever process is still running is not touched.
	ActionFail
)

var actionNames = map[Action]string{
	ActionStartNew: "StartNew", ActionStartResume: "StartResume", ActionWait: "Wait",
	ActionRecordProcess: "RecordProcess", ActionMarkUUIDSeen: "MarkUUIDSeen",
	ActionMarkReady: "MarkReady", ActionDone: "Done", ActionFail: "Fail",
}

func (a Action) String() string { return nameOf(actionNames, a, "Action") }

// Reason says why Decide chose its action.
type Reason int

const (
	// ReasonNotStarted: no turn process was ever recorded, none runs and the
	// session file does not exist.
	ReasonNotStarted Reason = iota + 1
	// ReasonProcessEndedWithoutSession: the recorded process is gone, none
	// runs and the session file does not exist, so nothing was created yet.
	ReasonProcessEndedWithoutSession
	// ReasonRecordWithoutFirstPrompt: the session file exists, no process runs
	// and the first prompt is not in it.
	ReasonRecordWithoutFirstPrompt
	// ReasonTurnRunning: the recorded turn process is alive.
	ReasonTurnRunning
	// ReasonMultipleProcesses: more than one distinct process carries the uuid.
	ReasonMultipleProcesses
	// ReasonProcessFoundByCommandLine: a process with the uuid on its command
	// line runs and is not the recorded one.
	ReasonProcessFoundByCommandLine
	// ReasonProcessFoundInRegistry: only the runtime's registry names a
	// process for this session, and the process table confirms it.
	ReasonProcessFoundInRegistry
	// ReasonSessionRecordHasID: the session file contains the uuid.
	ReasonSessionRecordHasID
	// ReasonFirstPromptRecorded: the first prompt is in the session record.
	ReasonFirstPromptRecorded
	// ReasonAlreadyReady: the attempt reached the ready stage earlier.
	ReasonAlreadyReady
	// ReasonStartTimedOut: a turn process ran longer than StartTimeout without
	// the first prompt reaching the session record.
	ReasonStartTimedOut
	// ReasonStartsExhausted: MaxStarts processes ended without the first
	// prompt reaching the session record.
	ReasonStartsExhausted
)

var reasonNames = map[Reason]string{
	ReasonNotStarted: "NotStarted", ReasonProcessEndedWithoutSession: "ProcessEndedWithoutSession",
	ReasonRecordWithoutFirstPrompt: "RecordWithoutFirstPrompt", ReasonTurnRunning: "TurnRunning",
	ReasonMultipleProcesses: "MultipleProcesses", ReasonProcessFoundByCommandLine: "ProcessFoundByCommandLine",
	ReasonProcessFoundInRegistry: "ProcessFoundInRegistry", ReasonSessionRecordHasID: "SessionRecordHasID",
	ReasonFirstPromptRecorded: "FirstPromptRecorded", ReasonAlreadyReady: "AlreadyReady",
	ReasonStartTimedOut: "StartTimedOut", ReasonStartsExhausted: "StartsExhausted",
}

func (r Reason) String() string { return nameOf(reasonNames, r, "Reason") }

func nameOf[K ~int](names map[K]string, k K, kind string) string {
	if n, ok := names[k]; ok {
		return n
	}
	return fmt.Sprintf("%s(%d)", kind, k)
}

// Decision is the verdict of Decide.
type Decision struct {
	Action Action
	Reason Reason
	// Process is the process to record; set for ActionRecordProcess only.
	Process Process
}

// Decide returns the next step of a creation attempt.
//
// Whatever has already run, a second turn process for the session is never
// started and --session-id is never used once the session file exists. A
// process counts as running when the process table lists it with the uuid on
// its command line, when the runtime's registry names it and the table
// confirms it by PID and start time, or when it is the recorded process and
// the PID is still held with the recorded start time. A PID that is alive
// with another start time is a reused number and counts as nothing.
//
// The order of the rules is the order of the evidence: first make sure no
// running process goes unrecorded (so every later step knows the writer), then
// take readiness from the session record, and only when nothing runs and the
// record lacks the first prompt start a process.
func Decide(s State, o Observations, p Params, now time.Time) (Decision, error) {
	if err := s.validate(); err != nil {
		return Decision{}, err
	}
	if err := o.validate(s); err != nil {
		return Decision{}, err
	}
	if err := p.Validate(); err != nil {
		return Decision{}, err
	}
	if now.IsZero() {
		return Decision{}, fmt.Errorf("%w: now is unset", ErrInvalidInput)
	}

	if s.Stage == StageReady {
		return Decision{Action: ActionDone, Reason: ReasonAlreadyReady}, nil
	}

	running, err := runningProcesses(s, o)
	if err != nil {
		return Decision{}, err
	}
	if len(running) > 1 {
		return Decision{Action: ActionWait, Reason: ReasonMultipleProcesses}, nil
	}
	if len(running) == 1 && !running[0].process.Same(s.Process) {
		reason := ReasonProcessFoundInRegistry
		if running[0].viaCommandLine {
			reason = ReasonProcessFoundByCommandLine
		}
		return Decision{Action: ActionRecordProcess, Reason: reason, Process: running[0].process}, nil
	}

	switch {
	case o.Record.HasFirstPrompt:
		return Decision{Action: ActionMarkReady, Reason: ReasonFirstPromptRecorded}, nil
	case o.Record.HasSessionID && s.Stage < StageUUIDSeen:
		return Decision{Action: ActionMarkUUIDSeen, Reason: ReasonSessionRecordHasID}, nil
	}

	if len(running) == 1 {
		if now.Sub(running[0].process.StartTime) > p.StartTimeout {
			return Decision{Action: ActionFail, Reason: ReasonStartTimedOut}, nil
		}
		return Decision{Action: ActionWait, Reason: ReasonTurnRunning}, nil
	}

	if s.Starts >= p.MaxStarts {
		return Decision{Action: ActionFail, Reason: ReasonStartsExhausted}, nil
	}
	switch {
	case o.Record.Exists:
		return Decision{Action: ActionStartResume, Reason: ReasonRecordWithoutFirstPrompt}, nil
	case s.Process.IsZero():
		return Decision{Action: ActionStartNew, Reason: ReasonNotStarted}, nil
	default:
		return Decision{Action: ActionStartNew, Reason: ReasonProcessEndedWithoutSession}, nil
	}
}

type runningProcess struct {
	process        Process
	viaCommandLine bool
}

// runningProcesses returns the distinct processes that run for the session.
// The same process reported by several sources counts once; one PID with two
// start times in a single snapshot cannot be true and is refused.
func runningProcesses(s State, o Observations) ([]runningProcess, error) {
	var out []runningProcess
	add := func(p Process, viaCommandLine bool) error {
		for i := range out {
			if out[i].process.Same(p) {
				out[i].viaCommandLine = out[i].viaCommandLine || viaCommandLine
				return nil
			}
			if out[i].process.PID == p.PID {
				return fmt.Errorf("%w: PID %d appears with two start times in one snapshot", ErrInvalidInput, p.PID)
			}
		}
		out = append(out, runningProcess{process: p, viaCommandLine: viaCommandLine})
		return nil
	}
	for _, p := range o.Running {
		if err := add(p, true); err != nil {
			return nil, err
		}
	}
	for _, e := range o.Registry {
		if e.SessionID == s.SessionID && e.confirmed() {
			if err := add(Process{PID: e.PID, StartTime: e.StartTime}, false); err != nil {
				return nil, err
			}
		}
	}
	if !s.Process.IsZero() && o.Recorded.Alive && sameStart(o.Recorded.StartTime, s.Process.StartTime) {
		if err := add(s.Process, false); err != nil {
			return nil, err
		}
	}
	return out, nil
}
