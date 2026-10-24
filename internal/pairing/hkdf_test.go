package pairing

import (
	"bytes"
	"testing"
)

// The known-answer vectors of RFC 5869 appendix A.1 to A.3 (SHA-256). They are
// checked first: the verification code and the credential nonce rest on this
// derivation, and a mistake in how it is called would otherwise show only as
// two nodes that agree on a wrong code.
func TestHKDFRFC5869(t *testing.T) {
	seq := func(from, to int) []byte {
		var b []byte
		for i := from; i <= to; i++ {
			b = append(b, byte(i))
		}
		return b
	}
	tests := []struct {
		name            string
		ikm, salt, info []byte
		length          int
		prk, okm        string
	}{
		{
			name: "A.1 basic",
			ikm:  bytes.Repeat([]byte{0x0b}, 22), salt: seq(0x00, 0x0c), info: seq(0xf0, 0xf9), length: 42,
			prk: "077709362c2e32df0ddc3f0dc47bba6390b6c73bb50f9c3122ec844ad7c2b3e5",
			okm: "3cb25f25faacd57a90434f64d0362f2a2d2d0a90cf1a5a4c5db02d56ecc4c5bf34007208d5b887185865",
		},
		{
			name: "A.2 longer inputs and outputs",
			ikm:  seq(0x00, 0x4f), salt: seq(0x60, 0xaf), info: seq(0xb0, 0xff), length: 82,
			prk: "06a6b88c5853361a06104c9ceb35b45cef760014904671014a193f40c15fc244",
			okm: "b11e398dc80327a1c8e7f78c596a49344f012eda2d4efad8a050cc4c19afa97c59045a99cac7827271cb41c65e590e09da3275600c2f09b8367793a9aca3db71cc30c58179ec3e87c14c01d5c1f3434f1d87",
		},
		{
			name: "A.3 zero-length salt and info",
			ikm:  bytes.Repeat([]byte{0x0b}, 22), salt: nil, info: nil, length: 42,
			prk: "19ef24a32c717b167f33a91d6f648bdf96596776afdb6377ac434c1c293ccb04",
			okm: "8da4e775a563c18f715f802a063c5a31b8a11f5c5ee1879ec3454e5f3c738d2d9d201395faa4b61a96c8",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prk, err := hkdfExtract(tt.salt, tt.ikm)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(prk, mustHex(t, tt.prk)) {
				t.Fatalf("PRK = %x, want %s", prk, tt.prk)
			}
			okm, err := hkdfExpand(prk, tt.info, tt.length)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(okm, mustHex(t, tt.okm)) {
				t.Fatalf("OKM = %x, want %s", okm, tt.okm)
			}
		})
	}
}
