package ipc

import (
	"io/fs"
	"log/slog"
	"net"
)

// Options tune Listen and Dial. The zero value is valid.
type Options struct {
	// Logger receives one line for every connection a listener turns away.
	// Nil discards them. Lines never contain request data.
	Logger *slog.Logger

	// files is where the directory and socket checks look. Nil means the real
	// file system below the data directory. Tests replace it to present a
	// directory that another user owns, which a test cannot create.
	files lstater

	// peerUID reads the user at the other end of a connected Unix socket. Nil
	// means the platform's call. Tests replace it to present another user.
	peerUID func(*net.UnixConn) (uint32, error)

	// pipeServer reads, from the client's handle to a Windows pipe, the owner
	// of the pipe and the user of the process serving it, as SID strings. Nil
	// means the system calls. Tests replace it to present a pipe of another
	// user, which a test cannot create. An error that is not a *PeerCheckError
	// is reported as FailServerQuery.
	pipeServer func(handle uintptr) (owner, user string, err error)
}

// lstater is the part of a read-only view of the data directory that the
// directory and socket checks use: Lstat of a slash-separated path relative to
// the data directory, never following a symbolic link.
type lstater interface {
	Lstat(name string) (fs.FileInfo, error)
}
