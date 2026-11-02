package attached

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"time"
)

// ErrRegistryForm is wrapped by the error of ParseRegistry for a file in a form
// this package does not read.
var ErrRegistryForm = errors.New("attached: the session registry is not in the form the adapter reads")

// Status is what the runtime says a session is doing.
type Status int

// The statuses, as the registry names them. A name this package does not know
// is StatusUnknown.
const (
	StatusUnknown Status = iota
	// StatusIdle: waiting for input.
	StatusIdle
	// StatusBusy: a turn is running.
	StatusBusy
	// StatusWaiting: a turn waits for the person to answer a permission prompt.
	StatusWaiting
	// StatusShell: the turn is over and a background shell command still runs.
	StatusShell
)

// Active reports whether a turn is running: busy, or waiting for the person.
// A session in shell has ended its turn.
func (s Status) Active() bool { return s == StatusBusy || s == StatusWaiting }

func parseStatus(name string) Status {
	switch name {
	case "idle":
		return StatusIdle
	case "busy":
		return StatusBusy
	case "waiting":
		return StatusWaiting
	case "shell":
		return StatusShell
	}
	return StatusUnknown
}

// Registry is one session's registry file, `<claude dir>/sessions/<pid>.json`,
// reduced to what the adapter reads.
type Registry struct {
	PID int
	// SessionID is the session now running in the process; it changes at /clear.
	SessionID string
	// Cwd is the working directory the session's records are kept by.
	Cwd string
	// Interactive: a session a person drives in a terminal.
	Interactive bool
	// ProcStart is when the process started, as the runtime wrote it.
	ProcStart time.Time
	Status    Status
	// StatusName is the status as written, for a message about one this package
	// does not know.
	StatusName string
	// StatusAt is when the status last changed, in milliseconds since the epoch.
	StatusAt int64
	// Messaging: the runtime lists support for messages between sessions.
	Messaging bool
}

// RegistryFile is the path of the registry file of a process in the runtime's
// configuration directory.
func RegistryFile(claudeDir string, pid int) string {
	return filepath.Join(claudeDir, "sessions", strconv.Itoa(pid)+".json")
}

// ParseRegistry reads a registry file. The runtime writes the process start in
// the form of `ps -o lstart` in UTC.
func ParseRegistry(data []byte) (Registry, error) {
	var raw struct {
		PID                 int    `json:"pid"`
		SessionID           string `json:"sessionId"`
		Cwd                 string `json:"cwd"`
		Kind                string `json:"kind"`
		ProcStart           string `json:"procStart"`
		Status              string `json:"status"`
		StatusUpdatedAt     int64  `json:"statusUpdatedAt"`
		PeerProtocol        int    `json:"peerProtocol"`
		MessagingSocketPath string `json:"messagingSocketPath"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return Registry{}, fmt.Errorf("%w: not JSON of the expected shape", ErrRegistryForm)
	}
	missing := ""
	switch {
	case raw.PID <= 0:
		missing = "pid"
	case raw.SessionID == "":
		missing = "sessionId"
	case raw.Cwd == "":
		missing = "cwd"
	case raw.Kind == "":
		missing = "kind"
	case raw.Status == "":
		missing = "status"
	case raw.StatusUpdatedAt <= 0:
		missing = "statusUpdatedAt"
	}
	if missing != "" {
		return Registry{}, fmt.Errorf("%w: no usable %q", ErrRegistryForm, missing)
	}
	start, err := time.ParseInLocation(time.ANSIC, raw.ProcStart, time.UTC)
	if err != nil {
		return Registry{}, fmt.Errorf("%w: no usable %q", ErrRegistryForm, "procStart")
	}
	return Registry{
		PID: raw.PID, SessionID: raw.SessionID, Cwd: raw.Cwd, Interactive: raw.Kind == "interactive",
		ProcStart: start, Status: parseStatus(raw.Status), StatusName: raw.Status, StatusAt: raw.StatusUpdatedAt,
		Messaging: raw.PeerProtocol > 0 || raw.MessagingSocketPath != "",
	}, nil
}

// startTolerance is how far apart the registry's process start and the process
// table's may be: the registry has whole seconds.
const startTolerance = 2 * time.Second

// SameProcess reports whether a process of the table that started at startUnix
// (seconds since the epoch) is the one the registry file was written by. A file
// that outlives its process names a PID that may since belong to another.
func (r Registry) SameProcess(startUnix int64) bool {
	d := r.ProcStart.Sub(time.Unix(startUnix, 0))
	return d >= -startTolerance && d <= startTolerance
}
