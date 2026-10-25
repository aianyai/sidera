package keys_test

import (
	"errors"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/aianyai/sidera/internal/node/keys"
)

// These tests state requirements and pass on the GitHub-hosted Windows runner.
// They use the real file system.
func TestWindowsDACLWiderThanTheOwnerIsRefusedNamingTheFile(t *testing.T) {
	r := newRealRig(t)
	r.initialize(t)
	if _, err := keys.Load(r.w); err != nil {
		t.Fatalf("Load of a fresh init: %v", err)
	}

	everyone, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{keys.Dir, keys.IdentityKeyPath, keys.NetworkKeyPath, keys.CertificatePath, keys.SerialPath, keys.SelfRevocationPath, keys.ClockPath, keys.InitMarkerPath} {
		abs := filepath.Join(r.root, filepath.FromSlash(p))
		sd, err := windows.GetNamedSecurityInfo(abs, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
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
		if err := windows.SetNamedSecurityInfo(abs, windows.SE_FILE_OBJECT,
			windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
			t.Fatalf("SetNamedSecurityInfo(%s): %v", p, err)
		}

		_, err = keys.Load(r.w)
		if !errors.Is(err, keys.ErrPermissionsTooBroad) {
			t.Errorf("%s with Everyone granted read: Load = %v, want ErrPermissionsTooBroad", p, err)
		} else {
			asFileError(t, err, p)
		}

		// put the owner-only DACL back so the next file is the only broad one
		if err := windows.SetNamedSecurityInfo(abs, windows.SE_FILE_OBJECT,
			windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, before, nil); err != nil {
			t.Fatalf("restoring the DACL of %s: %v", p, err)
		}
		if _, err := keys.Load(r.w); err != nil {
			t.Errorf("%s with its DACL restored: Load = %v", p, err)
		}
	}
}
