//go:build unix

package ipc

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"syscall"
)

func dial(ctx context.Context, addr Address, opts Options) (net.Conn, error) {
	files := opts.fileView(addr.home)
	euid := uint32(os.Geteuid())

	if err := checkSocketDirectory(files, addr, euid); err != nil {
		return nil, missingMeansNotRunning(addr, err)
	}
	if err := checkSocketFile(files, addr, euid); err != nil {
		return nil, missingMeansNotRunning(addr, err)
	}

	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", addr.name)
	if err != nil {
		if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, fs.ErrNotExist) {
			return nil, notRunning(addr, err)
		}
		return nil, fmt.Errorf("ipc: dial %s: %w", addr.name, err)
	}

	// The second check of the same thing: the process that accepted is the
	// user's, whatever the checks of the files said a moment ago.
	uid, err := opts.peerUIDFunc()(conn.(*net.UnixConn))
	if err != nil {
		conn.Close()
		return nil, &PeerCheckError{Failure: FailServerCredentials, Path: addr.name,
			Detail: "cannot read the credentials of the process serving the socket", Err: err}
	}
	if uid != euid {
		conn.Close()
		return nil, &PeerCheckError{Failure: FailServerUID, Path: addr.name,
			Detail: fmt.Sprintf("the socket is served by uid %d, want the current user (uid %d)", uid, euid)}
	}
	return conn, nil
}

// missingMeansNotRunning turns "the directory or socket does not exist" into
// ErrDaemonNotRunning and leaves every other failed check as it is.
func missingMeansNotRunning(addr Address, err error) error {
	var pe *PeerCheckError
	if errors.As(err, &pe) && (pe.Failure == FailDirectoryMissing || pe.Failure == FailSocketMissing) {
		return notRunning(addr, pe.Err)
	}
	return err
}
