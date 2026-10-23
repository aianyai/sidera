package record

import (
	"errors"
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/ident"
)

// SignerSlot is one position in a record type's signer rule: it says which
// public keys may fill that position. It returns nil when k may fill the slot
// and otherwise the reason it may not; a refusal that only means "this is not
// the key the slot names" is the package's own plain refusal, and a slot that
// can say more (NetworkKeyOf) returns an error that callers can inspect with
// errors.As. A record is complete when every slot is filled by exactly one
// signer, and no signer is left without a slot. Slots of one rule must not
// overlap (no key satisfies two of them), which holds when each slot names a
// different identifier.
type SignerSlot func(ident.PublicKey) error

// errNotTheKey is the plain refusal of a slot: k is not the key it names.
var errNotTheKey = errors.New("not the key this slot names")

// NodeIdentityKey is the slot for the identity key of node id: the key whose
// derived node identifier is id.
func NodeIdentityKey(id ident.NodeID) SignerSlot {
	return func(k ident.PublicKey) error {
		if got, err := ident.NodeIDFromKey(k); err != nil || got != id {
			return errNotTheKey
		}
		return nil
	}
}

// PersonRootKey is the slot for the root key of person id: the key whose
// derived person identifier is id.
func PersonRootKey(id ident.PersonID) SignerSlot {
	return func(k ident.PublicKey) error {
		if got, err := ident.PersonIDFromKey(k); err != nil || got != id {
			return errNotTheKey
		}
		return nil
	}
}

// BindingError is the reason a NetworkKeyOf slot refused a key. A verification
// that fails because of a binding returns an error wrapping ErrSignerNotAllowed
// and a *BindingError, so a caller can tell an expired certificate from a key
// the certificate does not name:
//
//	var be *record.BindingError
//	if errors.As(err, &be) && be.Binding == record.BindingExpired { ... }
type BindingError struct {
	// Binding is the specific reason; it is never BindingOK.
	Binding Binding
	// Node is the node whose network key was expected.
	Node ident.NodeID
}

func (e *BindingError) Error() string {
	return fmt.Sprintf("network key is not accepted for node %s: %s", e.Node, e.Binding)
}

// NetworkKeyOf is the slot for the network key of node: the key that
// NetworkKeyBelongs accepts for it given what is accepted now. It is the one
// way a record signed by a network key states its signer rule, so the binding
// rules live only in NetworkKeyBelongs. A refused key yields a *BindingError.
//
// The rule depends on the caller (the accepted certificate, its revocation and
// the time), so the descriptor of such a record type is built per verification
// by a function of that context, not once at package initialisation:
//
//	func DeclarationDescriptor(accepted AcceptedNetworkKey, now time.Time) Descriptor[Declaration]
func NetworkKeyOf(node ident.NodeID, accepted AcceptedNetworkKey, now time.Time) SignerSlot {
	return func(k ident.PublicKey) error {
		if b := NetworkKeyBelongs(accepted, node, k, now); b != BindingOK {
			return &BindingError{Binding: b, Node: node}
		}
		return nil
	}
}

// Descriptor describes one record type: its type string (which carries the
// version), how to read and write its payload, and who must sign it. The package
// that defines a record type exports its Descriptor, and callers pass it to
// Verify and Seal. There is no registry, so a package can define record types
// without this package knowing about them.
//
// The zero Descriptor is invalid and every operation on it fails.
type Descriptor[P any] struct {
	typ     string
	decode  func([]byte) (P, error)
	encode  func(P) ([]byte, error)
	signers func(P) []SignerSlot
}

