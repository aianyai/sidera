package pairing

import (
	"errors"
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/record"
)

// MaxMessage bounds one protocol message. Messages are small (the largest
// carries one credential record); the bound keeps a hostile peer from making
// the decoder work on a large frame.
const MaxMessage = 8 << 10

// MaxRetryAfter bounds the retry hint of a refusal or a result: a peer cannot
// park the other side for longer than this.
const MaxRetryAfter = 24 * time.Hour

// Message type strings. Every protocol message is a deterministic CBOR map
// whose key 0 is its type string and whose other keys are the fields the
// message defines. A message of one type never decodes as another: the type is
// checked, and so are unknown fields. Only credentials are signed records; the
// sender of a protocol message is authenticated by the connection.
const (
	TypeCommit   = "sidera/pairing-commit/1"
	TypeReveal   = "sidera/pairing-reveal/1"
	TypeOpen     = "sidera/pairing-open/1"
	TypeAccepted = "sidera/pairing-accepted/1"
	TypeRefused  = "sidera/pairing-refused/1"
	TypeFetch    = "sidera/pairing-fetch/1"
	TypeResult   = "sidera/pairing-result/1"
)

// ErrMessage reports a frame that is not a well-formed message of the expected
// type. The error wraps the decoding error of the record package where there is
// one.
var ErrMessage = errors.New("invalid pairing message")

// RefusalReason says why a target refused a request, without telling a
// stranger more than it must: a node that is retired, locally revoked, already
// trusted or the sender itself all answer NotAccepted.
type RefusalReason uint64

const (
	// RefusalPaused: verification failures or the reveal budget stop new
	// requests from a node this one does not expect. RetryAfter says when the
	// earliest slot frees.
	RefusalPaused RefusalReason = iota + 1
	// RefusalBusy: the pending requests of unexpected nodes are at their cap, or
	// this initiator already has a request that holds its signature.
	RefusalBusy
	// RefusalNotAccepted: the request cannot be accepted, for a reason this node
	// does not disclose.
	RefusalNotAccepted
	// RefusalClockSkew: the initiator's clock differs from the target's by more
	// than the clock tolerance.
	RefusalClockSkew
	// RefusalProtocolError: the request was malformed or out of order.
	RefusalProtocolError
)

func (r RefusalReason) valid() bool { return r >= RefusalPaused && r <= RefusalProtocolError }

func (r RefusalReason) String() string {
	switch r {
	case RefusalPaused:
		return "paused"
	case RefusalBusy:
		return "busy"
	case RefusalNotAccepted:
		return "not_accepted"
	case RefusalClockSkew:
		return "clock_skew"
	case RefusalProtocolError:
		return "protocol_error"
	}
	return "invalid"
}

// WithdrawReason says why a pairing was ended by a human: the owner declined
// it, or reported that the two codes differ. Only the second counts as a
// verification failure, and only on the side whose owner reported it.
type WithdrawReason uint64

const (
	// WithdrawNone is the unset value.
	WithdrawNone WithdrawReason = iota
	// WithdrawDeclined: the owner does not want this pairing.
	WithdrawDeclined
	// WithdrawMismatch: the owner compared the codes and they differ.
	WithdrawMismatch
)

func (w WithdrawReason) valid() bool { return w == WithdrawDeclined || w == WithdrawMismatch }

func (w WithdrawReason) String() string {
	switch w {
	case WithdrawNone:
		return "none"
	case WithdrawDeclined:
		return "declined"
	case WithdrawMismatch:
		return "mismatch"
	}
	return "invalid"
}

// ResultStatus is the state of a pairing as the target reports it to the
// initiator.
type ResultStatus uint64

const (
	// StatusAwaitingInitiatorSignature: the target holds the request and has
	// not received the initiator's signature.
	StatusAwaitingInitiatorSignature ResultStatus = iota + 1
	// StatusAwaitingTarget: the initiator's signature is in; the target's owner
	// has not confirmed.
	StatusAwaitingTarget
	// StatusComplete: both signed; the result carries the credential.
	StatusComplete
	// StatusRejected: the target's owner ended it, with a WithdrawReason.
	StatusRejected
	// StatusExpired: the request outlived its time to live.
	StatusExpired
	// StatusUnknown: no such request for this requester. It is the answer for
	// a request that never existed, for one already forgotten and for a
	// requester who is not the initiator, so none of the three can be told
	// apart.
	StatusUnknown
	// StatusCancelled: the initiator withdrew.
	StatusCancelled
)

