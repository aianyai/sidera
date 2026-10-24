package pairing

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
)

// MaxPersisted bounds one persisted record when it is read back.
const MaxPersisted = 64 << 10

// Type strings of the persisted records. Like the messages, a persisted record
// is a deterministic CBOR map whose key 0 is its type; it is read back
// strictly, and everything derived from the transcript (the identifier, the
// code, the credential) is recomputed on loading and never stored.
const (
	TypeTargetRecord    = "sidera/pairing-target-record/1"
	TypeInitiatorRecord = "sidera/pairing-initiator-record/1"
	TypeLimits          = "sidera/pairing-limits/1"
	TypeExpectations    = "sidera/pairing-expectations/1"
)

// ErrPersisted reports persisted bytes that are not a record of the expected
// type that obeys the rules of its kind.
var ErrPersisted = errors.New("invalid persisted pairing state")

type partyWire struct {
	Identity record.KeyWire `cbor:"1,keyasint"`
	Network  record.KeyWire `cbor:"2,keyasint"`
	Serial   uint64         `cbor:"3,keyasint"`
}

type termsWire struct {
	Protocol  string    `cbor:"1,keyasint"`
	Initiator partyWire `cbor:"2,keyasint"`
	Target    partyWire `cbor:"3,keyasint"`
	CreatedAt int64     `cbor:"4,keyasint"`
	NotAfter  *int64    `cbor:"5,keyasint,omitempty"`
}

type transcriptWire struct {
	Terms      termsWire `cbor:"1,keyasint"`
	Commitment []byte    `cbor:"2,keyasint"`
	NB         []byte    `cbor:"3,keyasint"`
	NA         []byte    `cbor:"4,keyasint"`
}

type authorWire struct {
	Kind   uint64 `cbor:"1,keyasint"`
	Person []byte `cbor:"2,keyasint,omitempty"`
}

type confirmationWire struct {
	Method    uint64     `cbor:"1,keyasint"`
	By        authorWire `cbor:"2,keyasint"`
	Operation []byte     `cbor:"3,keyasint"`
	At        int64      `cbor:"4,keyasint"`
}

type completionWire struct {
	At         int64  `cbor:"1,keyasint"`
	Credential []byte `cbor:"2,keyasint"`
}

type targetWire struct {
	Type          string            `cbor:"0,keyasint"`
	Transcript    transcriptWire    `cbor:"1,keyasint"`
	ReceivedAt    int64             `cbor:"2,keyasint"`
	ExpiresAt     int64             `cbor:"3,keyasint"`
	HighestSerial uint64            `cbor:"4,keyasint"`
	Expected      *authorWire       `cbor:"5,keyasint,omitempty"`
	Status        uint64            `cbor:"6,keyasint"`
	Confirmation  *confirmationWire `cbor:"7,keyasint,omitempty"`
	Signature     []byte            `cbor:"8,keyasint,omitempty"`
	Rejection     uint64            `cbor:"9,keyasint,omitempty"`
	Completed     *completionWire   `cbor:"10,keyasint,omitempty"`
	EndedAt       *int64            `cbor:"11,keyasint,omitempty"`
}

type initiatorWire struct {
	Type         string            `cbor:"0,keyasint"`
	Transcript   transcriptWire    `cbor:"1,keyasint"`
	ReceivedAt   int64             `cbor:"2,keyasint"`
	ExpiresAt    int64             `cbor:"3,keyasint"`
	Addresses    []string          `cbor:"4,keyasint,omitempty"`
	Expected     *authorWire       `cbor:"5,keyasint,omitempty"`
	Status       uint64            `cbor:"6,keyasint"`
	Confirmation *confirmationWire `cbor:"7,keyasint,omitempty"`
	Signature    []byte            `cbor:"8,keyasint,omitempty"`
	Released     bool              `cbor:"9,keyasint,omitempty"`
	Reason       uint64            `cbor:"10,keyasint,omitempty"`
	Completed    *completionWire   `cbor:"11,keyasint,omitempty"`
	EndedAt      *int64            `cbor:"12,keyasint,omitempty"`
}

