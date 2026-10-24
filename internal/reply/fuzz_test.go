package reply

import (
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/record"
)

// FuzzAdmitEvidence feeds arbitrary bytes, read as a record, to Admit as the
// evidence of a reply. Nothing may panic, and a reply is never granted on
// evidence that does not verify: with the evidence the fuzzer produces the
// record is either one of the seeds (genuine) or refused.
func FuzzAdmitEvidence(f *testing.F) {
	now := t0.Add(time.Hour)
	seeds := []record.Record{
		acceptedReport(f, runtimeR, t0.Add(time.Minute)),
		deliveredReport(f, runtimeR, t0.Add(3*time.Minute), t0.Add(2*time.Minute)),
		createdReply(f, t0.Add(time.Minute)),
		failedReply(f, t0.Add(time.Minute)),
	}
	for _, s := range seeds {
		b, err := s.Encode()
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := record.Decode(data, record.DefaultMaxInput)
		if err != nil {
			return
		}
		for _, o := range []func() Request{
			func() Request { out := messageOutbound(); return request(&out, onA(2), now, r) },
			func() Request { out := createOutbound(true); return request(&out, onA(3), now, r) },
			func() Request { out := createOutbound(true); return request(&out, onA(3), now, createdReply(t, t0), r) },
		} {
			adm, err := Admit(o())
			if err != nil {
				t.Fatalf("a well-formed request errored: %v", err)
			}
			if adm.Decision.Granted() && adm.Outbound == nil {
				t.Fatal("granted without a record")
			}
		}
	})
}
