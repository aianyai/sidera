package delivery

import "testing"

var allStates = []State{
	StateQueued, StateAccepted, StateDelivered, StateRejected,
	StateExpiredUnconfirmed, StateDeliveryUnknown, StateClosedUnconfirmed,
}

func TestCanTransition(t *testing.T) {
	legal := map[State][]State{
		StateQueued:             {StateAccepted, StateDelivered, StateRejected, StateDeliveryUnknown, StateExpiredUnconfirmed, StateClosedUnconfirmed},
		StateExpiredUnconfirmed: {StateAccepted, StateDelivered, StateRejected, StateDeliveryUnknown, StateClosedUnconfirmed},
		StateAccepted:           {StateDelivered, StateRejected, StateDeliveryUnknown, StateClosedUnconfirmed},
		StateDeliveryUnknown:    {StateDelivered, StateClosedUnconfirmed},
	}
	for _, from := range allStates {
		for _, to := range allStates {
			want := false
			for _, l := range legal[from] {
				if l == to {
					want = true
				}
			}
			t.Run(from.String()+" to "+to.String(), func(t *testing.T) {
				if got := CanTransition(from, to); got != want {
					t.Errorf("CanTransition = %v, want %v", got, want)
				}
			})
		}
	}
}

func TestStateClasses(t *testing.T) {
	rows := []struct {
		rule        string
		state       State
		terminal    bool
		correctable bool
	}{
		{"queued is open", StateQueued, false, false},
		{"accepted is open", StateAccepted, false, false},
		{"delivered is final", StateDelivered, true, false},
		{"rejected is final", StateRejected, true, false},
		{"expired_unconfirmed is open and correctable", StateExpiredUnconfirmed, false, true},
		{"delivery_unknown is open and correctable", StateDeliveryUnknown, false, true},
		{"closed_unconfirmed is final", StateClosedUnconfirmed, true, false},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if r.state.IsTerminal() != r.terminal || r.state.IsCorrectable() != r.correctable {
				t.Errorf("terminal=%v correctable=%v", r.state.IsTerminal(), r.state.IsCorrectable())
			}
		})
	}
	t.Run("a final state has no outgoing transition", func(t *testing.T) {
		for _, s := range allStates {
			if !s.IsTerminal() {
				continue
			}
			for _, to := range allStates {
				if CanTransition(s, to) {
					t.Errorf("%s may move to %s", s, to)
				}
			}
		}
	})
}
