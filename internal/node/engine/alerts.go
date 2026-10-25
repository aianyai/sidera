package engine

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/aianyai/sidera/internal/node/store"
)

type cmdRaiseAlert struct {
	call[store.AlertID]
	kind, subject, detail string
}

func (c *cmdRaiseAlert) gate() gate { return gateAlways }
func (c *cmdRaiseAlert) run(e *Engine, now time.Time) {
	id, err := e.raiseAlert(now, c.kind, c.subject, c.detail)
	c.done(id, e.classify(err))
}

// RaiseAlert records an alert for the owner: a fact that wants attention and does
// not stop the node. Kind is a name of the form [a-z][a-z0-9_]{0,63}, subject
// says what it is about, detail says more. The alert is on disk before this
// returns. Neither may hold a message body, a token or a key.
func (e *Engine) RaiseAlert(ctx context.Context, kind, subject, detail string) (store.AlertID, error) {
	c := &cmdRaiseAlert{call: newCall[store.AlertID](), kind: kind, subject: subject, detail: detail}
	return roundTrip(ctx, e, submitInternal, c, c.call)
}

func (e *Engine) raiseAlert(now time.Time, kind, subject, detail string) (store.AlertID, error) {
	var id store.AlertID
	if _, err := io.ReadFull(e.randomReader(), id[:]); err != nil {
		return store.AlertID{}, err
	}
	if id == (store.AlertID{}) {
		return store.AlertID{}, errors.New("engine: the random source gave an alert identifier of zero bytes")
	}
	a := store.AlertRecord{ID: id, Kind: kind, At: now, Subject: subject, Detail: detail}
	if err := e.d.Store.AlertRaise(a); err != nil {
		return store.AlertID{}, err
	}
	e.view.alerts = append(e.view.alerts, a)
	return id, nil
}

type cmdAlerts struct{ call[[]store.AlertRecord] }

func (c *cmdAlerts) gate() gate { return gateRead }
func (c *cmdAlerts) run(e *Engine, _ time.Time) {
	c.done(append([]store.AlertRecord(nil), e.view.alerts...), nil)
}

// Alerts lists the alerts of the node, the acknowledged ones included, oldest
// first.
func (e *Engine) Alerts(ctx context.Context) ([]store.AlertRecord, error) {
	c := &cmdAlerts{call: newCall[[]store.AlertRecord]()}
	return roundTrip(ctx, e, submitExternal, c, c.call)
}
