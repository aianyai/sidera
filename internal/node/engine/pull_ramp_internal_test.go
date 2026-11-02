package engine

import (
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
)

// The pulls for the reply to a control signal come on the short waits of the ramp, one
// wait for each pull, and then every pull interval; the waits of a message, and of the
// delivery a creation derives from its initial message, are always the interval.
func TestPullDelayRampsOnlyForTheDeliveryOfAControlSignal(t *testing.T) {
	e := &Engine{p: DefaultParams()}
	endpoint := func(n byte) ident.Address {
		id, err := ident.EndpointIDFromBytes(append([]byte{n}, make([]byte, ident.EndpointIDLen-1)...))
		if err != nil {
			t.Fatal(err)
		}
		return ident.Address{Endpoint: id}
	}
	signalTo, derivedTo := endpoint(1), endpoint(2)
	control := envelope.Outbound{Kind: envelope.KindSessionCreate, Recipients: []envelope.Tracked{{Address: signalTo}}, Derived: &envelope.Tracked{Address: derivedTo}}
	message := envelope.Outbound{Kind: envelope.KindMessage, Recipients: []envelope.Tracked{{Address: signalTo}}}

	var st outState
	var got []time.Duration
	for range 5 {
		got = append(got, e.pullDelay(control, control.Recipients[0], &st))
	}
	p := e.p
	want := []time.Duration{p.ControlPullRamp[0], p.ControlPullRamp[1], p.ControlPullRamp[2], p.PullInterval, p.PullInterval}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("waits of the pulls for a control reply are %v, want %v", got, want)
		}
	}

	st = outState{}
	if d := e.pullDelay(control, *control.Derived, &st); d != p.PullInterval || st.rampPulls != 0 {
		t.Errorf("the derived delivery waits %v (ramp used %d), want %v", d, st.rampPulls, p.PullInterval)
	}
	if d := e.pullDelay(message, message.Recipients[0], &st); d != p.PullInterval || st.rampPulls != 0 {
		t.Errorf("a message waits %v (ramp used %d), want %v", d, st.rampPulls, p.PullInterval)
	}

	// A wait of the ramp longer than the interval is the interval.
	e.p.PullInterval = time.Second
	if d := e.pullDelay(control, control.Recipients[0], &outState{}); d != time.Second {
		t.Errorf("a ramp wait beyond the pull interval is %v, want the interval", d)
	}
}
