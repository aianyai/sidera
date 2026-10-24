package keys_test

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"reflect"
	"syscall"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/keys"
	"github.com/aianyai/sidera/internal/node/txn"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/timing"
)

// A renewal writes the serial first and the certificate second, each durable
// before the next step: the sequence of points is written out by hand.
func TestRenewWritesTheSerialBeforeTheCertificate(t *testing.T) {
	r := newRig(t)
	r.mustInit(1, t0)
	k := r.mustLoad()
	r.obs.points = nil
	if err := k.Renew(t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"cert-renew/serial.create#1", "cert-renew/serial.write#1", "cert-renew/serial.sync#1", "cert-renew/serial.rename#1", "cert-renew/serial.sync-dir#1",
		"cert-renew/cert.create#1", "cert-renew/cert.write#1", "cert-renew/cert.sync#1", "cert-renew/cert.rename#1", "cert-renew/cert.sync-dir#1",
		"cert-renew/commit#1",
	}
	if got := r.obs.names(); !reflect.DeepEqual(got, want) {
		t.Errorf("points of a renewal:\n got %v\nwant %v", got, want)
	}
}

// Renew writes a certificate that a verifier accepts: written for the node and
// its network key, signed by the identity key, one serial higher, valid from
// the clock tolerance before it was issued for thirty days, and named by the
// serial file's digest, which is computed here by hand.
func TestRenewIssuesACertificateForTheNetworkKey(t *testing.T) {
	r := newRig(t)
	r.mustInit(1, t0)
	k := r.mustLoad()
	now := t0.Add(20 * 24 * time.Hour).Add(1500 * time.Millisecond)
	if err := k.Renew(now); err != nil {
		t.Fatal(err)
	}

	wire := r.read(keys.CertificatePath)
	rec, err := record.Decode(wire, record.DefaultMaxInput)
	if err != nil {
		t.Fatal(err)
	}
	v, err := record.Verify(record.NetworkKeyCertificateDescriptor, rec)
	if err != nil {
		t.Fatalf("the certificate does not verify: %v", err)
	}
	c := v.Payload()
	if c.Serial != 2 || c.Node != k.Node() || c.NetworkKey != k.NetworkSigner().Public() {
		t.Errorf("certificate %+v is not serial 2 for this node and its network key", c)
	}
	whole := time.Unix(now.Unix(), 0).UTC()
	if want := whole.Add(-timing.ClockTolerance); !c.NotBefore.Equal(want) {
		t.Errorf("NotBefore = %v, want the issue time less the clock tolerance, %v", c.NotBefore, want)
	}
	if want := whole.Add(30 * 24 * time.Hour); !c.NotAfter.Equal(want) {
		t.Errorf("NotAfter = %v, want thirty days after the issue time, %v", c.NotAfter, want)
	}
	if signers := v.Signers(); len(signers) != 1 || signers[0].IsZero() {
		t.Errorf("signers = %v, want the identity key alone", signers)
	}

	node, serial, digest := oracleSerial(t, r.read(keys.SerialPath))
	if node != k.Node().String() || serial != 2 || digest != oracleDigest(rec.Type, rec.Payload) {
		t.Errorf("serial file = %s/%d/%s, want this node, 2 and the digest %s of the certificate", node, serial, digest, oracleDigest(rec.Type, rec.Payload))
	}
	if !reflect.DeepEqual(k.CertificateWire(), wire) {
		t.Error("the loaded certificate is not the written one")
	}
}

// oracleDigest is the record digest by its definition: SHA-256 over the
// domain, the type and the payload, each behind its uvarint length.
func oracleDigest(typ string, payload []byte) string {
	h := sha256.New()
	for _, f := range [][]byte{[]byte("sidera/record-digest"), []byte(typ), payload} {
		h.Write(binary.AppendUvarint(nil, uint64(len(f))))
		h.Write(f)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// The renewal rule: due when the remaining validity is at most ten days
// (inclusive), when the certificate has expired, or when an earlier renewal was
// interrupted, and the reason says which.
func TestRenewalDue(t *testing.T) {
	cert := record.NetworkKeyCertificate{
		NotBefore: t0.Add(-timing.ClockTolerance),
		NotAfter:  t0.Add(30 * 24 * time.Hour),
	}
	at := func(remaining time.Duration) time.Time { return cert.NotAfter.Add(-remaining) }
	rows := []struct {
		name        string
		now         time.Time
		interrupted bool
		verdict     keys.RenewVerdict
		reason      keys.RenewReason
	}{
		{"just issued", t0, false, keys.RenewNotDue, keys.RenewReasonCurrent},
		{"eleven days left", at(11 * 24 * time.Hour), false, keys.RenewNotDue, keys.RenewReasonCurrent},
		{"ten days and a second left", at(10*24*time.Hour + time.Second), false, keys.RenewNotDue, keys.RenewReasonCurrent},
		{"exactly ten days left", at(10 * 24 * time.Hour), false, keys.RenewDue, keys.RenewReasonExpiring},
		{"nine days left", at(9 * 24 * time.Hour), false, keys.RenewDue, keys.RenewReasonExpiring},
		{"one second left", at(time.Second), false, keys.RenewDue, keys.RenewReasonExpiring},
		{"expired", cert.NotAfter, false, keys.RenewDue, keys.RenewReasonExpiring},
		{"long expired", cert.NotAfter.Add(90 * 24 * time.Hour), false, keys.RenewDue, keys.RenewReasonExpiring},
		{"interrupted, freshly issued", t0, true, keys.RenewDue, keys.RenewReasonInterrupted},
		{"interrupted and expiring: the interruption is the reason", at(time.Hour), true, keys.RenewDue, keys.RenewReasonInterrupted},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			v, reason := keys.RenewalDue(cert, row.interrupted, row.now)
			if v != row.verdict || reason != row.reason {
				t.Errorf("RenewalDue = %v/%v, want %v/%v", v, reason, row.verdict, row.reason)
			}
		})
	}
	if v, reason := keys.RenewalDue(record.NetworkKeyCertificate{}, false, t0); v != keys.RenewDue {
		t.Errorf("a zero certificate: %v/%v, want it due (fail closed)", v, reason)
	}
}

