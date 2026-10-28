//go:build windows

package fsx

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func widenPrivateFileParent(t *testing.T, parent string) {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	err = windows.SetNamedSecurityInfo(parent, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
	runtime.KeepAlive(sd)
	if err != nil {
		t.Fatal(err)
	}
	if ownerOnly(parent, nil) {
		t.Fatal("test parent must permit other users")
	}
}

func assertPrivateFile(t *testing.T, f *os.File) {
	t.Helper()
	h := windows.Handle(f.Fd())
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.KeepAlive(sd)
	if !ownerOnlyDescriptor(sd) {
		t.Fatalf("new file is not private: %s", sd.String())
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatalf("new DACL is not protected: %s, %v", sd.String(), err)
	}
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil || dacl.AceCount != 1 {
		t.Fatalf("new DACL must name only the current user: %s, %v", sd.String(), err)
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil {
		t.Fatal(err)
	}
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE ||
		!windows.EqualSid((*windows.SID)(unsafe.Pointer(&ace.SidStart)), tu.User.Sid) {
		t.Fatalf("new DACL does not grant only the current user: %s", sd.String())
	}
}

func TestWindowsPrivateFileRefusesUnverifiedVolumeBeforeWriting(t *testing.T) {
	queryErr := errors.New("volume query failed")
	for _, tc := range []struct {
		name  string
		flags uint32
		err   error
	}{
		{"query-failure", 0, queryErr},
		{"no-persistent-acls", 0, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "export")
			f, err := openPrivateFile(p)
			if err != nil {
				t.Fatal(err)
			}
			got, err := finishPrivateFile(f, func(f *os.File) error {
				return verifyPrivateFileOnVolume(f, func(h windows.Handle) (uint32, error) {
					if h != windows.Handle(f.Fd()) {
						t.Fatal("volume query must use the created file's handle")
					}
					info, err := f.Stat()
					if err != nil || info.Size() != 0 {
						t.Fatalf("volume check after secret write: %v, %v", info, err)
					}
					return tc.flags, tc.err
				})
			})
			if got != nil || err == nil || !strings.Contains(err.Error(), "stdout without --out") {
				t.Fatalf("unsafe volume returned %v, %v", got, err)
			}
			if tc.err != nil && !errors.Is(err, tc.err) {
				t.Fatalf("lost query error: %v", err)
			}
			if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("rejected empty file remains: %v", err)
			}
		})
	}
}

func TestWindowsPrivateFileRejectsAnActuallyBroadDACL(t *testing.T) {
	parent := t.TempDir()
	widenPrivateFileParent(t, parent)
	p := filepath.Join(parent, "export")
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	got, err := finishPrivateFile(f, verifyPrivateFile)
	if got != nil || err == nil || !strings.Contains(err.Error(), "not owner-only") {
		t.Fatalf("inherited broad DACL returned %v, %v", got, err)
	}
	if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("rejected empty file remains: %v", err)
	}
}

func TestWindowsPrivateFileSecurityQueryFailureReturnsNoWritableHandle(t *testing.T) {
	p := filepath.Join(t.TempDir(), "export")
	f, err := openPrivateFile(p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := finishPrivateFile(f, func(f *os.File) error {
		return verifyPrivateFileOnVolume(f, func(h windows.Handle) (uint32, error) {
			// Lose the handle after the volume check, before GetSecurityInfo.
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			return windows.FILE_PERSISTENT_ACLS, nil
		})
	})
	if got != nil || !errors.Is(err, windows.ERROR_INVALID_HANDLE) {
		t.Fatalf("failed security query returned %v, %v", got, err)
	}
	if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("rejected empty file remains: %v", err)
	}
}

func TestWindowsPrivateFileRejectsADeviceWithoutRemovingIt(t *testing.T) {
	f, err := CreatePrivateFile("NUL")
	if f != nil || err == nil {
		t.Fatalf("device returned %v, %v", f, err)
	}
	device, err := os.OpenFile("NUL", os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("device must remain usable: %v", err)
	}
	device.Close()
}
