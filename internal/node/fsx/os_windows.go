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

// Platform primitives for Windows. What follows is pending verification by the
// Windows CI after it lands: nothing here has run on a Windows machine.

// retryable is the error set the Go toolchain's own robustio retries: another
// process (antivirus, indexer, a reader holding the file open) briefly keeps
// the file from being opened, replaced or removed.
func retryable(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) ||
		errors.Is(err, windows.ERROR_SHARING_VIOLATION) ||
		errors.Is(err, windows.ERROR_FILE_NOT_FOUND)
}

// withRetry runs one operation under the retry loop. Retries happen inside the
// primitive, below Observe, so a logical step reaches the observer once.
func withRetry(do func() error) error {
	return retry(sleepSystem, func() (bool, error) {
		err := do()
		return retryable(err), err
	})
}

// readFile reads and closes at once: Go opens files without FILE_SHARE_DELETE,
// so a handle that stays open makes replacing the file fail. A read that fails
// transiently is retried.
func readFile(abs string) ([]byte, error) {
	var data []byte
	err := withRetry(func() error {
		var err error
		data, err = os.ReadFile(abs)
		return err
	})
	return data, err
}

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

// trustedSIDs are the identities whose access to a private file is expected:
// the object's owner, the user running this process (which differs from the
// owner when an elevated process creates the file and Administrators own it),
// SYSTEM and Administrators.
func trustedSIDs(owner *windows.SID) ([]*windows.SID, error) {
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
	return []*windows.SID{owner, tu.User.Sid, system, admins}, nil
}

// ownerOnly: the DACL exists and every access-allowed entry names a trusted
// identity. Entries that deny access cannot widen it and are ignored. Any
// failure to read the descriptor reports false: the caller refuses to start
// rather than assume a key file is private.
func ownerOnly(abs string, _ fs.FileInfo) bool {
	sd, err := windows.GetNamedSecurityInfo(abs, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil {
		return false
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return false // no DACL grants everyone full access
	}
	trusted, err := trustedSIDs(owner)
	if err != nil {
		return false
	}
	defer runtime.KeepAlive(sd) // owner and dacl point into the descriptor
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return false
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		known := false
		for _, t := range trusted {
			if windows.EqualSid(sid, t) {
				known = true
				break
			}
		}
		if !known {
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
