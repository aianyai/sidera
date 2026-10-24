package preauth

import (
	"errors"
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/record"
)

// ExchangeDeadline is how long a connection has to complete the certificate
// exchange, counted from the moment the connection exists. A connection that is
// still unauthenticated then is closed. The value is a protocol rule, not a
// tuning knob: it is the same on every node, and the longest time a stranger
// can hold an unauthenticated slot. It binds the inbound connections the
// accounting tracks; the time an outbound connection may take is the network
// layer's own, because the node chose to dial it and its dial timeout already
// bounds it.
const ExchangeDeadline = 5 * time.Second

// MaxMessageSize is the largest exchange message, in bytes, on the wire. A
// certificate record is about 230 bytes; the bound leaves room for a longer
// future type string and a few more signatures, and is small enough that a
// stranger cannot make a node buffer anything of consequence before it is
// authenticated. A decoder reads at most this many bytes and refuses more.
//
// The bound covers Ed25519 certificates. Raising it, for example to carry the
// larger signatures of a post-quantum algorithm, is a protocol change: a node
// that keeps the old bound refuses the longer message, so the exchange protocol
// gets a new version identifier alongside.
const MaxMessageSize = 1024

// Kind says which certificate an exchange message carries. It is the type the
// receiver dispatches on: one protocol serves nodes and persons.
type Kind uint8

const (
	// KindUnset is the zero value; it is never valid.
	KindUnset Kind = iota
	// KindNode: the record is the sender's network-key certificate
	// (record.NetworkKeyCertificate).
	KindNode
	// KindPerson: the record is the sender's device certificate
	// (record.DeviceCertificate). The person is the one the certificate names;
	// the root key that signed it derives that person's identifier, which
	// verification checks, so nothing else is sent. Standing on the receiver's
	// list and revocations are the receiver's own facts.
	KindPerson
)

func (k Kind) String() string {
	switch k {
	case KindNode:
		return "node"
	case KindPerson:
		return "person"
	}
	return "unset"
}

// Errors of the exchange message. They wrap or sit next to the errors of
// package record, which decodes the certificate record inside.
var (
	// ErrKind reports a message kind that is not one of the two.
	ErrKind = errors.New("unknown exchange kind")
)

// Exchange is one certificate exchange message: a typed envelope around the
// certificate record. It is not a signed record: the sender is already
// authenticated by the secure handshake, and the certificate inside carries the
// signature that matters. It is canonical CBOR, `{1: kind, 2: record}`, where
// the record is the encoded signed record as a byte string.
type Exchange struct {
	Kind   Kind
	Record record.Record
}

// NewNodeExchange wraps a node's network-key certificate record.
func NewNodeExchange(cert record.Record) Exchange { return Exchange{Kind: KindNode, Record: cert} }

// NewPersonExchange wraps a person's device certificate record.
func NewPersonExchange(cert record.Record) Exchange {
	return Exchange{Kind: KindPerson, Record: cert}
}

type exchangeWire struct {
	Kind   uint64 `cbor:"1,keyasint"`
	Record []byte `cbor:"2,keyasint"`
}

func (k Kind) valid() bool { return k == KindNode || k == KindPerson }

// Encode returns the canonical bytes of the message. It refuses an unset kind, a
// record that does not encode, and a message longer than MaxMessageSize, which
// no receiver would read.
func (e Exchange) Encode() ([]byte, error) {
	if !e.Kind.valid() {
		return nil, fmt.Errorf("%w: %d", ErrKind, e.Kind)
	}
	rec, err := e.Record.Encode()
	if err != nil {
		return nil, err
	}
	out, err := record.MarshalCanonical(exchangeWire{Kind: uint64(e.Kind), Record: rec})
	if err != nil {
		return nil, err
	}
	if len(out) > MaxMessageSize {
		return nil, fmt.Errorf("%w: %d bytes, limit %d", record.ErrOversize, len(out), MaxMessageSize)
	}
	return out, nil
}

// DecodeExchange decodes one message with the strict rules of package record:
// the size bound first, then no duplicate keys, unknown fields, indefinite
// lengths or tags, and the bytes must be the one canonical encoding. The
// certificate record inside is decoded the same way. Nothing is verified:
// signatures, the certificate's type and its standing are judged by Decide.
func DecodeExchange(data []byte) (Exchange, error) {
	var w exchangeWire
	if err := record.UnmarshalStrict(data, &w, MaxMessageSize); err != nil {
		return Exchange{}, err
	}
	if w.Kind > 0xff || !Kind(w.Kind).valid() {
		return Exchange{}, fmt.Errorf("%w: %d", ErrKind, w.Kind)
	}
	rec, err := record.Decode(w.Record, MaxMessageSize)
	if err != nil {
		return Exchange{}, err
	}
	return Exchange{Kind: Kind(w.Kind), Record: rec}, nil
}
