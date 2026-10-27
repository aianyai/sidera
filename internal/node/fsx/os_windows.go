//go:build windows

package fsx

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Platform primitives for Windows. The Windows tests of this package pass on the
// GitHub-hosted Windows runner. What Windows makes durable at a power loss is not
// observed by any CI job, and the result of flushing a directory handle is only
// logged by a test (see TestWindowsDirectoryFlushAttempt), not captured.

// retryable is the error set the Go toolchain's own robustio retries: another
// process (antivirus, indexer, a reader holding the file open) briefly keeps
// the file from being opened, replaced or removed.
func retryable(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) ||
		errors.Is(err, windows.ERROR_SHARING_VIOLATION) ||
		errors.Is(err, windows.ERROR_FILE_NOT_FOUND)
}

// retryableRead is retryable without file not found. A rename or a remove that
// reports it may be racing a scanner that holds the file, as Go's own loop
// assumes, but a read of a file that does not exist is an answer: Go's
// cmd/internal/robustio leaves file not found out of its read retry for the
// same reason, and retrying would cost every absent file the whole budget.
func retryableRead(err error) bool {
	return retryable(err) && !errors.Is(err, windows.ERROR_FILE_NOT_FOUND)
}

// withRetry runs one operation under the retry loop. Retries happen inside the
// primitive, below Observe, so a logical step reaches the observer once.
func withRetry(do func() error) error { return withRetryWhile(retryable, do) }

func withRetryWhile(mayRetry func(error) bool, do func() error) error {
	return retry(sleepSystem, func() (bool, error) {
		err := do()
		return mayRetry(err), err
	})
}

// readFile reads and closes at once: Go opens files without FILE_SHARE_DELETE,
// so a handle that stays open makes replacing the file fail. A read that fails
// transiently is retried, except when the file is not found.
func readFile(abs string, maxBytes int64) ([]byte, error) {
	var data []byte
	err := withRetryWhile(retryableRead, func() error {
		var err error
		data, err = readRegularFile(abs, maxBytes)
		return err
	})
	return data, err
}

func openRead(abs string) (*os.File, error) { return os.Open(abs) }

// openAppend opens with GENERIC_WRITE and moves to the end of the file. It does
// not use Go's O_APPEND, which on Windows drops GENERIC_WRITE and keeps only
// FILE_APPEND_DATA, while FlushFileBuffers requires GENERIC_WRITE. Under the
// instance lock there is one writer, so positioning at open time is equivalent.
func openAppend(abs string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(abs)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: abs, Err: err}
	}
	var h windows.Handle
	err = withRetry(func() error {
		var err error
		h, err = windows.CreateFile(name, windows.GENERIC_WRITE,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil,
			windows.OPEN_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL, 0)
		return err
	})
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: abs, Err: err}
	}
	if _, err := windows.Seek(h, 0, io.SeekEnd); err != nil {
		windows.CloseHandle(h)
		return nil, &fs.PathError{Op: "seek", Path: abs, Err: err}
	}
	return os.NewFile(uintptr(h), abs), nil
}

// syncDir does not flush. FlushFileBuffers needs a handle with GENERIC_WRITE and
// its documentation does not mention directories; Pebble, RocksDB and NATS skip
// the flush on Windows. It still checks that the directory exists, so a wrong
// path fails the same way on every platform.
func syncDir(abs string) error {
	fi, err := os.Stat(abs)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return &fs.PathError{Op: "syncdir", Path: abs, Err: windows.ERROR_DIRECTORY}
	}
	return nil
}