type limitsWire struct {
	Type     string  `cbor:"0,keyasint"`
	Reveals  []int64 `cbor:"1,keyasint,omitempty"`
	Failures []int64 `cbor:"2,keyasint,omitempty"`
}

type expectationWire struct {
	Node      []byte     `cbor:"1,keyasint"`
	Author    authorWire `cbor:"2,keyasint"`
	Operation []byte     `cbor:"3,keyasint"`
	CreatedAt int64      `cbor:"4,keyasint"`
}

type expectationsWire struct {
	Type string            `cbor:"0,keyasint"`
	List []expectationWire `cbor:"1,keyasint,omitempty"`
}

func persisted(err error) error { return fmt.Errorf("%w: %w", ErrPersisted, err) }

func decodePersisted[W any](data []byte, w *W, want string, typeOf func(*W) string) error {
	if err := record.UnmarshalStrict(data, w, MaxPersisted); err != nil {
		return persisted(err)
	}
	if got := typeOf(w); got != want {
		return persisted(fmt.Errorf("type %q, want %q", got, want))
	}
	return nil
}

func secs(t time.Time) int64 { return t.Unix() }

func fromSecs(sec int64) (time.Time, error) {
	t, err := record.TimeFromWire(sec)
	if err != nil {
		return time.Time{}, persisted(err)
	}
	return t, nil
}

func optSecs(t time.Time) *int64 {
	if t.IsZero() {
		return nil
	}
	v := t.Unix()
	return &v
}

func fromOptSecs(p *int64) (time.Time, error) {
	if p == nil {
		return time.Time{}, nil
	}
	return fromSecs(*p)
}

func partyToWire(p Party) partyWire {
	return partyWire{Identity: record.KeyToWire(p.identity), Network: record.KeyToWire(p.network), Serial: p.serial}
}

func partyFromWire(w partyWire) (Party, error) {
	id, err := record.KeyFromWire(w.Identity)
	if err != nil {
		return Party{}, persisted(err)
	}
	nw, err := record.KeyFromWire(w.Network)
	if err != nil {
		return Party{}, persisted(err)
	}
	p, err := partyOf(id, nw, w.Serial)
	if err != nil {
		return Party{}, persisted(err)
	}
	return p, nil
}

func transcriptToWire(t Transcript) transcriptWire {
	nB, nA := t.Nonces()
	return transcriptWire{
		Terms: termsWire{
			Protocol: t.terms.Protocol, Initiator: partyToWire(t.terms.Initiator), Target: partyToWire(t.terms.Target),
			CreatedAt: secs(t.terms.CreatedAt), NotAfter: optSecs(t.terms.NotAfter),
		},
		Commitment: t.commitment[:], NB: nB[:], NA: nA[:],
	}
}

// transcriptFromWire rebuilds the transcript through Open, so the commitment is
// checked again and the identifier, code and credential are recomputed.
func transcriptFromWire(w transcriptWire) (Transcript, error) {
	initiator, err := partyFromWire(w.Terms.Initiator)
	if err != nil {
		return Transcript{}, err
	}
	target, err := partyFromWire(w.Terms.Target)
	if err != nil {
		return Transcript{}, err
	}
	created, err := fromSecs(w.Terms.CreatedAt)
	if err != nil {
		return Transcript{}, err
	}
	notAfter, err := fromOptSecs(w.Terms.NotAfter)
	if err != nil {
		return Transcript{}, err
	}
	var c Commitment
	if len(w.Commitment) != len(c) || len(w.NB) != NonceLen || len(w.NA) != NonceLen {
		return Transcript{}, persisted(errors.New("commitment or nonce has the wrong length"))
	}
	copy(c[:], w.Commitment)
	var nB, nA Nonce
	copy(nB[:], w.NB)
	copy(nA[:], w.NA)
	terms := Terms{Protocol: w.Terms.Protocol, Initiator: initiator, Target: target, CreatedAt: created, NotAfter: notAfter}
	t, err := Open(terms, c, nB, nA)
	if err != nil {
		return Transcript{}, persisted(err)
	}
	return t, nil
}

