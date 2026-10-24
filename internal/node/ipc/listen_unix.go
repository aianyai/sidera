//go:build darwin || linux

package ipc

import (
	"fmt"
	"log/slog"
	"net"
	"os"
)

func (o Options) logger() *slog.Logger {
	if o.Logger != nil {
		return o.Logger
	}
	return slog.New(slog.DiscardHandler)
}

func (o Options) peerUIDFunc() func(*net.UnixConn) (uint32, error) {
	if o.peerUID != nil {
		return o.peerUID
	}
	return peerUID
}

func listen(addr Address, opts Options) (net.Listener, error) {
	files := opts.fileView(addr.home)
	euid := uint32(os.Geteuid())

	if err := checkSocketDirectory(files, addr, euid); err != nil {
		return nil, err
	}
	if err := removeStaleSocket(files, addr, euid); err != nil {
		return nil, err
	}

	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: addr.name, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("ipc: listen on %s: %w", addr.name, err)
	}
	// Not umask: it is process-wide and would change the files other goroutines
	// create. Between listen and chmod the 0700 directory keeps others out.
	if err := os.Chmod(addr.name, 0o600); err != nil {
		l.Close()
		return nil, fmt.Errorf("ipc: make socket %s owner-only: %w", addr.name, err)
	}
	return &guardedListener{
		inner:   l,
		addr:    addr,
		euid:    euid,
		peerUID: opts.peerUIDFunc(),
		log:     opts.logger(),
	}, nil
}

// removeStaleSocket deletes the socket a previous daemon left behind. Listen
// only gets here with the instance lock held, so no other daemon serves the
// directory and whatever socket exists is stale. Anything at the path that is
// not a socket of the current user is not ours to delete, and the error says
// so instead.
func removeStaleSocket(files lstater, addr Address, euid uint32) error {
	err := checkSocketFile(files, addr, euid)
	if err == nil {
		if rerr := os.Remove(addr.name); rerr != nil && !os.IsNotExist(rerr) {
			return fmt.Errorf("ipc: remove stale socket %s: %w", addr.name, rerr)
		}
		return nil
	}
	if pe, ok := err.(*PeerCheckError); ok && pe.Failure == FailSocketMissing {
		return nil
	}
	return err
}

// guardedListener accepts only connections from the current user. The checks
// of the directory and socket modes already keep other users out; this one
// does not depend on them, because Linux documents socket permissions as
// unreliable for security.
//
// The underlying listener is a named field and not embedded: embedding would
// promote AcceptUnix, File and SetUnlinkOnClose, and a caller that reached
// them would accept connections without the check.
type guardedListener struct {
	inner   *net.UnixListener
	addr    Address
	euid    uint32
	peerUID func(*net.UnixConn) (uint32, error)
	log     *slog.Logger
}

// Accept returns the next connection whose peer is the current user. A
// connection from anyone else, or whose peer cannot be determined, is closed
// and logged, and Accept waits for the next one.
func (l *guardedListener) Accept() (net.Conn, error) {
	for {
		c, err := l.inner.AcceptUnix()
		if err != nil {
			return nil, err
		}
		uid, err := l.peerUID(c)
		switch {
		case err != nil:
			l.log.Warn("ipc: closed a connection whose peer credentials could not be read",
				"socket", l.addr.name, "error", err)
		case uid != l.euid:
			l.log.Warn("ipc: closed a connection from another user",
				"socket", l.addr.name, "peer_uid", uid, "want_uid", l.euid)
		default:
			return c, nil
		}
		c.Close()
	}
}

// Close stops the listener and, as net.UnixListener does by default, removes
// the socket file.
func (l *guardedListener) Close() error { return l.inner.Close() }

// Addr is the socket address.
func (l *guardedListener) Addr() net.Addr { return l.inner.Addr() }
