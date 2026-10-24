package pairing

import (
	"errors"
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/credential"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
)

// ErrRecord reports a record that breaks the rules every record this package
// writes obeys.
var ErrRecord = errors.New("invalid pairing record")

// ConfirmMethod says how a side's owner confirmed a pairing.
type ConfirmMethod int

const (
	// ConfirmedByCode: an owner entered the code shown on the other screen.
	ConfirmedByCode ConfirmMethod = iota + 1
	// ConfirmedByExpectation: the node identifier had been entered ahead of
	// time, which stands for a confirmation made in advance.
	ConfirmedByExpectation
)

// Confirmation is the fact that a side confirmed: who, by which request, when,
// and how. It is the only thing that lets a side sign.
type Confirmation struct {
	Method ConfirmMethod
	// By is who confirmed; for an expectation, who entered it.
	By Author
	// Operation is the management request that confirmed, or for an
	// expectation the one that entered it.
	Operation admin.OperationID
	At        time.Time
}

func (c Confirmation) validate() error {
	switch {
	case c.Method != ConfirmedByCode && c.Method != ConfirmedByExpectation:
		return fmt.Errorf("%w: confirmation method", ErrRecord)
	case !c.By.valid():
		return fmt.Errorf("%w: confirmation author", ErrRecord)
	case c.Operation.IsZero():
		return fmt.Errorf("%w: confirmation operation", ErrRecord)
	case !wholeSecond(c.At):
		return fmt.Errorf("%w: confirmation time", ErrRecord)
	}
	return nil
}

// Completion is the outcome of a pairing that ended with a credential: when,
// and the complete credential record, which a late initiator can still fetch.
type Completion struct {
	At         time.Time
	Credential record.Record
}

func wholeSecond(t time.Time) bool { return !t.IsZero() && t.Equal(time.Unix(t.Unix(), 0)) }

// Display is what a human compares: the identifier, the code, the two nodes and
// the times. A command-line client that gets a record from a node through a
// management connection derives the display itself, from the decoded record,
// and never shows digits the node reports.
type Display struct {
	ID ID
	// Code is the code to compare with the one on the other node's screen.
	Code Code
	// Local and Remote are the two nodes; Initiating says whether Local started
	// the pairing.
	Local, Remote ident.NodeID
	Initiating    bool
	// CredentialNotAfter is when the credential would expire; zero for never.
	CredentialNotAfter time.Time
	// ExpiresAt is when this request stops waiting.
	ExpiresAt time.Time
}

func displayOf(t Transcript, initiating bool, expiresAt time.Time) Display {
	local, remote := t.terms.Target.node, t.terms.Initiator.node
	if initiating {
		local, remote = remote, local
	}
	return Display{ID: t.id, Code: t.code, Local: local, Remote: remote, Initiating: initiating,
		CredentialNotAfter: t.terms.NotAfter, ExpiresAt: expiresAt}
}

// checkComplete is the rule for a complete credential record of a transcript:
// the payload is the one computed locally, both identity keys signed and nobody
// else did.
func checkComplete(t Transcript, r record.Record) (record.Verified[credential.Credential], error) {
	local, err := t.credential.Payload()
	if err != nil {
		return record.Verified[credential.Credential]{}, err
	}
	if string(r.Payload) != string(local) {
		return record.Verified[credential.Credential]{}, fmt.Errorf("%w: credential payload is not the one this pairing computes", ErrRecord)
	}
	ver, err := record.Verify(credential.Descriptor, r)
	if err != nil {
		return record.Verified[credential.Credential]{}, fmt.Errorf("%w: %w", ErrRecord, err)
	}
	signers := ver.Signers()
	a, b := t.terms.Initiator.identity, t.terms.Target.identity
	if len(signers) != 2 || !(signers[0] == a && signers[1] == b || signers[0] == b && signers[1] == a) {
		return record.Verified[credential.Credential]{}, fmt.Errorf("%w: credential signers are not the two identity keys", ErrRecord)
	}
	return ver, nil
}

// checkPartial is the rule for the credential record the initiator sends
// first: the payload is the one computed locally and the one signature is the
// initiator's, valid.
func checkPartial(t Transcript, r record.Record) error {
	local, err := t.credential.Payload()
	if err != nil {
		return err
	}
	if string(r.Payload) != string(local) {
		return fmt.Errorf("%w: credential payload is not the one this pairing computes", ErrRecord)
	}
	p, err := record.VerifyPartial(credential.Descriptor, r)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrRecord, err)
	}
	signed := 0
	for _, slot := range p.Slots() {
		if !slot.Signed {
			continue
		}
		signed++
		if slot.Signer != t.terms.Initiator.identity {
			return fmt.Errorf("%w: signature is not the initiator's", ErrRecord)
		}
	}
	if signed != 1 {
		return fmt.Errorf("%w: want exactly the initiator's signature, have %d", ErrRecord, signed)
	}
	return nil
}

