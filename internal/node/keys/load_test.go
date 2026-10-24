package keys_test

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/keys"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/revocation"
)

// Load of a set that init just wrote: every accessor answers from the files.
func TestLoadOfAFreshInit(t *testing.T) {
	r := newRig(t)
	plan := r.mustInit(1, t0)
	k := r.mustLoad()

	if k.Node() != plan.Node() {
		t.Errorf("Node = %s, init said %s", k.Node(), plan.Node())
	}
	cert := k.Certificate().Payload()
	if cert.Serial != 1 || cert.Node != k.Node() || cert.NetworkKey != k.NetworkSigner().Public() {
		t.Errorf("certificate %+v does not name serial 1 for this node and its network key", cert)
	}
	if want := t0.Add(keys.CertificateLifetime); !cert.NotAfter.Equal(want) {
		t.Errorf("NotAfter = %v, want %v", cert.NotAfter, want)
	}
	if seen := k.SerialSeen(); seen.Serial != 1 || seen.Node != k.Node() || seen.Digest != k.Certificate().Digest() {
		t.Errorf("SerialSeen = %+v, want serial 1 with the digest of the certificate", seen)
	}
	if k.RenewalInterrupted() {
		t.Error("a fresh init reports an interrupted renewal")
	}
	if !bytes.Equal(k.CertificateWire(), r.read(keys.CertificatePath)) {
		t.Error("CertificateWire differs from the file")
	}
	if v, reason := k.RenewalDue(t0); v != keys.RenewNotDue || reason != keys.RenewReasonCurrent {
		t.Errorf("RenewalDue right after init = %v/%v", v, reason)
	}
}

