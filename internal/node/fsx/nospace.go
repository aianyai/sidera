package fsx

// IsNoSpace reports whether err, or an error it wraps, says the volume or the
// quota of the data directory is full: ENOSPC and EDQUOT on Unix, ERROR_DISK_FULL,
// ERROR_HANDLE_DISK_FULL and ERROR_DISK_QUOTA_EXCEEDED on Windows. Only what the
// platform itself returns counts: the fault a test injects for a full disk is
// fsxtest.DiskFull, which is such a value on every platform.
//
// The class of the failure (Error.Class) is not changed by it: a write that
// failed for want of space is an ordinary failed operation, and only the caller
// that tells a full volume from an unavailable one asks this question.
func IsNoSpace(err error) bool { return err != nil && isNoSpace(err) }