// TargetStatus is the lifecycle of a request on the target.
type TargetStatus int

const (
	// TargetPending: held for the owner, until it is confirmed and signed by
	// both, rejected, withdrawn or expired.
	TargetPending TargetStatus = iota + 1
	// TargetComplete: both signed; the record keeps the credential for a late
	// initiator.
	TargetComplete
	// TargetRejected: the target's owner ended it.
	TargetRejected
	// TargetExpired: it outlived PendingTTL.
	TargetExpired
	// TargetCancelled: the initiator withdrew.
	TargetCancelled
)

func (s TargetStatus) valid() bool { return s >= TargetPending && s <= TargetCancelled }

func (s TargetStatus) String() string {
	switch s {
	case TargetPending:
		return "pending"
	case TargetComplete:
		return "complete"
	case TargetRejected:
		return "rejected"
	case TargetExpired:
		return "expired"
	case TargetCancelled:
		return "cancelled"
	}
	return "invalid"
}

// TargetRecord is a request the target holds. It is persisted by the caller,
// one record per file named by ID(), replaced atomically with a checksum;
// deleting it is the same as a crash. The record keeps the transcript, so the
// code and the credential are recomputed whenever it is loaded and never
// stored.
type TargetRecord struct {
	// Transcript is everything both sides bound. Required.
	Transcript Transcript
	// ReceivedAt is when the target accepted the request; ExpiresAt when it
	// stops waiting (ReceivedAt plus PendingTTL).
	ReceivedAt, ExpiresAt time.Time
	// HighestSerial is the highest certificate serial of the initiator seen on
	// a connection that fetched this request; only that serial or a higher one
	// is accepted afterwards.
	HighestSerial uint64
	// Expected is set when the initiator was an expected node: the author of the
	// expectation. Such a request is confirmed from the start and used none of
	// the unexpected-node limits.
	Expected Author
	Status   TargetStatus
	// Confirmation is set once the target's owner confirmed (or the expectation
	// did).
	Confirmation *Confirmation
	// InitiatorSignature is the credential record carrying the initiator's
	// signature, once received and verified.
	InitiatorSignature *record.Record
	// Rejection says why a rejected request ended.
	Rejection WithdrawReason
	// Completed is set exactly when Status is TargetComplete.
	Completed *Completion
	// EndedAt is when the status became terminal, zero while pending.
	EndedAt time.Time
}

// ID returns the pairing identifier.
func (r TargetRecord) ID() ID { return r.Transcript.id }

// Display returns what the target's owner compares.
func (r TargetRecord) Display() Display { return displayOf(r.Transcript, false, r.ExpiresAt) }

// Terminal reports whether the request has ended.
func (r TargetRecord) Terminal() bool { return r.Status != TargetPending }

// ExpiredAt reports whether a pending request has run out at now.
func (r TargetRecord) ExpiredAt(now time.Time) bool {
	return r.Status == TargetPending && !now.Before(r.ExpiresAt)
}

// Waiting reports whether the request still waits for its owner at now.
func (r TargetRecord) Waiting(now time.Time) bool {
	return r.Status == TargetPending && !r.ExpiredAt(now)
}