func authorToWire(a Author) authorWire {
	w := authorWire{Kind: uint64(a.kind)}
	if !a.person.IsZero() {
		w.Person = a.person.Bytes()
	}
	return w
}

func authorFromWire(w authorWire) (Author, error) {
	var a Author
	switch admin.ActorKind(w.Kind) {
	case admin.ActorLocalOwner:
		if len(w.Person) != 0 {
			return Author{}, persisted(errors.New("local owner with a person"))
		}
		a = LocalAuthor()
	case admin.ActorRemotePerson:
		p, err := ident.PersonIDFromBytes(w.Person)
		if err != nil {
			return Author{}, persisted(err)
		}
		a, err = PersonAuthor(p)
		if err != nil {
			return Author{}, persisted(err)
		}
	default:
		return Author{}, persisted(errors.New("author kind"))
	}
	return a, nil
}

func optAuthorToWire(a Author) *authorWire {
	if a.IsZero() {
		return nil
	}
	w := authorToWire(a)
	return &w
}

func optAuthorFromWire(w *authorWire) (Author, error) {
	if w == nil {
		return Author{}, nil
	}
	return authorFromWire(*w)
}

func confirmationToWire(c *Confirmation) *confirmationWire {
	if c == nil {
		return nil
	}
	return &confirmationWire{Method: uint64(c.Method), By: authorToWire(c.By), Operation: c.Operation[:], At: secs(c.At)}
}

func confirmationFromWire(w *confirmationWire) (*Confirmation, error) {
	if w == nil {
		return nil, nil
	}
	by, err := authorFromWire(w.By)
	if err != nil {
		return nil, err
	}
	var op admin.OperationID
	if len(w.Operation) != len(op) {
		return nil, persisted(errors.New("operation identifier length"))
	}
	copy(op[:], w.Operation)
	at, err := fromSecs(w.At)
	if err != nil {
		return nil, err
	}
	return &Confirmation{Method: ConfirmMethod(w.Method), By: by, Operation: op, At: at}, nil
}

func completionToWire(c *Completion) (*completionWire, error) {
	if c == nil {
		return nil, nil
	}
	b, err := c.Credential.Encode()
	if err != nil {
		return nil, persisted(err)
	}
	return &completionWire{At: secs(c.At), Credential: b}, nil
}

func completionFromWire(w *completionWire) (*Completion, error) {
	if w == nil {
		return nil, nil
	}
	at, err := fromSecs(w.At)
	if err != nil {
		return nil, err
	}
	r, err := record.Decode(w.Credential, MaxPersisted)
	if err != nil {
		return nil, persisted(err)
	}
	return &Completion{At: at, Credential: r}, nil
}

func recordBytes(r *record.Record) ([]byte, error) {
	if r == nil {
		return nil, nil
	}
	b, err := r.Encode()
	if err != nil {
		return nil, persisted(err)
	}
	return b, nil
}

func recordFromBytes(b []byte) (*record.Record, error) {
	if b == nil {
		return nil, nil
	}
	r, err := record.Decode(b, MaxPersisted)
	if err != nil {
		return nil, persisted(err)
	}
	return &r, nil
}

// Encode returns the persisted bytes of the record.
func (r TargetRecord) Encode() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	sig, err := recordBytes(r.InitiatorSignature)
	if err != nil {
		return nil, err
	}
	done, err := completionToWire(r.Completed)
	if err != nil {
		return nil, err
	}
	return record.MarshalCanonical(targetWire{
		Type: TypeTargetRecord, Transcript: transcriptToWire(r.Transcript),
		ReceivedAt: secs(r.ReceivedAt), ExpiresAt: secs(r.ExpiresAt), HighestSerial: r.HighestSerial,
		Expected: optAuthorToWire(r.Expected), Status: uint64(r.Status), Confirmation: confirmationToWire(r.Confirmation),
		Signature: sig, Rejection: uint64(r.Rejection), Completed: done, EndedAt: optSecs(r.EndedAt),
	})
}

