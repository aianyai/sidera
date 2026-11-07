package nodeproto

import (
	"bytes"
	"testing"
)

// The property of every decoder: an input that decodes encodes again to the
// identical bytes, and nothing panics, whatever the input.
func fuzzDecoder[T interface{ Encode() ([]byte, error) }](f *testing.F, decode func([]byte) (T, error), seeds ...string) {
	for _, s := range seeds {
		f.Add(unhex(f, s))
	}
	f.Add([]byte{})
	f.Add([]byte{0xa0})
	f.Fuzz(func(t *testing.T, data []byte) {
		v, err := decode(data)
		if err != nil {
			return
		}
		again, err := v.Encode()
		if err != nil {
			t.Fatalf("a decoded message does not encode: %v", err)
		}
		if !bytes.Equal(again, data) {
			t.Fatalf("re-encoding differs:\n got %x\nwant %x", again, data)
		}
	})
}

func FuzzDecodeSendRequest(f *testing.F) {
	fuzzDecoder(f, DecodeSendRequest, goldenSendRequest)
}

func FuzzDecodeSendAnswer(f *testing.F) {
	fuzzDecoder(f, DecodeSendAnswer, goldenSendAnswerOK, goldenSendAnswerRefused)
}

func FuzzDecodePullRequest(f *testing.F) {
	fuzzDecoder(f, DecodePullRequest, goldenPullRequest)
}

func FuzzDecodePullAnswer(f *testing.F) {
	fuzzDecoder(f, DecodePullAnswer, goldenPullAnswerOK)
}

func FuzzDecodeDirectoryRequest(f *testing.F) {
	fuzzDecoder(f, DecodeDirectoryRequest, goldenDirectoryRequest)
}

func FuzzDecodeDirectoryAnswer(f *testing.F) {
	fuzzDecoder(f, DecodeDirectoryAnswer, goldenDirectoryAnswer)
}

func FuzzDecodeDialBackRequest(f *testing.F) {
	fuzzDecoder(f, DecodeDialBackRequest, goldenDialBackRequest)
}

func FuzzDecodeDialBackAnswer(f *testing.F) {
	fuzzDecoder(f, DecodeDialBackAnswer, goldenDialBackReached, goldenDialBackNoAddress, goldenDialBackBusy)
}

func FuzzDecodeProfileRequest(f *testing.F) {
	fuzzDecoder(f, DecodeProfileRequest, goldenProfileRequest)
}

func FuzzDecodeProfileAnswer(f *testing.F) {
	fuzzDecoder(f, DecodeProfileAnswer, goldenProfileAnswerOK, goldenProfileRefused)
}