// A renewal that stopped between the serial and the certificate is recognised
// when the keys are loaded: the node starts, says the renewal was interrupted,
// the recovery predicate names it, a renewal is due at once whatever the
// remaining validity, and the repair issues a certificate at the serial that
// was reserved or above, after which every trace of the interruption is gone.
func TestInterruptedRenewalIsRecognisedAndRepaired(t *testing.T) {
	r := newRig(t)
	r.mustInit(1, t0)

	var crashed *fsxtest.Mem
	r.obs.on = func(p fsx.Point) {
		if p.String() == "cert-renew/cert.create#1" {
			crashed = r.mem.Clone()
		}
	}
	if err := r.mustLoad().Renew(t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	r2 := open(t, crashed)
	work, err := keys.RecoveryWork(r2.w.Reader())
	if err != nil {
		t.Fatal(err)
	}
	if want := []keys.Work{{Reason: keys.WorkRenewalInterrupted, Path: keys.CertificatePath}}; !reflect.DeepEqual(work, want) {
		t.Fatalf("RecoveryWork = %v, want %v", work, want)
	}
	k := r2.mustLoad()
	if !k.RenewalInterrupted() {
		t.Fatal("Load did not report the interrupted renewal")
	}
	now := t0.Add(2 * time.Hour) // the certificate has twenty-nine days left
	if v, reason := k.RenewalDue(now); v != keys.RenewDue || reason != keys.RenewReasonInterrupted {
		t.Errorf("RenewalDue = %v/%v, want due because of the interruption", v, reason)
	}

	if err := k.Renew(now); err != nil {
		t.Fatal(err)
	}
	if k.RenewalInterrupted() {
		t.Error("the repair left the renewal interrupted")
	}
	if got := k.Certificate().Payload().Serial; got != 3 {
		t.Errorf("repaired certificate serial = %d, want 3", got)
	}
	again := r2.restart().mustLoad()
	if again.RenewalInterrupted() || again.Certificate().Payload().Serial != 3 || again.SerialSeen().Serial != 3 {
		t.Errorf("after the repair and a restart: interrupted=%v cert=%d seen=%d", again.RenewalInterrupted(), again.Certificate().Payload().Serial, again.SerialSeen().Serial)
	}
	if work, err := keys.RecoveryWork(r2.mem); err != nil || len(work) != 0 {
		t.Errorf("RecoveryWork after the repair = %v, %v; want nothing", work, err)
	}
}

// The pure predicate, by its rule: the serial seen is above the certificate's.
func TestRenewalInterruptedPredicate(t *testing.T) {
	r := newRig(t)
	r.mustInit(1, t0)
	k := r.mustLoad()
	cert := k.Certificate()
	seen := k.SerialSeen()

	if keys.RenewalInterrupted(seen, cert) {
		t.Error("serial seen equal to the certificate's is not an interruption")
	}
	above := seen
	above.Serial = cert.Payload().Serial + 1
	if !keys.RenewalInterrupted(above, cert) {
		t.Error("serial seen above the certificate's is an interruption")
	}
	above.Serial = cert.Payload().Serial + 40
	if !keys.RenewalInterrupted(above, cert) {
		t.Error("serial seen far above the certificate's is an interruption")
	}
	below := seen
	below.Serial = 0
	if keys.RenewalInterrupted(below, cert) {
		t.Error("serial seen below the certificate's is not an interruption (Load refuses it as inconsistent)")
	}
}

// A renewal never signs while the wall clock is in doubt: during a rollback
// pause, or when the clock is behind the persisted high water mark by more than
// the tolerance. A reading within the tolerance is fine, and so is a first
// start with no mark. Nothing is written when it refuses.
func TestRenewIsRefusedWhileTheClockIsInDoubt(t *testing.T) {
	setClock := func(t *testing.T, r *rig, hw time.Time, paused time.Time) {
		t.Helper()
		body := `{"high_water":"` + rfc3339(hw) + `"`
		if !paused.IsZero() {
			body += `,"rollback_paused_since":"` + rfc3339(paused) + `"`
		}
		put(t, r.mem, keys.ClockPath, oracleRecordFile("clock", []byte(body+"}")))
	}
	rows := []struct {
		name   string
		clock  func(t *testing.T, r *rig)
		now    time.Time
		refuse error
	}{
		{"paused", func(t *testing.T, r *rig) { setClock(t, r, t0, t0.Add(-time.Hour)) }, t0.Add(time.Hour), keys.ErrRollbackPaused},
		{"paused even with a clock ahead of the mark", func(t *testing.T, r *rig) { setClock(t, r, t0, t0) }, t0.Add(48 * time.Hour), keys.ErrRollbackPaused},
		{"behind the mark by more than the tolerance", func(t *testing.T, r *rig) { setClock(t, r, t0.Add(time.Hour), time.Time{}) }, t0.Add(time.Hour - timing.ClockTolerance - time.Second), keys.ErrClockRolledBack},
		{"behind the mark by exactly the tolerance", func(t *testing.T, r *rig) { setClock(t, r, t0.Add(time.Hour), time.Time{}) }, t0.Add(time.Hour - timing.ClockTolerance), nil},
		{"ahead of the mark", func(t *testing.T, r *rig) { setClock(t, r, t0, time.Time{}) }, t0.Add(time.Hour), nil},
		{"no clock record: a first start", func(t *testing.T, r *rig) { remove(t, r.mem, keys.ClockPath) }, t0.Add(time.Hour), nil},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			r := newRig(t)
			r.mustInit(1, t0)
			k := r.mustLoad()
			row.clock(t, r)
			before := snapshot(t, r.mem)
			r.obs.points = nil
			err := k.Renew(row.now)
			if row.refuse != nil {
				if !errors.Is(err, row.refuse) {
					t.Fatalf("Renew = %v, want %v", err, row.refuse)
				}
				if r.obs.count() != 0 || !reflect.DeepEqual(snapshot(t, r.mem), before) {
					t.Errorf("a refused renewal wrote: %v", r.obs.names())
				}
				if k.SerialSeen().Serial != 1 {
					t.Error("a refused renewal advanced the serial in memory")
				}
				return
			}
			if err != nil {
				t.Fatalf("Renew = %v, want it to succeed", err)
			}
		})
	}
}

