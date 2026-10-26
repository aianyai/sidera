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
		{"closing from queued reports the expiry the sender was never told", StateQueued, StateClosedUnconfirmed, none, []Kind{KindExpiredUnconfirmed}},
		{"closing from queued after the unreachable hint still reports the expiry", StateQueued, StateClosedUnconfirmed, none.With(KindUnreachable), []Kind{KindExpiredUnconfirmed}},
		{"closing from accepted reports that the result could not be learned", StateAccepted, StateClosedUnconfirmed, none, []Kind{KindAcceptedUnconfirmed}},
		{"closing from accepted while blocked reports it too", StateAccepted, StateClosedUnconfirmed, none.With(KindDeliveryBlocked), []Kind{KindAcceptedUnconfirmed}},
		{"closing from accepted after a corrected expiry reports it too", StateAccepted, StateClosedUnconfirmed, none.With(KindExpiredUnconfirmed).With(KindExpiredCorrected), []Kind{KindAcceptedUnconfirmed}},
		{"closing from accepted is not reported twice", StateAccepted, StateClosedUnconfirmed, none.With(KindAcceptedUnconfirmed), nil},
		{"closing from expired is silent: the sender was told", StateExpiredUnconfirmed, StateClosedUnconfirmed, none.With(KindExpiredUnconfirmed), nil},
		{"closing from delivery_unknown is silent: the sender was told", StateDeliveryUnknown, StateClosedUnconfirmed, none.With(KindDeliveryUnknown), nil},
		{"a rejection already reported is not reported again", StateQueued, StateRejected, none.With(KindRejected), nil},
		{"an expiry already reported is not reported again", StateQueued, StateExpiredUnconfirmed, none.With(KindExpiredUnconfirmed), nil},
		{"a correction already reported is not reported again", StateExpiredUnconfirmed, StateDelivered, none.With(KindExpiredCorrected), nil},
		{"a reported block that was never recovered is recovered by delivery", StateAccepted, StateDelivered, none.With(KindDeliveryBlocked), []Kind{KindBlockedRecovered}},
		{"a recovered block is not recovered again", StateAccepted, StateDelivered, none.With(KindDeliveryBlocked).With(KindBlockedRecovered), nil},
		{"a correction and a block recovery come together", StateDeliveryUnknown, StateDelivered, none.With(KindDeliveryBlocked), []Kind{KindUnknownCorrected, KindBlockedRecovered}},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			got := TransitionFeedback(r.from, r.to, UnknownHandedOver, r.reported)
			if !sameKinds(got, r.want) {
				t.Errorf("got %v, want %v", got, r.want)
			}
		})
	}
}

func TestSummaries(t *testing.T) {
	t.Run("every kind has a summary", func(t *testing.T) {
		for k := KindUnreachable; k <= LastRecordKind; k++ {
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
	t.Run("delivery_unknown wording holds for a message and a control signal and forbids assuming non-delivery", func(t *testing.T) {
		s := KindDeliveryUnknown.Summary()
		for _, want := range []string{"handed it over", "agent", "control signal", "adapter", "seen or executed is unknown", "do not assume it was not delivered"} {
			if !strings.Contains(s, want) {
				t.Errorf("summary %q lacks %q", s, want)
			}
		}
	})
	t.Run("accepted_unconfirmed wording says the target accepted it, that the result is unknown and forbids assuming non-delivery", func(t *testing.T) {
		s := KindAcceptedUnconfirmed.Summary()
		for _, want := range []string{"accepted the message", "could not be learned", "unknown", "do not assume it was not delivered"} {
			if !strings.Contains(s, want) {
				t.Errorf("summary %q lacks %q", s, want)
			}
		}
	})
	t.Run("rejection wording says it was not delivered", func(t *testing.T) {
		if !strings.Contains(KindRejected.Summary(), "not delivered") {
			t.Errorf("summary %q", KindRejected.Summary())
		}
	})
}
