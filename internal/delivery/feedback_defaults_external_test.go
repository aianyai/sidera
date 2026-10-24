package delivery_test

import (
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/handoff"
)

func TestDefaultFeedbackParamsAgainstHandoff(t *testing.T) {
	p := delivery.DefaultFeedbackParams()
	t.Run("the cap per hour is no more than the prompts an endpoint may be given per hour", func(t *testing.T) {
		perHour := int(time.Hour/p.RateWindow) * p.RateCap
		if perHour > handoff.DefaultMaxPerHour {
			t.Fatalf("%d notices per hour exceed the prompt cap of %d per hour", perHour, handoff.DefaultMaxPerHour)
		}
	})
}
