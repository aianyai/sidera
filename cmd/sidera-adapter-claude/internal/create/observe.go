package create

import "fmt"

// Lookup is the process table's answer for one PID.
//
// A PID that no process holds is Alive false with the PID present; a lookup
// never answers with its zero value for a PID that was asked about. Alive must
// be false for a zombie: a process that has exited but not been reaped is not
// running.
type Lookup struct {
	// PID is the PID that was looked up.
	PID   int
	Alive bool
	// StartUnix is the start of the process that holds the PID, derived as
	// Process.StartUnix is; set only when Alive. A different process may hold
	// a PID the system has reused.
	StartUnix int64
}

// RegistryEntry is one entry of the runtime's own session registry. The
// registry is auxiliary evidence: it is an unpublished format, and a process
// killed with SIGKILL leaves its entry behind. An entry counts only when the
// process table confirms the process behind it.
type RegistryEntry struct {
	SessionID SessionID
	PID       int
	// StartUnix is the process start written in the entry.
	StartUnix int64
	// Current is the process table's answer for PID, from the same snapshot as
	// Observations.Running.
	Current Lookup
}

// confirmed reports whether the entry's process is alive with the start the
// entry claims.
func (e RegistryEntry) confirmed() bool {
	return e.Current.Alive && e.Current.StartUnix == e.StartUnix
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
	// HasFirstPrompt: the file contains the first prompt, found by
	// State.FirstPromptID (the id the caller put on the first user message),
	// scanning the raw file and not only the current branch.
	HasFirstPrompt bool
}

// Observations is what the caller gathered for one Decide call.
//
// The caller must take the process observations (Recorded, Running, Registry)
// before it reads the session record. The record then can only be newer than
// the processes: a turn process seen as gone has written everything it ever
// will, so "no process and no file" cannot be a file written in between.
// Recorded, Running and the Current lookups of Registry come from one
// process-table snapshot.
//
// Absent facts are zero: a nil Running or Registry means none, and Recorded is
// the zero Lookup while the state records no process.
type Observations struct {
	// Recorded is the lookup of State.Process.PID. While the state records a
	// process its PID must be that PID; while it records none, Recorded must be
	// the zero Lookup.
	Recorded Lookup
	// Running are the turn processes of the session. A process counts only if
	// its argument vector contains the pair "--session-id <uuid>" or
	// "--resume <uuid>" and its executable is the preset's Claude. Each launch
	// is listed once: the caller removes the wrapper and ancestor processes of
	// the same launch (a shell, a node launcher) and its own observer
	// processes (a grep or ps whose argument vector mentions the uuid). Each
	// carries the PID and start the process table reports, and none is a
	// zombie.
	//
	// The caller must be able to read the argument vector of every process of
	// the same user. When it cannot, that is an observation error the caller
	// reports as an error result; it is never reported as "not running".
	Running []Process
	// Registry are the entries of the runtime's session registry, all
	// sessions; Decide keeps those of this session. A confirmed entry whose
	// process is neither listed in Running nor the recorded process is a
	// process that works on the session without carrying its uuid, which only
	// an outside writer does (the user resumed the session by hand).
	Registry []RegistryEntry
	Record   SessionRecord
}

func (o Observations) validate(s State) error {
	for _, p := range o.Running {
		if !p.valid() {
			return fmt.Errorf("%w: running process needs a PID and a start time: %+v", ErrInvalidInput, p)
		}
	}
	switch {
	case s.Process.IsZero() && o.Recorded != (Lookup{}):
		return fmt.Errorf("%w: a process was looked up (%+v) but the state records none", ErrInvalidInput, o.Recorded)
	case !s.Process.IsZero() && o.Recorded.PID != s.Process.PID:
		return fmt.Errorf("%w: recorded process has PID %d, the lookup is for PID %d", ErrInvalidInput, s.Process.PID, o.Recorded.PID)
	}
	if err := o.Recorded.validate(); err != nil {
		return err
	}
	for _, e := range o.Registry {
		if !e.SessionID.Valid() || e.PID <= 0 || e.StartUnix <= 0 {
			return fmt.Errorf("%w: registry entry needs a session id, a PID and a start time: %+v", ErrInvalidInput, e)
		}
		if e.Current.PID != e.PID {
			return fmt.Errorf("%w: registry entry has PID %d, the lookup is for PID %d", ErrInvalidInput, e.PID, e.Current.PID)
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
	case s.Stage == StageUUIDSeen && !r.Exists:
		return fmt.Errorf("%w: the uuid was seen in the session file earlier, now there is no file", ErrInvalidInput)
	}
	return nil
}

func (l Lookup) validate() error {
	if l.PID < 0 {
		return fmt.Errorf("%w: negative PID in a lookup: %+v", ErrInvalidInput, l)
	}
	if l.Alive && (l.PID == 0 || l.StartUnix <= 0) {
		return fmt.Errorf("%w: an alive process needs a PID and a start time: %+v", ErrInvalidInput, l)
	}
	return nil
}
