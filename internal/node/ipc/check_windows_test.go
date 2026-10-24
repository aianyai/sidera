package ipc

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"testing"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// The client's check of the pipe is exercised on a real pipe of this user and,
// for every way it must refuse, with the owner and the server user presented by
// the unexported seam: a test cannot create a pipe of another user.

// anotherUser is the SID of the built-in Administrators group, which is never
// the TokenUser or the default owner of a pipe this test creates.
const anotherUser = "S-1-5-32-544"

// presenting makes the check see the given owner and server user. Both are
// the current user's unless the test changes one of them.
func presenting(t *testing.T, owner, user string, err error) (Options, *int) {
	t.Helper()
	calls := new(int)
	return Options{pipeServer: func(uintptr) (string, string, error) {
		*calls++
		return owner, user, err
	}}, calls
}

func TestCheckAcceptsThePipeOfTheCurrentUser(t *testing.T) {
	addr := listenOn(t, t.TempDir(), KindAgent)
	conn, err := winio.DialPipeContext(context.Background(), addr.Name())
	if err != nil {
		t.Fatalf("open %s: %v", addr.Name(), err)
	}
	defer conn.Close()
	if err := checkPipeServerIsCurrentUser(conn, addr, Options{}); err != nil {
		t.Fatalf("check of the user's own pipe %s on %s: %v", addr.Name(), runtime.GOOS, err)
	}
}

func TestClientRefusesAPipeOwnedByAnotherUser(t *testing.T) {
	addr := listenOn(t, t.TempDir(), KindAgent)
	opts, calls := presenting(t, anotherUser, ownSID(t).String(), nil)
	conn, err := Dial(context.Background(), addr, opts)
	if err == nil {
		conn.Close()
		t.Fatalf("Dial succeeded on %s although the pipe is owned by %s", addr.Name(), anotherUser)
	}
	assertPeerCheck(t, err, FailPipeOwner)
	if *calls != 1 {
		t.Fatalf("the seam was used %d time(s), the injection point was not hit exactly once", *calls)
	}
}

// The owner is the first line, but the server's token user is still required:
// a pipe owned by the user and served from another account must be refused.
func TestClientRefusesAServerRunningAsAnotherUser(t *testing.T) {
	addr := listenOn(t, t.TempDir(), KindAgent)
	opts, calls := presenting(t, ownSID(t).String(), anotherUser, nil)
	conn, err := Dial(context.Background(), addr, opts)
	if err == nil {
		conn.Close()
		t.Fatalf("Dial succeeded on %s although the server runs as %s", addr.Name(), anotherUser)
	}
	assertPeerCheck(t, err, FailServerSID)
	if *calls != 1 {
		t.Fatalf("the seam was used %d time(s), the injection point was not hit exactly once", *calls)
	}
}

func TestClientFailsClosedWhenThePipeServerCannotBeRead(t *testing.T) {
	cause := errors.New("access is denied")
	for _, tc := range []struct {
		name  string
		owner string
		user  string
		err   error
		want  Failure
	}{
		{"an error no step names", "", "", cause, FailServerQuery},
		{"an error of a named step", "", "", &PeerCheckError{Failure: FailServerTokenOpen, Err: cause}, FailServerTokenOpen},
		{"an empty owner", "", ownSID(t).String(), nil, FailPipeOwner},
		{"an empty server user", ownSID(t).String(), "", nil, FailServerSID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			addr := listenOn(t, t.TempDir(), KindAgent)
			opts, calls := presenting(t, tc.owner, tc.user, tc.err)
			conn, err := Dial(context.Background(), addr, opts)
			if err == nil {
				conn.Close()
				t.Fatalf("Dial succeeded on %s although the server could not be verified", addr.Name())
			}
			assertPeerCheck(t, err, tc.want)
			var pe *PeerCheckError
			if errors.As(err, &pe) && pe.Path != addr.Name() {
				t.Errorf("the error names the path %q, want the pipe %q", pe.Path, addr.Name())
			}
			if *calls != 1 {
				t.Fatalf("the seam was used %d time(s), the injection point was not hit exactly once", *calls)
			}
		})
	}
}

// A refusal of access when opening the pipe means another process created the
// name: the daemon's own pipe always grants the user. It must read as a failed
// check, not as a daemon that is not running.
func TestClientReportsAPipeThatDeniesTheUserAsAFailedCheck(t *testing.T) {
	addr, err := Locate(t.TempDir(), KindAgent)
	if err != nil {
		t.Fatal(err)
	}
	// Owned by the user, allowed to SYSTEM only: the user is not in the list.
	sddl := fmt.Sprintf("O:%sD:P(A;;GA;;;SY)", ownSID(t))
	l, err := winio.ListenPipe(addr.Name(), &winio.PipeConfig{SecurityDescriptor: sddl})
	if err != nil {
		t.Fatalf("create the pipe %s with descriptor %s: %v", addr.Name(), sddl, err)
	}
	defer l.Close()

	conn, err := Dial(context.Background(), addr, Options{})
	if err == nil {
		conn.Close()
		t.Fatalf("Dial succeeded on %s with descriptor %s, want access denied", addr.Name(), sddl)
	}
	t.Logf("Dial on a pipe that denies the user: %v", err)
	assertPeerCheck(t, err, FailAccessDenied)
}

func TestOpenErrorClassification(t *testing.T) {
	addr := Address{kind: KindAgent, name: `\\.\pipe\sidera-test-agent`}
	missing := openError(addr, windows.ERROR_FILE_NOT_FOUND)
	if !errors.Is(missing, ErrDaemonNotRunning) || errors.Is(missing, ErrPeerCheckFailed) {
		t.Errorf("a missing pipe = %v, want ErrDaemonNotRunning and not a failed check", missing)
	}
	assertPeerCheck(t, openError(addr, windows.ERROR_ACCESS_DENIED), FailAccessDenied)
	other := openError(addr, windows.ERROR_BROKEN_PIPE)
	if errors.Is(other, ErrDaemonNotRunning) || errors.Is(other, ErrPeerCheckFailed) {
		t.Errorf("an unrelated error = %v, want neither sentinel", other)
	}
}