// DecodeTargetRecord reads a target record, recomputing the identifier, the
// code and the credential from the transcript. The caller compares ID() with
// the name the bytes were stored under and refuses a mismatch: a record moved
// under another name is not the request that name stands for.
func DecodeTargetRecord(data []byte) (TargetRecord, error) {
	var w targetWire
	if err := decodePersisted(data, &w, TypeTargetRecord, func(w *targetWire) string { return w.Type }); err != nil {
		return TargetRecord{}, err
	}
	tr, err := transcriptFromWire(w.Transcript)
	if err != nil {
		return TargetRecord{}, err
	}
	r := TargetRecord{Transcript: tr, HighestSerial: w.HighestSerial, Status: TargetStatus(w.Status), Rejection: WithdrawReason(w.Rejection)}
	if r.ReceivedAt, err = fromSecs(w.ReceivedAt); err != nil {
		return TargetRecord{}, err
	}
	if r.ExpiresAt, err = fromSecs(w.ExpiresAt); err != nil {
		return TargetRecord{}, err
	}
	if r.Expected, err = optAuthorFromWire(w.Expected); err != nil {
		return TargetRecord{}, err
	}
	if r.Confirmation, err = confirmationFromWire(w.Confirmation); err != nil {
		return TargetRecord{}, err
	}
	if r.InitiatorSignature, err = recordFromBytes(w.Signature); err != nil {
		return TargetRecord{}, err
	}
	if r.Completed, err = completionFromWire(w.Completed); err != nil {
		return TargetRecord{}, err
	}
	if r.EndedAt, err = fromOptSecs(w.EndedAt); err != nil {
		return TargetRecord{}, err
	}
	if err := r.Validate(); err != nil {
		return TargetRecord{}, persisted(err)
	}
	return r, nil
}

// Encode returns the persisted bytes of the record.
func (r InitiatorRecord) Encode() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	sig, err := recordBytes(r.Signature)
	if err != nil {
		return nil, err
	}
	done, err := completionToWire(r.Completed)
	if err != nil {
		return nil, err
	}
	return record.MarshalCanonical(initiatorWire{
		Type: TypeInitiatorRecord, Transcript: transcriptToWire(r.Transcript),
		ReceivedAt: secs(r.ReceivedAt), ExpiresAt: secs(r.ExpiresAt), Addresses: slices.Clone(r.Addresses),
		Expected: optAuthorToWire(r.Expected), Status: uint64(r.Status), Confirmation: confirmationToWire(r.Confirmation),
		Signature: sig, Released: r.Released, Reason: uint64(r.Reason), Completed: done, EndedAt: optSecs(r.EndedAt),
	})
}

// DecodeInitiatorRecord reads an initiator record; see DecodeTargetRecord.
func DecodeInitiatorRecord(data []byte) (InitiatorRecord, error) {
	var w initiatorWire
	if err := decodePersisted(data, &w, TypeInitiatorRecord, func(w *initiatorWire) string { return w.Type }); err != nil {
		return InitiatorRecord{}, err
	}
	tr, err := transcriptFromWire(w.Transcript)
	if err != nil {
		return InitiatorRecord{}, err
	}
	r := InitiatorRecord{Transcript: tr, Addresses: w.Addresses, Status: InitiatorStatus(w.Status), Released: w.Released, Reason: WithdrawReason(w.Reason)}
	if r.ReceivedAt, err = fromSecs(w.ReceivedAt); err != nil {
		return InitiatorRecord{}, err
	}
	if r.ExpiresAt, err = fromSecs(w.ExpiresAt); err != nil {
		return InitiatorRecord{}, err
	}
	if r.Expected, err = optAuthorFromWire(w.Expected); err != nil {
		return InitiatorRecord{}, err
	}
	if r.Confirmation, err = confirmationFromWire(w.Confirmation); err != nil {
		return InitiatorRecord{}, err
	}
	if r.Signature, err = recordFromBytes(w.Signature); err != nil {
		return InitiatorRecord{}, err
	}
	if r.Completed, err = completionFromWire(w.Completed); err != nil {
		return InitiatorRecord{}, err
	}
	if r.EndedAt, err = fromOptSecs(w.EndedAt); err != nil {
		return InitiatorRecord{}, err
	}
	if err := r.Validate(); err != nil {
		return InitiatorRecord{}, persisted(err)
	}
	return r, nil
}

