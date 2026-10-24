package preauth

import (
	"bytes"
	"testing"
)

// FuzzDecodeExchange: any input that decodes re-encodes to the identical bytes,
// carries a known kind and stays within the size bound; nothing panics.
func FuzzDecodeExchange(f *testing.F) {
	f.Add(goldenNodeExchange)
	f.Add(goldenPersonExchange)
	f.Add([]byte{})
	f.Add([]byte{0xa0})
	f.Add(cborw.mapOf(cborw.uint(1), cborw.uint(1), cborw.uint(2), cborw.bstr(goldenNodeExchange[6:])))
	f.Add(append([]byte{0xa2, 0x01, 0x18, 0x01, 0x02}, cborw.bstr(goldenNodeExchange[6:])...))
	f.Fuzz(func(t *testing.T, data []byte) {
		ex, err := DecodeExchange(data)
		if err != nil {
			return
		}
		if !ex.Kind.valid() {
			t.Fatalf("decoded an invalid kind %d", ex.Kind)
		}
		again, err := ex.Encode()
		if err != nil {
			t.Fatalf("a decoded message does not encode: %v", err)
		}
		if !bytes.Equal(again, data) {
			t.Fatalf("re-encoding differs:\n got %x\nwant %x", again, data)
		}
		if len(data) > MaxMessageSize {
			t.Fatalf("decoded %d bytes, above the bound", len(data))
		}
	})
}
