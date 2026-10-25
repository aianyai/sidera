package fsx_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/aianyai/sidera/internal/node/fsx"
)

// These tests state requirements and pass on the GitHub-hosted Windows runner.
// Each logs the measured values that the next fix would need; those values show
// only with -v, which the CI step "go test -v (platform packages)" passes.

// Go opens files without FILE_SHARE_DELETE, so a handle held by os.Open keeps a
// rename over the file from succeeding until it is closed. A short-lived
// holder (an antivirus scan, a reader about to finish) must not fail the
// replacement: the retry in the rename primitive waits it out.
func TestWindowsReplaceWhileAReadHandleIsHeldSucceedsOnceItIsReleased(t *testing.T) {
	fsys, root := openTemp(t)
	if err := fsx.Replace(fsys, tx, "rec", []byte("old"), fsx.ModePrivate); err != nil {
		t.Fatal(err)
	}
	h, err := os.Open(filepath.Join(root, "rec"))
	if err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		defer close(released)
		time.Sleep(300 * time.Millisecond)
		h.Close()
	}()

	start := time.Now()
	err = fsx.Replace(fsys, tx, "rec", []byte("new"), fsx.ModePrivate)
	elapsed := time.Since(start)
	<-released

	t.Logf("Replace with a read handle held for 300ms took %v, error: %v", elapsed, err)
	if err != nil {
		t.Fatalf("Replace failed although the handle was released within the retry budget: %v", err)
	}
	if got, _ := fsys.ReadFile("rec"); string(got) != "new" {
		t.Errorf("target = %q, want the new content", got)
	}
}

// A holder that outlives the retry budget makes the replacement fail, and a
// failed replacement is an ordinary failure: the target is as it was and no
// temporary file is left. (If this Windows lets the replacement through the
// open handle, the content must be the new one.)
func TestWindowsReplaceWhileAReadHandleOutlivesTheRetryBudget(t *testing.T) {
	fsys, root := openTemp(t)
	if err := fsx.Replace(fsys, tx, "rec", []byte("old"), fsx.ModePrivate); err != nil {
		t.Fatal(err)
	}
	h, err := os.Open(filepath.Join(root, "rec"))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	start := time.Now()
	err = fsx.Replace(fsys, tx, "rec", []byte("new"), fsx.ModePrivate)
	t.Logf("Replace with a read handle held throughout took %v, error: %v", time.Since(start), err)

	got, _ := fsys.ReadFile("rec")
	if err == nil {
		if string(got) != "new" {
			t.Errorf("Replace succeeded but the target is %q", got)
		}
		return
	}
	if fsx.IsFatal(err) {
		t.Errorf("a failed rename was classified fatal: %v", err)
	}
	if string(got) != "old" {
		t.Errorf("target = %q after a failed Replace, want it unchanged", got)
	}
	if left := listTemporary(t, fsys, "."); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
}

