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

// Verified is the result of checking a record against its descriptor. The
// payload is trustworthy only as far as the record type's signer rule: the
// signers were allowed, and their signatures verify.
type Verified[P any] struct {
	Payload P
	Digest  Digest
	// Signers are the keys that signed, in the record's order.
	Signers []ident.PublicKey
	// Complete reports that every slot of the signer rule is filled. It is
	// always true from Verify; VerifyPartial may return false.
	Complete bool
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
	used := make([]bool, len(slots))
	for _, s := range signers {
		if err := assign(slots, used, s.Public()); err != nil {
			return Record{}, fmt.Errorf("%s: %w", d.typ, err)
		}
	}
	return Sign(d.typ, b, signers...)
}

// Verify checks that r is a complete, correctly signed record of the type d
// describes and returns its payload.
func Verify[P any](d Descriptor[P], r Record) (Verified[P], error) {
	v, err := verify(d, r)
	if err != nil {
		return Verified[P]{}, err
	}
	if !v.Complete {
		return Verified[P]{}, fmt.Errorf("%w: %s", ErrIncomplete, d.typ)
	}
	return v, nil
}

// VerifyPartial is Verify for a record that may still be waiting for other
// signers: every signature present must verify and every signer present must
// be allowed, but slots may be empty. Verified.Complete tells which case it is.
// The second party of a two-party record uses it to check what the first party
// signed before adding its own signature.
func VerifyPartial[P any](d Descriptor[P], r Record) (Verified[P], error) {
	return verify(d, r)
}

func verify[P any](d Descriptor[P], r Record) (Verified[P], error) {
	if !d.valid() {
		return Verified[P]{}, ErrDescriptor
	}
	if r.Type != d.typ {
		return Verified[P]{}, fmt.Errorf("%w: got %q, want %q", ErrType, r.Type, d.typ)
	}
	if err := r.shape(); err != nil {
		return Verified[P]{}, err
	}
	p, err := d.decode(r.Payload)
	if err != nil {
		return Verified[P]{}, err
	}
	slots := d.signers(p)
	used := make([]bool, len(slots))
	signers := make([]ident.PublicKey, len(r.Signatures))
	for i, s := range r.Signatures {
		if err := assign(slots, used, s.Key); err != nil {
			return Verified[P]{}, fmt.Errorf("signature %d: %w", i, err)
		}
		signers[i] = s.Key
	}
	msg := SignedBytes(r.Type, r.Payload)
	for i, s := range r.Signatures {
		if !s.Key.Verify(msg, s.Sig) {
			return Verified[P]{}, fmt.Errorf("%w: signature %d", ErrSignature, i)
		}
	}
	complete := true
	for _, u := range used {
		complete = complete && u
	}
	return Verified[P]{Payload: p, Digest: r.Digest(), Signers: signers, Complete: complete}, nil
}

// assign puts k into the first free slot that accepts it. When none does, the
// error wraps ErrSignerNotAllowed and the specific reasons the free slots gave
// (a plain "not the key this slot names" is not worth repeating).
func assign(slots []SignerSlot, used []bool, k ident.PublicKey) error {
	var reasons []error
	for i, slot := range slots {
		if used[i] {
			continue
		}
		err := slot(k)
		if err == nil {
			used[i] = true
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