// Memory follows the disk and never runs ahead of it: a failure before the
// serial reached the disk leaves both as they were and the next renewal tries
// the same serial; a failure after it leaves the serial advanced in memory too,
// so a retry in the same process cannot reuse it, and the failure is fatal.
func TestRenewFailureLeavesMemoryAndDiskConsistent(t *testing.T) {
	setup := func(t *testing.T) (*rig, *fsxtest.Faulty) {
		t.Helper()
		base := newRig(t)
		base.mustInit(1, t0)
		mem := base.mem.Clone()
		faulty := fsxtest.NewFaulty(t, mem)
		return openOver(t, mem, faulty), faulty
	}

	t.Run("before the serial is durable", func(t *testing.T) {
		r, faulty := setup(t)
		k := r.mustLoad()
		faulty.Fail(fsxtest.Match{Tx: "cert-renew", Role: "serial", Step: fsx.StepRename, Hit: 1}, syscall.EIO)
		err := k.Renew(t0.Add(time.Hour))
		if err == nil || txn.IsFatal(err) {
			t.Fatalf("Renew = %v, want an ordinary failure", err)
		}
		if k.SerialSeen().Serial != 1 || k.Certificate().Payload().Serial != 1 || k.RenewalInterrupted() {
			t.Error("memory moved although nothing was written")
		}
		if _, serial, _ := oracleSerial(t, r.read(keys.SerialPath)); serial != 1 {
			t.Errorf("serial file = %d, want it untouched", serial)
		}
	})

	t.Run("after the serial is durable", func(t *testing.T) {
		r, faulty := setup(t)
		k := r.mustLoad()
		faulty.Fail(fsxtest.Match{Tx: "cert-renew", Role: "cert", Step: fsx.StepCreate, Hit: 1}, syscall.EIO)
		err := k.Renew(t0.Add(time.Hour))
		if err == nil || !txn.IsFatal(err) {
			t.Fatalf("Renew = %v, want a fatal failure: the commit point had been passed", err)
		}
		if !k.RenewalInterrupted() || k.SerialSeen().Serial != 2 || k.Certificate().Payload().Serial != 1 {
			t.Errorf("memory: interrupted=%v seen=%d cert=%d; want the reserved serial remembered and the old certificate kept",
				k.RenewalInterrupted(), k.SerialSeen().Serial, k.Certificate().Payload().Serial)
		}
		if _, serial, _ := oracleSerial(t, r.read(keys.SerialPath)); serial != 2 {
			t.Errorf("serial file = %d, want 2", serial)
		}
	})
}
