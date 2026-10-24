package keys_test

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/keys"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/revocation"
)

// What init writes, file by file, checked against the formats rather than
// against the code: the seeds are the first bytes of the random source, the key
// files hold them in the documented envelope, the clock and marker files hold
// the time of init.
func TestInitWritesEveryFileOfKeys(t *testing.T) {
	r := newRig(t)
	rd := newSeeded(1)
	plan, err := r.runInit(rd, t0)
	if err != nil {
		t.Fatal(err)
	}

	if rd.n != 64 {
		t.Errorf("init drew %d random bytes, want 64: one 32-byte seed per key", rd.n)
	}
	seeds := stream(1, 64)
	idSeed, netSeed := seeds[:32], seeds[32:]
	if got := oracleSeed(t, "identity-key", r.read(keys.IdentityKeyPath)); !bytes.Equal(got, idSeed) {
		t.Error("identity.key does not hold the first 32 bytes of the random source")
	}
	if got := oracleSeed(t, "network-key", r.read(keys.NetworkKeyPath)); !bytes.Equal(got, netSeed) {
		t.Error("network.key does not hold the second 32 bytes of the random source")
	}

	// the self-revocation verifies and names the node of the identity key
	rec, err := record.Decode(r.read(keys.SelfRevocationPath), record.DefaultMaxInput)
	if err != nil {
		t.Fatal(err)
	}
	rv, err := revocation.Verify(rec)
	if err != nil {
		t.Fatalf("self-revocation does not verify: %v", err)
	}
	if rv.Kind() != revocation.KindNodeSelf || rv.Node() != plan.Node() {
		t.Errorf("self-revocation is %v for %s, want a node self-revocation for %s", rv.Kind(), rv.Node(), plan.Node())
	}
	if plan.SelfRevocation().Digest != rv.Digest() || !bytes.Equal(plan.SelfRevocation().Wire, r.read(keys.SelfRevocationPath)) {
		t.Error("the plan does not describe the statement that was written")
	}
	// the node is the one that the identity public key derives (checked through the certificate's signer)
	idPub := ed25519.NewKeyFromSeed(idSeed).Public().(ed25519.PublicKey)
	if len(rec.Signatures) != 1 || !bytes.Equal(rec.Signatures[0].Key.Bytes(), idPub) {
		t.Error("the self-revocation is not signed by the identity key")
	}

	node, serial, digest := oracleSerial(t, r.read(keys.SerialPath))
	crec, err := record.Decode(r.read(keys.CertificatePath), record.DefaultMaxInput)
	if err != nil {
		t.Fatal(err)
	}
	if node != plan.Node().String() || serial != 1 || digest != oracleDigest(crec.Type, crec.Payload) {
		t.Errorf("serial file = %s/%d/%s, want this node, serial 1 and the digest of the certificate", node, serial, digest)
	}
	cv, err := record.Verify(record.NetworkKeyCertificateDescriptor, crec)
	if err != nil {
		t.Fatalf("certificate does not verify: %v", err)
	}
	netPub := ed25519.NewKeyFromSeed(netSeed).Public().(ed25519.PublicKey)
	if !bytes.Equal(cv.Payload().NetworkKey.Bytes(), netPub) {
		t.Error("the certificate does not name the network key that was written")
	}

	if hw, paused := oracleClock(t, r.read(keys.ClockPath)); hw != rfc3339(t0) || paused != "" {
		t.Errorf("clock.rec = %q paused %q, want the time of init and no pause", hw, paused)
	}
	if body := oracleBody(t, "init", r.read(keys.InitMarkerPath)); string(body) != `{"completed_at":"`+rfc3339(t0)+`"}` {
		t.Errorf("init.rec body = %s", body)
	}
	if init, err := keys.Initialized(r.mem); err != nil || !init {
		t.Errorf("Initialized = %v, %v", init, err)
	}
}

