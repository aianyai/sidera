package transport_test

import (
	"testing"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
)

// FuzzSendWithKnobsArmed sends arbitrary bytes through a loopback that holds the
// reports of every id the input could carry and replays it. The loopback decodes
// the bytes itself to find the id and to rebuild the envelope, so this is its one
// reader of untrusted input: it must not panic, and when it does replay, the
// replay is the same id with other content.
func FuzzSendWithKnobsArmed(f *testing.F) {
	r := newRig(f)
	f.Add(r.message(f, "m1", "hello"))
	f.Add(r.status(f, "s1"))
	f.Add([]byte{})
	f.Add([]byte("not an envelope"))
	f.Fuzz(func(t *testing.T, data []byte) {
		r := newRig(t)
		var id delivery.MessageID
		if e, err := envelope.Decode(data, envelope.DefaultLimits()); err == nil {
			id = e.ID
		}
		r.knobs.HoldReports(id)
		r.knobs.Mutate(id)
		_, _ = r.send(data)
		calls := r.target.inbound()
		if len(calls) == 2 && (calls[0].env.ID != calls[1].env.ID || calls[0].env.Digest() == calls[1].env.Digest()) {
			t.Fatalf("replay is %q with digest equal=%v, want the same id with other content", calls[1].env.ID, calls[0].env.Digest() == calls[1].env.Digest())
		}
	})
}
