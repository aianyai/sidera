package store

import (
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/handoff"
)

// ControlEffect is the endpoint change authorized by a committed control result.
// A creation with an initial message also settles its first prompt before the
// endpoint opens.
type ControlEffect struct {
	EndpointRecord
	promptAt time.Time
	initial  *InRecord
}

// CreationEffect builds the effect of a creation's result. Only a confirmed
// creation that opens its endpoint counts a first prompt; a closed endpoint
// merely records the native session it must release.
func CreationEffect(c CtlRecord, endpoint EndpointRecord, initial *InRecord) ControlEffect {
	e := ControlEffect{EndpointRecord: endpoint}
	if c.Initial != nil && c.Intent != nil && c.Intent.State == IntentCreated && endpoint.Status == EndpointOpen {
		e.promptAt, e.initial = c.Record.DeliveredAt(), initial
	}
	return e
}

// PromptRecords returns the initial prompt's complete records for a writer and
// its in-memory view. A missing or terminal delivery has nothing left to notify,
// as for any prompt whose covered delivery was lost or settled meanwhile.
func (e ControlEffect) PromptRecords() (*AttemptsRecord, []InRecord, error) {
	if e.promptAt.IsZero() {
		return nil, nil, nil
	}
	head := &AttemptsRecord{Endpoint: e.ID, Attempts: []handoff.Attempt{{
		At: e.promptAt, Outcome: handoff.AttemptDelivered, Kind: handoff.PromptKindFirst,
	}}}
	if e.initial == nil {
		return head, nil, nil
	}
	in := *e.initial
	rec, out, err := in.Record.Apply(e.promptAt, delivery.Event{Type: delivery.EventNotified})
	if err != nil {
		return nil, nil, err
	}
	if out.Result != delivery.ResultApplied {
		return head, nil, nil
	}
	in.Record = rec
	return head, []InRecord{in}, nil
}