func (s ResultStatus) valid() bool {
	return s >= StatusAwaitingInitiatorSignature && s <= StatusCancelled
}

func (s ResultStatus) awaiting() bool {
	return s == StatusAwaitingInitiatorSignature || s == StatusAwaitingTarget
}

func (s ResultStatus) String() string {
	switch s {
	case StatusAwaitingInitiatorSignature:
		return "awaiting_initiator_signature"
	case StatusAwaitingTarget:
		return "awaiting_target"
	case StatusComplete:
		return "complete"
	case StatusRejected:
		return "rejected"
	case StatusExpired:
		return "expired"
	case StatusUnknown:
		return "unknown"
	case StatusCancelled:
		return "cancelled"
	}
	return "invalid"
}

// CommitMsg (M1, initiator to target) opens the online segment: the
// commitment, and the times the initiator proposes.
type CommitMsg struct {
	Commitment Commitment
	CreatedAt  time.Time
	// NotAfter is zero when the credential does not expire.
	NotAfter time.Time
}

// RevealMsg (M2, target to initiator) reveals the target's nonce, after the
// initiator has committed.
type RevealMsg struct{ NB Nonce }

// OpenMsg (M3, initiator to target) opens the initiator's nonce, after the
// target has revealed.
type OpenMsg struct{ NA Nonce }

// AcceptedMsg (M4, target to initiator) says the target holds the request: its
// identifier, which the initiator checks against its own, and when it expires
// by the target's clock.
type AcceptedMsg struct {
	ID        ID
	ExpiresAt time.Time
}

// RefusalMsg (target to initiator, instead of M2 or M4) refuses the request.
type RefusalMsg struct {
	Reason RefusalReason
	// RetryAfter is a hint, zero when there is none. An explicit zero on the
	// wire is refused.
	RetryAfter time.Duration
}

// FetchMsg (F1, initiator to target) asks for the result. It may carry the
// initiator's partially signed credential, or the withdrawal of the request,
// but not both.
type FetchMsg struct {
	ID ID
	// Signed is the credential record with the initiator's signature only.
	Signed *record.Record
	// Withdraw is set when the initiator ends the request.
	Withdraw WithdrawReason
}

// ResultMsg (F2, target to initiator) answers a fetch.
type ResultMsg struct {
	ID     ID
	Status ResultStatus
	// Credential is the complete credential record; present exactly when the
	// status is StatusComplete.
	Credential *record.Record
	// RetryAfter is a hint for the awaiting statuses, zero when there is none.
	RetryAfter time.Duration
	// Rejection says why a rejected request was ended; present exactly when the
	// status is StatusRejected.
	Rejection WithdrawReason
}

// Wire structs. Key 0 is the message type; keys 1 and up are the fields.

type commitWire struct {
	Type      string `cbor:"0,keyasint"`
	Commit    []byte `cbor:"1,keyasint"`
	CreatedAt int64  `cbor:"2,keyasint"`
	NotAfter  *int64 `cbor:"3,keyasint,omitempty"`
}

type revealWire struct {
	Type string `cbor:"0,keyasint"`
	NB   []byte `cbor:"1,keyasint"`
}

type openWire struct {
	Type string `cbor:"0,keyasint"`
	NA   []byte `cbor:"1,keyasint"`
}

type acceptedWire struct {
	Type      string `cbor:"0,keyasint"`
	ID        []byte `cbor:"1,keyasint"`
	ExpiresAt int64  `cbor:"2,keyasint"`
}

type refusalWire struct {
	Type       string `cbor:"0,keyasint"`
	Reason     uint64 `cbor:"1,keyasint"`
	RetryAfter *int64 `cbor:"2,keyasint,omitempty"`
}

type fetchWire struct {
	Type     string `cbor:"0,keyasint"`
	ID       []byte `cbor:"1,keyasint"`
	Signed   []byte `cbor:"2,keyasint,omitempty"`
	Withdraw uint64 `cbor:"3,keyasint,omitempty"`
}

