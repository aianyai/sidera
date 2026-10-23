package delivery

import (
	"strings"
	"testing"
)

func TestTransitionFeedback(t *testing.T) {
	none := KindSet(0)
	rows := []struct {
		rule     string
		from, to State
		reported KindSet
		want     []Kind
	}{
		{"queued to accepted is normal progress", StateQueued, StateAccepted, none, nil},
		{"queued to delivered is normal progress", StateQueued, StateDelivered, none, nil},
		{"accepted to delivered is normal progress", StateAccepted, StateDelivered, none, nil},
		{"queued to rejected reports the rejection", StateQueued, StateRejected, none, []Kind{KindRejected}},
		{"accepted to rejected reports the rejection", StateAccepted, StateRejected, none, []Kind{KindRejected}},
		{"accepted to delivery_unknown reports it", StateAccepted, StateDeliveryUnknown, none, []Kind{KindDeliveryUnknown}},
		{"queued to delivery_unknown reports it", StateQueued, StateDeliveryUnknown, none, []Kind{KindDeliveryUnknown}},
		{"queued to expired_unconfirmed reports it", StateQueued, StateExpiredUnconfirmed, none, []Kind{KindExpiredUnconfirmed}},
		{"expired_unconfirmed to accepted is corrected", StateExpiredUnconfirmed, StateAccepted, none, []Kind{KindExpiredCorrected}},
		{"expired_unconfirmed to delivered is corrected", StateExpiredUnconfirmed, StateDelivered, none, []Kind{KindExpiredCorrected}},
		{"expired_unconfirmed to rejected is the new news, not a correction", StateExpiredUnconfirmed, StateRejected, none, []Kind{KindRejected}},
		{"expired_unconfirmed to delivery_unknown is the new news", StateExpiredUnconfirmed, StateDeliveryUnknown, none, []Kind{KindDeliveryUnknown}},
		{"delivery_unknown to delivered is corrected", StateDeliveryUnknown, StateDelivered, none, []Kind{KindUnknownCorrected}},
		{"closed_unconfirmed from queued is silent", StateQueued, StateClosedUnconfirmed, none, nil},
		{"closed_unconfirmed from expired is silent", StateExpiredUnconfirmed, StateClosedUnconfirmed, none.With(KindExpiredUnconfirmed), nil},
		{"closed_unconfirmed from delivery_unknown is silent", StateDeliveryUnknown, StateClosedUnconfirmed, none.With(KindDeliveryUnknown), nil},
		{"a rejection already reported is not reported again", StateQueued, StateRejected, none.With(KindRejected), nil},
		{"an expiry already reported is not reported again", StateQueued, StateExpiredUnconfirmed, none.With(KindExpiredUnconfirmed), nil},
		{"a correction already reported is not reported again", StateExpiredUnconfirmed, StateDelivered, none.With(KindExpiredCorrected), nil},
		{"a reported block that was never recovered is recovered by delivery", StateAccepted, StateDelivered, none.With(KindDeliveryBlocked), []Kind{KindBlockedRecovered}},
		{"a recovered block is not recovered again", StateAccepted, StateDelivered, none.With(KindDeliveryBlocked).With(KindBlockedRecovered), nil},
		{"a correction and a block recovery come together", StateDeliveryUnknown, StateDelivered, none.With(KindDeliveryBlocked), []Kind{KindUnknownCorrected, KindBlockedRecovered}},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			got := TransitionFeedback(r.from, r.to, r.reported)
			if !sameKinds(got, r.want) {
				t.Errorf("got %v, want %v", got, r.want)
			}
		})
	}
}

func TestSummaries(t *testing.T) {
	t.Run("every kind has a summary", func(t *testing.T) {
		for k := KindUnreachable; k <= KindUnknownCorrected; k++ {
			if k.Summary() == "" || k.String() == "invalid" {
				t.Errorf("kind %d has no summary or name", k)
			}
		}
	})
	t.Run("expiry wording says the target may have received the message", func(t *testing.T) {
		s := KindExpiredUnconfirmed.Summary()
		if !strings.Contains(s, "may have received") {
			t.Errorf("summary %q does not warn that the message may have arrived", s)
		}
	})
	t.Run("rejection wording says it was not delivered", func(t *testing.T) {
		if !strings.Contains(KindRejected.Summary(), "not delivered") {
			t.Errorf("summary %q", KindRejected.Summary())
		}
	})
}
