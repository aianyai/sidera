package ipc

import (
	"errors"
	"strings"
	"testing"
)

func TestSocketPathLengthLimit(t *testing.T) {
	for _, tc := range []struct {
		name    string
		length  int
		limit   int
		tooLong bool
	}{
		{"macOS accepts 103 bytes", 103, darwinSocketPathLimit, false},
		{"macOS refuses 104 bytes", 104, darwinSocketPathLimit, true},
		{"Linux accepts 107 bytes", 107, linuxSocketPathLimit, false},
		{"Linux refuses 108 bytes", 108, linuxSocketPathLimit, true},
		{"a short path is accepted", 20, darwinSocketPathLimit, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := "/" + strings.Repeat("a", tc.length-1)
			err := checkSocketPathLength(path, tc.limit)
			if !tc.tooLong {
				if err != nil {
					t.Fatalf("path of %d bytes with limit %d: %v", tc.length, tc.limit, err)
				}
				return
			}
			var pe *PathTooLongError
			if !errors.As(err, &pe) {
				t.Fatalf("path of %d bytes with limit %d: got %v, want *PathTooLongError", tc.length, tc.limit, err)
			}
			if pe.Path != path || pe.Length != tc.length || pe.Limit != tc.limit {
				t.Fatalf("error carries %q %d %d, want the path, %d and %d", pe.Path, pe.Length, pe.Limit, tc.length, tc.limit)
			}
		})
	}
	if darwinSocketPathLimit != 103 || linuxSocketPathLimit != 107 {
		t.Fatalf("limits are %d and %d, want 103 and 107", darwinSocketPathLimit, linuxSocketPathLimit)
	}
}

func TestPathTooLongMessageNamesPathLengthAndLimit(t *testing.T) {
	msg := (&PathTooLongError{Path: "/p/q", Length: 4, Limit: 3}).Error()
	for _, want := range []string{`"/p/q"`, "4 bytes", "limit on this platform is 3"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message %q lacks %q", msg, want)
		}
	}
}

// Known-answer vectors, computed outside Go with
// printf '<sid>\0<dir>' | shasum -a 256 and cut to 16 hex characters.
func TestPipeHashKnownAnswers(t *testing.T) {
	const alice = "S-1-5-21-1004336348-1177238915-682003330-1001"
	const aliceOther = "S-1-5-21-1004336348-1177238915-682003330-1002"
	for _, tc := range []struct {
		name, sid, home, want string
	}{
		{"user and directory", alice, `c:\users\alice\.sidera`, "acdfba2228c93641"},
		{"another user, same directory string", aliceOther, `c:\users\alice\.sidera`, "30ccc91c00f64297"},
		{"same user, another directory", alice, `c:\users\bob\.sidera`, "d07104d3a0b740b8"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := pipeHash(tc.sid, tc.home); got != tc.want {
				t.Fatalf("pipeHash(%q, %q) = %s, want %s", tc.sid, tc.home, got, tc.want)
			}
		})
	}
}

func TestPipeNames(t *testing.T) {
	if got, want := pipeName("acdfba2228c93641", KindAgent), `\\.\pipe\sidera-acdfba2228c93641-agent`; got != want {
		t.Errorf("agent pipe = %s, want %s", got, want)
	}
	if got, want := pipeName("acdfba2228c93641", KindManage), `\\.\pipe\sidera-acdfba2228c93641-manage`; got != want {
		t.Errorf("manage pipe = %s, want %s", got, want)
	}
}

// The descriptor names the user as owner and as the only allowed principal,
// protected from inheritance, written out and not with the owner shorthand.
func TestPipeSDDLGrantsOnlyTheUser(t *testing.T) {
	const sid = "S-1-5-21-1-2-3-1001"
	want := "O:S-1-5-21-1-2-3-1001D:P(A;;GA;;;S-1-5-21-1-2-3-1001)"
	got := pipeSDDL(sid)
	if got != want {
		t.Fatalf("pipeSDDL = %s, want %s", got, want)
	}
	if strings.Contains(got, "OW") {
		t.Fatal("the descriptor must not use the owner-rights shorthand")
	}
}

func TestCanonicalPipeHomeIsAbsoluteCleanAndLowerCase(t *testing.T) {
	// The rule is the absolute path made clean and lower case, which is what
	// Locate puts into the hash.
	canonicalPipeHome := func(home string) (string, error) {
		abs, err := absoluteHome(home)
		if err != nil {
			return "", err
		}
		return pipeHomeKey(abs), nil
	}
	for _, tc := range []struct{ name, in, want string }{
		{"upper case", "/Users/Alice/.Sidera", "/users/alice/.sidera"},
		{"dot segments and trailing slash", "/a/./B/../C/", "/a/c"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := canonicalPipeHome(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			// On Windows the result carries a drive letter; the tail is the rule.
			if !strings.HasSuffix(strings.ReplaceAll(got, `\`, "/"), tc.want) {
				t.Fatalf("canonicalPipeHome(%q) = %q, want it to end with %q", tc.in, got, tc.want)
			}
		})
	}
	if _, err := canonicalPipeHome(""); err == nil {
		t.Fatal("an empty data directory must be refused, not read as the current directory")
	}
}

func TestPeerCheckErrorMatchesItsSentinelAndKeepsItsCause(t *testing.T) {
	cause := errors.New("boom")
	err := error(&PeerCheckError{Failure: FailDirectoryMode, Path: "/x/run", Detail: "mode 0755, want 0700", Err: cause})
	if !errors.Is(err, ErrPeerCheckFailed) || !errors.Is(err, cause) {
		t.Fatalf("%v: must match ErrPeerCheckFailed and its cause", err)
	}
	if errors.Is(err, ErrDaemonNotRunning) {
		t.Fatal("a failed check must not match ErrDaemonNotRunning")
	}
	for _, want := range []string{"/x/run", "directory_mode", "0755", "boom"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("message %q lacks %q", err, want)
		}
	}
}

func TestListenNeedsAnAddressFromLocate(t *testing.T) {
	if _, err := Listen(Address{}, heldLock(true), Options{}); err == nil {
		t.Fatal("Listen accepted the zero Address")
	}
}

func TestListenNeedsTheInstanceLock(t *testing.T) {
	addr, err := Locate(testHome(t), KindAgent)
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	for _, tc := range []struct {
		name string
		lock InstanceLock
	}{
		{"no lock", nil},
		{"a released lock", heldLock(false)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, err := Listen(addr, tc.lock, Options{})
			if err == nil {
				l.Close()
				t.Fatal("Listen succeeded without the instance lock")
			}
			if !errors.Is(err, ErrLockNotHeld) {
				t.Fatalf("got %v, want ErrLockNotHeld", err)
			}
		})
	}
}
