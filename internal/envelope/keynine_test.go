package envelope

import (
	"testing"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/record"
)

// TestStatusReportRefusesKeyNine: a status report has eight keys. Key 9 was once
// the cause of an unknown result; a target never reports a loss now, so a
// payload that carries it is refused like any other unknown key, whatever the
// state and the value, and no report is ever encoded with it.
func TestStatusReportRefusesKeyNine(t *testing.T) {
	verify := func(payload []byte) error {
		rec, err := record.Sign(StatusReportType, payload, signerOf(t, keyNetworkA))
		requireNoErr(t, err)
		_, err = VerifyStatusReport(rec, nodeOf(t, keyIdentityA), localNode(t), acceptedFor(t, keyIdentityA, keyNetworkA), t0.Add(3600e9))
		return err
	}
	for _, c := range []struct{ name, pairs, state, afterState, afterReported string }{
		{"accepted", "a6", "01", "", ""},
		{"delivered", "a7", "02", "", "071a6553f484"},
		{"rejected", "a7", "03", "0401", ""},
		{"delivery_unknown", "a6", "04", "", ""},
		{"closed_unconfirmed", "a6", "05", "", ""},
	} {
		without := string([]byte{c.pairs[0], c.pairs[1] - 1})
		t.Run(c.name, func(t *testing.T) {
			// the same payload without key 9 is valid, so the refusal is the key's alone
			requireNoErr(t, verify(reportPayload(without, c.state, c.afterState, c.afterReported, "")))
			for _, value := range []string{"01", "00", "02"} {
				requireErr(t, verify(reportPayload(c.pairs, c.state, c.afterState, c.afterReported, "09"+value)), record.ErrMalformed)
			}
		})
	}
	t.Run("a report of an unknown result has the state alone", func(t *testing.T) {
		rep := StatusReport{Sender: localNode(t), Message: "m", Recipient: ep(1), Status: delivery.StatusReport{State: delivery.StateDeliveryUnknown}, ReportedAt: t0}
		w, err := statusReportToWire(rep)
		requireNoErr(t, err)
		payload, err := record.MarshalCanonical(w)
		requireNoErr(t, err)
		requireNoErr(t, verify(payload))
		if payload[0] != 0xa5 {
			t.Errorf("the payload has %d pairs, want 5", payload[0]&0x1f)
		}
	})
}