// NewDescriptor builds the descriptor of a record type.
//
// typ is the type string including its version, for example "…/1". W is the
// payload's wire struct: integer-keyed fields, optional ones tagged omitempty.
//
// Validation of a payload lives in exactly one place, fromWire. It converts a
// wire value back and must refuse every wire value that is well formed but not
// valid (zero required fields, unreduced values, an optional field written as
// zero, a rule between fields). toWire only converts a payload to its wire form;
// it may fail where a value has no wire representation at all (a time outside
// the wire range, a level with no wire value) and must not repeat the rules of
// fromWire. Encoding runs the decoding path on the bytes it produced
// (UnmarshalStrict and fromWire) and refuses when that fails, so a payload this
// package writes is one every receiver accepts, and the two directions cannot
// drift apart. signers returns the slots a payload requires.
//
// NewDescriptor panics on an empty or over-long type or a nil function: that is
// a programming error, found by the first test run, not a runtime condition.
func NewDescriptor[W, P any](
	typ string,
	toWire func(P) (W, error),
	fromWire func(W) (P, error),
	signers func(P) []SignerSlot,
) Descriptor[P] {
	if checkTypeString(typ) != nil || toWire == nil || fromWire == nil || signers == nil {
		panic("record: malformed descriptor for " + typ)
	}
	decode := func(payload []byte) (P, error) {
		var w W
		if err := UnmarshalStrict(payload, &w, len(payload)); err != nil {
			var zero P
			return zero, fmt.Errorf("payload of %s: %w", typ, err)
		}
		p, err := fromWire(w)
		if err != nil {
			var zero P
			return zero, fmt.Errorf("payload of %s: %w: %w", typ, ErrPayload, err)
		}
		return p, nil
	}
	return Descriptor[P]{
		typ:    typ,
		decode: decode,
		encode: func(p P) ([]byte, error) {
			w, err := toWire(p)
			if err != nil {
				return nil, fmt.Errorf("payload of %s: %w: %w", typ, ErrPayload, err)
			}
			b, err := MarshalCanonical(w)
			if err != nil {
				return nil, err
			}
			if _, err := decode(b); err != nil {
				return nil, err
			}
			return b, nil
		},
		signers: signers,
	}
}

// Type returns the record type string.
func (d Descriptor[P]) Type() string { return d.typ }

func (d Descriptor[P]) valid() bool {
	return d.typ != "" && d.decode != nil && d.encode != nil && d.signers != nil
}

// Verified is the result of checking a complete record against its descriptor:
// every slot of the signer rule is filled by an allowed signer whose signature
// verifies. Only Verify returns it. A record that may still lack signers is
// checked with VerifyPartial, which returns a Partial, a different type that
// cannot be used where a Verified is expected.
type Verified[P any] struct {
	Payload P
	Digest  Digest
	// Signers are the keys that signed, in the record's order.
	Signers []ident.PublicKey
}

// SlotState says whether one slot of a signer rule is filled and by whom.
type SlotState struct {
	// Signed reports that a signature fills the slot.
	Signed bool
	// Signer is the key that fills the slot; the zero key if Signed is false.
	Signer ident.PublicKey
}

// Partial is the result of checking a record that may still be waiting for
// signers: every signature present verifies and every signer present is
// allowed, but slots may be empty. It is not a Verified: nothing of it may be
// acted on as a statement of the record's signers until Verify accepts the
// complete record.
type Partial[P any] struct {
	Payload P
	Digest  Digest
	// Slots holds one entry per slot of the signer rule, in the rule's order.
	Slots []SlotState
}

// Complete reports that every slot is filled. A complete Partial is still not a
// Verified; pass the record to Verify for that.
func (p Partial[P]) Complete() bool {
	for _, s := range p.Slots {
		if !s.Signed {
			return false
		}
	}
	return true
}

// Seal encodes payload and signs it with signers, which must be keys the
// record type allows. For a record that needs two signatures, pass one signer
// now and let the second party call CoSign on the result.
func Seal[P any](d Descriptor[P], payload P, signers ...Signer) (Record, error) {
	if !d.valid() {
		return Record{}, ErrDescriptor
	}
	b, err := d.encode(payload)
	if err != nil {
		return Record{}, err
	}
	// Refuse a signer that the rule would refuse, so the mistake shows here and
	// not at the far end.
	slots := d.signers(payload)
	state := make([]SlotState, len(slots))
	for _, s := range signers {
		if err := assign(slots, state, s.Public()); err != nil {
			return Record{}, fmt.Errorf("%s: %w", d.typ, err)
		}
	}
	return Sign(d.typ, b, signers...)
}

