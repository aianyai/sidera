package engine

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/adapterhost"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
)

// The release of the native session of a session endpoint that is closed.
//
// Closing an endpoint ends the session in the channel, by whichever way it is closed (the
// signal session.close, the owner, a creation that ends for an endpoint closed meanwhile,
// the disabling of the instance), and the native session may still be there. What the node
// owes is on the endpoint record: a closed session endpoint with a native session that is
// not recorded released (store.EndpointRecord.ReleasePending). The record is the whole
// state of the obligation, so a restart finds it again, and a record is changed only when
// the adapter answers closed: that answer, or the signal that was answered closed, is
// recorded with the endpoint (endpoint-update, or the transaction that closes it).
//
// The adapter is asked at once, and again after RetryBackoff when it did not answer, could
// not confirm or was not connected, and at every connection of its instance once it is in
// service. The host tells the node that an adapter started before it puts the connection in
// service, so a call made between the two finds it not connected, and no later connection
// comes to ask again. A
// close of a native session that is released or gone is answered closed, so asking again
// after a crash between an answer and its record is harmless. When it stays owed for
// Params.ControlReplyDeadline from the moment the node began, the owner gets one alert of
// the kind of a session nobody controls; the adapter is asked on.

// release is what the loop remembers of the release of one endpoint.
type release struct {
	// inflight says a call is with the adapter: its result schedules the next try, and no
	// second call is made beside it.
	inflight bool
}

// releasePending is the record of the endpoint when its native session is still owed.
func (e *Engine) releasePending(id ident.EndpointID) (store.EndpointRecord, bool) {
	st := e.view.endpoints[id]
	if st == nil || !st.rec.ReleasePending() {
		return store.EndpointRecord{}, false
	}
	return st.rec, true
}

// releasedRecord is the record of a closed endpoint whose native session was released.
func releasedRecord(rec store.EndpointRecord) store.EndpointRecord {
	s := *rec.Session
	s.Released = true
	rec.Session = &s
	return rec
}

// cmdRelease begins the release of an endpoint that was just recorded closed.
type cmdRelease struct{ endpoint ident.EndpointID }

func (c *cmdRelease) gate() gate { return gateAlways }
func (c *cmdRelease) fail(error) {}
func (c *cmdRelease) run(e *Engine, now time.Time) {
	if e.releases[c.endpoint] == nil {
		e.startRelease(now, c.endpoint)
	}
}

// startRelease asks the adapter to release the native session of the endpoint, if it is
// owed and the adapter is connected and no call is out. The first call for an endpoint
// also sets the time after which the owner is told.
func (e *Engine) startRelease(now time.Time, id ident.EndpointID) {
	rec, ok := e.releasePending(id)
	if !ok {
		e.forgetRelease(id)
		return
	}
	r := e.releases[id]
	if r == nil {
		r = &release{}
		e.releases[id] = r
		if !e.alertExists(releaseAlertID(rec)) {
			e.sched.set(schedKey{schedReleaseDeadline, id.String()}, now.Add(e.p.ControlReplyDeadline))
		}
	}
	if a := e.adapters[rec.Instance]; r.inflight || e.d.Adapters == nil || a == nil || !a.online {
		return // an adapter that is not online is not asked: its connection asks again
	}
	instance := rec.Instance
	params := adapterproto.CloseParams{Endpoint: adapterproto.EndpointRef(id.String()), NativeRef: adapterproto.NativeRef(rec.Session.NativeRef)}
	r.inflight = true
	if !e.spawn(func(ctx context.Context) {
		e.adapterCalls.Add(1)
		c := &cmdReleaseResult{call: newCall[struct{}](), endpoint: id}
		c.closed, c.err = e.d.Adapters.Close(ctx, instance, params)
		e.adapterCalls.Add(-1)
		_, _ = roundTrip(ctx, e, submitInternal, c, c.call)
	}) {
		r.inflight = false
	}
}

type cmdReleaseResult struct {
	call[struct{}]
	endpoint ident.EndpointID
	closed   adapterproto.CloseResult
	err      error
}

func (c *cmdReleaseResult) gate() gate { return gateAlways }
func (c *cmdReleaseResult) run(e *Engine, now time.Time) {
	e.releaseResult(now, c)
	c.done(struct{}{}, nil)
}

