package preauth

import (
	"bytes"
	"errors"
	"testing"

	"github.com/aianyai/sidera/internal/record"
)

// The record inside each golden message, as a standalone slice: everything
// after the wrapper header (6 bytes: a2 01 kk 02 58 ll).
func innerRecord(t testing.TB, message []byte) []byte {
	t.Helper()
	return message[6:]
}

func TestGoldenExchangeDecodes(t *testing.T) {
	for _, row := range []struct {
		name    string
		message []byte
		kind    Kind
		typ     string
	}{
		{"a node message carries the network-key certificate", goldenNodeExchange, KindNode, record.NetworkKeyCertificateType},
		{"a person message carries the device certificate", goldenPersonExchange, KindPerson, record.DeviceCertificateType},
	} {
		t.Run(row.name, func(t *testing.T) {
			ex, err := DecodeExchange(row.message)
			if err != nil {
				t.Fatal(err)
			}
			if ex.Kind != row.kind || ex.Record.Type != row.typ {
				t.Errorf("got kind %v, record type %q; want %v, %q", ex.Kind, ex.Record.Type, row.kind, row.typ)
			}
			again, err := ex.Encode()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(again, row.message) {
				t.Errorf("re-encoding differs from the golden bytes:\n got %x\nwant %x", again, row.message)
			}
		})
	}
}

// The golden messages are built from the golden records alone, and the record
// inside is exactly the record the other package encodes.
func TestExchangeEncodesTheGoldenBytes(t *testing.T) {
	rec, err := record.Decode(innerRecord(t, goldenNodeExchange), MaxMessageSize)
	if err != nil {
		t.Fatal(err)
	}
	got, err := NewNodeExchange(rec).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, goldenNodeExchange) {
		t.Errorf("node message differs:\n got %x\nwant %x", got, goldenNodeExchange)
	}
	rec, err = record.Decode(innerRecord(t, goldenPersonExchange), MaxMessageSize)
	if err != nil {
		t.Fatal(err)
	}
	got, err = NewPersonExchange(rec).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, goldenPersonExchange) {
		t.Errorf("person message differs:\n got %x\nwant %x", got, goldenPersonExchange)
	}
}

func TestDecodeExchangeRefuses(t *testing.T) {
	rec := innerRecord(t, goldenNodeExchange)
	w := cborw
	oversize := append(append([]byte{}, goldenNodeExchange...), bytes.Repeat([]byte{0}, MaxMessageSize)...)
	corruptInner := append([]byte{}, rec...)
	corruptInner[0] = 0xa2 // the record envelope claims 2 pairs instead of 3
	for _, row := range []struct {
		rule  string
		input []byte
		is    error // the sentinel the refusal wraps, when it has one
	}{
		{"empty input", nil, record.ErrMalformed},
		{"input longer than the size bound is refused before decoding", oversize, record.ErrOversize},
		{"trailing bytes after the message", append(append([]byte{}, goldenNodeExchange...), 0x00), record.ErrMalformed},
		{"a kind outside the two is refused", w.mapOf(w.uint(1), w.uint(3), w.uint(2), w.bstr(rec)), ErrKind},
		{"kind zero is refused", w.mapOf(w.uint(1), w.uint(0), w.uint(2), w.bstr(rec)), ErrKind},
		{"a kind too large for the enumeration is refused", w.mapOf(w.uint(1), w.uint(257), w.uint(2), w.bstr(rec)), ErrKind},
		{"a kind written in more bytes than needed is not canonical", append([]byte{0xa2, 0x01, 0x18, 0x01, 0x02}, w.bstr(rec)...), record.ErrNotCanonical},
		{"keys out of canonical order", w.mapOf(w.uint(2), w.bstr(rec), w.uint(1), w.uint(1)), record.ErrNotCanonical},
		{"a duplicate key", w.mapOf(w.uint(1), w.uint(1), w.uint(1), w.uint(1), w.uint(2), w.bstr(rec)), record.ErrMalformed},
		{"an unknown field", w.mapOf(w.uint(1), w.uint(1), w.uint(2), w.bstr(rec), w.uint(3), w.uint(0)), record.ErrMalformed},
		{"a missing record", w.mapOf(w.uint(1), w.uint(1)), record.ErrNotCanonical},
		{"a missing kind", w.mapOf(w.uint(2), w.bstr(rec)), record.ErrNotCanonical},
		{"an indefinite-length record", append(append([]byte{0xa2, 0x01, 0x01, 0x02, 0x5f}, w.bstr(rec)...), 0xff), record.ErrMalformed},
		{"a tagged message", append([]byte{0xc1}, goldenNodeExchange...), record.ErrMalformed},
		{"a record that is not a byte string", w.mapOf(w.uint(1), w.uint(1), w.uint(2), w.uint(7)), record.ErrMalformed},
		{"an empty record", w.mapOf(w.uint(1), w.uint(1), w.uint(2), w.bstr(nil)), record.ErrMalformed},
		{"a certificate record that does not decode", w.mapOf(w.uint(1), w.uint(1), w.uint(2), w.bstr(corruptInner)), record.ErrMalformed},
	} {
		t.Run(row.rule, func(t *testing.T) {
			if _, err := DecodeExchange(row.input); err == nil {
				t.Fatal("accepted")
			} else if row.is != nil && !errors.Is(err, row.is) {
				t.Errorf("error %v does not wrap %v", err, row.is)
			}
		})
	}
}

func TestEncodeExchangeRefuses(t *testing.T) {
	rec, err := record.Decode(innerRecord(t, goldenNodeExchange), MaxMessageSize)
	if err != nil {
		t.Fatal(err)
	}
	big, err := record.Sign("sidera/test/big/1", bytes.Repeat([]byte{1}, MaxMessageSize), rfcSigner(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		rule string
		ex   Exchange
		is   error
	}{
		{"an unset kind", Exchange{Record: rec}, ErrKind},
		{"an unknown kind", Exchange{Kind: 3, Record: rec}, ErrKind},
		{"a zero record", Exchange{Kind: KindNode}, record.ErrType},
		{"a message that no receiver would read", NewNodeExchange(big), record.ErrOversize},
	} {
		t.Run(row.rule, func(t *testing.T) {
			if _, err := row.ex.Encode(); !errors.Is(err, row.is) {
				t.Errorf("got %v, want an error wrapping %v", err, row.is)
			}
		})
	}
}

// The exchange deadline is the one decided number of the package.
func TestExchangeDeadlineIsFiveSeconds(t *testing.T) {
	if ExchangeDeadline.Seconds() != 5 {
		t.Errorf("ExchangeDeadline = %v", ExchangeDeadline)
	}
}

func TestMessageSizeBoundHoldsACertificate(t *testing.T) {
	for _, m := range [][]byte{goldenNodeExchange, goldenPersonExchange} {
		if len(m) > MaxMessageSize/2 {
			t.Errorf("a certificate message is %d bytes, more than half the bound %d: the bound leaves too little room", len(m), MaxMessageSize)
		}
	}
}
