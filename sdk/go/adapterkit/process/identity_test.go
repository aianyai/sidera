package process

import (
	"errors"
	"testing"
)

// Running turns the answer of Lookup into a verdict. An error is never "not
// running": a caller that took it for that starts a second writer beside a process
// that runs. The one exception is a refused process whose start is known to differ,
// which is another process holding the PID.
func TestNamed(t *testing.T) {
	id := Identity{PID: 7, StartKey: "100"}
	refusal := errors.New("access denied")
	platformFailure := errors.New("the platform did not answer")
	for _, tc := range []struct {
		name    string
		p       Process
		found   bool
		err     error
		running bool
		fails   bool
	}{
		{"same start", Process{Identity: id}, true, nil, true, false},
		{"another start", Process{Identity: Identity{PID: 7, StartKey: "101"}}, true, nil, false, false},
		{"not found", Process{}, false, nil, false, false},
		{"platform failure", Process{}, false, platformFailure, false, true},
		{"refused, another start", Process{}, false, &deniedError{pid: 7, startKey: "101", err: refusal}, false, false},
		{"refused, the same start", Process{}, false, &deniedError{pid: 7, startKey: "100", err: refusal}, false, true},
		{"refused, start not readable", Process{}, false, refusal, false, true},
	} {
		running, err := named(id, tc.p, tc.found, tc.err)
		if running != tc.running || (err != nil) != tc.fails {
			t.Errorf("%s: named = %v, %v, want %v and an error %v", tc.name, running, err, tc.running, tc.fails)
		}
	}
}
