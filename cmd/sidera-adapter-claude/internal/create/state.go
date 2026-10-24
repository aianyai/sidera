// Package create decides, step by step, how the Claude adapter creates one
// session without ever running two turn processes for it or creating it twice.
//
// It is pure: it reads a persisted State and the Observations the caller
// gathered, and returns the next Action with a typed Reason. It starts no
// process, reads no file and never looks at the clock; the caller executes the
// action, commits the matching State transition and calls Decide again.
package create

import (
	"errors"
	"fmt"
)

// ErrInvalidInput is wrapped by every error about a State, an Observations
// value or a time that is not valid input. Zero values are never valid.
var ErrInvalidInput = errors.New("create: invalid input")

// AttemptID names one creation attempt. It is chosen by the caller before the
// intent is committed and never changes.
type AttemptID string

// SessionID is the session uuid allocated before anything is started, in the
// canonical lower-case 8-4-4-4-12 hexadecimal form. It names the session file
// and appears on the command line of every turn process of the session, which
// is how a process whose PID was never recorded is found again.
type SessionID string

// Valid reports whether id is a canonical lower-case uuid.
func (id SessionID) Valid() bool { return validUUID(string(id)) }

// PromptID identifies the first prompt of the session, in the same canonical
// lower-case form as SessionID. The caller puts it on the first user message,
// and the session record is searched for the message carrying it: the prompt
// is found by this id and by nothing else. It is allocated with the session
// uuid and persisted in the intent.
type PromptID string

// Valid reports whether id is a canonical lower-case uuid.
func (id PromptID) Valid() bool { return validUUID(string(id)) }

func validUUID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				return false
			}
		}
	}
	return true
}

// Process identifies one operating-system process. A PID alone is not an
// identity because the system reuses PIDs; the start time completes it.
//
// StartUnix is the process start in Unix seconds, read from the process table
// and never from a clock the adapter keeps. Each platform derives it from the
// finest value its process table offers, truncated to whole seconds, and the
// same derivation is used for every start time the caller reports:
//
//   - Linux: the system boot time plus the process start in clock ticks since
//     boot (field 22 of /proc/<pid>/stat). The boot time is read once and is
//     fixed for the boot, so the value of a process never changes.
//   - macOS: the start seconds of the process record (p_starttime).
//   - Windows: the process creation time, truncated to seconds.
//
// More identity fields (a service unit, a process group, a job object) may be
// added later, but two Process values denote the same process exactly when PID
// and StartUnix are equal, and Same is where that rule lives.
type Process struct {
	PID       int
	StartUnix int64
}

// IsZero reports whether p is the unset value.
func (p Process) IsZero() bool { return p.PID == 0 && p.StartUnix == 0 }

func (p Process) valid() bool { return p.PID > 0 && p.StartUnix > 0 }

// Same reports whether p and q are the same process: the same PID and the same
// start second.
func (p Process) Same(q Process) bool { return p.PID == q.PID && p.StartUnix == q.StartUnix }

// Stage is how far a creation attempt has progressed. Stages only advance, and
// StageReady and StageFailed are final: neither ever changes again.
type Stage int

const (
	// StageIntent: the attempt, the session uuid, the first prompt id and the
	// project directory are committed; no turn process has been recorded.
	StageIntent Stage = iota + 1
	// StageStarted: a turn process has been recorded with its PID and start time.
	StageStarted
	// StageUUIDSeen: the session record in the project directory contains the uuid.
	StageUUIDSeen
	// StageReady: the first prompt is in the session record. The caller answers
	// session.create from this state, and again from it after a crash.
	StageReady
	// StageFailed: the attempt ended in failure; session.create is answered
	// failed, with the reason's CreateFailure. No process is ever started for
	// this uuid afterwards; a process that is still running is left alone.
	StageFailed
)

// State is the persisted state of one creation attempt; the adapter stores it
// as its checkpoint. Construct it with NewIntent and advance it with Started,
// UUIDSeen, Ready and Failed, each committed before the next Decide.
type State struct {
	AttemptID AttemptID
	SessionID SessionID
	// FirstPromptID is the id the caller puts on the first user message.
	FirstPromptID PromptID
	// ProjectDir is the preset's project directory. It is fixed for the whole
	// attempt: the session file is looked for in this directory only.
	ProjectDir string
	Stage      Stage
	// Process is the turn process recorded last; zero until Stage reaches
	// StageStarted. A later start replaces it.
	Process Process
	// Starts counts the distinct turn processes recorded. A process that was
	// started but whose crash kept it from being recorded is counted when it
	// is found again.
	Starts int
	// FailReason is why the attempt failed; set exactly when Stage is
	// StageFailed. The values of the failure reasons are persisted: they never
	// change their numbers.
	FailReason Reason
}

// NewIntent returns the state committed before any native interface is called.
func NewIntent(attempt AttemptID, session SessionID, firstPrompt PromptID, projectDir string) (State, error) {
	s := State{AttemptID: attempt, SessionID: session, FirstPromptID: firstPrompt, ProjectDir: projectDir, Stage: StageIntent}
	if err := s.validate(); err != nil {
		return State{}, err
	}
	return s, nil
}