// Every way the files can fail to load, each naming the file at fault and
// saying which kind of failure it is. Load never repairs and never writes.
func TestLoadRefusals(t *testing.T) {
	base := newRig(t)
	base.mustInit(1, t0)
	stale := base.read(keys.SerialPath) // serial 1
	// a renewed set: certificate and serial are at 2
	if err := base.mustLoad().Renew(t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	other := newRig(t)
	other.mustInit(9, t0)
	thisNode := base.mustLoad().Node().String()

	flip := func(p string) func(*fsxtest.Mem) {
		return func(m *fsxtest.Mem) {
			b := append([]byte(nil), base.read(p)...)
			b[len(b)-3] ^= 0x01
			put(t, m, p, b)
		}
	}
	replace := func(p string, data []byte) func(*fsxtest.Mem) {
		return func(m *fsxtest.Mem) { put(t, m, p, data) }
	}
	del := func(p string) func(*fsxtest.Mem) {
		return func(m *fsxtest.Mem) { remove(t, m, p) }
	}

	rows := []struct {
		name   string
		mutate func(*fsxtest.Mem)
		file   string
		is     error
	}{
		{"identity key missing", del(keys.IdentityKeyPath), keys.IdentityKeyPath, keys.ErrMissing},
		{"network key missing", del(keys.NetworkKeyPath), keys.NetworkKeyPath, keys.ErrMissing},
		{"certificate missing", del(keys.CertificatePath), keys.CertificatePath, keys.ErrMissing},
		{"serial missing", del(keys.SerialPath), keys.SerialPath, keys.ErrMissing},
		{"self-revocation missing", del(keys.SelfRevocationPath), keys.SelfRevocationPath, keys.ErrMissing},
		{"init marker missing: init did not finish", del(keys.InitMarkerPath), keys.InitMarkerPath, keys.ErrMissing},
		{"init marker damaged", flip(keys.InitMarkerPath), keys.InitMarkerPath, keys.ErrCorrupt},
		{"identity key damaged", flip(keys.IdentityKeyPath), keys.IdentityKeyPath, keys.ErrCorrupt},
		{"network key damaged", flip(keys.NetworkKeyPath), keys.NetworkKeyPath, keys.ErrCorrupt},
		{"certificate damaged", flip(keys.CertificatePath), keys.CertificatePath, keys.ErrCorrupt},
		{"serial damaged", flip(keys.SerialPath), keys.SerialPath, keys.ErrCorrupt},
		{"self-revocation damaged", flip(keys.SelfRevocationPath), keys.SelfRevocationPath, keys.ErrCorrupt},
		{"identity key empty", replace(keys.IdentityKeyPath, nil), keys.IdentityKeyPath, keys.ErrCorrupt},
		{"identity key is a network key file", replace(keys.IdentityKeyPath, base.read(keys.NetworkKeyPath)), keys.IdentityKeyPath, keys.ErrCorrupt},
		{"network key is an identity key file", replace(keys.NetworkKeyPath, base.read(keys.IdentityKeyPath)), keys.NetworkKeyPath, keys.ErrCorrupt},
		{"certificate of another node", replace(keys.CertificatePath, other.read(keys.CertificatePath)), keys.CertificatePath, keys.ErrInconsistent},
		{"identity key of another node", replace(keys.IdentityKeyPath, other.read(keys.IdentityKeyPath)), keys.CertificatePath, keys.ErrInconsistent},
		{"network key that the certificate does not name", replace(keys.NetworkKeyPath, other.read(keys.NetworkKeyPath)), keys.NetworkKeyPath, keys.ErrInconsistent},
		{"serial of another node", replace(keys.SerialPath, other.read(keys.SerialPath)), keys.SerialPath, keys.ErrInconsistent},
		{"serial behind the certificate", replace(keys.SerialPath, stale), keys.SerialPath, keys.ErrInconsistent},
		{"serial at the certificate's with another digest", func(m *fsxtest.Mem) {
			put(t, m, keys.SerialPath, oracleSerialFile(thisNode, 2, strings.Repeat("ab", 32)))
		}, keys.SerialPath, keys.ErrInconsistent},
		{"self-revocation of another node", replace(keys.SelfRevocationPath, other.read(keys.SelfRevocationPath)), keys.SelfRevocationPath, keys.ErrInconsistent},
		{"self-revocation is a certificate", replace(keys.SelfRevocationPath, base.read(keys.CertificatePath)), keys.SelfRevocationPath, keys.ErrCorrupt},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			mem := base.mem.Clone()
			row.mutate(mem)
			before := snapshot(t, mem)
			r := open(t, mem)
			_, err := keys.Load(r.w)
			if !errors.Is(err, row.is) {
				t.Fatalf("Load = %v, want %v", err, row.is)
			}
			asFileError(t, err, row.file)
			if r.obs.count() != 0 {
				t.Errorf("Load wrote: %v", r.obs.names())
			}
			after := snapshot(t, mem)
			if len(after) != len(before) {
				t.Errorf("Load changed the set of files")
			}
			for p, content := range before {
				if after[p] != content {
					t.Errorf("Load changed %s", p)
				}
			}
		})
	}
}

// A clock.rec and an init.rec that are damaged refuse the start of the node
// too: nothing in keys/ may be unreadable.
func TestLoadRefusesDamagedClockAndMarkerFiles(t *testing.T) {
	base := newRig(t)
	base.mustInit(1, t0)
	for _, p := range []string{keys.ClockPath, keys.InitMarkerPath} {
		mem := base.mem.Clone()
		b := append([]byte(nil), base.read(p)...)
		b[len(b)-3] ^= 0x01
		put(t, mem, p, b)
		r := open(t, mem)
		_, err := keys.Load(r.w)
		if !errors.Is(err, keys.ErrCorrupt) {
			t.Errorf("%s damaged: Load = %v, want ErrCorrupt", p, err)
			continue
		}
		asFileError(t, err, p)
	}
}

// A renewal that was interrupted is no reason to refuse: the node starts and
// reports the interruption.
func TestLoadAcceptsAnInterruptedRenewal(t *testing.T) {
	r := newRig(t)
	r.mustInit(1, t0)
	mem := r.mem.Clone()
	// the state after the serial was reserved and before the certificate was written
	put(t, mem, keys.SerialPath, oracleSerialFile(r.mustLoad().Node().String(), 2, strings.Repeat("cd", 32)))
	k := open(t, mem).mustLoad()
	if !k.RenewalInterrupted() {
		t.Fatal("serial 2 over certificate 1 is not reported as an interrupted renewal")
	}
	if got := k.Certificate().Payload().Serial; got != 1 {
		t.Errorf("certificate serial = %d, want the old 1", got)
	}
}