// The marker is what says init finished: before it exists the keys are not
// initialized, whatever else is in keys/.
func TestInitializedIsFalseUntilTheMarkerIsWritten(t *testing.T) {
	r := newRig(t)
	if init, err := keys.Initialized(r.mem); err != nil || init {
		t.Fatalf("Initialized on an empty disk = %v, %v", init, err)
	}
	if _, err := r.runInitKeysOnly(newSeeded(1), t0); err != nil {
		t.Fatal(err)
	}
	if init, err := keys.Initialized(r.mem); err != nil || init {
		t.Fatalf("Initialized without the marker = %v, %v", init, err)
	}
	if _, err := r.runInit(newSeeded(2), t0); err != nil {
		t.Fatal(err)
	}
	if init, err := keys.Initialized(r.mem); err != nil || !init {
		t.Fatalf("Initialized with the marker = %v, %v", init, err)
	}
	b := append([]byte(nil), r.read(keys.InitMarkerPath)...)
	b[len(b)-2] ^= 1
	put(t, r.mem, keys.InitMarkerPath, b)
	if _, err := keys.Initialized(r.mem); !errors.Is(err, keys.ErrCorrupt) {
		t.Errorf("Initialized with a damaged marker = %v, want ErrCorrupt", err)
	}
}

// PlanInit only reads: it writes nothing, and plans from what it found.
func TestPlanInitWritesNothingItself(t *testing.T) {
	r := newRig(t)
	before := snapshot(t, r.mem)
	if _, err := keys.PlanInit(r.w.Reader(), newSeeded(1), t0); err != nil {
		t.Fatal(err)
	}
	if r.obs.count() != 0 || !reflect.DeepEqual(snapshot(t, r.mem), before) {
		t.Errorf("PlanInit wrote: %v", r.obs.names())
	}
}

// Once init has finished, a second init is refused: nothing is generated and
// nothing is touched.
func TestInitRefusesWhenAlreadyInitialized(t *testing.T) {
	r := newRig(t)
	r.mustInit(1, t0)
	before := snapshot(t, r.mem)
	rd := newSeeded(2)
	if _, err := r.runInit(rd, t0.Add(time.Hour)); !errors.Is(err, keys.ErrAlreadyInitialized) {
		t.Fatalf("second init = %v, want ErrAlreadyInitialized", err)
	}
	if rd.n != 0 {
		t.Errorf("the refused init drew %d random bytes", rd.n)
	}
	if !reflect.DeepEqual(snapshot(t, r.mem), before) {
		t.Error("the refused init changed the disk")
	}
}

