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
		return nil, openError(addr, err)
	}
	if err := checkPipeServerIsCurrentUser(conn, addr, opts); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

// openError classifies a failure to open the pipe. A missing pipe means nobody
// serves the name. A refusal of access is a failed check, never "not running":
// the pipe the daemon creates always grants the user, so a refusal means
// another process created the name first.
func openError(addr Address, err error) error {
	switch {
	case errors.Is(err, windows.ERROR_FILE_NOT_FOUND):
		return notRunning(addr, err)
	case errors.Is(err, windows.ERROR_ACCESS_DENIED):
		return accessDenied(addr, err)
	}
	return fmt.Errorf("ipc: dial pipe %s: %w", addr.name, err)
}

// checkPipeServerIsCurrentUser refuses a pipe that was not created by the
// current user. Without it, another local user could create the pipe while the
// daemon is down and collect the token the client is about to send. Every step
// that fails fails the check: the client sends nothing it could not verify the
// receiver of.
//
// Two lines, in this order. The owner of the pipe is the one that holds: the
// server process id the system reports can be stale, because a process may hand
// its pipe handle to another and exit, and its id can then be reused by a
// process an attacker controls; an unprivileged process cannot make another
// user the owner of an object it creates, and the daemon's descriptor names the
// user as owner explicitly. The token user of the reported server process is
// the second line, which would catch a pipe owned by the user but served from
// a different account's process.
func checkPipeServerIsCurrentUser(conn net.Conn, addr Address, opts Options) error {
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

	read := opts.pipeServer
	if read == nil {
		read = readPipeServer
	}
	owner, user, err := read(handle.Fd())
	if err != nil {
		var pe *PeerCheckError
		if errors.As(err, &pe) {
			checked := *pe
			checked.Path = addr.name
			return &checked
		}
		return fail(FailServerQuery, "cannot read the owner and the server of the pipe", err)
	}

	if owner != own.String() {
		return fail(FailPipeOwner, fmt.Sprintf("the pipe is owned by %s, want the current user %s", owner, own.String()), nil)
	}
	if user != own.String() {
		return fail(FailServerSID, fmt.Sprintf("the server process runs as %s, want the current user %s", user, own.String()), nil)
	}
	return nil
}

// readPipeServer reads the owner of the pipe and the user of the process that
// serves it, from the client's handle. Its errors are *PeerCheckError without a
// path; the caller adds it.
func readPipeServer(handle uintptr) (owner, user string, err error) {
	step := func(f Failure, detail string, err error) error {
		return &PeerCheckError{Failure: f, Detail: detail, Err: err}
	}
	h := windows.Handle(handle)

	sd, err := windows.GetSecurityInfo(h, windows.SE_KERNEL_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return "", "", step(FailPipeOwner, "GetSecurityInfo of the pipe failed", err)
	}
	ownerSID, _, err := sd.Owner()
	if err != nil || ownerSID == nil {
		return "", "", step(FailPipeOwner, "the pipe has no readable owner", err)
	}
	owner = ownerSID.String()

	var pid uint32
	if err := windows.GetNamedPipeServerProcessId(h, &pid); err != nil {
		return "", "", step(FailServerProcessID, "GetNamedPipeServerProcessId failed", err)
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", "", step(FailServerProcessOpen, fmt.Sprintf("OpenProcess of server process %d failed", pid), err)
	}
	defer windows.CloseHandle(process)

	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		return "", "", step(FailServerTokenOpen, fmt.Sprintf("OpenProcessToken of server process %d failed", pid), err)
	}
	defer token.Close()

	tokenUser, err := token.GetTokenUser()
	if err != nil {
		return "", "", step(FailServerTokenUser, fmt.Sprintf("reading TokenUser of server process %d failed", pid), err)
	}
	return owner, tokenUser.User.Sid.String(), nil
}