// Validate checks the rules every target record obeys. A record this package
// returns satisfies them; a record read from storage is checked with it.
func (r TargetRecord) Validate() error {
	if r.Transcript.IsZero() {
		return fmt.Errorf("%w: no transcript", ErrRecord)
	}
	switch {
	case !wholeSecond(r.ReceivedAt) || !wholeSecond(r.ExpiresAt) || !r.ExpiresAt.After(r.ReceivedAt):
		return fmt.Errorf("%w: times", ErrRecord)
	case r.HighestSerial < r.Transcript.terms.Initiator.serial:
		return fmt.Errorf("%w: highest serial is below the initiator's own", ErrRecord)
	case !r.Status.valid():
		return fmt.Errorf("%w: status", ErrRecord)
	case !r.Expected.IsZero() && !r.Expected.valid():
		return fmt.Errorf("%w: expected author", ErrRecord)
	}
	if r.Confirmation != nil {
		if err := r.Confirmation.validate(); err != nil {
			return err
		}
	}
	if !r.Expected.IsZero() && (r.Confirmation == nil || r.Confirmation.Method != ConfirmedByExpectation || r.Confirmation.By != r.Expected) {
		return fmt.Errorf("%w: an expected request is confirmed by its expectation", ErrRecord)
	}
	if r.Confirmation != nil && r.Confirmation.Method == ConfirmedByExpectation && r.Expected != r.Confirmation.By {
		return fmt.Errorf("%w: confirmation by expectation without an expected author", ErrRecord)
	}
	if r.InitiatorSignature != nil {
		if err := checkPartial(r.Transcript, *r.InitiatorSignature); err != nil {
			return err
		}
	}
	if (r.Status == TargetRejected) != (r.Rejection != WithdrawNone) || (r.Rejection != WithdrawNone && !r.Rejection.valid()) {
		return fmt.Errorf("%w: rejection reason", ErrRecord)
	}
	if (r.Status == TargetPending) != r.EndedAt.IsZero() || (!r.EndedAt.IsZero() && !wholeSecond(r.EndedAt)) {
		return fmt.Errorf("%w: ended time", ErrRecord)
	}
	if (r.Status == TargetComplete) != (r.Completed != nil) {
		return fmt.Errorf("%w: completion", ErrRecord)
	}
	if r.Completed != nil {
		if r.Confirmation == nil || r.InitiatorSignature == nil || !r.Completed.At.Equal(r.EndedAt) {
			return fmt.Errorf("%w: a completed request was confirmed, signed and ended when it completed", ErrRecord)
		}
		if _, err := checkComplete(r.Transcript, r.Completed.Credential); err != nil {
			return err
		}
	}
	return nil
}

// InitiatorStatus is the lifecycle of a request on the initiator.
type InitiatorStatus int

const (
	// InitiatorActive: waiting for the owner to confirm, or for the target's
	// answer after the signature was handed over.
	InitiatorActive InitiatorStatus = iota + 1
	// InitiatorWithdrawing: the owner ended it; the target is still to be told.
	InitiatorWithdrawing
	// InitiatorComplete: the complete credential was received and stored.
	InitiatorComplete
	// InitiatorRejected: the target's owner ended it.
	InitiatorRejected
	// InitiatorExpired: the target reported it expired, or it expired before the
	// owner confirmed.
	InitiatorExpired
	// InitiatorCancelled: the target reported it cancelled, though this node did
	// not withdraw it.
	InitiatorCancelled
	// InitiatorUnknown: the target no longer knows the request.
	InitiatorUnknown
	// InitiatorGaveUp: no answer came before the target stopped keeping results.
	InitiatorGaveUp
	// InitiatorWithdrawn: the withdrawal was delivered, or can no longer matter.
	InitiatorWithdrawn
)

func (s InitiatorStatus) valid() bool { return s >= InitiatorActive && s <= InitiatorWithdrawn }

// Terminal reports whether the status is final. A withdrawing record is not:
// the target still has to be told.
func (s InitiatorStatus) Terminal() bool {
	return s.valid() && s != InitiatorActive && s != InitiatorWithdrawing
}

func (s InitiatorStatus) String() string {
	switch s {
	case InitiatorActive:
		return "active"
	case InitiatorWithdrawing:
		return "withdrawing"
	case InitiatorComplete:
		return "complete"
	case InitiatorRejected:
		return "rejected"
	case InitiatorExpired:
		return "expired"
	case InitiatorCancelled:
		return "cancelled"
	case InitiatorUnknown:
		return "unknown"
	case InitiatorGaveUp:
		return "gave_up"
	case InitiatorWithdrawn:
		return "withdrawn"
	}
	return "invalid"
}

// Limits on the dial hints an initiator record carries for the fetch segment.
const (
	MaxAddresses  = 16
	MaxAddressLen = 256
)