func moveFile(from, to string, flags uint32) error {
	pf, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	pt, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	err = withRetry(func() error { return windows.MoveFileEx(pf, pt, flags) })
	if err != nil {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	return nil
}

// renameFile replaces an existing target file. MOVEFILE_WRITE_THROUGH asks for
// the move to reach the disk before the call returns; the documentation only
// spells that out for a move done as copy and delete, so the power-loss
// behaviour of a same-volume rename is not established. os.Rename passes only
// MOVEFILE_REPLACE_EXISTING, which is why it is not used.
func renameFile(from, to string) error {
	return moveFile(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

// renameDir moves a directory. MOVEFILE_REPLACE_EXISTING cannot replace a
// directory, so the caller has checked that the target is absent and the flag
// is left out.
func renameDir(from, to string) error {
	return moveFile(from, to, windows.MOVEFILE_WRITE_THROUGH)
}

func removeFile(abs string) error { return withRetry(func() error { return os.Remove(abs) }) }

func removeAll(abs string) error { return withRetry(func() error { return os.RemoveAll(abs) }) }

func available(abs string) (uint64, error) {
	p, err := windows.UTF16PtrFromString(abs)
	if err != nil {
		return 0, &fs.PathError{Op: "diskfree", Path: abs, Err: err}
	}
	var free, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree); err != nil {
		return 0, &fs.PathError{Op: "diskfree", Path: abs, Err: err}
	}
	return free, nil
}

// lockFile takes LockFileEx on the first byte of the lock file, exclusive and
// failing at once. The system releases the lock when the process ends, but
// "the time it takes ... depends upon available system resources", so a killed
// process's successor can briefly see ErrLocked; fsx does not retry, because
// for a real second daemon an immediate error is the right answer.
func lockFile(abs string) (func() error, error) {
	f, err := os.OpenFile(abs, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	h := windows.Handle(f.Fd())
	ol := new(windows.Overlapped)
	err = windows.LockFileEx(h, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ol)
	if err != nil {
		f.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, ErrLocked
		}
		return nil, &fs.PathError{Op: "lockfileex", Path: abs, Err: err}
	}
	var once sync.Once
	var unlockErr error
	return func() error {
		once.Do(func() {
			unlockErr = windows.UnlockFileEx(h, 0, 1, 0, ol)
			if cerr := f.Close(); unlockErr == nil {
				unlockErr = cerr
			}
		})
		return unlockErr
	}, nil
}

// Access-denied entry types other than ACCESS_DENIED_ACE_TYPE, which
// golang.org/x/sys does not name.
const (
	accessDeniedObjectACEType         = 0x6
	accessDeniedCallbackACEType       = 0xA
	accessDeniedCallbackObjectACEType = 0xC
)

// isDenyACE reports the entry types that can only remove access, which
// ownerOnly may skip. Every other type is judged: an allow entry by its SID, any
// type this code does not know as not private.
func isDenyACE(aceType byte) bool {
	switch aceType {
	case windows.ACCESS_DENIED_ACE_TYPE, accessDeniedObjectACEType,
		accessDeniedCallbackACEType, accessDeniedCallbackObjectACEType:
		return true
	}
	return false
}

// trustedSIDs are the identities that may own a private file or be granted
// access to it: the user running this process, SYSTEM and Administrators.
// Administrators matter because an elevated process creates files owned by that
// group. The owner of the object is not trusted by being the owner: it must be
// one of these.
func trustedSIDs() ([]*windows.SID, error) {
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return nil, err
	}
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return nil, err
	}
	return []*windows.SID{tu.User.Sid, system, admins}, nil
}

func isTrusted(sid *windows.SID, trusted []*windows.SID) bool {
	for _, t := range trusted {
		if windows.EqualSid(sid, t) {
			return true
		}
	}
	return false
}

// ownerOnly fails closed: it is true only when the owner is trusted, a DACL
// exists, every access-allowed entry names a trusted identity, and every other
// entry is of a type that can only deny access. Entries that deny access cannot
// widen it and are skipped; an entry of any other type (a callback or object
// allow entry, for example) may grant access in a way this check does not
// evaluate, so it makes the answer false, as does any failure to read the
// descriptor: the caller refuses to start rather than assume a key file is
// private.
func ownerOnly(abs string, _ fs.FileInfo) bool {
	sd, err := windows.GetNamedSecurityInfo(abs, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false
	}
	defer runtime.KeepAlive(sd) // owner and dacl point into the descriptor
	owner, _, err := sd.Owner()
	if err != nil || owner == nil {
		return false
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return false // no DACL grants everyone full access
	}
	trusted, err := trustedSIDs()
	if err != nil || !isTrusted(owner, trusted) {
		return false
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return false
		}
		switch {
		case isDenyACE(ace.Header.AceType):
			continue
		case ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE:
			return false
		}
		if !isTrusted((*windows.SID)(unsafe.Pointer(&ace.SidStart)), trusted) {
			return false
		}
	}
	return true
}

// makeRoot creates the directory with a protected DACL (no inheritance from
// the parent) granting full control to the user running this process only,
// inherited by every file and directory created inside. SYSTEM and
// Administrators are not listed: the data directory is the user's own.
func makeRoot(p string) error {
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + tu.User.Sid.String() + ")")
	if err != nil {
		return err
	}
	sa := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: sd,
	}
	name, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return &fs.PathError{Op: "mkdir", Path: p, Err: err}
	}
	if err := windows.CreateDirectory(name, sa); err != nil {
		return &fs.PathError{Op: "mkdir", Path: p, Err: err}
	}
	return nil
}
