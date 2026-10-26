package engine

import (
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// The adapter of an interrupt that was accepted before the node went down is admitted
// before the signals are redriven, and may report the end of a turn in between: the
// signal has no execution yet, and the end of the turn has nothing to settle.
func TestTurnEndedForAnInterruptThatHasNoExecutionSettlesNothing(t *testing.T) {
	session := endpointRec(t, 1, adapterreg.KindSession, store.EndpointOpen, "claude")
	c := ctlRec(t, 1, session.ID, delivery.StateAccepted)
	c.Kind = envelope.KindSessionInterrupt
	v, err := buildView(nodeOf(t, 7), &store.Scan{Endpoints: []store.EndpointRecord{session}, Ctl: []store.CtlRecord{c}})
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{view: v, executions: map[store.TargetKey]*execution{}}
	e.interruptTurnEnded(time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC), adapterproto.EndpointRef(session.ID.String()))
	if len(e.executions) != 0 {
		t.Errorf("the end of a turn made %d executions", len(e.executions))
	}
}
