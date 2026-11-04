package kanban

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every recorded failure output of Hermes 0.22.2 with the class it must get. A
// file in testdata/errors that is not here fails the test, so a newly recorded
// output is classified on purpose.
var recordedErrors = map[string]Class{
	"attach-no-file":        Permanent,
	"attach-too-large":      Permanent,
	"attach-unknown":        Permanent,
	"attachments-unknown":   Permanent,
	"block-refused":         Permanent,
	"board-bad-slug":        Permanent,
	"board-missing":         Transient, // the board may be created while the adapter retries
	"claim-unknown":         Permanent,
	"claim-wrong-state":     Permanent,
	"comment-unknown":       Permanent,
	"complete-empty":        Permanent,
	"complete-open-parents": Permanent,
	"complete-terminal":     Permanent,
	"create-no-title":       Permanent,
	"create-unknown-parent": Permanent,
	"db-locked":             Transient,
	"metadata-bad":          Permanent,
	"notify-unknown":        Permanent,
	"show-unknown":          Permanent,
	"usage-bad-choice":      Permanent,
}

func TestClassifyRecordedErrors(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "errors", "*.txt"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no samples: %v", err)
	}
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".txt")
		want, ok := recordedErrors[name]
		if !ok {
			t.Errorf("%s: recorded but not classified here", name)
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		first, stderr, _ := strings.Cut(string(b), "\n")
		var code int
		if _, err := fmt.Sscanf(first, "exit %d", &code); err != nil {
			t.Fatalf("%s: first line %q: %v", name, first, err)
		}
		if got := classify(code, stderr); got != want {
			t.Errorf("%s: exit %d classified %v, want %v", name, code, got, want)
		}
	}
	if len(files) != len(recordedErrors) {
		t.Errorf("%d samples for %d expectations", len(files), len(recordedErrors))
	}
}

// Cases that were not recorded: a Python traceback quotes anything, so it is
// transient even when the text it quotes looks permanent; a process killed by a
// signal and an exit code nobody knows may clear by themselves.
func TestClassifyUnrecorded(t *testing.T) {
	for _, tc := range []struct {
		name   string
		code   int
		stderr string
		want   Class
	}{
		{"traceback quoting a permanent line", 1, "Traceback (most recent call last):\n  ...\nsqlite3.OperationalError: no such table\nno such task: t_1\n", Transient},
		{"killed by a signal", -1, "", Transient},
		{"unknown exit code", 127, "no such task: t_1", Transient},
		{"unrecognised message", 1, "kanban: something new\n", Transient},
		{"empty stderr", 1, "", Transient},
		{"permanent text inside a longer line", 1, "kanban: x no such task: t_1", Transient},
	} {
		if got := classify(tc.code, tc.stderr); got != tc.want {
			t.Errorf("%s: classified %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestErrorMatching(t *testing.T) {
	var err error = &Error{Op: "claim", Class: Permanent, ExitCode: 1, Stderr: "no such task: t_1"}
	var ke *Error
	if !errors.Is(err, ErrPermanent) || errors.Is(err, ErrTransient) || !errors.As(err, &ke) || ke.Class != Permanent {
		t.Fatalf("permanent error matches wrongly: %v", err)
	}
	if !errors.Is(&Error{Class: Transient}, ErrTransient) {
		t.Fatal("transient error does not match ErrTransient")
	}
}
