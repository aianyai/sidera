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
	"time"
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
func (id SessionID) Valid() bool {
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
type Process struct {
	PID       int
	StartTime time.Time
}

// IsZero reports whether p is the unset value.
func (p Process) IsZero() bool { return p.PID == 0 && p.StartTime.IsZero() }

func (p Process) valid() bool { return p.PID > 0 && !p.StartTime.IsZero() }

// Same reports whether p and q are the same process: the same PID and the same
// start time. Start times are compared in whole seconds, the resolution of the
// process table, so a start time read from another source with finer
// resolution still matches.
func (p Process) Same(q Process) bool {
	return p.PID == q.PID && sameStart(p.StartTime, q.StartTime)
}

// sameStart compares two process start times in whole seconds, the resolution
// of the process table.
func sameStart(a, b time.Time) bool { return a.Unix() == b.Unix() }

// Stage is how far a creation attempt has progressed. Stages only advance.
type Stage int

const (
	// StageIntent: the attempt, the session uuid and the project directory are
	// committed; no turn process has been recorded.
	StageIntent Stage = iota + 1
	// StageStarted: a turn process has been recorded with its PID and start time.
	StageStarted
	// StageUUIDSeen: the session record in the project directory contains the uuid.
	StageUUIDSeen
	// StageReady: the first prompt is in the session record and the control
	// reply has been reported.
	StageReady
)

// State is the persisted state of one creation attempt. Construct it with
// NewIntent and advance it with Started, UUIDSeen and Ready, each committed
// before the next Decide.
type State struct {
	AttemptID AttemptID
	SessionID SessionID
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
}

// NewIntent returns the state committed before any native interface is called.
func NewIntent(attempt AttemptID, session SessionID, projectDir string) (State, error) {
	s := State{AttemptID: attempt, SessionID: session, ProjectDir: projectDir, Stage: StageIntent}
	if err := s.validate(); err != nil {
		return State{}, err
	}
	return s, nil
}

// Started records the turn process just started, or found by the decision
// RecordProcess. Recording the process already recorded changes nothing.
func (s State) Started(p Process) (State, error) {
	if err := s.validate(); err != nil {
		return State{}, err
	}
	if s.Stage == StageReady {
		return State{}, fmt.Errorf("%w: attempt is already ready", ErrInvalidInput)
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
	if s.Stage == StageReady {
		return State{}, fmt.Errorf("%w: attempt is already ready", ErrInvalidInput)
	}
	if s.Stage < StageUUIDSeen {
		s.Stage = StageUUIDSeen
	}
	return s, nil
}

// Ready records that the first prompt is in the session record. The caller
// commits it together with the control reply, so a ready attempt never needs
// its reply reported again.
func (s State) Ready() (State, error) {
	if err := s.validate(); err != nil {
		return State{}, err
	}
	s.Stage = StageReady
	return s, nil
}

func (s State) validate() error {
	switch {
	case s.AttemptID == "":
		return fmt.Errorf("%w: attempt id is unset", ErrInvalidInput)
	case !s.SessionID.Valid():
		return fmt.Errorf("%w: session id %q is not a canonical lower-case uuid", ErrInvalidInput, s.SessionID)
	case s.ProjectDir == "":
		return fmt.Errorf("%w: project directory is unset", ErrInvalidInput)
	case s.Stage < StageIntent || s.Stage > StageReady:
		return fmt.Errorf("%w: unknown stage %d", ErrInvalidInput, s.Stage)
	case s.Starts < 0:
		return fmt.Errorf("%w: negative start count %d", ErrInvalidInput, s.Starts)
	case !s.Process.IsZero() && !s.Process.valid():
		return fmt.Errorf("%w: recorded process needs a PID and a start time: %+v", ErrInvalidInput, s.Process)
	case s.Process.IsZero() && s.Stage == StageStarted:
		return fmt.Errorf("%w: started attempt has no recorded process", ErrInvalidInput)
	case s.Process.IsZero() != (s.Starts == 0):
		return fmt.Errorf("%w: start count %d does not match the recorded process", ErrInvalidInput, s.Starts)
	}
	return nil
}
