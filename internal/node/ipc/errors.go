package ipc

import (
	"errors"
	"fmt"
	"runtime"
)

var (
	// ErrDaemonNotRunning means nothing is listening at the address: the socket
	// or pipe does not exist, or nobody answers on it. It is deliberately not
	// returned for a failed check, which may mean someone else took the place.
	ErrDaemonNotRunning = errors.New("ipc: the daemon is not running")

	// ErrPeerCheckFailed matches every *PeerCheckError.
	ErrPeerCheckFailed = errors.New("ipc: peer check failed")

	// ErrLockNotHeld is returned by Listen when the caller cannot show that it
	// holds the data directory's instance lock.
	ErrLockNotHeld = errors.New("ipc: the instance lock is not held")
)

// Failure says which check failed, as a stable machine-readable value.
type Failure string

const (
	FailDirectoryMissing    Failure = "directory_missing"
	FailDirectoryNotDir     Failure = "directory_not_directory"
	FailDirectorySymlink    Failure = "directory_symlink"
	FailDirectoryOwner      Failure = "directory_owner"
	FailDirectoryMode       Failure = "directory_mode"
	FailSocketMissing       Failure = "socket_missing"
	FailSocketNotSocket     Failure = "socket_not_socket"
	FailSocketOwner         Failure = "socket_owner"
	FailStat                Failure = "stat_failed"
	FailServerCredentials   Failure = "server_credentials"
	FailServerUID           Failure = "server_uid"
	FailOwnSID              Failure = "own_sid"
	FailServerProcessID     Failure = "server_process_id"
	FailServerProcessOpen   Failure = "server_process_open"
	FailServerTokenOpen     Failure = "server_token_open"
	FailServerTokenUser     Failure = "server_token_user"
	FailServerSID           Failure = "server_sid"
	FailPipeHandle          Failure = "pipe_handle"
	FailListenerNotOwner    Failure = "listener_not_owner"
	FailStaleSocketNotOurs  Failure = "stale_socket_not_ours"
	FailUnsupportedPlatform Failure = "unsupported_platform"
)

// PeerCheckError reports that the path to an interface or the process at its
// other end is not what the current user must be able to rely on. A client
// that gets one must not send its request; a listener that gets one must not
// start. Callers report it as unavailable with the reason peer_check_failed.
type PeerCheckError struct {
	Failure Failure // which check
	Path    string  // the directory, socket or pipe the check was about
	Detail  string  // what was found and what was required
	Err     error   // the underlying system error, when there is one
}

func (e *PeerCheckError) Error() string {
	s := fmt.Sprintf("ipc: peer check failed on %s (%s): %s: %s", runtime.GOOS, e.Failure, e.Path, e.Detail)
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

func (e *PeerCheckError) Unwrap() error { return e.Err }

// Is makes every PeerCheckError match ErrPeerCheckFailed.
func (e *PeerCheckError) Is(target error) bool { return target == ErrPeerCheckFailed }

// PathTooLongError reports that a socket path does not fit the platform's
// sockaddr. The path, its length and the limit are all in the message.
type PathTooLongError struct {
	Path   string
	Length int // bytes, without the terminating NUL
	Limit  int // the longest path in bytes the platform accepts
}

func (e *PathTooLongError) Error() string {
	return fmt.Sprintf("ipc: socket path %q is %d bytes, the limit on this platform is %d; use a shorter data directory (--home or SIDERA_HOME)", e.Path, e.Length, e.Limit)
}
