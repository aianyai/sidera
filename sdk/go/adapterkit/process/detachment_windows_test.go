package process

import (
	"errors"
	"testing"

	"golang.org/x/sys/windows"
)

// A start that was refused breakaway promises only what the job it stays in shows.
// When the innermost job allows breakaway the refusal was an outer job's, whose
// kill-on-close cannot be read, and nothing is promised.
func TestAfterRefusedBreakaway(t *testing.T) {
	const (
		breakawayOK = windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK
		silent      = windows.JOB_OBJECT_LIMIT_SILENT_BREAKAWAY_OK
		killOnClose = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	)
	for _, tc := range []struct {
		name     string
		inJob    bool
		flags    uint32
		err      error
		outlives bool
	}{
		{"innermost forbids breakaway", true, 0, nil, true},
		{"innermost forbids breakaway, kill on close", true, killOnClose, nil, false},
		{"innermost allows breakaway", true, breakawayOK, nil, false},
		{"innermost allows breakaway, kill on close", true, breakawayOK | killOnClose, nil, false},
		{"innermost allows breakaway silently", true, silent, nil, false},
		{"not in a job", false, 0, nil, true},
		{"job unreadable", true, 0, errors.New("denied"), false},
	} {
		d := afterRefusedBreakaway(tc.inJob, tc.flags, tc.err)
		if d.OutlivesDaemon != tc.outlives {
			t.Errorf("%s: OutlivesDaemon = %v, want %v (%+v)", tc.name, d.OutlivesDaemon, tc.outlives, d)
		}
	}
}
