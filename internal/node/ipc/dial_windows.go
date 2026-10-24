package ipc

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

func dial(ctx context.Context, addr Address, opts Options) (net.Conn, error) {
	conn, err := winio.DialPipeContext(ctx, addr.name)
	if err != nil {
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			return nil, notRunning(addr, err)
		}
		return nil, fmt.Errorf("ipc: dial pipe %s: %w", addr.name, err)
	}
	if err := checkPipeServerIsCurrentUser(conn, addr); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

// checkPipeServerIsCurrentUser refuses a pipe whose server process does not run
// as the current user. Without it, another local user could create the pipe
// while the daemon is down and collect the token the client is about to send.
// Every step that fails fails the check: the client sends nothing it could not
// verify the receiver of.
func checkPipeServerIsCurrentUser(conn net.Conn, addr Address) error {
	fail := func(f Failure, detail string, err error) error {
		return &PeerCheckError{Failure: f, Path: addr.name, Detail: detail, Err: err}
	}

	own, err := currentUserSID()
	if err != nil {
		return fail(FailOwnSID, "cannot determine the current user's SID", err)
	}

	handle, ok := conn.(interface{ Fd() uintptr })
	if !ok {
		return fail(FailServerProcessID, "the pipe connection does not expose its handle", nil)
	}
	var pid uint32
	if err := windows.GetNamedPipeServerProcessId(windows.Handle(handle.Fd()), &pid); err != nil {
		return fail(FailServerProcessID, "GetNamedPipeServerProcessId failed", err)
	}

	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return fail(FailServerProcessOpen, fmt.Sprintf("OpenProcess of server process %d failed", pid), err)
	}
	defer windows.CloseHandle(process)

	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		return fail(FailServerTokenOpen, fmt.Sprintf("OpenProcessToken of server process %d failed", pid), err)
	}
	defer token.Close()

	user, err := token.GetTokenUser()
	if err != nil {
		return fail(FailServerTokenUser, fmt.Sprintf("reading TokenUser of server process %d failed", pid), err)
	}
	if !windows.EqualSid(user.User.Sid, own) {
		return fail(FailServerSID, fmt.Sprintf("server process %d runs as %s, want the current user %s",
			pid, user.User.Sid.String(), own.String()), nil)
	}
	return nil
}