type resultWire struct {
	Type       string `cbor:"0,keyasint"`
	ID         []byte `cbor:"1,keyasint"`
	Status     uint64 `cbor:"2,keyasint"`
	Credential []byte `cbor:"3,keyasint,omitempty"`
	RetryAfter *int64 `cbor:"4,keyasint,omitempty"`
	Rejection  uint64 `cbor:"5,keyasint,omitempty"`
}

// decodeWire reads b strictly into w and checks the message type.
func decodeWire[W any](b []byte, w *W, typ string, gotType func(*W) string) error {
	if err := record.UnmarshalStrict(b, w, MaxMessage); err != nil {
		return fmt.Errorf("%w: %w", ErrMessage, err)
	}
	if got := gotType(w); got != typ {
		return fmt.Errorf("%w: type %q, want %q", ErrMessage, got, typ)
	}
	return nil
}

func fixed32(b []byte, what string) ([32]byte, error) {
	var out [32]byte
	if len(b) != len(out) {
		return out, fmt.Errorf("%w: %s is %d bytes, want %d", ErrMessage, what, len(b), len(out))
	}
	copy(out[:], b)
	return out, nil
}

func nonceFrom(b []byte, what string) (Nonce, error) {
	n, err := fixed32(b, what)
	if err != nil {
		return Nonce{}, err
	}
	if Nonce(n).IsZero() {
		return Nonce{}, fmt.Errorf("%w: %s is all zero", ErrMessage, what)
	}
	return Nonce(n), nil
}

func idFrom(b []byte) (ID, error) {
	id, err := fixed32(b, "pairing id")
	if err != nil {
		return ID{}, err
	}
	if ID(id).IsZero() {
		return ID{}, fmt.Errorf("%w: pairing id is all zero", ErrMessage)
	}
	return ID(id), nil
}

func requiredTime(sec int64, what string) (time.Time, error) {
	t, err := record.TimeFromWire(sec)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %s: %w", ErrMessage, what, err)
	}
	return t, nil
}

func retryFromWire(p *int64) (time.Duration, error) {
	if p == nil {
		return 0, nil
	}
	if *p < 1 || *p > int64(MaxRetryAfter/time.Second) {
		return 0, fmt.Errorf("%w: retry_after %d is outside 1..%d seconds", ErrMessage, *p, int64(MaxRetryAfter/time.Second))
	}
	return time.Duration(*p) * time.Second, nil
}

func retryToWire(d time.Duration) (*int64, error) {
	if d == 0 {
		return nil, nil
	}
	if d%time.Second != 0 || d < 0 {
		return nil, fmt.Errorf("%w: retry_after must be whole seconds", ErrMessage)
	}
	s := int64(d / time.Second)
	return &s, nil
}

func recordFrom(b []byte, what string) (*record.Record, error) {
	r, err := record.Decode(b, MaxMessage)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrMessage, what, err)
	}
	return &r, nil
}

func recordTo(r *record.Record) ([]byte, error) {
	if r == nil {
		return nil, nil
	}
	b, err := r.Encode()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMessage, err)
	}
	return b, nil
}

// encoded marshals w and checks that the bytes decode back, so a sender can
// never produce a frame that a receiver of this package refuses.
func encoded[W any, M any](w W, decode func([]byte) (M, error)) ([]byte, error) {
	b, err := record.MarshalCanonical(w)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMessage, err)
	}
	if _, err := decode(b); err != nil {
		return nil, err
	}
	return b, nil
}

// DecodeCommit reads M1.
func DecodeCommit(b []byte) (CommitMsg, error) {
	var w commitWire
	if err := decodeWire(b, &w, TypeCommit, func(w *commitWire) string { return w.Type }); err != nil {
		return CommitMsg{}, err
	}
	c, err := fixed32(w.Commit, "commitment")
	if err != nil {
		return CommitMsg{}, err
	}
	if Commitment(c).IsZero() {
		return CommitMsg{}, fmt.Errorf("%w: commitment is all zero", ErrMessage)
	}
	created, err := requiredTime(w.CreatedAt, "created_at")
	if err != nil {
		return CommitMsg{}, err
	}
	var notAfter time.Time
	if w.NotAfter != nil {
		if notAfter, err = requiredTime(*w.NotAfter, "not_after"); err != nil {
			return CommitMsg{}, err
		}
		if !notAfter.After(created) {
			return CommitMsg{}, fmt.Errorf("%w: not_after must be after created_at", ErrMessage)
		}
	}
	return CommitMsg{Commitment: Commitment(c), CreatedAt: created, NotAfter: notAfter}, nil
}