// An init that was interrupted and is run again never overwrites a key: after a
// crash at any point of init, in any kind of crash, the second run leaves every
// key file that existed byte for byte as it was, the self-revocation and the
// clock record included, and ends in a complete, loadable set whose node is the
// one the surviving identity key names. The disk of every crash state is
// compared with a copy taken before the second run.
func TestInitRerunNeverOverwritesAKey(t *testing.T) {
	protected := []string{keys.IdentityKeyPath, keys.NetworkKeyPath, keys.SelfRevocationPath, keys.ClockPath}
	rng := rand.New(rand.NewPCG(5, 6))

	golden := newRig(t)
	var states []crashState
	golden.obs.on = func(p fsx.Point) {
		for _, s := range crashStates(golden.mem, rng, 4) {
			s.name += " before " + p.String()
			states = append(states, s)
		}
	}
	golden.mustInit(1, t0)
	golden.obs.on = nil

	partial := map[string]int{}
	finished := 0
	for _, s := range states {
		before := map[string][]byte{}
		for _, p := range protected {
			if b, err := s.mem.ReadFile(p); err == nil {
				before[p] = b
			}
		}
		serialBefore, serialErr := s.mem.ReadFile(keys.SerialPath)

		r := open(t, s.mem)
		if done, _ := keys.Initialized(s.mem); done {
			// the crash came after the marker: init had finished, and says so
			if _, err := r.runInit(newSeeded(2), t0.Add(time.Minute)); !errors.Is(err, keys.ErrAlreadyInitialized) {
				t.Errorf("%s: init over a finished init = %v, want ErrAlreadyInitialized", s.name, err)
			}
			finished++
			continue
		}
		rd := newSeeded(2) // a different source: a rerun that regenerated anything would show
		plan, err := r.runInit(rd, t0.Add(time.Minute))
		if err != nil {
			t.Fatalf("%s: rerun of init: %v", s.name, err)
		}
		for _, p := range protected {
			if want, ok := before[p]; ok && !bytes.Equal(r.read(p), want) {
				t.Errorf("%s: %s was overwritten by the rerun", s.name, p)
			}
		}
		drawn := 0
		if _, ok := before[keys.IdentityKeyPath]; !ok {
			drawn += 32
		}
		if _, ok := before[keys.NetworkKeyPath]; !ok {
			drawn += 32
		}
		if rd.n != drawn {
			t.Errorf("%s: the rerun drew %d random bytes, want %d (one seed per key that was missing)", s.name, rd.n, drawn)
		}
		if serialErr == nil {
			// the serial of an earlier run is never lowered
			_, was, _ := oracleSerial(t, serialBefore)
			if _, now, _ := oracleSerial(t, r.read(keys.SerialPath)); now < was {
				t.Errorf("%s: the serial went from %d to %d", s.name, was, now)
			}
		}
		k, err := keys.Load(r.w)
		if err != nil {
			t.Fatalf("%s: Load after the rerun: %v", s.name, err)
		}
		if k.Node() != plan.Node() {
			t.Errorf("%s: node %s after the rerun, plan said %s", s.name, k.Node(), plan.Node())
		}
		if k.RenewalInterrupted() {
			t.Errorf("%s: the rerun left a renewal interrupted", s.name)
		}
		if init, err := keys.Initialized(r.mem); err != nil || !init {
			t.Errorf("%s: not initialized after the rerun: %v, %v", s.name, init, err)
		}
		key := ""
		if _, ok := before[keys.IdentityKeyPath]; ok {
			key += "I"
		}
		if _, ok := before[keys.NetworkKeyPath]; ok {
			key += "N"
		}
		partial[key]++
	}
	if finished == 0 {
		t.Error("no crash state came after the marker")
	}
	// the crash states cover init from nothing to everything but the marker
	for _, want := range []string{"", "I", "IN"} {
		if partial[want] == 0 {
			t.Errorf("no crash state had exactly the keys %q; coverage %v", want, partial)
		}
	}
}