// InitiatorRecord is a request the initiator started. It is written when the
// target's acceptance arrives and before the code is shown, and replaced
// atomically at each change the fields below describe.
type InitiatorRecord struct {
	Transcript Transcript
	// ReceivedAt is when the acceptance arrived. ExpiresAt is when the owner
	// can no longer confirm: the target's time clamped to PendingTTL plus the
	// clock tolerance from ReceivedAt, so a target cannot hold this node's
	// request open for longer than it holds its own.
	ReceivedAt, ExpiresAt time.Time
	// Addresses are opaque dial hints for the target that the network layer
	// supplied when it started the pairing, kept for the fetch segment.
	Addresses []string
	// Expected is set when the target was an expected node.
	Expected Author
	Status   InitiatorStatus
	// Confirmation is set once the owner confirmed (or the expectation did).
	Confirmation *Confirmation
	// Signature is the credential record with this node's signature, made at
	// confirmation and persisted before it is first sent.
	Signature *record.Record
	// Released is set, and persisted, before the signature is first handed to
	// the network layer. From then on a copy may exist anywhere, and any end
	// without the complete credential makes this node revoke the credential.
	Released bool
	// Reason says why the pairing was withdrawn here or rejected there.
	Reason WithdrawReason
	// Completed is set exactly when Status is InitiatorComplete.
	Completed *Completion
	// EndedAt is when the status became terminal, or for a withdrawing record
	// when the owner withdrew; zero while active.
	EndedAt time.Time
}

// ID returns the pairing identifier.
func (r InitiatorRecord) ID() ID { return r.Transcript.id }

// Display returns what the initiating owner compares.
func (r InitiatorRecord) Display() Display { return displayOf(r.Transcript, true, r.ExpiresAt) }

// Validate checks the rules every initiator record obeys.
func (r InitiatorRecord) Validate() error {
	if r.Transcript.IsZero() {
		return fmt.Errorf("%w: no transcript", ErrRecord)
	}
	switch {
	case !wholeSecond(r.ReceivedAt) || !wholeSecond(r.ExpiresAt) || !r.ExpiresAt.After(r.ReceivedAt):
		return fmt.Errorf("%w: times", ErrRecord)
	case !r.Status.valid():
		return fmt.Errorf("%w: status", ErrRecord)
	case len(r.Addresses) > MaxAddresses:
		return fmt.Errorf("%w: too many addresses", ErrRecord)
	case !r.Expected.IsZero() && !r.Expected.valid():
		return fmt.Errorf("%w: expected author", ErrRecord)
	}
	for _, a := range r.Addresses {
		if a == "" || len(a) > MaxAddressLen {
			return fmt.Errorf("%w: address length", ErrRecord)
		}
	}
	if r.Confirmation != nil {
		if err := r.Confirmation.validate(); err != nil {
			return err
		}
	}
	if !r.Expected.IsZero() && (r.Confirmation == nil || r.Confirmation.Method != ConfirmedByExpectation || r.Confirmation.By != r.Expected) {
		return fmt.Errorf("%w: an expected request is confirmed by its expectation", ErrRecord)
	}
	if r.Confirmation != nil && r.Confirmation.Method == ConfirmedByExpectation && r.Expected != r.Confirmation.By {
		return fmt.Errorf("%w: confirmation by expectation without an expected author", ErrRecord)
	}
	if r.Signature != nil {
		if r.Confirmation == nil {
			return fmt.Errorf("%w: a signature without a confirmation", ErrRecord)
		}
		if err := checkPartial(r.Transcript, *r.Signature); err != nil {
			return err
		}
	}
	if r.Released && r.Signature == nil {
		return fmt.Errorf("%w: released without a signature", ErrRecord)
	}
	withReason := r.Status == InitiatorRejected || r.Status == InitiatorWithdrawing || r.Status == InitiatorWithdrawn
	if withReason != (r.Reason != WithdrawNone) || (r.Reason != WithdrawNone && !r.Reason.valid()) {
		return fmt.Errorf("%w: reason", ErrRecord)
	}
	if (r.Status == InitiatorActive) != r.EndedAt.IsZero() || (!r.EndedAt.IsZero() && !wholeSecond(r.EndedAt)) {
		return fmt.Errorf("%w: ended time", ErrRecord)
	}
	if (r.Status == InitiatorComplete) != (r.Completed != nil) {
		return fmt.Errorf("%w: completion", ErrRecord)
	}
	if r.Completed != nil {
		if r.Signature == nil || !r.Released || !r.Completed.At.Equal(r.EndedAt) {
			return fmt.Errorf("%w: a completed request was signed, released and ended when it completed", ErrRecord)
		}
		if _, err := checkComplete(r.Transcript, r.Completed.Credential); err != nil {
			return err
		}
	}
	return nil
}

// needsRevocation reports whether a signature of this node may exist outside it
// and the pairing ended without the complete credential, so the credential must
// be revoked.
func (r InitiatorRecord) needsRevocation() bool {
	return r.Released && r.Status != InitiatorActive && r.Status != InitiatorComplete
}
