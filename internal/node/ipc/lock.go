package ipc

// InstanceLock is the caller's assertion that it holds the data directory's
// instance lock. Listen needs it because a leftover socket may only be deleted
// by the one process that is certain no other daemon serves the directory.
//
// It is a precondition the caller states, which Listen cannot verify: it
// guards against ordering mistakes in the composition root (listening before
// the lock is taken, or after it is released), and is not a security boundary.
type InstanceLock interface {
	// Held reports whether the lock is held by the caller right now.
	Held() bool
}