// A valid self-revocation is reused as it is; nothing is drawn from the random
// source and nothing is written for it.
func TestInitReusesAValidSelfRevocation(t *testing.T) {
	r := newRig(t)
	if _, err := r.runInitKeysOnly(newSeeded(1), t0); err != nil {
		t.Fatal(err)
	}
	was := r.read(keys.SelfRevocationPath)

	r2 := r.restart()
	rd := newSeeded(2)
	plan, err := keys.PlanInit(r2.w.Reader(), rd, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if rd.n != 0 {
		t.Errorf("init drew %d random bytes although every key exists", rd.n)
	}
	if !bytes.Equal(plan.SelfRevocation().Wire, was) {
		t.Error("the plan carries another statement than the one on disk")
	}
	if _, err := r2.runInit(newSeeded(3), t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(r2.read(keys.SelfRevocationPath), was) {
		t.Error("the self-revocation was rewritten")
	}
	// the certificate and the serial that agree are reused as they are as well
	if !bytes.Equal(r2.read(keys.CertificatePath), r.read(keys.CertificatePath)) || !bytes.Equal(r2.read(keys.SerialPath), r.read(keys.SerialPath)) {
		t.Error("a certificate that matches its serial was signed again")
	}
}

// A self-revocation that does not verify under the current identity key is
// refused, naming the file, and the keys are not touched. It is never reused,
// and never replaced: it may belong to an identity the owner deleted.
func TestInitRefusesASelfRevocationThatDoesNotVerifyUnderTheCurrentKey(t *testing.T) {
	base := newRig(t)
	if _, err := base.runInitKeysOnly(newSeeded(1), t0); err != nil {
		t.Fatal(err)
	}
	other := newRig(t)
	other.mustInit(9, t0)
	mine := base.read(keys.SelfRevocationPath)

	flipped := append([]byte(nil), mine...)
	flipped[len(flipped)-5] ^= 0x01
	rows := []struct {
		name string
		data []byte
		is   error
	}{
		{"another identity's statement", other.read(keys.SelfRevocationPath), keys.ErrInconsistent},
		{"a certificate", base.read(keys.CertificatePath), keys.ErrCorrupt},
		{"a revocation of a certificate, which verifies but is not a node's self-revocation", certRevocationWire(t, base), keys.ErrCorrupt},
		{"a flipped signature bit", flipped, keys.ErrCorrupt},
		{"truncated", mine[:len(mine)-10], keys.ErrCorrupt},
		{"empty", nil, keys.ErrCorrupt},
		{"text", []byte("revoked\n"), keys.ErrCorrupt},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			mem := base.mem.Clone()
			put(t, mem, keys.SelfRevocationPath, row.data)
			before := snapshot(t, mem)
			r := open(t, mem)
			rd := newSeeded(2)
			_, err := keys.PlanInit(r.w.Reader(), rd, t0)
			if !errors.Is(err, row.is) {
				t.Fatalf("PlanInit = %v, want %v", err, row.is)
			}
			asFileError(t, err, keys.SelfRevocationPath)
			if rd.n != 0 || r.obs.count() != 0 || !reflect.DeepEqual(snapshot(t, mem), before) {
				t.Errorf("the refusal drew %d random bytes, wrote %v or changed the disk", rd.n, r.obs.names())
			}
		})
	}
}

// A key file that cannot be parsed is refused, naming it. Init generates
// nothing in its place.
func TestInitRefusesAnInvalidKeyFile(t *testing.T) {
	base := newRig(t)
	if _, err := base.runInitKeysOnly(newSeeded(1), t0); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{keys.IdentityKeyPath, keys.NetworkKeyPath, keys.SerialPath, keys.ClockPath} {
		good := base.read(p)
		flip := append([]byte(nil), good...)
		flip[len(flip)-4] ^= 0x01
		for name, data := range map[string][]byte{
			"flipped bit": flip,
			"truncated":   good[:len(good)-1],
			"trailing":    append(append([]byte(nil), good...), '\n'),
			"empty":       nil,
		} {
			t.Run(p+" "+name, func(t *testing.T) {
				mem := base.mem.Clone()
				put(t, mem, p, data)
				before := snapshot(t, mem)
				r := open(t, mem)
				rd := newSeeded(2)
				_, err := keys.PlanInit(r.w.Reader(), rd, t0)
				if !errors.Is(err, keys.ErrCorrupt) {
					t.Fatalf("PlanInit = %v, want ErrCorrupt", err)
				}
				asFileError(t, err, p)
				if rd.n != 0 || !reflect.DeepEqual(snapshot(t, mem), before) {
					t.Errorf("the refusal drew %d random bytes or changed the disk", rd.n)
				}
			})
		}
	}
}

// A clock record that is valid is kept whatever it says: the pause flag, and a
// high water mark in the future. Deleting init.rec and running init again must
// not clear a rollback pause.
func TestInitNeverOverwritesTheClockRecord(t *testing.T) {
	base := newRig(t)
	if _, err := base.runInitKeysOnly(newSeeded(1), t0); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"paused":                 []byte(goldenClockPausedFile),
		"mark far in the future": oracleRecordFile("clock", []byte(`{"high_water":"2099-01-01T00:00:00Z"}`)),
	} {
		t.Run(name, func(t *testing.T) {
			mem := base.mem.Clone()
			put(t, mem, keys.ClockPath, data)
			r := open(t, mem)
			if _, err := r.runInit(newSeeded(2), t0); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(r.read(keys.ClockPath), data) {
				t.Error("clock.rec was rewritten")
			}
		})
	}
}