// The directory flush that fsx documents as absent: measure what the
// operating system says when a directory handle is flushed, so the storage
// reference can record it. Only SyncDir's own contract is asserted.
func TestWindowsDirectoryFlushAttempt(t *testing.T) {
	fsys, root := openTemp(t)
	name, err := windows.UTF16PtrFromString(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		label  string
		access uint32
	}{
		{"GENERIC_WRITE", windows.GENERIC_WRITE},
		{"GENERIC_READ|GENERIC_WRITE", windows.GENERIC_READ | windows.GENERIC_WRITE},
		{"GENERIC_READ", windows.GENERIC_READ},
	} {
		h, err := windows.CreateFile(name, tc.access,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
			windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
		if err != nil {
			t.Logf("directory flush attempt, access %s: opening the directory failed: %v", tc.label, err)
			continue
		}
		flushErr := windows.FlushFileBuffers(h)
		windows.CloseHandle(h)
		t.Logf("directory flush attempt, access %s: FlushFileBuffers error: %v", tc.label, flushErr)
	}
	if err := fsys.SyncDir(tx, "."); err != nil {
		t.Errorf("SyncDir of an existing directory = %v, want nil", err)
	}
	if err := fsys.SyncDir(tx, "missing"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("SyncDir of a missing directory = %v, want fs.ErrNotExist", err)
	}
}

// FlushFileBuffers needs GENERIC_WRITE, which Go's own O_APPEND would drop:
// Sync on an append handle must succeed, and appends must land at the end.
func TestWindowsAppendHandleSyncSucceedsAndAppendsAtTheEnd(t *testing.T) {
	fsys, root := openTemp(t)
	for _, chunk := range []string{"one;", "two;"} {
		f, err := fsys.OpenAppend(tx, "log")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
		if err := f.Sync(); err != nil {
			t.Fatalf("Sync on an append handle: %v", err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(filepath.Join(root, "log"))
	if err != nil || string(got) != "one;two;" {
		t.Errorf("log = %q, %v; want one;two;", got, err)
	}
}

func TestWindowsRenameDirRefusesAnExistingTarget(t *testing.T) {
	fsys, _ := openTemp(t)
	for _, d := range []string{"stage", "pkg"} {
		if err := fsx.MakeDir(fsys, tx, d); err != nil {
			t.Fatal(err)
		}
	}
	if err := fsx.Replace(fsys, tx, "stage/new", []byte("new"), fsx.ModePrivate); err != nil {
		t.Fatal(err)
	}
	if err := fsx.Replace(fsys, tx, "pkg/old", []byte("old"), fsx.ModePrivate); err != nil {
		t.Fatal(err)
	}

	err := fsys.RenameDir(tx, "stage", "pkg")

	t.Logf("RenameDir onto an existing directory: %v", err)
	if !errors.Is(err, fs.ErrExist) {
		t.Errorf("RenameDir onto an existing directory = %v, want fs.ErrExist", err)
	}
	for p, want := range map[string]string{"stage/new": "new", "pkg/old": "old"} {
		if got, _ := fsys.ReadFile(p); string(got) != want {
			t.Errorf("%s = %q, want %q", p, got, want)
		}
	}
}

func TestWindowsSecondLockGetsErrLocked(t *testing.T) {
	root := t.TempDir()
	a, err := fsx.OpenOS(root, &countingReader{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := fsx.OpenOS(root, &countingReader{})
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := a.Lock("daemon.lock")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = b.Lock("daemon.lock")
	t.Logf("second Lock returned after %v: %v", time.Since(start), err)
	if !errors.Is(err, fsx.ErrLocked) {
		t.Errorf("second Lock = %v, want fsx.ErrLocked", err)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	unlockB, err := b.Lock("daemon.lock")
	if err != nil {
		t.Fatalf("Lock after release: %v", err)
	}
	unlockB()
}

// The data directory is created with a protected DACL for the current user,
// and what is created inside inherits it, so OwnerOnly holds for the root and
// for new files.
func TestWindowsMakeRootIsOwnerOnlyAndInherited(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".sidera")
	if err := fsx.MakeRoot(root); err != nil {
		t.Fatal(err)
	}
	fsys, err := fsx.OpenOS(root, &countingReader{})
	if err != nil {
		t.Fatal(err)
	}
	if err := fsx.Replace(fsys, tx, "key", []byte("secret"), fsx.ModePrivate); err != nil {
		t.Fatal(err)
	}
	if err := fsx.MakeDir(fsys, tx, "keys"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{".", "key", "keys"} {
		abs := filepath.Join(root, p)
		sd, err := windows.GetNamedSecurityInfo(abs, windows.SE_FILE_OBJECT,
			windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			t.Errorf("GetNamedSecurityInfo(%s): %v", abs, err)
			continue
		}
		t.Logf("security descriptor of %s: %s", p, sd.String())
		info, err := fsys.Stat(p)
		if err != nil || !info.OwnerOnly {
			t.Errorf("Stat(%s) = %+v, %v; want OwnerOnly", p, info, err)
		}
	}
	sd, err := windows.GetNamedSecurityInfo(root, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	if control, _, err := sd.Control(); err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		t.Errorf("root DACL control = %#x, %v; want the protected flag so the parent's permissions are not inherited", control, err)
	}
	if err := fsx.MakeRoot(root); !errors.Is(err, fs.ErrExist) {
		t.Errorf("MakeRoot of an existing directory = %v, want fs.ErrExist", err)
	}
}

func TestWindowsOwnerOnlyIsFalseWhenEveryoneHasAccess(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".sidera")
	if err := fsx.MakeRoot(root); err != nil {
		t.Fatal(err)
	}
	fsys, err := fsx.OpenOS(root, &countingReader{})
	if err != nil {
		t.Fatal(err)
	}
	if err := fsx.Replace(fsys, tx, "key", []byte("secret"), fsx.ModePrivate); err != nil {
		t.Fatal(err)
	}
	// Grant Everyone read access: a DACL no longer limited to the owner.
	everyone, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		t.Fatal(err)
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_READ,
		AccessMode:        windows.GRANT_ACCESS,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
			TrusteeValue: windows.TrusteeValueFromSID(everyone),
		},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = windows.SetNamedSecurityInfo(filepath.Join(root, "key"), windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
	if err != nil {
		t.Fatalf("SetNamedSecurityInfo: %v", err)
	}
	if info, err := fsys.Stat("key"); err != nil || info.OwnerOnly {
		t.Errorf("Stat(key) = %+v, %v; want OwnerOnly false once Everyone can read it", info, err)
	}
}

// Reading a file that is not there is an answer, not a transient failure: it
// must return fs.ErrNotExist without spending the retry budget. The elapsed
// time is logged so one CI log shows what a miss costs.
func TestWindowsReadOfAMissingFileReturnsNotExistImmediately(t *testing.T) {
	fsys, _ := openTemp(t)
	start := time.Now()
	_, err := fsys.ReadFile("absent")
	elapsed := time.Since(start)
	t.Logf("ReadFile of a missing file took %v and returned %v", elapsed, err)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ReadFile of a missing file = %v, want fs.ErrNotExist", err)
	}
	if elapsed > time.Second {
		t.Errorf("ReadFile of a missing file took %v, want no retrying (the budget is 2s)", elapsed)
	}
}

// createOwnedKey returns an FS over a fresh data directory and the absolute
// path of a file in it, "key".
func createOwnedKey(t *testing.T) (fsx.FS, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), ".sidera")
	if err := fsx.MakeRoot(root); err != nil {
		t.Fatal(err)
	}
	fsys, err := fsx.OpenOS(root, &countingReader{})
	if err != nil {
		t.Fatal(err)
	}
	if err := fsx.Replace(fsys, tx, "key", []byte("secret"), fsx.ModePrivate); err != nil {
		t.Fatal(err)
	}
	return fsys, filepath.Join(root, "key")
}

func currentUserSID(t *testing.T) *windows.SID {
	t.Helper()
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	return tu.User.Sid
}

// An owner that is not the user running the process, Administrators or SYSTEM
// makes a file not owner-only, even when every DACL entry is trusted: the owner
// can always rewrite the DACL.
func TestWindowsOwnerOnlyIsFalseWhenTheOwnerIsNotTrusted(t *testing.T) {
	for _, tc := range []struct {
		name string
		sid  func(t *testing.T) *windows.SID
	}{
		{"everyone", func(t *testing.T) *windows.SID {
			sid, err := windows.CreateWellKnownSid(windows.WinWorldSid)
			if err != nil {
				t.Fatal(err)
			}
			return sid
		}},
		{"another-user", func(t *testing.T) *windows.SID {
			sid, err := windows.CreateWellKnownSid(windows.WinBuiltinUsersSid)
			if err != nil {
				t.Fatal(err)
			}
			return sid
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fsys, abs := createOwnedKey(t)
			owner := tc.sid(t)
			err := windows.SetNamedSecurityInfo(abs, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION, owner, nil, nil, nil)
			if err != nil {
				t.Skipf("cannot assign owner %s without the privilege to do so (%v); known untested item", owner, err)
			}
			if info, err := fsys.Stat("key"); err != nil || info.OwnerOnly {
				t.Errorf("Stat(key) = %+v, %v; want OwnerOnly false for owner %s", info, err, owner)
			}
		})
	}
}

// An entry that is not a plain allow or deny entry may grant access in a way
// the check does not evaluate, so it counts as not private even when it names
// the current user.
func TestWindowsOwnerOnlyIsFalseWithACallbackAllowEntry(t *testing.T) {
	fsys, abs := createOwnedKey(t)
	// XA is an access-allowed callback (conditional) entry.
	sd, err := windows.SecurityDescriptorFromString(`D:P(XA;;FA;;;` + currentUserSID(t).String() + `;(@User.Title=="PM"))`)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	err = windows.SetNamedSecurityInfo(abs, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
	if err != nil {
		t.Fatalf("SetNamedSecurityInfo: %v", err)
	}
	if info, err := fsys.Stat("key"); err != nil || info.OwnerOnly {
		t.Errorf("Stat(key) = %+v, %v; want OwnerOnly false with a callback allow entry", info, err)
	}
}

// Stat does not follow a link and ReadFile does, so a link is never owner-only,
// whatever its own descriptor says. Creating a symbolic link needs a privilege
// (or developer mode) that an account may lack.
func TestWindowsOwnerOnlyIsFalseForASymbolicLink(t *testing.T) {
	fsys, root := openTemp(t)
	if err := fsx.Replace(fsys, tx, "target", []byte("secret"), fsx.ModePrivate); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "target"), filepath.Join(root, "link")); err != nil {
		t.Skipf("the account cannot create symbolic links (needs SeCreateSymbolicLinkPrivilege or developer mode): %v", err)
	}
	info, err := fsys.Stat("link")
	t.Logf("Stat(link) = %+v, %v", info, err)
	if err != nil || info.OwnerOnly {
		t.Errorf("Stat(link) = %+v, %v; want OwnerOnly false for a symbolic link", info, err)
	}
}
