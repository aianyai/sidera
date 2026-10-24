package create

import (
	"fmt"
	"sort"
	"time"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// Action is the next step of the attempt.
type Action int

const (
	// ActionStartNew: start a turn process with --session-id <uuid>, giving it
	// the first prompt with the launch, then commit State.Started with the
	// process at once. Chosen only when no process runs and the session file
	// does not exist.
	ActionStartNew Action = iota + 1
	// ActionStartResume: start a turn process with --resume <uuid>, giving it
	// the first prompt with the launch, then commit State.Started at once.
	// Chosen when the session file exists, because --session-id fails on an
	// existing session.
	ActionStartResume
	// ActionWait: a turn process of the session runs; start nothing and decide
	// again when it ends, when the clock passes Decision.RecheckBy, or on the
	// next observation, whichever comes first.
	ActionWait
	// ActionRecordProcess: a turn process was found whose PID was never
	// recorded. Commit Decision.Process with State.Started.
	ActionRecordProcess
	// ActionMarkUUIDSeen: commit State.UUIDSeen.
	ActionMarkUUIDSeen
	// ActionMarkReady: persist State.Ready, then answer session.create created.
	ActionMarkReady
	// ActionDone: the attempt is final (ready, or failed with
	// Decision.FailReason). Nothing is decided; the caller answers a repeated
	// session.create from the stored state.
	ActionDone
	// ActionFail: the attempt failed for good; persist State.Failed with
	// Decision.Reason, then answer session.create failed with
	// Decision.Reason.CreateFailure(). Whatever process is
	// still running is not touched, and no process is started for this uuid
	// afterwards.
	ActionFail
	// ActionControlUnknown: more than one process works on the session, or a
	// process that is not the adapter's does. Like ActionFail it ends the
	// attempt for good (persist State.Failed with Decision.Reason, then answer
	// session.create failed with control_unknown), and nothing is started or
	// touched. The session never attaches to the channel, so nothing is marked
	// there; the daemon notifies the local owner when it receives the reason.
	// Decision.Processes lists every process seen.
	ActionControlUnknown
)

var actionNames = map[Action]string{
	ActionStartNew: "StartNew", ActionStartResume: "StartResume", ActionWait: "Wait",
	ActionRecordProcess: "RecordProcess", ActionMarkUUIDSeen: "MarkUUIDSeen",
	ActionMarkReady: "MarkReady", ActionDone: "Done", ActionFail: "Fail",
	ActionControlUnknown: "ControlUnknown",
}

func (a Action) String() string { return nameOf(actionNames, a, "Action") }

// Reason says why Decide chose its action. The reasons that fail an attempt are
// persisted in State.FailReason, so their numbers never change.
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
	// ReasonMultipleProcesses: more than one distinct process carries the
	// uuid. Fails the attempt as control unknown, even when the first prompt
	// is already in the record: a second writer is a second writer.
	ReasonMultipleProcesses
	// ReasonProcessFoundByCommandLine: a process with the uuid on its command
	// line runs and is not the recorded one.
	ReasonProcessFoundByCommandLine
	// ReasonSessionRecordHasID: the session file contains the uuid.
	ReasonSessionRecordHasID
	// ReasonFirstPromptRecorded: the first prompt is in the session record.
	ReasonFirstPromptRecorded
	// ReasonAlreadyReady: the attempt reached the ready stage earlier.
	ReasonAlreadyReady
	// ReasonStartTimedOut: a turn process ran longer than StartTimeout without
	// the first prompt reaching the session record. Fails the attempt; the
	// process is still running, so the session's control is unknown.
	ReasonStartTimedOut
	// ReasonStartsExhausted: MaxStarts processes ended without the first
	// prompt reaching the session record. Fails the attempt.
	ReasonStartsExhausted
	// ReasonOutsideWriter: the runtime's registry names a live process for
	// this session that is neither the recorded process nor lists the uuid on
	// its command line, so someone else works on the session (the user resumed
	// it by hand). Fails the attempt as control unknown.
	ReasonOutsideWriter
	// ReasonAlreadyFailed: the attempt failed earlier; Decision.FailReason is
	// the reason it failed with.
	ReasonAlreadyFailed
)

var reasonNames = map[Reason]string{
	ReasonNotStarted: "NotStarted", ReasonProcessEndedWithoutSession: "ProcessEndedWithoutSession",
	ReasonRecordWithoutFirstPrompt: "RecordWithoutFirstPrompt", ReasonTurnRunning: "TurnRunning",
	ReasonMultipleProcesses: "MultipleProcesses", ReasonProcessFoundByCommandLine: "ProcessFoundByCommandLine",
	ReasonSessionRecordHasID:  "SessionRecordHasID",
	ReasonFirstPromptRecorded: "FirstPromptRecorded", ReasonAlreadyReady: "AlreadyReady",
	ReasonStartTimedOut: "StartTimedOut", ReasonStartsExhausted: "StartsExhausted",
	ReasonOutsideWriter: "OutsideWriter", ReasonAlreadyFailed: "AlreadyFailed",
}

func (r Reason) String() string { return nameOf(reasonNames, r, "Reason") }