// Encode returns the canonical bytes of M1.
func (m CommitMsg) Encode() ([]byte, error) {
	created, err := record.TimeToWire(m.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("%w: created_at: %w", ErrMessage, err)
	}
	notAfter, err := record.OptionalTimeToWire(m.NotAfter)
	if err != nil {
		return nil, fmt.Errorf("%w: not_after: %w", ErrMessage, err)
	}
	return encoded(commitWire{Type: TypeCommit, Commit: m.Commitment[:], CreatedAt: created, NotAfter: notAfter}, DecodeCommit)
}

// DecodeReveal reads M2.
func DecodeReveal(b []byte) (RevealMsg, error) {
	var w revealWire
	if err := decodeWire(b, &w, TypeReveal, func(w *revealWire) string { return w.Type }); err != nil {
		return RevealMsg{}, err
	}
	n, err := nonceFrom(w.NB, "nB")
	return RevealMsg{NB: n}, err
}

// Encode returns the canonical bytes of M2.
func (m RevealMsg) Encode() ([]byte, error) {
	return encoded(revealWire{Type: TypeReveal, NB: m.NB[:]}, DecodeReveal)
}

// DecodeOpen reads M3.
func DecodeOpen(b []byte) (OpenMsg, error) {
	var w openWire
	if err := decodeWire(b, &w, TypeOpen, func(w *openWire) string { return w.Type }); err != nil {
		return OpenMsg{}, err
	}
	n, err := nonceFrom(w.NA, "nA")
	return OpenMsg{NA: n}, err
}

// Encode returns the canonical bytes of M3.
func (m OpenMsg) Encode() ([]byte, error) {
	return encoded(openWire{Type: TypeOpen, NA: m.NA[:]}, DecodeOpen)
}

// DecodeAccepted reads M4.
func DecodeAccepted(b []byte) (AcceptedMsg, error) {
	var w acceptedWire
	if err := decodeWire(b, &w, TypeAccepted, func(w *acceptedWire) string { return w.Type }); err != nil {
		return AcceptedMsg{}, err
	}
	id, err := idFrom(w.ID)
	if err != nil {
		return AcceptedMsg{}, err
	}
	expires, err := requiredTime(w.ExpiresAt, "expires_at")
	if err != nil {
		return AcceptedMsg{}, err
	}
	return AcceptedMsg{ID: id, ExpiresAt: expires}, nil
}

// Encode returns the canonical bytes of M4.
func (m AcceptedMsg) Encode() ([]byte, error) {
	expires, err := record.TimeToWire(m.ExpiresAt)
	if err != nil {
		return nil, fmt.Errorf("%w: expires_at: %w", ErrMessage, err)
	}
	return encoded(acceptedWire{Type: TypeAccepted, ID: m.ID[:], ExpiresAt: expires}, DecodeAccepted)
}

// DecodeRefusal reads a refusal.
func DecodeRefusal(b []byte) (RefusalMsg, error) {
	var w refusalWire
	if err := decodeWire(b, &w, TypeRefused, func(w *refusalWire) string { return w.Type }); err != nil {
		return RefusalMsg{}, err
	}
	reason := RefusalReason(w.Reason)
	if !reason.valid() {
		return RefusalMsg{}, fmt.Errorf("%w: refusal reason %d", ErrMessage, w.Reason)
	}
	retry, err := retryFromWire(w.RetryAfter)
	if err != nil {
		return RefusalMsg{}, err
	}
	return RefusalMsg{Reason: reason, RetryAfter: retry}, nil
}

// Encode returns the canonical bytes of a refusal.
func (m RefusalMsg) Encode() ([]byte, error) {
	retry, err := retryToWire(m.RetryAfter)
	if err != nil {
		return nil, err
	}
	return encoded(refusalWire{Type: TypeRefused, Reason: uint64(m.Reason), RetryAfter: retry}, DecodeRefusal)
}

