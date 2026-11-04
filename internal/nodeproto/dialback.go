package nodeproto

import (
	"bytes"

	ma "github.com/multiformats/go-multiaddr"

	"github.com/aianyai/sidera/internal/record"
)

// ReasonDialBackBusy is the reason a node refuses a dial-back request with when
// it is already dialing back for the asker, or for as many nodes as it will at
// once.
const ReasonDialBackBusy = "busy"

// MaxAddressLen is the longest multiaddr, in bytes, a dial-back answer carries.
const MaxAddressLen = 256

// DialBackRequest asks the target to dial the asker back. It carries nothing:
// the target dials only the addresses it holds for the asking node, which is the
// asker's own question ("can you reach me the way you would if you had to find
// me"), and it also means the request cannot be used to make the target dial an
// address of the asker's choosing. The asking node is the one the connection
// authenticated.
type DialBackRequest struct{}

type dialBackRequestWire struct{}

// Encode returns the canonical bytes of the request, the empty map.
func (DialBackRequest) Encode() ([]byte, error) {
	return encodeChecked(dialBackRequestWire{}, DecodeDialBackRequest)
}

// DecodeDialBackRequest parses a dial-back request strictly, within the request
// limit of the protocol: it is the empty map and nothing else.
func DecodeDialBackRequest(data []byte) (DialBackRequest, error) {
	var w dialBackRequestWire
	if err := record.UnmarshalStrict(data, &w, limit(ProtocolDialBack).Request); err != nil {
		return DialBackRequest{}, err
	}
	return DialBackRequest{}, nil
}

// DialBackResult is what an answered dial-back says. The zero value is not a
// result.
type DialBackResult uint8

// The results of a dial-back that was made.
const (
	// DialBackReached: a new connection to the asker was made and its secure
	// handshake and stream-muxer negotiation completed.
	DialBackReached DialBackResult = iota + 1
	// DialBackNotReached: the target holds addresses for the asker and none of
	// them connected within the target's connect budget.
	DialBackNotReached
	// DialBackNoAddress: the target holds no address for the asker.
	DialBackNoAddress
)

func (r DialBackResult) valid() bool { return r >= DialBackReached && r <= DialBackNoAddress }

// String returns the name of the result.
func (r DialBackResult) String() string {
	switch r {
	case DialBackReached:
		return "reached"
	case DialBackNotReached:
		return "not reached"
	case DialBackNoAddress:
		return "no address"
	}
	return "invalid"
}

// DialBackAnswer is the target's answer to a dial-back request. With the outcome
// ok it holds a result and, for a result of reached, the address that connected.
// A refused answer carries the reason name and a failed one nothing.
type DialBackAnswer struct {
	Outcome Outcome
	Result  DialBackResult
	// Address is the address of the asker that connected, without a /p2p/ part;
	// present exactly when the result is reached.
	Address ma.Multiaddr
	Reason  string
}

type dialBackAnswerWire struct {
	Outcome uint64 `cbor:"1,keyasint"`
	Result  uint64 `cbor:"2,keyasint,omitempty"`
	Address []byte `cbor:"3,keyasint,omitempty"`
	Reason  string `cbor:"4,keyasint,omitempty"`
}

// Encode returns the canonical bytes of the answer. It refuses what
// DecodeDialBackAnswer would.
func (a DialBackAnswer) Encode() ([]byte, error) {
	var addr []byte
	if len(a.Address) > 0 {
		addr = a.Address.Bytes()
	}
	return encodeChecked(dialBackAnswerWire{Outcome: uint64(a.Outcome), Result: uint64(a.Result), Address: addr, Reason: a.Reason}, DecodeDialBackAnswer)
}

// DecodeDialBackAnswer parses a dial-back answer strictly, within the answer
// limit of the protocol. A result goes with the outcome ok and with no other, and
// an address with the result reached and with no other; the address is a binary
// multiaddr of at most MaxAddressLen bytes in the one form the library writes.
func DecodeDialBackAnswer(data []byte) (DialBackAnswer, error) {
	var w dialBackAnswerWire
	if err := record.UnmarshalStrict(data, &w, limit(ProtocolDialBack).Answer); err != nil {
		return DialBackAnswer{}, err
	}
	o, err := checkOutcome(w.Outcome, w.Reason)
	if err != nil {
		return DialBackAnswer{}, err
	}
	if o != OutcomeOK {
		if w.Result != 0 || len(w.Address) > 0 {
			return DialBackAnswer{}, invalid("a %s answer carries no result", o)
		}
		return DialBackAnswer{Outcome: o, Reason: w.Reason}, nil
	}
	if w.Result > uint64(DialBackNoAddress) || !DialBackResult(w.Result).valid() {
		return DialBackAnswer{}, invalid("result %d", w.Result)
	}
	res := DialBackResult(w.Result)
	if (res == DialBackReached) != (len(w.Address) > 0) {
		return DialBackAnswer{}, invalid("an address goes with the result reached and with no other, result %s", res)
	}
	var addr ma.Multiaddr
	if len(w.Address) > 0 {
		if len(w.Address) > MaxAddressLen {
			return DialBackAnswer{}, invalid("address is %d bytes, at most %d", len(w.Address), MaxAddressLen)
		}
		if addr, err = ma.NewMultiaddrBytes(w.Address); err != nil {
			return DialBackAnswer{}, invalid("address: %v", err)
		}
		if !bytes.Equal(addr.Bytes(), w.Address) {
			return DialBackAnswer{}, invalid("address is not in its one binary form")
		}
	}
	return DialBackAnswer{Outcome: o, Result: res, Address: addr, Reason: w.Reason}, nil
}
