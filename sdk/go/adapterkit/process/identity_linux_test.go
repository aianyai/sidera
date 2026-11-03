package process

import (
	"os"
	"syscall"
	"testing"
)

// The command name of /proc/<pid>/stat is free text in parentheses; the fields are
// counted from its last ')'.
func TestParseStatCountsFromTheLastParenthesis(t *testing.T) {
	const tail = " S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 424242 20 21"
	for _, tc := range []struct {
		name, stat string
	}{
		{"plain", "123 (bash)" + tail},
		{"spaces", "123 (my prog 1 2 3)" + tail},
		{"parentheses", "123 (a) (b) c)" + tail},
		{"empty name", "123 ()" + tail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, ticks, err := parseStat([]byte(tc.stat + "\n"))
			if err != nil || state != 'S' || ticks != 424242 {
				t.Fatalf("parseStat = %q, %d, %v, want 'S', 424242", state, ticks, err)
			}
		})
	}
	for _, bad := range []string{"", "123 bash S 1", "123 (x) S 1 2", "123 (x) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 late 20"} {
		if _, _, err := parseStat([]byte(bad)); err == nil {
			t.Errorf("parseStat(%q) did not fail", bad)
		}
	}
}

func TestEffectiveUID(t *testing.T) {
	const status = "Name:\tbash\nState:\tS (sleeping)\nUid:\t1000\t1001\t1002\t1003\nGid:\t1000\t1000\t1000\t1000\n"
	if uid, err := effectiveUID([]byte(status)); err != nil || uid != 1001 {
		t.Fatalf("effectiveUID = %d, %v, want 1001, the second field", uid, err)
	}
	for _, bad := range []string{"", "Name:\tx\n", "Uid:\t1000\n", "Uid:\t1000\tme\t1\t1\n"} {
		if _, err := effectiveUID([]byte(bad)); err == nil {
			t.Errorf("effectiveUID(%q) did not fail", bad)
		}
	}
}

// A process that may not be read is an error and never "not found": taking it for
// gone starts a second writer beside one that runs. Only a process that is not
// there is not found.
func TestReadClassifiesWhatTheKernelAnswers(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		found bool // the read of a missing process, not an error
		fails bool
	}{
		{"no such file", &os.PathError{Op: "open", Path: "/proc/1/stat", Err: syscall.ENOENT}, false, false},
		{"no such process", &os.PathError{Op: "read", Path: "/proc/1/stat", Err: syscall.ESRCH}, false, false},
		{"permission denied", &os.PathError{Op: "open", Path: "/proc/1/stat", Err: syscall.EACCES}, false, true},
		{"operation not permitted", &os.PathError{Op: "open", Path: "/proc/1/stat", Err: syscall.EPERM}, false, true},
		{"i/o error", &os.PathError{Op: "read", Path: "/proc/1/stat", Err: syscall.EIO}, false, true},
	} {
		_, found, err := readWith(func(string) ([]byte, error) { return nil, tc.err }, 1, false)
		if found || (err != nil) != tc.fails {
			t.Errorf("%s: read = found %v, error %v, want an error: %v", tc.name, found, err, tc.fails)
		}
	}
}