// DecodeFetch reads F1.
func DecodeFetch(b []byte) (FetchMsg, error) {
	var w fetchWire
	if err := decodeWire(b, &w, TypeFetch, func(w *fetchWire) string { return w.Type }); err != nil {
		return FetchMsg{}, err
	}
	id, err := idFrom(w.ID)
	if err != nil {
		return FetchMsg{}, err
	}
	m := FetchMsg{ID: id}
	if w.Signed != nil {
		if m.Signed, err = recordFrom(w.Signed, "signed credential"); err != nil {
			return FetchMsg{}, err
		}
	}
	if w.Withdraw != 0 {
		m.Withdraw = WithdrawReason(w.Withdraw)
		if !m.Withdraw.valid() {
			return FetchMsg{}, fmt.Errorf("%w: withdraw reason %d", ErrMessage, w.Withdraw)
		}
	}
	if m.Signed != nil && m.Withdraw != WithdrawNone {
		return FetchMsg{}, fmt.Errorf("%w: a fetch cannot both sign and withdraw", ErrMessage)
	}
	return m, nil
}

// Encode returns the canonical bytes of F1.
func (m FetchMsg) Encode() ([]byte, error) {
	signed, err := recordTo(m.Signed)
	if err != nil {
		return nil, err
	}
	return encoded(fetchWire{Type: TypeFetch, ID: m.ID[:], Signed: signed, Withdraw: uint64(m.Withdraw)}, DecodeFetch)
}

// DecodeResult reads F2.
func DecodeResult(b []byte) (ResultMsg, error) {
	var w resultWire
	if err := decodeWire(b, &w, TypeResult, func(w *resultWire) string { return w.Type }); err != nil {
		return ResultMsg{}, err
	}
	id, err := idFrom(w.ID)
	if err != nil {
		return ResultMsg{}, err
	}
	m := ResultMsg{ID: id, Status: ResultStatus(w.Status), Rejection: WithdrawReason(w.Rejection)}
	if !m.Status.valid() {
		return ResultMsg{}, fmt.Errorf("%w: result status %d", ErrMessage, w.Status)
	}
	hasCredential := w.Credential != nil
	if hasCredential != (m.Status == StatusComplete) {
		return ResultMsg{}, fmt.Errorf("%w: a credential is carried exactly by a complete result", ErrMessage)
	}
	if w.Credential != nil {
		if m.Credential, err = recordFrom(w.Credential, "credential"); err != nil {
			return ResultMsg{}, err
		}
	}
	hasRejection := w.Rejection != 0
	if hasRejection != (m.Status == StatusRejected) || (hasRejection && !m.Rejection.valid()) {
		return ResultMsg{}, fmt.Errorf("%w: a rejection reason is carried exactly by a rejected result", ErrMessage)
	}
	if m.RetryAfter, err = retryFromWire(w.RetryAfter); err != nil {
		return ResultMsg{}, err
	}
	if m.RetryAfter != 0 && !m.Status.awaiting() {
		return ResultMsg{}, fmt.Errorf("%w: retry_after only goes with an awaiting status", ErrMessage)
	}
	return m, nil
}

// Encode returns the canonical bytes of F2.
func (m ResultMsg) Encode() ([]byte, error) {
	cred, err := recordTo(m.Credential)
	if err != nil {
		return nil, err
	}
	retry, err := retryToWire(m.RetryAfter)
	if err != nil {
		return nil, err
	}
	return encoded(resultWire{Type: TypeResult, ID: m.ID[:], Status: uint64(m.Status), Credential: cred, RetryAfter: retry, Rejection: uint64(m.Rejection)}, DecodeResult)
}

// Opening is the first frame of a stream on the target: either the commitment
// that starts the online segment or the fetch that starts a result exchange.
// Exactly one field is set.
type Opening struct {
	Commit *CommitMsg
	Fetch  *FetchMsg
}

// DecodeOpening reads the first frame of a stream. Its type decides which
// segment the stream carries.
func DecodeOpening(b []byte) (Opening, error) {
	if c, err := DecodeCommit(b); err == nil {
		return Opening{Commit: &c}, nil
	}
	if f, err := DecodeFetch(b); err == nil {
		return Opening{Fetch: &f}, nil
	}
	return Opening{}, fmt.Errorf("%w: neither a commitment nor a fetch", ErrMessage)
}