// CheckNotRetired is the one refusal of a node whose own identity was retired.
func TestCheckNotRetired(t *testing.T) {
	r := newRig(t)
	r.mustInit(1, t0)
	k := r.mustLoad()
	other := newRig(t)
	other.mustInit(9, t0)

	own, err := keys.ReadSelfRevocation(r.mem)
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := keys.ReadSelfRevocation(other.mem)
	if err != nil {
		t.Fatal(err)
	}
	add := func(s *revocation.Set, sr keys.SelfRevocation) {
		rec, err := record.Decode(sr.Wire(), record.DefaultMaxInput)
		if err != nil {
			t.Fatal(err)
		}
		rv, err := revocation.Verify(rec)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Add(rv); err != nil {
			t.Fatal(err)
		}
	}

	empty := revocation.NewSet()
	if err := k.CheckNotRetired(empty); err != nil {
		t.Errorf("empty set: %v", err)
	}
	foreign := revocation.NewSet()
	add(foreign, theirs)
	if err := k.CheckNotRetired(foreign); err != nil {
		t.Errorf("a set that retires another node: %v", err)
	}
	retired := revocation.NewSet()
	add(retired, own)
	if err := k.CheckNotRetired(retired); !errors.Is(err, keys.ErrIdentityRetired) {
		t.Errorf("a set that retires this node: %v, want ErrIdentityRetired", err)
	}
}

// An error about a key file never repeats what is in it: logs and terminals
// are not places for a seed, damaged or not.
func TestKeyFileErrorsNeverRepeatTheKey(t *testing.T) {
	base := newRig(t)
	base.mustInit(1, t0)
	seeds := stream(1, 64)
	seedTexts := []string{
		base64.RawURLEncoding.EncodeToString(seeds[:32]), base64.RawURLEncoding.EncodeToString(seeds[32:]),
		hex.EncodeToString(seeds[:32]), hex.EncodeToString(seeds[32:]),
	}
	for _, p := range []string{keys.IdentityKeyPath, keys.NetworkKeyPath} {
		good := base.read(p)
		for name, data := range map[string][]byte{
			"flipped in the seed": func() []byte { b := append([]byte(nil), good...); b[len(b)-10] ^= 1; return b }(),
			"truncated":           good[:len(good)-6],
			"doubled":             append(append([]byte(nil), good...), good...),
		} {
			mem := base.mem.Clone()
			put(t, mem, p, data)
			_, err := keys.Load(open(t, mem).w)
			if err == nil {
				t.Fatalf("%s %s: Load succeeded", p, name)
			}
			msg := err.Error()
			for _, s := range append(seedTexts, string(data[bytes.IndexByte(data, '\n')+1:])) {
				if strings.Contains(msg, s) {
					t.Errorf("%s %s: the error shows key material: %v", p, name, err)
				}
			}
		}
	}
}

// While the node runs, the statement that retires it comes from the keys that
// were loaded: the one Load verified as this node's, not a file read again that
// the caller would have to compare with the node itself.
func TestLoadedKeysHandOutTheVerifiedSelfRevocationOfThisNode(t *testing.T) {
	r := newRig(t)
	r.mustInit(1, t0)
	k := r.mustLoad()

	sr := k.SelfRevocation()
	if !bytes.Equal(sr.Wire(), r.read(keys.SelfRevocationPath)) {
		t.Error("the statement is not the bytes of the file")
	}
	if sr.Node() != k.Node() {
		t.Errorf("the statement is for %s, the node is %s", sr.Node(), k.Node())
	}
	onDisk, err := keys.ReadSelfRevocation(r.mem)
	if err != nil {
		t.Fatal(err)
	}
	if sr.Digest() != onDisk.Digest() {
		t.Errorf("digest %s, the file's is %s", sr.Digest(), onDisk.Digest())
	}
	// what the caller gets is a copy: changing it changes nothing in the keys
	sr.Wire()[0] ^= 0xff
	if !bytes.Equal(k.SelfRevocation().Wire(), r.read(keys.SelfRevocationPath)) {
		t.Error("changing the returned bytes changed the statement of the keys")
	}
}