// The certificate is the one file init may replace, because it can be issued
// again: when it does not match the keys and the serial it is signed anew, at a
// serial above every serial known, written to the serial file first.
func TestInitReSignsACertificateThatDoesNotMatch(t *testing.T) {
	base := newRig(t)
	if _, err := base.runInitKeysOnly(newSeeded(1), t0); err != nil {
		t.Fatal(err)
	}
	other := newRig(t)
	other.mustInit(9, t0)
	oldSerial := base.read(keys.SerialPath)

	rows := []struct {
		name    string
		mutate  func(*rig)
		want    uint64
		changed bool // the certificate file is replaced
	}{
		{"certificate of another node", func(r *rig) { put(t, r.mem, keys.CertificatePath, other.read(keys.CertificatePath)) }, 2, true},
		{"certificate damaged", func(r *rig) { put(t, r.mem, keys.CertificatePath, []byte("junk")) }, 2, true},
		{"certificate missing", func(r *rig) { remove(t, r.mem, keys.CertificatePath) }, 2, true},
		{"serial ahead of the certificate: an interrupted renewal", func(r *rig) {
			put(t, r.mem, keys.SerialPath, oracleSerialFile(r.mustLoadNode(), 5, strings.Repeat("ab", 32)))
		}, 6, true},
		{"serial missing, certificate valid", func(r *rig) { remove(t, r.mem, keys.SerialPath) }, 1, false},
		{"serial behind a valid certificate of this node", func(r *rig) {
			// two renewals: the certificate is at 3, then the serial file is put back to 1
			k := r.mustLoad()
			for i := 1; i <= 2; i++ {
				if err := k.Renew(t0.Add(time.Duration(i) * time.Hour)); err != nil {
					t.Fatal(err)
				}
			}
			put(t, r.mem, keys.SerialPath, oldSerial)
		}, 4, true},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			r := open(t, base.mem.Clone())
			row.mutate(r)
			certBefore, _ := r.mem.ReadFile(keys.CertificatePath)
			r = open(t, r.mem.Clone())
			if _, err := r.runInit(newSeeded(2), t0.Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			k := r.mustLoad()
			if got := k.Certificate().Payload().Serial; got != row.want {
				t.Errorf("certificate serial = %d, want %d", got, row.want)
			}
			if k.SerialSeen().Serial != row.want || k.RenewalInterrupted() {
				t.Errorf("seen %d interrupted %v after init, want %d and not interrupted", k.SerialSeen().Serial, k.RenewalInterrupted(), row.want)
			}
			if same := bytes.Equal(r.read(keys.CertificatePath), certBefore); same == row.changed {
				t.Errorf("certificate replaced = %v, want %v", !same, row.changed)
			}
			if !row.changed && !bytes.Equal(r.read(keys.SerialPath), oldSerial) {
				// a missing serial is written from the certificate: the same content as the original
				t.Error("the serial written from the certificate differs from the one init made")
			}
		})
	}
}

func (r *rig) mustLoadNode() string {
	r.t.Helper()
	// the node of the statement on disk, read without Load: the files may be inconsistent
	rec, err := record.Decode(r.read(keys.SelfRevocationPath), record.DefaultMaxInput)
	if err != nil {
		r.t.Fatal(err)
	}
	rv, err := revocation.Verify(rec)
	if err != nil {
		r.t.Fatal(err)
	}
	return rv.Node().String()
}

// certRevocationWire is a statement that verifies as a revocation, signed by the
// identity key of the rig's node, of the kind that retires one certificate. It
// is not the statement that retires the identity.
func certRevocationWire(t *testing.T, r *rig) []byte {
	t.Helper()
	k := open(t, r.mem.Clone()).mustLoad()
	signer, err := record.NewEd25519Signer(ed25519.NewKeyFromSeed(stream(1, 32)))
	if err != nil {
		t.Fatal(err)
	}
	rec, err := revocation.IssueNetworkKeyCertificateRevocation(k.Certificate(), signer)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := rec.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return wire
}
