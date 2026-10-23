package create

import (
	"fmt"
	"time"
)

// Lookup is the process table's answer for one PID.
//
// The zero value says that no process holds the PID. Alive must be false for
// a zombie: a process that has exited but not been reaped is not running.
type Lookup struct {
	Alive bool
	// StartTime is the start time of the process that holds the PID; set only
	// when Alive. A different process may hold a PID the system has reused.
	StartTime time.Time
}

// RegistryEntry is one entry of the runtime's own session registry. The
// registry is auxiliary evidence: it is an unpublished format, and a process
// killed with SIGKILL leaves its entry behind. An entry counts only when the
// process table confirms the process behind it.
type RegistryEntry struct {
	SessionID SessionID
	PID       int
	// StartTime is the process start time written in the entry.
	StartTime time.Time
	// Current is the process table's answer for PID.
	Current Lookup
}

// confirmed reports whether the entry's process is alive with the start time
// the entry claims.
func (e RegistryEntry) confirmed() bool {
	return e.Current.Alive && sameStart(e.Current.StartTime, e.StartTime)
}

// SessionRecord is what the project directory shows about the session.
type SessionRecord struct {
	// Dir is the directory that was searched. It must be the attempt's
	// project directory: a file that is absent from another directory says
	// nothing about the session.
	Dir string
	// Exists: <Dir>/<uuid>.jsonl exists.
	Exists bool
	// HasSessionID: the file contains the uuid.
	HasSessionID bool
	// HasFirstPrompt: the file contains the first prompt (found by the id the
	// adapter put on it, scanning the raw file, not only the current branch).
	HasFirstPrompt bool
}

// Observations is what the caller gathered for one Decide call.
//
// The caller must take the process observations (Recorded, Running, Registry)
// before it reads the session record. The record then can only be newer than
// the processes: a turn process seen as gone has written everything it ever
// will, so "no process and no file" cannot be a file written in between.
type Observations struct {
	// Recorded is the lookup of State.Process.PID; ignored while the state has
	// no recorded process.
	Recorded Lookup
	// Running are the processes whose command line contains the session uuid,
	// each with the PID and start time the process table reports. The caller
	// leaves out processes that are not alive, zombies included.
	Running []Process
	// Registry are the entries of the runtime's session registry, all
	// sessions; Decide keeps those of this session.
	Registry []RegistryEntry
	Record   SessionRecord
}

func (o Observations) validate(s State) error {
	for _, p := range o.Running {
		if !p.valid() {
			return fmt.Errorf("%w: running process needs a PID and a start time: %+v", ErrInvalidInput, p)
		}
	}
	if err := o.Recorded.validate(); err != nil {
		return err
	}
	for _, e := range o.Registry {
		if !e.SessionID.Valid() || e.PID <= 0 || e.StartTime.IsZero() {
			return fmt.Errorf("%w: registry entry needs a session id, a PID and a start time: %+v", ErrInvalidInput, e)
		}
		if err := e.Current.validate(); err != nil {
			return err
		}
	}
	r := o.Record
	switch {
	case r.Dir != s.ProjectDir:
		return fmt.Errorf("%w: session record was looked up in %q, the attempt's directory is %q", ErrInvalidInput, r.Dir, s.ProjectDir)
	case r.HasSessionID && !r.Exists:
		return fmt.Errorf("%w: session file lacks existence but contains the uuid", ErrInvalidInput)
	case r.HasFirstPrompt && !r.HasSessionID:
		return fmt.Errorf("%w: session file has the first prompt but not the uuid", ErrInvalidInput)
	}
	return nil
}

func (l Lookup) validate() error {
	if l.Alive && l.StartTime.IsZero() {
		return fmt.Errorf("%w: an alive process needs a start time", ErrInvalidInput)
	}
	return nil
}
