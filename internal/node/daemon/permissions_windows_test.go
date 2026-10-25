//go:build windows

package daemon_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/aianyai/sidera/internal/node/daemon"
)

// These tests state requirements and are first run by the Windows job of the CI,
// after the code lands: this machine type-checks them and cannot run them. They
// use the real file system.

// A data directory whose access list grants anyone but the owner (Everyone here)
// is refused by the start and by init, and the same directory with its list put
// back starts. The check is the file layer's OwnerOnly, which fails closed on any
// entry it does not know.
func TestWindowsADataDirectoryThatEveryoneCanReadIsRefused(t *testing.T) {
	c, _ := initialized(t)
	everyone, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.GetNamedSecurityInfo(c.Home, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	before, _, err := sd.DACL()
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
	}}, before)
	if err != nil {
		t.Fatal(err)
	}
	const flags = windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION
	if err := windows.SetNamedSecurityInfo(c.Home, windows.SE_FILE_OBJECT, flags, nil, nil, acl, nil); err != nil {
		t.Fatalf("SetNamedSecurityInfo: %v", err)
	}
	restored := false
	restore := func() {
		if restored {
			return
		}
		restored = true
		if err := windows.SetNamedSecurityInfo(c.Home, windows.SE_FILE_OBJECT, flags, nil, nil, before, nil); err != nil {
			t.Errorf("restoring the access list: %v", err)
		}
	}
	defer restore()

	if _, err := daemon.Start(context.Background(), c); !errors.Is(err, daemon.ErrPermissions) {
		t.Errorf("a start on a directory that Everyone can read: %v; want ErrPermissions (platform %s)", err, "windows")
	}
	if _, err := daemon.Init(context.Background(), c); !errors.Is(err, daemon.ErrPermissions) {
		t.Errorf("init on a directory that Everyone can read: %v; want ErrPermissions", err)
	}
	restore()
	started(t, c)
}

// A data directory that is a symbolic link is refused whatever it points to. Making
// a link needs a privilege that the CI's runner has; where it is missing the test
// says so and skips.
func TestWindowsASymbolicLinkAsTheDataDirectoryIsRefused(t *testing.T) {
	c, clk := initialized(t)
	link := newHome(t)
	if err := os.Symlink(c.Home, link); err != nil {
		t.Skipf("a symbolic link cannot be made here (platform windows): %v", err)
	}
	lc := config(link, clk)
	if _, err := daemon.Start(context.Background(), lc); !errors.Is(err, daemon.ErrPermissions) {
		t.Errorf("a start on a symbolic link: %v; want ErrPermissions", err)
	}
	if _, err := daemon.Init(context.Background(), lc); !errors.Is(err, daemon.ErrPermissions) {
		t.Errorf("init on a symbolic link: %v; want ErrPermissions", err)
	}
}
