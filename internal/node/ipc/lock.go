package ipc

// InstanceLock is the caller's proof that it holds the data directory's
// instance lock. Listen needs it because a leftover socket may only be deleted
// by the one process that is certain no other daemon serves the directory.
type InstanceLock interface {
	// Held reports whether the lock is held by the caller right now.
	Held() bool
}
