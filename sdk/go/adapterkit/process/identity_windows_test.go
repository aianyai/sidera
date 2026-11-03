package process

import (
	"errors"
	"strconv"
	"testing"

	"golang.org/x/sys/windows"
)

// Only "no process holds the PID" is gone. A refusal is a process that runs beyond
// the adapter's reach and an unknown failure is no answer; taking either for gone
// starts a second writer beside one that runs.
func TestClassifyOpen(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want openOutcome
	}{
		{"opened", nil, opened},
		{"invalid parameter", windows.ERROR_INVALID_PARAMETER, absent},
		{"access denied", windows.ERROR_ACCESS_DENIED, refused},
		{"wrapped access denied", &wrapped{windows.ERROR_ACCESS_DENIED}, refused},
		{"anything else", windows.ERROR_NOT_ENOUGH_MEMORY, failed},
	} {
		if got := classifyOpen(tc.err); got != tc.want {
			t.Errorf("%s: classifyOpen = %d, want %d", tc.name, got, tc.want)
		}
	}
}

type wrapped struct{ err error }

func (w *wrapped) Error() string { return "wrapped: " + w.err.Error() }
func (w *wrapped) Unwrap() error { return w.err }

// A process that cannot be opened carries the start the system reports for it, and
// one the system no longer lists has ended. If the system cannot be asked the start
// is unknown and the answer stays an error.
func TestLookupOfAProcessThatCannotBeOpened(t *testing.T) {
	refusal := windows.ERROR_ACCESS_DENIED
	_, found, err := lookupRefused(9, refusal, func(int) (uint64, bool, error) { return 42, true, nil })
	var d *deniedError
	if found || !errors.As(err, &d) || d.startKey != "42" || !errors.Is(err, refusal) {
		t.Errorf("a listed process: found %v, %v, want the refusal carrying the start 42", found, err)
	}
	if _, found, err := lookupRefused(9, refusal, func(int) (uint64, bool, error) { return 0, false, nil }); found || err != nil {
		t.Errorf("a process the system does not list: found %v, %v, want not found", found, err)
	}
	_, found, err = lookupRefused(9, refusal, func(int) (uint64, bool, error) { return 0, false, errors.New("boom") })
	if found || err == nil || errors.As(err, &d) {
		t.Errorf("no answer from the system: found %v, %v, want a plain error", found, err)
	}
}

// The System process (PID 4) cannot be opened with the access a lookup asks for,
// and its start is read from the system all the same. On a host that lets it be
// opened this has nothing to show.
func TestSystemProcessIsToldApartByItsStart(t *testing.T) {
	if h, err := windows.OpenProcess(openAccess, false, 4); err == nil {
		windows.CloseHandle(h)
		t.Skip("the System process can be opened here")
	}
	ft, listed, err := systemStart(4)
	if err != nil || !listed || ft == 0 {
		t.Fatalf("systemStart(4) = %d, %v, %v", ft, listed, err)
	}
	key := strconv.FormatUint(ft, 10)
	if running, err := Running(Identity{PID: 4, StartKey: key + "1"}); running || err != nil {
		t.Fatalf("Running with another start = %v, %v, want false and no error", running, err)
	}
	if running, err := Running(Identity{PID: 4, StartKey: key}); running || err == nil {
		t.Fatalf("Running with the same start = %v, %v, want an error", running, err)
	}
}

// The system's table agrees with the handle on the start of a process that can be
// opened, which checks the layout of the entries it is read from.
func TestSystemStartMatchesTheHandle(t *testing.T) {
	pid := int(windows.GetCurrentProcessId())
	self, found, err := lookup(pid, false)
	if err != nil || !found {
		t.Fatalf("lookup(self) = %+v, %v, %v", self, found, err)
	}
	ft, listed, err := systemStart(pid)
	if err != nil || !listed || strconv.FormatUint(ft, 10) != self.StartKey {
		t.Fatalf("systemStart(self) = %d, %v, %v, want %s", ft, listed, err, self.StartKey)
	}
	if _, listed, err := systemStart(1 << 22); listed || err != nil {
		t.Fatalf("systemStart of no process = %v, %v", listed, err)
	}
}

func TestUnlistable(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"access denied", windows.ERROR_ACCESS_DENIED, true},
		{"denied as a status", windows.STATUS_ACCESS_DENIED, true},
		{"anything else, no handle to say it ended", windows.ERROR_NOT_ENOUGH_MEMORY, false},
	} {
		if got := unlistable(0, tc.err); got != tc.want {
			t.Errorf("%s: unlistable = %v, want %v", tc.name, got, tc.want)
		}
	}
}
