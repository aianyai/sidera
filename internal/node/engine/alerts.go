package engine

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"slices"
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
	return id, e.putAlert(store.AlertRecord{ID: id, Kind: kind, At: now, Subject: subject, Detail: detail})
}

// raiseAlertOnce raises the alert of a fact that belongs to one record, named by
// owner (the file name of the record): the identifier follows from the kind and the
// owner, so that coming to the same fact again, as every start does for a creation it
// cannot settle, finds the alert and raises no other. The alert that is there stays as
// it is, an acknowledgement of the owner's included.
func (e *Engine) raiseAlertOnce(now time.Time, kind, owner, subject, detail string) error {
	id := onceAlertID(kind, owner)
	if e.alertExists(id) {
		return nil
	}
	return e.putAlert(store.AlertRecord{ID: id, Kind: kind, At: now, Subject: subject, Detail: detail})
}

// onceAlertID is the identifier of the alert of a fact that belongs to one record: it
// follows from the kind and the owner, the one place it is made.
func onceAlertID(kind, owner string) store.AlertID {
	sum := sha256.Sum256([]byte(kind + "\x00" + owner))
	var id store.AlertID
	copy(id[:], sum[:])
	return id
}

// alertExists says whether the view holds the alert, acknowledged or not.
func (e *Engine) alertExists(id store.AlertID) bool {
	return slices.ContainsFunc(e.view.alerts, func(a store.AlertRecord) bool { return a.ID == id })
}

// putAlert writes an alert and takes it into the view.
func (e *Engine) putAlert(a store.AlertRecord) error {
	if err := e.d.Store.AlertRaise(a); err != nil {
		return err
	}
	e.view.alerts = append(e.view.alerts, a)
	return nil
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
