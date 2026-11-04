package kanban

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Class says whether retrying a failed command can succeed.
type Class int

const (
	// Transient failures may clear by themselves: the command cannot be run, the
	// database is locked, the call timed out, or Hermes said something this client
	// does not recognise. The caller retries and loses nothing by doing so.
	Transient Class = iota + 1
	// Permanent failures will fail the same way every time: the card does not
	// exist, its state does not allow the operation, an argument is invalid.
	Permanent
)

func (c Class) String() string {
	switch c {
	case Transient:
		return "transient"
	case Permanent:
		return "permanent"
	}
	return fmt.Sprintf("Class(%d)", int(c))
}

var (
	// ErrTransient matches, under errors.Is, every Error of class Transient.
	ErrTransient = errors.New("kanban: transient failure")
	// ErrPermanent matches, under errors.Is, every Error of class Permanent.
	ErrPermanent = errors.New("kanban: permanent failure")
	// ErrArgTooLong is wrapped by the Error that refuses a command line argument
	// above MaxArgBytes before anything is run.
	ErrArgTooLong = errors.New("kanban: command-line argument too long")
	// ErrUnexpectedOutput is wrapped by the Error of a command that succeeded but
	// printed something this client cannot decode, which is how a Hermes release
	// that changed a shape shows up. It is Transient: the card state is untouched,
	// and a caller that pauses and retries loses nothing while the client is fixed.
	ErrUnexpectedOutput = errors.New("kanban: unexpected hermes output")
)

// Error is what every Client method returns on failure.
type Error struct {
	Op       string // the Hermes subcommand, e.g. "claim"
	Class    Class
	ExitCode int    // the exit code of hermes, -1 when it did not run to an exit
	Stderr   string // what hermes printed on stderr, trimmed and bounded
	Err      error  // the underlying cause when there is no stderr to show
}

func (e *Error) Error() string {
	msg := "kanban " + e.Op + ": " + e.Class.String()
	if e.ExitCode >= 0 {
		msg += fmt.Sprintf(" (exit %d)", e.ExitCode)
	}
	if e.Stderr != "" {
		msg += ": " + e.Stderr
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Err }

// Is makes errors.Is(err, ErrTransient) and errors.Is(err, ErrPermanent) answer
// by class.
func (e *Error) Is(target error) bool {
	switch target {
	case ErrTransient:
		return e.Class == Transient
	case ErrPermanent:
		return e.Class == Permanent
	}
	return false
}

// maxStderr bounds the stderr kept in an Error; hermes prints a usage text for
// every argument error, and the first lines say what is wrong.
const maxStderr = 512

func boundedStderr(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxStderr {
		s = strings.ToValidUTF8(s[:maxStderr], "") + "..."
	}
	return s
}

// transientMarkers are stderr fragments that mean the database or the Python
// process failed for a reason outside the command: a locked database (Hermes
// waits out its busy timeout, 120 s by default, and then reports it), a failed
// initialisation, or an exception that reached the interpreter's top level.
// They are checked first because a traceback can quote anything.
var transientMarkers = []string{
	"database is locked",
	"database table is locked",
	"could not initialize database",
	"Traceback (most recent call last)",
	"OperationalError",
	"disk I/O error",
}

// permanentLines match whole stderr lines that Hermes 0.22.2 prints when a card
// does not exist, its state refuses the operation, or the input is invalid. Each
// pattern is anchored at the start of a line so text a card carries cannot
// reach one.
var permanentLines = regexp.MustCompile(`(?m)^(?:` +
	`no such task: ` +
	`|no such attachment: ` +
	`|cannot (?:claim|complete|block|unblock) ` +
	`|kanban: unknown task ` +
	`|kanban: unknown parent task` +
	`|kanban: title is required` +
	`|kanban: no such file: ` +
	`|kanban: attachment exceeds ` +
	`)`)

// classify decides the class of a hermes failure from its exit code and stderr.
// Hermes exits 2 for an argument it rejects (argparse and its own validation)
// and 1 for everything else, so for exit 1 only the text tells a missing card
// from a locked database.
func classify(exitCode int, stderr string) Class {
	if exitCode == 2 {
		return Permanent
	}
	for _, m := range transientMarkers {
		if strings.Contains(stderr, m) {
			return Transient
		}
	}
	if exitCode == 1 && permanentLines.MatchString(stderr) {
		return Permanent
	}
	return Transient
}