// releaseResult takes in what the call brought. A call that got no answer or found the
// adapter not connected, and a close the adapter could not confirm, are tried again after
// RetryBackoff. A close answered closed is recorded.
func (e *Engine) releaseResult(now time.Time, c *cmdReleaseResult) {
	if r := e.releases[c.endpoint]; r != nil {
		r.inflight = false
	}
	rec, ok := e.releasePending(c.endpoint)
	if !ok {
		e.forgetRelease(c.endpoint)
		return
	}
	again := schedKey{schedReleaseRetry, c.endpoint.String()}
	switch {
	case errors.Is(c.err, context.Canceled):
		return // the engine is stopping; the start finds the endpoint again
	case errors.Is(c.err, adapterhost.ErrNotConnected), errors.Is(c.err, peer.ErrClosed):
		// The connection is not in service yet, or closed with the call out; either way a
		// connection that is already made will not ask again. startRelease ends the chain
		// when the adapter is not online.
		e.log.Debug("the adapter is not connected; the release of a native session is tried again", "endpoint", c.endpoint.String())
		e.sched.set(again, now.Add(e.p.RetryBackoff))
		return
	case c.err != nil:
		e.log.Warn("a call to release the native session of a closed session got no answer; it is made again", "endpoint", c.endpoint.String(), "error", c.err.Error())
		e.sched.set(again, now.Add(e.p.RetryBackoff))
		return
	case c.closed.Outcome != adapterproto.CloseClosed:
		e.sched.set(again, now.Add(e.p.RetryBackoff))
		return
	}
	done := releasedRecord(rec)
	if err := e.d.Store.EndpointUpdate(done); err != nil {
		e.classify(err)
		if e.fatal == nil {
			e.log.Warn("the release of a native session could not be recorded; it is asked again", "endpoint", c.endpoint.String(), "error", err.Error())
			e.sched.set(again, now.Add(e.p.RetryBackoff))
		}
		return
	}
	e.putEndpoint(done, e.view.endpoints[c.endpoint].decl)
	e.forgetRelease(c.endpoint)
}

// releaseDeadline tells the owner, once, that a native session could not be released in
// the time a control signal has. While the wall clock is in doubt nothing is judged late,
// and it is looked at again.
func (e *Engine) releaseDeadline(now time.Time, id ident.EndpointID) {
	rec, ok := e.releasePending(id)
	if !ok {
		return
	}
	if e.paused {
		e.sched.set(schedKey{schedReleaseDeadline, id.String()}, now.Add(e.p.RetryBackoff))
		return
	}
	subject := id.String()
	if err := e.raiseAlertOnce(now, alertControlUnknown, rec.Name(), subject,
		"the native session of a closed session endpoint could not be released and may still exist (session endpoint "+subject+")"); err != nil {
		e.classify(err)
		if e.fatal == nil {
			e.log.Error("the alert of a native session that is not released could not be recorded; it is tried again", "endpoint", subject, "error", err.Error())
			e.sched.set(schedKey{schedReleaseDeadline, subject}, now.Add(e.p.RetryBackoff))
		}
	}
}

// releaseAlertID is the identifier of the alert of the release of the endpoint: one for
// the endpoint, however many times the node meets it owed.
func releaseAlertID(rec store.EndpointRecord) store.AlertID {
	return onceAlertID(alertControlUnknown, rec.Name())
}

// forgetRelease stops the work for an endpoint: its retry, its deadline and what the loop
// remembers.
func (e *Engine) forgetRelease(id ident.EndpointID) {
	e.sched.remove(schedKey{schedReleaseRetry, id.String()})
	e.sched.remove(schedKey{schedReleaseDeadline, id.String()})
	delete(e.releases, id)
}

// pendingReleases are the endpoints whose native session is owed, in the order of their
// identifiers; instance narrows them to one when it is not empty.
func (e *Engine) pendingReleases(instance string) []ident.EndpointID {
	var ids []ident.EndpointID
	for id, st := range e.view.endpoints {
		if st.rec.ReleasePending() && (instance == "" || st.rec.Instance == instance) {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	return ids
}

// redriveReleases asks again for every native session the instance owes: it has just
// connected.
func (e *Engine) redriveReleases(instance string, now time.Time) {
	for _, id := range e.pendingReleases(instance) {
		e.startRelease(now, id)
	}
}

// recoverReleases goes on with every release that was owed when the node went down
// (step 11): the time for the owner's alert starts again, and an adapter that is
// connected is asked.
func (e *Engine) recoverReleases(now time.Time) {
	e.redriveReleases("", now)
}