// Verify checks that r is a complete, correctly signed record of the type d
// describes and returns its payload.
func Verify[P any](d Descriptor[P], r Record) (Verified[P], error) {
	p, err := VerifyPartial(d, r)
	if err != nil {
		return Verified[P]{}, err
	}
	if !p.Complete() {
		return Verified[P]{}, fmt.Errorf("%w: %s", ErrIncomplete, d.typ)
	}
	signers := make([]ident.PublicKey, len(r.Signatures))
	for i, s := range r.Signatures {
		signers[i] = s.Key
	}
	return Verified[P]{Payload: p.Payload, Digest: p.Digest, Signers: signers}, nil
}

// VerifyPartial is Verify for a record that may still be waiting for other
// signers: every signature present must verify and every signer present must
// be allowed, but slots may be empty. The result is a Partial, which says which
// slots are filled and cannot stand in for a Verified. The second party of a
// two-party record uses it to check what the first party signed before adding
// its own signature.
func VerifyPartial[P any](d Descriptor[P], r Record) (Partial[P], error) {
	if !d.valid() {
		return Partial[P]{}, ErrDescriptor
	}
	if r.Type != d.typ {
		return Partial[P]{}, fmt.Errorf("%w: got %q, want %q", ErrType, r.Type, d.typ)
	}
	if err := r.shape(); err != nil {
		return Partial[P]{}, err
	}
	p, err := d.decode(r.Payload)
	if err != nil {
		return Partial[P]{}, err
	}
	slots := d.signers(p)
	state := make([]SlotState, len(slots))
	for i, s := range r.Signatures {
		if err := assign(slots, state, s.Key); err != nil {
			return Partial[P]{}, fmt.Errorf("signature %d: %w", i, err)
		}
	}
	msg := SignedBytes(r.Type, r.Payload)
	for i, s := range r.Signatures {
		if !s.Key.Verify(msg, s.Sig) {
			return Partial[P]{}, fmt.Errorf("%w: signature %d", ErrSignature, i)
		}
	}
	return Partial[P]{Payload: p, Digest: r.Digest(), Slots: state}, nil
}

// CoSign adds s's signature to r, a record of the type d describes that is
// still waiting for signers. The first party signs with Seal and sends the
// record; the second party calls CoSign on what it received. Before signing,
// CoSign verifies the record against d as VerifyPartial does, so a signer is
// never made to put a signature next to a payload or signatures it has not
// checked, and it requires s to fill an empty slot of the signer rule: a signer
// the rule does not allow, a signer that has already signed and a record whose
// slots are all filled are refused. A zero Record is refused. The result may
// still be incomplete (a rule with more than two slots); check it with Verify or
// VerifyPartial.
func CoSign[P any](d Descriptor[P], r Record, s Signer) (Record, error) {
	partial, err := VerifyPartial(d, r)
	if err != nil {
		return Record{}, err
	}
	pub := s.Public()
	if pub.IsZero() {
		return Record{}, fmt.Errorf("%w: signer has no key", ErrKey)
	}
	for _, have := range r.Signatures {
		if have.Key == pub {
			return Record{}, ErrDuplicateSigner
		}
	}
	if err := assign(d.signers(partial.Payload), partial.Slots, pub); err != nil {
		return Record{}, fmt.Errorf("%s: %w", d.typ, err)
	}
	return r.withSignature(s)
}

// assign puts k into the first free slot that accepts it and records it in
// state. When none does, the error wraps ErrSignerNotAllowed and the specific
// reasons the free slots gave (a plain "not the key this slot names" is not
// worth repeating).
func assign(slots []SignerSlot, state []SlotState, k ident.PublicKey) error {
	var reasons []error
	for i, slot := range slots {
		if state[i].Signed {
			continue
		}
		err := slot(k)
		if err == nil {
			state[i] = SlotState{Signed: true, Signer: k}
			return nil
		}
		if !errors.Is(err, errNotTheKey) {
			reasons = append(reasons, err)
		}
	}
	if len(reasons) > 0 {
		return fmt.Errorf("%w: %w", ErrSignerNotAllowed, errors.Join(reasons...))
	}
	return ErrSignerNotAllowed
}