func timesToWire(ts []time.Time) []int64 {
	if len(ts) == 0 {
		return nil
	}
	out := make([]int64, len(ts))
	for i, t := range ts {
		out[i] = secs(t)
	}
	return out
}

func timesFromWire(ws []int64) ([]time.Time, error) {
	var out []time.Time
	for _, w := range ws {
		t, err := fromSecs(w)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// Encode returns the persisted bytes of the counters.
func (l Limits) Encode() ([]byte, error) {
	return record.MarshalCanonical(limitsWire{Type: TypeLimits, Reveals: timesToWire(l.reveals), Failures: timesToWire(l.failures)})
}

// DecodeLimits reads persisted counters.
func DecodeLimits(data []byte) (Limits, error) {
	var w limitsWire
	if err := decodePersisted(data, &w, TypeLimits, func(w *limitsWire) string { return w.Type }); err != nil {
		return Limits{}, err
	}
	reveals, err := timesFromWire(w.Reveals)
	if err != nil {
		return Limits{}, err
	}
	failures, err := timesFromWire(w.Failures)
	if err != nil {
		return Limits{}, err
	}
	l, err := NewLimits(reveals, failures)
	if err != nil {
		return Limits{}, persisted(err)
	}
	return l, nil
}

// EncodeExpectations returns the persisted bytes of the list of expectations.
func EncodeExpectations(list []Expectation) ([]byte, error) {
	w := expectationsWire{Type: TypeExpectations}
	seen := map[ident.NodeID]bool{}
	for _, e := range list {
		if err := e.validate(); err != nil {
			return nil, err
		}
		if seen[e.Node] {
			return nil, fmt.Errorf("%w: node expected twice", ErrExpectation)
		}
		seen[e.Node] = true
		w.List = append(w.List, expectationWire{Node: e.Node.Bytes(), Author: authorToWire(e.Author), Operation: e.Operation[:], CreatedAt: secs(e.CreatedAt)})
	}
	return record.MarshalCanonical(w)
}

// DecodeExpectations reads a persisted list of expectations.
func DecodeExpectations(data []byte) ([]Expectation, error) {
	var w expectationsWire
	if err := decodePersisted(data, &w, TypeExpectations, func(w *expectationsWire) string { return w.Type }); err != nil {
		return nil, err
	}
	var out []Expectation
	seen := map[ident.NodeID]bool{}
	for _, x := range w.List {
		node, err := ident.NodeIDFromBytes(x.Node)
		if err != nil {
			return nil, persisted(err)
		}
		author, err := authorFromWire(x.Author)
		if err != nil {
			return nil, err
		}
		var op admin.OperationID
		if len(x.Operation) != len(op) {
			return nil, persisted(errors.New("operation identifier length"))
		}
		copy(op[:], x.Operation)
		created, err := fromSecs(x.CreatedAt)
		if err != nil {
			return nil, err
		}
		e := Expectation{Node: node, Author: author, Operation: op, CreatedAt: created}
		if err := e.validate(); err != nil {
			return nil, persisted(err)
		}
		if seen[node] {
			return nil, persisted(errors.New("node expected twice"))
		}
		seen[node] = true
		out = append(out, e)
	}
	return out, nil
}
