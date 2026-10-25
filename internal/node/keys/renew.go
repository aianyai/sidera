package keys

import (
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/record"
)

// RenewVerdict says whether a certificate is to be renewed. The zero value is
// unset.
type RenewVerdict uint8

const (
	RenewNotDue RenewVerdict = iota + 1
	RenewDue
)

// RenewReason says why RenewalDue decided as it did. The zero value is unset.
type RenewReason uint8

const (
	// RenewReasonCurrent: more than RenewBefore of the validity remains.
	RenewReasonCurrent RenewReason = iota + 1
	// RenewReasonInterrupted: an earlier renewal reserved a serial and stopped
	// before it wrote the certificate. It is the reason whatever the remaining
	// validity, because the certificate in hand carries a serial that is below
	// the one already issued.
	RenewReasonInterrupted
	// RenewReasonExpiring: RenewBefore or less of the validity remains, or the
	// certificate has expired, or it has no validity at all.
	RenewReasonExpiring
)

// RenewalDue decides whether the certificate is to be renewed at now: when an
// earlier renewal was interrupted, and when RenewBefore or less of the validity
// remains (the boundary is inclusive), which includes a certificate that has
// expired or has no validity at all, so that a value nobody filled in is renewed
// and never trusted.
func RenewalDue(cert record.NetworkKeyCertificate, interrupted bool, now time.Time) (RenewVerdict, RenewReason) {
	switch {
	case interrupted:
		return RenewDue, RenewReasonInterrupted
	case cert.NotAfter.IsZero() || cert.NotAfter.Sub(now) <= RenewBefore:
		return RenewDue, RenewReasonExpiring
	}
	return RenewNotDue, RenewReasonCurrent
}

// RenewalDue is RenewalDue for the certificate in force.
func (k *Keys) RenewalDue(now time.Time) (RenewVerdict, RenewReason) {
	return RenewalDue(k.s.cert.Payload(), k.RenewalInterrupted(), now)
}

// RenewalInterrupted reports whether the serial file is ahead of the
// certificate: the serial seen is above the certificate's, so a renewal
// reserved a serial and never wrote the certificate. It is the one place that
// says so; the recovery predicate and the load use it.
func RenewalInterrupted(seen record.SerialSeen, cert record.Verified[record.NetworkKeyCertificate]) bool {
	return record.DecideSerial(&seen, cert) == record.SerialStale && cert.Payload().Serial < seen.Serial
}

// Renew issues the next certificate: the serial is higher than every serial this
// node has issued, reserved in the serial file first and made durable, then the
// certificate is written. A serial is never issued twice, so a crash between the
// two writes leaves a gap, which peers accept (they require only that serials
// rise), and the interrupted renewal is recognised at the next load. Renew is
// the repair of an interrupted renewal as well: it issues at the serial after
// the reserved one.
//
// It refuses to sign while the rollback pause is in force or while the clock is
// behind the persisted mark by more than the clock tolerance; it writes nothing
// then. Memory follows the disk: the serial is remembered when it is durable,
// the certificate when the transaction has committed.
func (k *Keys) Renew(now time.Time) error {
	s := k.s
	clk, err := ReadClock(s.w.Reader())
	if err != nil {
		return err
	}
	switch {
	case clk.Paused():
		return fmt.Errorf("keys: no certificate is signed: %w", ErrRollbackPaused)
	case rolledBack(now, clk.HighWater):
		return fmt.Errorf("keys: no certificate is signed: %w", ErrClockRolledBack)
	}

	cert, err := issueCertificate(s.identity, s.network, max(s.seen.Serial, s.cert.Payload().Serial)+1, now)
	if err != nil {
		return err
	}
	if record.DecideSerial(&s.seen, cert.verified) != record.SerialAccept {
		return fmt.Errorf("keys: the certificate issued does not carry a serial above the one seen")
	}
	seen := record.SerialSeenOf(cert.verified)

	tx := s.w.Begin(certRenew)
	defer tx.Close()
	if err := tx.Replace(RoleSerial, SerialPath, encodeSerial(seen), fsx.ModePrivate); err != nil {
		return err
	}
	s.seen = seen // the serial is durable: this process never issues it again
	if err := tx.Replace(RoleCertificate, CertificatePath, cert.wire, fsx.ModePrivate); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.cert, s.certWire = cert.verified, cert.wire
	return nil
}

// issueCertificate signs the certificate of the network key at the serial, valid
// from the clock tolerance before now for CertificateLifetime, and verifies what
// it signed, so that a certificate that no reader would accept is never written.
func issueCertificate(identity, network *privateKey, serial uint64, now time.Time) (certificate, error) {
	body := record.IssueNetworkKeyCertificate(identity.node, network.pub, serial, now, now.Add(CertificateLifetime))
	rec, err := record.Seal(record.NetworkKeyCertificateDescriptor, body, identity.signer())
	if err != nil {
		return certificate{}, fmt.Errorf("keys: issuing the certificate: %w", err)
	}
	wire, err := rec.Encode()
	if err != nil {
		return certificate{}, fmt.Errorf("keys: issuing the certificate: %w", err)
	}
	v, err := record.Verify(record.NetworkKeyCertificateDescriptor, rec)
	if err != nil {
		return certificate{}, fmt.Errorf("keys: the certificate issued does not verify: %w", err)
	}
	return certificate{verified: v, wire: wire}, nil
}

// WorkReason says what a recovery predicate found. The zero value is unset.
type WorkReason uint8

// WorkRenewalInterrupted: the serial file is ahead of the certificate.
const WorkRenewalInterrupted WorkReason = iota + 1

// Work is one finding of the recovery predicate.
type Work struct {
	Reason WorkReason
	// Path is the file that is to be written again.
	Path string
}

// RecoveryWork is the recovery predicate of this package, a pure function of the
// disk: what a start would have to repair. It is the interrupted renewal and
// nothing else, and it is empty on every state that a run with no crash leaves,
// whatever the step. The load evaluates the same rule, and a test of the clean
// scenarios evaluates this function on a copy of the disk after each step. It
// reads and writes nothing else. A data directory with no keys/, or with keys/
// and no init marker, has no work: an unfinished init is refused by the start
// before any repair, and is the owner's to resume. A set of files that is
// damaged or inconsistent is an error, not an empty answer.
func RecoveryWork(r fsx.Reader) ([]Work, error) {
	exists, err := fsx.Exists(r, Dir)
	if err != nil {
		return nil, &FileError{Path: Dir, Err: err}
	}
	if !exists {
		return nil, nil
	}
	if done, err := Initialized(r); err != nil || !done {
		return nil, err
	}
	cert, err := readCertificate(r)
	if err != nil {
		return nil, err
	}
	seen, err := readSerial(r)
	if err != nil {
		return nil, err
	}
	if err := checkSerial(seen, cert.verified); err != nil {
		return nil, err
	}
	if RenewalInterrupted(seen, cert.verified) {
		return []Work{{Reason: WorkRenewalInterrupted, Path: CertificatePath}}, nil
	}
	return nil, nil
}
