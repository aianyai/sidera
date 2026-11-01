//go:build windows

package fsxtest

import (
	"runtime"
	"testing"

	"golang.org/x/sys/windows"
)

// ForeignSID is a SID that no account of the machine holds, standing for a group
// that another program (a sandbox) added to the access list of a path: it does not
// resolve to a name.
const ForeignSID = "S-1-5-21-1111111111-2222222222-3333333333-1001"

// GrantForeign adds to the access list of the file or directory at path an entry
// that allows ForeignSID the rights of mask and applies to that object only, the
// way a program that grants access to one path does. It keeps the entries already
// there, and the list is protected so that nothing propagates.
func GrantForeign(t testing.TB, path string, mask windows.ACCESS_MASK) {
	t.Helper()
	sid, err := windows.StringToSid(ForeignSID)
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	before, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: mask,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.NO_INHERITANCE,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_UNKNOWN,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}}, before)
	if err != nil {
		t.Fatal(err)
	}
	const flags = windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION
	err = windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, flags, nil, nil, acl, nil)
	runtime.KeepAlive(sd)
	if err != nil {
		t.Fatalf("granting a foreign entry on %s: %v", path, err)
	}
}

// Modify is the right a sandbox was granted on a state directory: read, write,
// execute and delete, without changing the access list.
const Modify windows.ACCESS_MASK = 0x1301BF
