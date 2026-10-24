package keys_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/aianyai/sidera/internal/node/keys"
)

// every path of keys/ that holds a file, and the directory itself
var protectedPaths = []string{
	keys.Dir,
	keys.IdentityKeyPath, keys.NetworkKeyPath, keys.CertificatePath, keys.SerialPath,
	keys.SelfRevocationPath, keys.ClockPath, keys.InitMarkerPath,
}

// A key file that anyone but its owner can reach is a leaked key: the node
// refuses to start and names the file. It never narrows the permissions itself,
// because only the owner knows who else already read the file.
func TestPermissionsTooBroadAreRefusedNamingTheFile(t *testing.T) {
	base := newRig(t)
	base.mustInit(1, t0)
	base.mustLoad()

	for _, p := range protectedPaths {
		t.Run(p, func(t *testing.T) {
			mem := base.mem.Clone()
			mem.SetOwnerOnly(p, false)
			r := open(t, mem)

			_, err := keys.Load(r.w)
			if !errors.Is(err, keys.ErrPermissionsTooBroad) {
				t.Fatalf("Load = %v, want ErrPermissionsTooBroad", err)
			}
			asFileError(t, err, p)

			if err := keys.CheckPermissions(r.w.Reader()); !errors.Is(err, keys.ErrPermissionsTooBroad) {
				t.Errorf("CheckPermissions = %v, want ErrPermissionsTooBroad", err)
			} else {
				asFileError(t, err, p)
			}

			if info, err := mem.Stat(p); err != nil || info.OwnerOnly {
				t.Errorf("after the refusal Stat(%s) = %+v, %v: the permissions were changed", p, info, err)
			}
			if n := r.obs.count(); n != 0 {
				t.Errorf("Load wrote %d steps: %v", n, r.obs.names())
			}
		})
	}
}

// The file readers refuse a broad file on their own, so a caller that skips
// Load (the offline export, the clock read) still cannot read through it.
func TestStandaloneReadersRefuseTooBroadFiles(t *testing.T) {
	base := newRig(t)
	base.mustInit(1, t0)

	t.Run("clock", func(t *testing.T) {
		mem := base.mem.Clone()
		mem.SetOwnerOnly(keys.ClockPath, false)
		_, err := keys.ReadClock(mem)
		if !errors.Is(err, keys.ErrPermissionsTooBroad) {
			t.Fatalf("ReadClock = %v", err)
		}
		asFileError(t, err, keys.ClockPath)
	})
	t.Run("self-revocation", func(t *testing.T) {
		mem := base.mem.Clone()
		mem.SetOwnerOnly(keys.SelfRevocationPath, false)
		_, err := keys.ReadSelfRevocation(mem)
		if !errors.Is(err, keys.ErrPermissionsTooBroad) {
			t.Fatalf("ReadSelfRevocation = %v", err)
		}
		asFileError(t, err, keys.SelfRevocationPath)
	})
	t.Run("initialized", func(t *testing.T) {
		mem := base.mem.Clone()
		mem.SetOwnerOnly(keys.InitMarkerPath, false)
		_, err := keys.Initialized(mem)
		if !errors.Is(err, keys.ErrPermissionsTooBroad) {
			t.Fatalf("Initialized = %v", err)
		}
		asFileError(t, err, keys.InitMarkerPath)
	})
}

// init meets the same rule: a key file it would reuse that others can reach is
// refused, naming the file, before anything is generated or written.
func TestInitRefusesAnExistingFileThatIsTooBroad(t *testing.T) {
	for _, p := range protectedPaths[:len(protectedPaths)-1] { // the marker's presence ends init before any file is read
		t.Run(p, func(t *testing.T) {
			base := newRig(t)
			if _, err := base.runInitKeysOnly(newSeeded(1), t0); err != nil {
				t.Fatal(err)
			}
			mem := base.mem.Clone()
			mem.SetOwnerOnly(p, false)
			before := snapshot(t, mem)
			r := open(t, mem)
			rd := newSeeded(2)

			_, err := keys.PlanInit(r.w.Reader(), rd, t0)
			if !errors.Is(err, keys.ErrPermissionsTooBroad) {
				t.Fatalf("PlanInit = %v, want ErrPermissionsTooBroad", err)
			}
			asFileError(t, err, p)
			if rd.n != 0 {
				t.Errorf("init drew %d random bytes before refusing", rd.n)
			}
			if !reflect.DeepEqual(snapshot(t, mem), before) {
				t.Error("the refusal changed the disk")
			}
		})
	}
}
