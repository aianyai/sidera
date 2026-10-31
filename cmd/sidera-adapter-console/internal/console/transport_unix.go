//go:build !windows

package console

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
)

// On Unix the control channel is a socket in the state directory. The daemon made
// that directory reachable by its owner alone, which is what keeps other users
// out; the socket is also 0600, set right after it is bound, while the directory
// still shuts out everyone else.

const socketName = "console.sock"

// listenLocal serves the control channel of the instance. The adapter holds the
// instance lock, so no other process serves this directory and a socket left
// there is stale.
func listenLocal(stateDir string) (net.Listener, error) {
	path := filepath.Join(stateDir, socketName)
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("%s is not a socket and is left alone", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("removing the stale socket: %w", err)
		}
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listening on %s: %w%s", path, err, pathHint(path))
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = l.Close()
		return nil, fmt.Errorf("making %s owner-only: %w", path, err)
	}
	return l, nil
}

// dialLocal connects to the control channel of the instance.
func dialLocal(ctx context.Context, stateDir string) (net.Conn, error) {
	path := filepath.Join(stateDir, socketName)
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	switch {
	case errors.Is(err, os.ErrNotExist), errors.Is(err, syscall.ECONNREFUSED):
		return nil, errNotRunning
	case err != nil:
		return nil, fmt.Errorf("connecting to %s: %w%s", path, err, pathHint(path))
	}
	return conn, nil
}

// pathHint explains the failure a long path causes: a Unix socket address holds
// about a hundred bytes.
func pathHint(path string) string {
	if len(path) > 100 {
		return fmt.Sprintf(" (the path is %d bytes and a Unix socket address holds about 100: use a shorter data directory)", len(path))
	}
	return ""
}