// Started records the turn process just started, or found by the decision
// RecordProcess. Recording the process already recorded changes nothing. The
// caller commits it immediately after spawning the process, so the window in
// which a running process is unrecorded is as short as it can be.
func (s State) Started(p Process) (State, error) {
	if err := s.validate(); err != nil {
		return State{}, err
	}
	if s.final() {
		return State{}, fmt.Errorf("%w: attempt is already final (stage %d)", ErrInvalidInput, s.Stage)
	}
	if !p.valid() {
		return State{}, fmt.Errorf("%w: process needs a PID and a start time: %+v", ErrInvalidInput, p)
	}
	if !p.Same(s.Process) {
		s.Starts++
	}
	s.Process = p
	if s.Stage < StageStarted {
		s.Stage = StageStarted
	}
	return s, nil
}

// UUIDSeen records that the session record contains the uuid.
func (s State) UUIDSeen() (State, error) {
	if err := s.validate(); err != nil {
		return State{}, err
	}
	if s.final() {
		return State{}, fmt.Errorf("%w: attempt is already final (stage %d)", ErrInvalidInput, s.Stage)
	}
	if s.Stage < StageUUIDSeen {
		s.Stage = StageUUIDSeen
	}
	return s, nil
}

// Ready records that the first prompt is in the session record. The caller
// persists it, then answers session.create; a crash between the two is repaired
// by the repeated session.create, which Decide answers from this state with
// ActionDone. A failed attempt is never ready.
func (s State) Ready() (State, error) {
	if err := s.validate(); err != nil {
		return State{}, err
	}
	if s.Stage == StageFailed {
		return State{}, fmt.Errorf("%w: attempt has failed (%v)", ErrInvalidInput, s.FailReason)
	}
	s.Stage = StageReady
	return s, nil
}

// Failed records that the attempt failed for the given reason, one of the
// reasons of a terminal failure (ReasonStartTimedOut, ReasonStartsExhausted,
// ReasonMultipleProcesses, ReasonOutsideWriter). The caller persists it, then
// answers session.create, symmetrically to Ready. Failing a failed
// attempt for the same reason changes nothing; a ready attempt cannot fail
// and a failed one keeps its first reason.
func (s State) Failed(reason Reason) (State, error) {
	if err := s.validate(); err != nil {
		return State{}, err
	}
	if !reason.endsAttempt() {
		return State{}, fmt.Errorf("%w: %v is not a reason that fails an attempt", ErrInvalidInput, reason)
	}
	switch {
	case s.Stage == StageReady:
		return State{}, fmt.Errorf("%w: attempt is already ready", ErrInvalidInput)
	case s.Stage == StageFailed && s.FailReason != reason:
		return State{}, fmt.Errorf("%w: attempt already failed with %v", ErrInvalidInput, s.FailReason)
	}
	s.Stage = StageFailed
	s.FailReason = reason
	return s, nil
}

func (s State) final() bool { return s.Stage == StageReady || s.Stage == StageFailed }

func (s State) validate() error {
	switch {
	case s.AttemptID == "":
		return fmt.Errorf("%w: attempt id is unset", ErrInvalidInput)
	case !s.SessionID.Valid():
		return fmt.Errorf("%w: session id %q is not a canonical lower-case uuid", ErrInvalidInput, s.SessionID)
	case !s.FirstPromptID.Valid():
		return fmt.Errorf("%w: first prompt id %q is not a canonical lower-case uuid", ErrInvalidInput, s.FirstPromptID)
	case s.ProjectDir == "":
		return fmt.Errorf("%w: project directory is unset", ErrInvalidInput)
	case s.Stage < StageIntent || s.Stage > StageFailed:
		return fmt.Errorf("%w: unknown stage %d", ErrInvalidInput, s.Stage)
	case s.Starts < 0:
		return fmt.Errorf("%w: negative start count %d", ErrInvalidInput, s.Starts)
	case !s.Process.IsZero() && !s.Process.valid():
		return fmt.Errorf("%w: recorded process needs a PID and a start time: %+v", ErrInvalidInput, s.Process)
	case !s.Process.IsZero() && s.Stage == StageIntent:
		return fmt.Errorf("%w: an attempt at the intent stage has a recorded process: %+v", ErrInvalidInput, s.Process)
	case s.Process.IsZero() && s.Stage == StageStarted:
		return fmt.Errorf("%w: started attempt has no recorded process", ErrInvalidInput)
	case s.Process.IsZero() != (s.Starts == 0):
		return fmt.Errorf("%w: start count %d does not match the recorded process", ErrInvalidInput, s.Starts)
	case (s.Stage == StageFailed) != (s.FailReason != 0):
		return fmt.Errorf("%w: failure reason %v does not match stage %d", ErrInvalidInput, s.FailReason, s.Stage)
	case s.Stage == StageFailed && !s.FailReason.endsAttempt():
		return fmt.Errorf("%w: %v is not a reason that fails an attempt", ErrInvalidInput, s.FailReason)
	}
	return nil
}
