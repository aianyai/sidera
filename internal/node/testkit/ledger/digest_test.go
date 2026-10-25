package ledger_test

import (
	"testing"

	"github.com/aianyai/sidera/internal/node/testkit/ledger"
)

// TestDigestIsSHA256InHex: a body is recorded as its SHA-256, lower-case hex, so
// that the ledger holds no body and a test can still say "this exact text". The
// vectors are the published ones for SHA-256 ("abc" and the empty string).
func TestDigestIsSHA256InHex(t *testing.T) {
	rows := []struct{ in, want string }{
		{"abc", "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"},
		{"", "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
	}
	for _, r := range rows {
		if got := ledger.Digest([]byte(r.in)); got != r.want {
			t.Errorf("Digest(%q) = %s, want %s", r.in, got, r.want)
		}
	}
}
