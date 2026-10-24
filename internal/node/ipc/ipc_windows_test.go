package ipc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Pipe names and access are requirements, not snapshots: the tests state what
// must hold and print what was measured, so one CI log is enough to fix a
// disagreement.

func ownSID(t *testing.T) *windows.SID {
	t.Helper()
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		t.Fatalf("TokenUser of this process: %v", err)
	}
	return user.User.Sid
}

func listenOn(t *testing.T, home string, kind Kind) Address {
	t.Helper()
	addr, err := Locate(home, kind)
	if err != nil {
		t.Fatalf("Locate(%q, %s): %v", home, kind, err)
	}
	l, err := Listen(addr, heldLock(true), Options{})
	if err != nil {
		t.Fatalf("Listen(%s) on %s: %v", addr.Name(), runtime.GOOS, err)
	}
	serveEcho(t, l)
	return addr
}

// The name is derived from the user's SID and the data directory with an
// independent implementation here, and differs by directory and interface but
// not by letter case.
func TestPipeNameFollowsUserSIDAndDataDirectory(t *testing.T) {
	home := t.TempDir()
	sum := sha256.Sum256([]byte(ownSID(t).String() + "\x00" + strings.ToLower(filepath.Clean(home))))
	wantAgent := `\\.\pipe\sidera-` + hex.EncodeToString(sum[:])[:16] + "-agent"

	agent, err := Locate(home, KindAgent)
	if err != nil {
		t.Fatal(err)
	}
	if agent.Name() != wantAgent {
		t.Fatalf("agent pipe = %s, want %s", agent.Name(), wantAgent)
	}
	manage, _ := Locate(home, KindManage)
	if manage.Name() != strings.TrimSuffix(wantAgent, "-agent")+"-manage" {
		t.Errorf("manage pipe = %s, want the agent name with the suffix -manage", manage.Name())
	}
	upper, _ := Locate(strings.ToUpper(home), KindAgent)
	if upper.Name() != agent.Name() {
		t.Errorf("upper-case spelling gives %s, want %s: NTFS paths are case-insensitive", upper.Name(), agent.Name())
	}
	other, _ := Locate(filepath.Join(home, "other"), KindAgent)
	if other.Name() == agent.Name() {
		t.Errorf("two data directories share the pipe %s", agent.Name())
	}
}

// A second connection after the first must work: go-winio opens a new pipe
// instance for each accept, which needs the descriptor to allow creating
// instances, and nothing else tests that combination.
func TestTwoConsecutiveConnectionsSucceed(t *testing.T) {
	addr := listenOn(t, t.TempDir(), KindAgent)
	for i, msg := range []string{"first", "second", "third"} {
		conn, err := Dial(context.Background(), addr, Options{})
		if err != nil {
			t.Fatalf("connection %d to %s: %v", i+1, addr.Name(), err)
		}
		echoOnce(t, conn, msg)
		conn.Close()
	}
}

func TestConnectionsMayOverlap(t *testing.T) {
	addr := listenOn(t, t.TempDir(), KindManage)
	first, err := Dial(context.Background(), addr, Options{})
	if err != nil {
		t.Fatalf("first connection: %v", err)
	}
	defer first.Close()
	second, err := Dial(context.Background(), addr, Options{})
	if err != nil {
		t.Fatalf("second connection while the first is open: %v", err)
	}
	defer second.Close()
	echoOnce(t, first, "one")
	echoOnce(t, second, "two")
}

func TestDialReportsDaemonNotRunning(t *testing.T) {
	addr, err := Locate(t.TempDir(), KindAgent)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Dial(context.Background(), addr, Options{})
	if err == nil {
		t.Fatalf("Dial succeeded on %s with nobody listening", addr.Name())
	}
	if !errors.Is(err, ErrDaemonNotRunning) || errors.Is(err, ErrPeerCheckFailed) {
		t.Fatalf("Dial on %s = %v, want ErrDaemonNotRunning and not a failed check", addr.Name(), err)
	}
}

// The client side of the check: from its own handle the client can learn the
// server's process and that process's user. The server runs in this process,
// so the process id must be ours and the user must be ours.
func TestClientCanReadTheServerProcessAndItsTokenUser(t *testing.T) {
	addr := listenOn(t, t.TempDir(), KindAgent)
	conn, err := Dial(context.Background(), addr, Options{})
	if err != nil {
		t.Fatalf("Dial(%s): %v", addr.Name(), err)
	}
	defer conn.Close()

	fder, ok := conn.(interface{ Fd() uintptr })
	if !ok {
		t.Fatalf("%T does not expose Fd(); the server check cannot work", conn)
	}
	var pid uint32
	if err := windows.GetNamedPipeServerProcessId(windows.Handle(fder.Fd()), &pid); err != nil {
		t.Fatalf("GetNamedPipeServerProcessId on a client handle: %v", err)
	}
	t.Logf("server process id from the client handle: %d (this process: %d)", pid, os.Getpid())
	if int(pid) != os.Getpid() {
		t.Fatalf("server process id = %d, want %d", pid, os.Getpid())
	}

	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		t.Fatalf("OpenProcess(%d): %v", pid, err)
	}
	defer windows.CloseHandle(process)
	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		t.Fatalf("OpenProcessToken(TOKEN_QUERY) of process %d: %v", pid, err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		t.Fatalf("TokenUser of process %d: %v", pid, err)
	}
	t.Logf("TokenUser of the server process: %s", user.User.Sid)
	if !windows.EqualSid(user.User.Sid, ownSID(t)) {
		t.Fatalf("TokenUser of the server = %s, want %s", user.User.Sid, ownSID(t))
	}
}

// The pipe is owned by the user and its list of allowed principals holds the
// user alone, whether or not the process runs elevated; this is the
// requirement the explicit owner in the descriptor exists for. The measured
// values are logged.
func TestPipeIsOwnedByAndGrantedToTheUserOnly(t *testing.T) {
	addr := listenOn(t, t.TempDir(), KindAgent)
	conn, err := Dial(context.Background(), addr, Options{})
	if err != nil {
		t.Fatalf("Dial(%s): %v", addr.Name(), err)
	}
	defer conn.Close()
	handle := windows.Handle(conn.(interface{ Fd() uintptr }).Fd())

	sd, err := windows.GetSecurityInfo(handle, windows.SE_KERNEL_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("GetSecurityInfo on the pipe %s: %v", addr.Name(), err)
	}
	t.Logf("security descriptor of the pipe: %s", sd.String())

	owner, _, err := sd.Owner()
	if err != nil {
		t.Fatal(err)
	}
	if !windows.EqualSid(owner, ownSID(t)) {
		t.Errorf("pipe owner = %s, want the user %s", owner, ownSID(t))
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		t.Fatalf("DACL of the pipe: %v, %v", dacl, err)
	}
	if dacl.AceCount != 1 {
		t.Fatalf("the DACL has %d entries, want 1 (the user)", dacl.AceCount)
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil {
		t.Fatal(err)
	}
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
		t.Errorf("the entry has type %d, want an allow entry", ace.Header.AceType)
	}
	if sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)); !windows.EqualSid(sid, ownSID(t)) {
		t.Errorf("the entry is for %s, want the user %s", sid, ownSID(t))
	}
}

// Needs a second local user, which a CI job does not have. It is listed as an
// untested item in the reference.
func TestClientRefusesAPipeServedByAnotherUser(t *testing.T) {
	t.Skip("needs a second local user to create the pipe first; listed as untested in docs/references/local-ipc")
}
