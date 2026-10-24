package keys_test

import (
	"encoding/hex"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/keys"
)

// The golden files load: the formats that other tools and later versions read
// are the ones written out in the fixtures, not whatever the code writes today.
func TestLoadOfTheGoldenFiles(t *testing.T) {
	r := open(t, goldenDisk(t))
	k := r.mustLoad()

	if k.Node().String() != goldenNode {
		t.Errorf("Node = %s, want %s", k.Node(), goldenNode)
	}
	if got := hex.EncodeToString(k.NetworkSigner().Public().Bytes()); got != "3d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c" {
		t.Errorf("network public key = %s, want the RFC 8032 key 2", got)
	}
	cert := k.Certificate()
	if cert.Payload().Serial != 1 || cert.Digest().String() != goldenCertificateDigest {
		t.Errorf("certificate serial %d digest %s", cert.Payload().Serial, cert.Digest())
	}
	if seen := k.SerialSeen(); seen.Serial != 1 || seen.Digest.String() != goldenCertificateDigest || seen.Node.String() != goldenNode {
		t.Errorf("SerialSeen = %+v", seen)
	}
	if k.RenewalInterrupted() {
		t.Error("the golden set is not an interrupted renewal")
	}
	// the certificate expired in 2023: renewal is due, and the reason is the expiry
	if v, reason := k.RenewalDue(t0); v != keys.RenewDue || reason != keys.RenewReasonExpiring {
		t.Errorf("RenewalDue = %v/%v, want due because the certificate expired", v, reason)
	}
}

func TestLoadOfTheGoldenFilesWithAnInterruptedRenewal(t *testing.T) {
	mem := goldenDisk(t)
	put(t, mem, keys.SerialPath, []byte(goldenSerialFileInterrupted))
	k := open(t, mem).mustLoad()
	if !k.RenewalInterrupted() || k.SerialSeen().Serial != 2 || k.Certificate().Payload().Serial != 1 {
		t.Errorf("interrupted=%v seen=%d cert=%d, want an interruption between serial 2 and certificate 1",
			k.RenewalInterrupted(), k.SerialSeen().Serial, k.Certificate().Payload().Serial)
	}
}

func TestClockAndMarkerGoldenFilesRead(t *testing.T) {
	mem := goldenDisk(t)
	got, err := keys.ReadClock(mem)
	if err != nil {
		t.Fatal(err)
	}
	want := keys.ClockState{HighWater: time.Date(2026, 10, 24, 5, 56, 0, 0, time.UTC)}
	if got != want || got.Paused() {
		t.Errorf("ReadClock = %+v, want %+v", got, want)
	}
	put(t, mem, keys.ClockPath, []byte(goldenClockPausedFile))
	got, err = keys.ReadClock(mem)
	if err != nil {
		t.Fatal(err)
	}
	want.RollbackPausedSince = time.Date(2026, 10, 24, 5, 50, 0, 0, time.UTC)
	if got != want || !got.Paused() {
		t.Errorf("ReadClock of a paused record = %+v, want %+v", got, want)
	}
	if init, err := keys.Initialized(mem); err != nil || !init {
		t.Errorf("Initialized = %v, %v", init, err)
	}
}
