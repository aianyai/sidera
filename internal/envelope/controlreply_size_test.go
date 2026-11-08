package envelope

import (
	"bytes"
	"testing"

	"github.com/aianyai/sidera/internal/record"
)

// A control reply is read with record.DefaultMaxInput, so the largest payload the
// check lets through must make a signed record a reader takes, and one byte more
// must be refused.
func TestControlReplySizeIsTheSignedRecordsAgainstWhatAReaderTakes(t *testing.T) {
	signedLen := func(n int) int {
		rec, err := record.Sign(ControlReplyType, bytes.Repeat([]byte{7}, n), signerOf(t, keyNetworkA))
		requireNoErr(t, err)
		return len(mustEncodeRecord(t, rec))
	}
	// The envelope around the payload is the same size near the limit.
	fit := record.DefaultMaxInput - (signedLen(record.DefaultMaxInput-1000) - (record.DefaultMaxInput - 1000))
	if got := signedLen(fit); got != record.DefaultMaxInput {
		t.Fatalf("the payload of %d bytes makes a record of %d bytes, want %d", fit, got, record.DefaultMaxInput)
	}
	requireNoErr(t, CheckControlReplySize(fit))
	requireErr(t, CheckControlReplySize(fit+1), ErrControlReplyTooLarge)
}
