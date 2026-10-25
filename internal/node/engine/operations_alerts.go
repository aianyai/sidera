package engine

import (
	"fmt"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/node/store"
)

// AcknowledgeAlertOp is the operation that marks an alert as seen by the owner.
// An alert that names a quarantined file of a directory that is not one of the
// deliveries has the file removed with the acknowledgement, before the alert is
// marked, so that an acknowledged alert never names a file that is still there;
// the quarantined files of deliveries stay for the sweep. An alert that was
// acknowledged before is written as it is and the result stored, so that a request
// under a new identifier is answered like the first. encode makes the result from
// the alert; it runs in the loop and must not wait.
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
	file := e.view.quarantinedFileOf(o.alert)
	if err := e.d.Store.AlertAck(store.AlertAckInput{Alert: alert, Result: x.record(result), Quarantined: file}); err != nil {
		return nil, err
	}
	e.view.alerts[i] = alert
	if file != nil {
		e.view.dropQuarantinedFile(*file)
	}
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

// quarantinedFileOf is the quarantined file the alert names, when it is one that
// the acknowledgement removes: not a file of the deliveries, which the sweep
// removes after their holding period.
func (v *view) quarantinedFileOf(id store.AlertID) *store.CorruptFile {
	for _, c := range v.corrupt {
		if c.Alert != id {
			continue
		}
		switch c.Dir {
		case store.OutDir, store.InDir, store.CtlDir:
			continue
		}
		return &c
	}
	return nil
}

func (v *view) dropQuarantinedFile(f store.CorruptFile) {
	for i, c := range v.corrupt {
		if c.Dir == f.Dir && c.Name == f.Name {
			v.corrupt = append(v.corrupt[:i], v.corrupt[i+1:]...)
			return
		}
	}
}