// CreateFailure is the reason session.create reports when an attempt failed
// with r. It is the one place that maps a failure reason to the protocol, and
// the answer to "can an attempt fail with r": ok is false for every reason that
// is not a failure.
//
//   - A second writer (ReasonMultipleProcesses, ReasonOutsideWriter) or a turn
//     process that is still running after the start timed out
//     (ReasonStartTimedOut) means a native process works on the session and the
//     adapter does not control it: control_unknown. The adapter leaves the
//     process alone and the daemon notifies the local owner.
//   - ReasonStartsExhausted means every process ended without the first prompt:
//     the runtime cannot start, which is native_error. Nothing is running.
func (r Reason) CreateFailure() (failure adapterproto.CreateFailure, ok bool) {
	switch r {
	case ReasonMultipleProcesses, ReasonOutsideWriter, ReasonStartTimedOut:
		return adapterproto.CreateFailControlUnknown, true
	case ReasonStartsExhausted:
		return adapterproto.CreateFailNativeError, true
	}
	return "", false
}

// endsAttempt reports whether r is a reason an attempt can fail with: the
// reasons of ActionFail and ActionControlUnknown.
func (r Reason) endsAttempt() bool {
	_, ok := r.CreateFailure()
	return ok
}

// ControlUnknown reports whether r is a reason that leaves the session's
// control unknown, which is how session.create reports it (control_unknown).
func (r Reason) ControlUnknown() bool {
	failure, ok := r.CreateFailure()
	return ok && failure == adapterproto.CreateFailControlUnknown
}

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
	// Processes are all processes seen, ordered by PID; set for
	// ActionControlUnknown only.
	Processes []Process
	// RecheckBy is a time in Unix seconds; set for ActionWait only. The
	// caller decides again when its clock passes it, even if no turn ended:
	// the wait ends in a timeout then. At RecheckBy itself the turn has run
	// exactly StartTimeout and has not yet timed out.
	RecheckBy int64
	// FailReason is the stored reason the attempt failed with; set for
	// ActionDone with ReasonAlreadyFailed only.
	FailReason Reason
}

// Decide returns the next step of a creation attempt.
//
// Whatever has already run, a second turn process for the session is never
// started, --session-id is never used once the session file exists, and a
// failed attempt never starts anything or becomes ready. A process counts as
// running when the process table lists it with the uuid on its command line,
// when it is the recorded process and the PID is still held with the recorded
// start, or when the runtime's registry names it and the table confirms it by
// PID and start. A PID that is alive with another start is a reused number and
// counts as nothing.
//
// The order of the rules is the order of the evidence: first make sure that
// exactly one writer exists (more than one, or a writer that is not ours, ends
// the attempt as control unknown, whatever the record says), then that no
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

	switch s.Stage {
	case StageReady:
		return Decision{Action: ActionDone, Reason: ReasonAlreadyReady}, nil
	case StageFailed:
		return Decision{Action: ActionDone, Reason: ReasonAlreadyFailed, FailReason: s.FailReason}, nil
	}

	running, outsideWriter, err := runningProcesses(s, o)
	if err != nil {
		return Decision{}, err
	}
	switch {
	case outsideWriter:
		return Decision{Action: ActionControlUnknown, Reason: ReasonOutsideWriter, Processes: running}, nil
	case len(running) > 1:
		return Decision{Action: ActionControlUnknown, Reason: ReasonMultipleProcesses, Processes: running}, nil
	}
	if len(running) == 1 && !running[0].Same(s.Process) {
		return Decision{Action: ActionRecordProcess, Reason: ReasonProcessFoundByCommandLine, Process: running[0]}, nil
	}

	switch {
	case o.Record.HasFirstPrompt:
		return Decision{Action: ActionMarkReady, Reason: ReasonFirstPromptRecorded}, nil
	case o.Record.HasSessionID && s.Stage < StageUUIDSeen:
		return Decision{Action: ActionMarkUUIDSeen, Reason: ReasonSessionRecordHasID}, nil
	}

	if len(running) == 1 {
		if now.Sub(time.Unix(running[0].StartUnix, 0)) > p.StartTimeout {
			return Decision{Action: ActionFail, Reason: ReasonStartTimedOut}, nil
		}
		return Decision{Action: ActionWait, Reason: ReasonTurnRunning, RecheckBy: running[0].StartUnix + ceilSeconds(p.StartTimeout)}, nil
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

func ceilSeconds(d time.Duration) int64 { return int64((d + time.Second - 1) / time.Second) }

// runningProcesses returns the distinct processes that run for the session,
// ordered by PID, and whether the registry names one that is not ours. The same
// process reported by several sources counts once; one PID with two starts in
// a single snapshot cannot be true and is refused. A process found through the
// registry is recorded with the process table's start, not the entry's.
func runningProcesses(s State, o Observations) (out []Process, outsideWriter bool, err error) {
	add := func(p Process) error {
		for _, q := range out {
			if q.Same(p) {
				return nil
			}
			if q.PID == p.PID {
				return fmt.Errorf("%w: PID %d appears with two start times in one snapshot", ErrInvalidInput, p.PID)
			}
		}
		out = append(out, p)
		return nil
	}
	for _, p := range o.Running {
		if err := add(p); err != nil {
			return nil, false, err
		}
	}
	if !s.Process.IsZero() && o.Recorded.Alive && o.Recorded.StartUnix == s.Process.StartUnix {
		if err := add(s.Process); err != nil {
			return nil, false, err
		}
	}
	for _, e := range o.Registry {
		if e.SessionID != s.SessionID || !e.confirmed() {
			continue
		}
		p := Process{PID: e.PID, StartUnix: e.Current.StartUnix}
		known := false
		for _, q := range out {
			known = known || q.Same(p)
		}
		if !known {
			outsideWriter = true
		}
		if err := add(p); err != nil {
			return nil, false, err
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out, outsideWriter, nil
}
