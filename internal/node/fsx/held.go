package fsx

// IsHeld reports whether err, or an error it wraps, says that another process
// holds the file: on Windows access denied or a sharing violation, which is what
// removing, replacing or opening a file that a running program is mapped from or
// keeps open without sharing gives. It is the same pair that the Windows retry
// treats as another process briefly holding a file; a caller that must tell a
// held file from any other failure asks this question instead of testing
// operating system error codes itself. File not found is not part of it: the file
// is not there.
//
// It is always false on Unix, where a file in use can be removed and its inode
// lives on until the last user ends.
func IsHeld(err error) bool { return err != nil && isHeld(err) }
