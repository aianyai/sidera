package record

import (
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/ident"
)

// Record types defined by the tests. They show that a package can define a
// record type with NewDescriptor alone, without touching this package.

// pairRecord must be signed by the identity keys of two nodes.
type pairRecord struct{ First, Second ident.NodeID }

type pairWire struct {
	First  []byte `cbor:"1,keyasint"`
	Second []byte `cbor:"2,keyasint"`
}

const pairType = "sidera/test/pair/1"

var pairDescriptor = NewDescriptor(pairType,
	func(p pairRecord) (pairWire, error) { return pairWire{p.First.Bytes(), p.Second.Bytes()}, nil },
	func(w pairWire) (pairRecord, error) {
		a, err := ident.NodeIDFromBytes(w.First)
		if err != nil {
			return pairRecord{}, err
		}
		b, err := ident.NodeIDFromBytes(w.Second)
		if err != nil {
			return pairRecord{}, err
		}
		if a == b {
			return pairRecord{}, fmt.Errorf("both parties are the same node")
		}
		return pairRecord{a, b}, nil
	},
	func(p pairRecord) []SignerSlot {
		return []SignerSlot{NodeIdentityKey(p.First), NodeIdentityKey(p.Second)}
	},
)

// pairDescriptorV2 has the same payload and rules under another type string.
var pairDescriptorV2 = NewDescriptor("sidera/test/pair/2",
	func(p pairRecord) (pairWire, error) { return pairWire{p.First.Bytes(), p.Second.Bytes()}, nil },
	func(w pairWire) (pairRecord, error) {
		a, _ := ident.NodeIDFromBytes(w.First)
		b, _ := ident.NodeIDFromBytes(w.Second)
		return pairRecord{a, b}, nil
	},
	func(p pairRecord) []SignerSlot {
		return []SignerSlot{NodeIdentityKey(p.First), NodeIdentityKey(p.Second)}
	},
)

// noteRecord has a required text and an optional time; any single key may sign.
type noteRecord struct {
	Text     string
	NotAfter time.Time // zero means "not provided"
}

type noteWire struct {
	Text     string `cbor:"1,keyasint"`
	NotAfter *int64 `cbor:"2,keyasint,omitempty"`
}

const noteType = "sidera/test/note/1"

var noteDescriptor = NewDescriptor(noteType,
	func(n noteRecord) (noteWire, error) {
		p, err := OptionalTimeToWire(n.NotAfter)
		return noteWire{n.Text, p}, err
	},
	func(w noteWire) (noteRecord, error) {
		t, err := OptionalTimeFromWire(w.NotAfter)
		if err != nil {
			return noteRecord{}, err
		}
		if w.Text == "" {
			return noteRecord{}, fmt.Errorf("empty text")
		}
		return noteRecord{w.Text, t}, nil
	},
	func(noteRecord) []SignerSlot { return []SignerSlot{func(ident.PublicKey) bool { return true }} },
)
