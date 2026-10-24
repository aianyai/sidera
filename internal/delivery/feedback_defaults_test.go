package delivery

import (
	"testing"
	"time"
)

func TestDefaultFeedbackParamsValidate(t *testing.T) {
	if err := DefaultFeedbackParams().Validate(); err != nil {
		t.Fatalf("the defaults fail their own Validate: %v", err)
	}
}

func TestDefaultFeedbackParamsValues(t *testing.T) {
	p := DefaultFeedbackParams()
	rows := []struct {
		rule string
		got  any
		want any
	}{
		{"merge window is 1 minute: a burst of 20 notices within seconds becomes one, and the unreachable notice still arrives in 3 minutes", p.MergeWindow, time.Minute},
		{"rate cap is 10 notices per window: one sender rarely meets more than 10 kinds of trouble in 10 minutes", p.RateCap, 10},
		{"rate window is 10 minutes: 10 notices per 10 minutes is 60 an hour", p.RateWindow, 10 * time.Minute},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if r.got != r.want {
				t.Fatalf("got %v, want %v", r.got, r.want)
			}
		})
	}
}

func TestDefaultFeedbackParamsReasons(t *testing.T) {
	p := DefaultFeedbackParams()
	// The most notices that can be waiting in a sender's queue during one
	// endpoint queue time: whole rate windows, each releasing RateCap.
	inQueueTime := int(DefaultEndpointMaxQueue/p.RateWindow) * p.RateCap
	rows := []struct {
		rule string
		ok   bool
	}{
		{"an unreachable notice reaches the sender within 3 minutes of the first failure",
			UnreachableHintAfter+p.MergeWindow == 3*time.Minute},
		{"an unreachable notice arrives well before the default delivery deadline",
			UnreachableHintAfter+p.MergeWindow < DefaultAcceptBefore},
		{"within one queue time at most 30 notices wait in the sender's queue",
			inQueueTime == 30},
		{"the notices a sender can receive within one queue time stay below the endpoint backlog limit",
			inQueueTime < DefaultEndpointBacklogLimit},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if !r.ok {
				t.Fatalf("does not hold for %+v", p)
			}
		})
	}
}
