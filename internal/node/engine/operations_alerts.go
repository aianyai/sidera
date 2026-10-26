package engine

import (
	"fmt"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/node/store"
)

// AcknowledgeAlertOp is the operation that marks an alert as seen by the owner.
// An alert that was acknowledged before is written as it is and the result
// stored, so that a request under a new identifier is answered like the first.
// encode makes the result from the alert; it runs in the loop and must not wait.
func AcknowledgeAlertOp(alert store.AlertID, encode func(store.AlertID) ([]byte, error)) Operation {
	return &acknowledgeAlertOp{alert: alert, encode: encode}
}

type acknowledgeAlertOp struct {
	alert  store.AlertID
	encode func(store.AlertID) ([]byte, error)
}

func (o *acknowledgeAlertOp) Admin() admin.Operation {
	return admin.Operation{Kind: admin.OpAcknowledgeAlert}
}
func (o *acknowledgeAlertOp) stops() bool { return false }
func (o *acknowledgeAlertOp) run(e *Engine, x *opRun) ([]byte, error) {
	i := e.view.alertIndex(o.alert)
	if i < 0 {
		return nil, refuse(ReasonAlertUnknown)
	}
	alert := e.view.alerts[i]
	if alert.Acked == nil {
		alert.Acked = &store.AlertAcked{At: x.now, Operation: x.key.ID}
	}
	result, err := o.encode(o.alert)
	if err != nil {
		return nil, fmt.Errorf("engine: the result of %s: %w", x.name, err)
	}
	if err := e.d.Store.AlertAck(store.AlertAckInput{Alert: alert, Result: x.record(result)}); err != nil {
		return nil, err
	}
	e.view.alerts[i] = alert
	return result, nil
}

// alertIndex is the position of the alert in the view, or -1.
func (v *view) alertIndex(id store.AlertID) int {
	for i, a := range v.alerts {
		if a.ID == id {
			return i
		}
	}
	return -1
}
