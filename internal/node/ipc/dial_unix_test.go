//go:build unix

package ipc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A client sends its request only to a socket whose directory is closed to
// everyone but the user; otherwise another user could have replaced the socket.
func TestClientRefusesSocketDirectoryThatIsNotOwnerOnly0700(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode os.FileMode
	}{
		{"group and others may read and search 0755", 0o755},
		{"group may read and search 0750", 0o750},
		{"group may write 0770", 0o770},
		{"others may search 0701", 0o701},
		{"owner may not write 0500", 0o500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := shortHome(t)
			mkdirExact(t, filepath.Join(home, "run"), 0o700)
			srv := serveCounting(t, filepath.Join(home, "run", "agent.sock"))
			if err := os.Chmod(filepath.Join(home, "run"), tc.mode); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chmod(filepath.Join(home, "run"), 0o700) })

			addr, err := Locate(home, KindAgent)
			if err != nil {
				t.Fatalf("Locate(%q): %v", home, err)
			}
			conn, err := Dial(context.Background(), addr, Options{})
			if err == nil {
				conn.Close()
				t.Fatalf("Dial succeeded although the socket directory has mode %04o", tc.mode)
			}
			assertPeerCheck(t, err, FailDirectoryMode)
			if n := srv.connectionsBeforeNow(t); n != 0 {
				t.Fatalf("the client connected %d time(s) before refusing", n)
			}
		})
	}
}

func TestClientRefusesSocketDirectoryNotOwnedByUser(t *testing.T) {
	home := shortHome(t)
	mkdirExact(t, filepath.Join(home, "run"), 0o700)
	srv := serveCounting(t, filepath.Join(home, "run", "agent.sock"))

	addr, err := Locate(home, KindAgent)
	if err != nil {
		t.Fatalf("Locate(%q): %v", home, err)
	}
	conn, err := Dial(context.Background(), addr, Options{files: foreignOwner{root: home}})
	if err == nil {
		conn.Close()
		t.Fatal("Dial succeeded although the socket directory belongs to another user")
	}
	assertPeerCheck(t, err, FailDirectoryOwner)
	if n := srv.connectionsBeforeNow(t); n != 0 {
		t.Fatalf("the client connected %d time(s) before refusing", n)
	}
}

// assertPeerCheck requires a failed check, and not "daemon not running", which
// would send the user to start a daemon that someone else may have displaced.
func assertPeerCheck(t *testing.T, err error, want Failure) {
	t.Helper()
	if errors.Is(err, ErrDaemonNotRunning) {
		t.Fatalf("got %q, a failed check must not read as daemon-not-running", err)
	}
	if !errors.Is(err, ErrPeerCheckFailed) {
		t.Fatalf("error %q does not match ErrPeerCheckFailed", err)
	}
	var pe *PeerCheckError
	if !errors.As(err, &pe) {
		t.Fatalf("error %q is not a *PeerCheckError", err)
	}
	if pe.Failure != want {
		t.Fatalf("failure = %q, want %q (%v)", pe.Failure, want, err)
	}
}
