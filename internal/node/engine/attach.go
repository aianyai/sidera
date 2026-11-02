package engine

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/adapterhost"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/publication"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// The attachment of a session a person opened. The owner's request makes (or finds)
// a session endpoint for a native session the node did not create, gives the session
// its token, and asks the adapter to take hold of it, so that prompts can be delivered
// to a session someone else runs.
//
// The endpoint and its token are written before the adapter is asked, in one command
// of the loop; the call to the adapter is made after it, on the caller's goroutine,
// and nothing is written for its result. What the adapter answers is only whether
// prompts can be delivered now, which the reply to the owner carries as the wake
// state. The endpoint and the token are returned whatever that is: the session may
// start a tool with the token before the adapter can reach it, and the same request
// repeated later asks the adapter again.
//
// The wake information the owner passes (how an adapter reaches the running session)
// is opaque to the node. It is held in memory only, in attached, for the open
// endpoints, and is passed to the adapter at every adapter.start and every attach; it
// is never written to the disk or to a log, and no error names a value of it. After a
// restart of the node an entry holds none, until the owner repeats the attachment.
//
// Each attachment is also the heartbeat of the session's controller: a controller
// repeats it while it runs. An endpoint whose controller has not attached for the
// endpoint's queue time (never less than attachedMinWindow), counted in the time the
// node was awake (awake.go), and that has nothing queued or leased, is closed the way
// the owner's close does it. An open
// session that is idle for days keeps attaching and is never closed by this.

// attachedSession is what the loop remembers of an open attached endpoint besides its
// record. It is memory only.
type attachedSession struct {
	// wake is the wake information the last attachment carried; nil for none.
	wake adapterproto.WakeInfo
	// lastAttach is the awake time (Engine.awake) of the last attachment; for an
	// endpoint seeded at the start, the start.
	lastAttach time.Duration
}

// attachedHeldRecheck is how long a controller that has been gone for the queue time
// but whose endpoint still holds queued or leased messages waits before the endpoint is
// looked at again: the messages expire under the delivery rules, and the endpoint is
// closed once none is left.
const attachedHeldRecheck = time.Minute

// attachedMinWindow is the least time an attached endpoint's controller may be gone
// before the endpoint is closed, whatever its queue time: three beats of the
// controller's heartbeat, which repeats the attachment every minute. A live controller
// whose beat is a little late (a busy machine, a stalled call) must never lose its
// endpoint, which a queue time set shorter than that would otherwise do.
const attachedMinWindow = 3 * time.Minute

// attachedWindow is how long the controller of the endpoint may be gone before the
// endpoint is closed: its queue time, and never less than attachedMinWindow.
func (e *Engine) attachedWindow(rec store.EndpointRecord) time.Duration {
	return max(e.d.Config.EndpointSettings(rec.Instance, rec.ID).MaxQueue, attachedMinWindow)
}

func attachedAbsenceKey(id ident.EndpointID) schedKey {
	return schedKey{schedAttachedAbsence, id.String()}
}

// AttachRequest is the owner's request to attach a session a person opened.
type AttachRequest struct {
	// Adapter is the identifier of the adapter that runs the session's runtime.
	Adapter string
	// Instance is the enabled instance of the adapter to attach to; empty to take the
	// only enabled instance of the adapter.
	Instance string
	// NativeRef is the adapter's reference to the session.
	NativeRef string
	// DisplayName names the endpoint in the directory; empty leaves the name as it
	// is, or none for a new endpoint.
	DisplayName string
	// Wake is the wake information for the adapter; nil for none. It replaces the one
	// held for the session.
	Wake adapterproto.WakeInfo
	// FindOnly asks only for the open attached endpoint the session already has. When
	// there is one the request is the same as without it; when there is none nothing is
	// made and the request is refused as ReasonSessionNotAttached.
	FindOnly bool
}

// The wake states of an attachment: what the adapter said to the request to attach.
const (
	// WakeReady: the adapter holds the session and can deliver prompts to it.
	WakeReady = "ready"
	// WakeUnavailable: the adapter answered, and prompts cannot be delivered.
	WakeUnavailable = "unavailable"
	// WakeUnknown: the adapter was not asked or did not answer.
	WakeUnknown = "unknown"
)

// The reasons a wake state gives that the adapter did not: a session the runtime does
// not have, an adapter that is not connected, and a call that got no usable answer.
const (
	WakeReasonNativeNotFound = "native_not_found"
	WakeReasonAdapterOffline = "adapter_offline"
	WakeReasonNoAnswer       = "no_answer"
)

// WakeReport is the state of the delivery to an attached session.
type WakeReport struct {
	// State is WakeReady, WakeUnavailable or WakeUnknown.
	State string
	// Reason says why, for the states that are not ready: the adapter's own reason of a
	// failure or one of the WakeReason names.
	Reason string
	// Detail is the adapter's remark, when it made one.
	Detail string
}

// AttachResult is the answer to an attachment.
type AttachResult struct {
	Endpoint ident.Address
	// Instance is the instance the session is attached to.
	Instance string
	// Token is the token of the endpoint as the environment of a session carries it.
	Token string
	Wake  WakeReport
}

// attachStep is what the command of the loop leaves for the call to the adapter.
type attachStep struct {
	id       ident.EndpointID
	instance string
	// token is the token text of the endpoint, made in the loop from the record.
	token string
	// inService says the adapter of the instance is connected and takes calls.
	inService bool
	params    adapterproto.SessionAttachParams
}

type cmdAttach struct {
	call[attachStep]
	req AttachRequest
}

func (c *cmdAttach) gate() gate { return gateWrite }
func (c *cmdAttach) run(e *Engine, now time.Time) {
	step, err := e.attach(now, c.req)
	c.done(step, e.classify(err))
}

// AttachSession attaches a session a person opened to the adapter instance, as the
// owner asked: the endpoint of the session, found or made, with its token, and the
// state of the wake. The same session attached again (the same instance and native
// session, while its endpoint is open) is the same endpoint with the same token; the
// request replaces the wake information held and the display name when one is given.
// An endpoint that is closed is never opened again: a later attachment makes a new
// one. With FindOnly a session that has no open endpoint is refused and nothing is
// made.
func (e *Engine) AttachSession(ctx context.Context, r AttachRequest) (AttachResult, error) {
	c := &cmdAttach{call: newCall[attachStep](), req: r}
	step, err := roundTrip(ctx, e, submitExternal, c, c.call)
	if err != nil {
		return AttachResult{}, err
	}
	return AttachResult{
		Endpoint: ident.Address{Node: e.d.Node, Endpoint: step.id},
		Instance: step.instance,
		Token:    step.token,
		Wake:     e.askToAttach(ctx, step),
	}, nil
}

func (e *Engine) attach(now time.Time, r AttachRequest) (attachStep, error) {
	var none attachStep
	if err := validateAttachRequest(r); err != nil {
		return none, err
	}
	inst, err := e.attachInstance(r)
	if err != nil {
		return none, err
	}
	// An instance not admitted since the engine started has not told what it can do
	// yet: it is allowed, and its first admission closes what it cannot take.
	if adm, ok := e.admission(inst); ok && !adm.Attaches() {
		return none, refuse(ReasonAttachUnsupported)
	}
	if e.createdSessionHolds(r.NativeRef) {
		return none, refuse(ReasonNativeSessionCreated)
	}

	var rec store.EndpointRecord
	if st := e.openAttached(inst, r.NativeRef); st != nil {
		rec = st.rec
		if r.DisplayName != "" && r.DisplayName != rec.DisplayName {
			rec.DisplayName = r.DisplayName
			if err := e.d.Store.EndpointUpdate(rec); err != nil {
				return none, err
			}
			e.putEndpoint(rec, st.decl)
		}
	} else {
		if r.FindOnly {
			return none, refuse(ReasonSessionNotAttached)
		}
		if e.view.OpenEndpoints() >= publication.MaxEndpointsPerNode {
			return none, &Refusal{Reason: ReasonEndpointLimit, Limit: LimitNode}
		}
		if rec, err = e.newAttachedEndpoint(inst, r); err != nil {
			return none, err
		}
		if err := e.d.Store.EndpointUpdate(rec); err != nil {
			return none, err
		}
		e.putEndpoint(rec, authz.Declaration{})
	}

	var wake adapterproto.WakeInfo
	if len(r.Wake) > 0 {
		wake = maps.Clone(r.Wake)
	}
	e.attached[rec.ID] = attachedSession{wake: wake, lastAttach: e.awake.total}
	e.armAttachedAbsence(now, rec)
	e.noteSessionAttached(rec.ID)

	a := e.adapters[inst]
	return attachStep{
		id: rec.ID, instance: inst, token: sessionTokenText(rec.Token),
		inService: e.d.Adapters != nil && a != nil && a.online && a.inService,
		params: adapterproto.SessionAttachParams{
			Endpoint: adapterproto.EndpointRef(rec.ID.String()), NativeRef: adapterproto.NativeRef(r.NativeRef), Wake: maps.Clone(wake),
		},
	}, nil
}

// validateAttachRequest checks the values of the request. The refusals name the field
// and, for the wake information, the key; never a value.
func validateAttachRequest(r AttachRequest) error {
	if err := adapterproto.ValidateAdapterID(r.Adapter); err != nil {
		return invalidArgument("adapter", err)
	}
	if err := adapterproto.NativeRef(r.NativeRef).Validate(); err != nil {
		return invalidArgument("native_ref", err)
	}
	if n := utf8.RuneCountInString(r.DisplayName); n > adapterproto.MaxDisplayName {
		return invalidArgument("display_name", fmt.Errorf("%d characters, at most %d", n, adapterproto.MaxDisplayName))
	}
	if len(r.Wake) > 0 {
		if err := r.Wake.Validate(); err != nil {
			var ve *adapterproto.ValidationError
			field := "wake"
			if errors.As(err, &ve) && ve.Field != "" {
				field += "." + ve.Field
			}
			return invalidArgument(field, errors.New("the wake information is not valid"))
		}
	}
	return nil
}

// attachInstance is the instance the session is attached to: the one the request
// names, or the only enabled one of the adapter.
func (e *Engine) attachInstance(r AttachRequest) (string, error) {
	items := e.d.Config.Instances().Items
	if r.Instance != "" {
		in, ok := items[r.Instance]
		switch {
		case !ok || !in.Enabled:
			return "", refuse(ReasonInstanceUnknown)
		case in.Adapter != r.Adapter:
			return "", invalidArgument("instance", errors.New("the instance runs another adapter"))
		}
		return r.Instance, nil
	}
	var names []string
	for name, in := range items {
		if in.Enabled && in.Adapter == r.Adapter {
			names = append(names, name)
		}
	}
	switch len(names) {
	case 0:
		return "", refuse(ReasonInstanceUnknown)
	case 1:
		return names[0], nil
	}
	return "", &Refusal{Reason: ReasonInvalidArgument, Field: "instance", Detail: DetailInstanceRequired}
}

// createdSessionHolds says a session endpoint this node created holds the native
// session: it is open, or closed with its release still owed. Such a session is under
// the node's control and is not one a person opened.
func (e *Engine) createdSessionHolds(ref string) bool {
	for _, st := range e.view.endpoints {
		rec := st.rec
		if rec.Kind == adapterreg.KindSession && rec.Origin() == adapterreg.OriginCreated && rec.Session != nil && rec.Session.NativeRef == ref &&
			(rec.Status == store.EndpointOpen || rec.ReleasePending()) {
			return true
		}
	}
	return false
}

// openAttached is the open attached endpoint of the instance for the native session.
func (e *Engine) openAttached(instance, ref string) *endpointState {
	for _, st := range e.view.endpoints {
		rec := st.rec
		if rec.Kind == adapterreg.KindSession && rec.Instance == instance && rec.Status == store.EndpointOpen &&
			rec.Origin() == adapterreg.OriginAttached && rec.Session.NativeRef == ref {
			return st
		}
	}
	return nil
}

// newAttachedEndpoint is the record of a new attached endpoint: open from the start,
// with its token, and mapped to the native session.
func (e *Engine) newAttachedEndpoint(instance string, r AttachRequest) (store.EndpointRecord, error) {
	rec, err := e.freshSessionEndpoint(instance)
	if err != nil {
		return store.EndpointRecord{}, err
	}
	rec.DisplayName, rec.Status = r.DisplayName, store.EndpointOpen
	rec.Session = &store.SessionInfo{Origin: adapterreg.OriginAttached, NativeRef: r.NativeRef}
	return rec, nil
}

// askToAttach asks the adapter to take the session, waiting for it at most
// Params.AttachWait, and words the answer as a wake state. It runs on the caller's
// goroutine, outside the loop. The wait is on the real clock, as the wait of a
// submitter is: it is the time a person waits for an answer.
func (e *Engine) askToAttach(ctx context.Context, s attachStep) WakeReport {
	if !s.inService {
		return WakeReport{State: WakeUnknown, Reason: WakeReasonAdapterOffline}
	}
	ctx, cancel := context.WithTimeout(ctx, e.p.AttachWait)
	defer cancel()
	res, err := e.d.Adapters.Attach(ctx, s.instance, s.params)
	switch {
	case errors.Is(err, adapterhost.ErrNotConnected):
		return WakeReport{State: WakeUnknown, Reason: WakeReasonAdapterOffline}
	case err != nil:
		return WakeReport{State: WakeUnknown, Reason: WakeReasonNoAnswer}
	}
	switch res.Outcome {
	case adapterproto.SessionAttachAttached:
		return WakeReport{State: WakeReady}
	case adapterproto.SessionAttachNotFound:
		return WakeReport{State: WakeUnavailable, Reason: WakeReasonNativeNotFound, Detail: res.Detail}
	case adapterproto.SessionAttachFailed:
		return WakeReport{State: WakeUnavailable, Reason: string(res.Reason), Detail: res.Detail}
	}
	return WakeReport{State: WakeUnknown, Reason: WakeReasonNoAnswer}
}

// seedAttached gives every open attached endpoint of the view its entry in attached,
// without wake information, when the engine starts: the wake information is memory
// only and is lost with the process, but the endpoint is still attached, and its
// close must still tell the adapter. The start counts as the last attachment, so a
// controller that kept running through the restart has a whole window to attach again.
func (e *Engine) seedAttached(now time.Time) {
	var open []ident.EndpointID
	for id, st := range e.view.endpoints {
		if st.rec.Kind == adapterreg.KindSession && st.rec.Status == store.EndpointOpen && st.rec.Origin() == adapterreg.OriginAttached {
			open = append(open, id)
		}
	}
	// Endpoints with the same window are due together: the order they are armed in is
	// the order the schedule breaks the tie in, and it is kept fixed.
	sort.Slice(open, func(i, j int) bool { return open[i].String() < open[j].String() })
	for _, id := range open {
		e.attached[id] = attachedSession{lastAttach: e.awake.total}
		e.armAttachedAbsence(now, e.view.endpoints[id].rec)
	}
}

// armAttachedAbsence schedules the first look at whether the controller of the
// endpoint is gone: a window from now. Awake time is a lower bound of the wall time,
// unless the wall clock was set back, so the controller cannot have been gone for the
// window sooner than this.
func (e *Engine) armAttachedAbsence(now time.Time, rec store.EndpointRecord) {
	e.sched.set(attachedAbsenceKey(rec.ID), now.Add(e.attachedWindow(rec)))
}

// attachedAbsenceDue judges an endpoint whose look came due. A controller gone for
// less than the window in awake time (the node slept, or the setting changed) is
// looked for again when the rest of it could have passed. Once it has been gone for
// the window the endpoint is closed as the owner's close does, but only when nothing
// is queued or leased on it: those messages get their say first, and the endpoint is
// looked at again while they are there. Outside the running phase nothing is closed
// (a command that closes is refused there); the look is repeated.
func (e *Engine) attachedAbsenceDue(now time.Time, id ident.EndpointID) {
	att, ok := e.attached[id]
	if !ok {
		return
	}
	key := attachedAbsenceKey(id)
	rec := e.view.endpoints[id].rec
	window := e.attachedWindow(rec)
	if gone := e.awake.total - att.lastAttach; gone < window {
		e.sched.set(key, now.Add(window-gone))
		return
	}
	if e.Phase() != PhaseRunning || len(e.view.queued[id]) > 0 {
		e.sched.set(key, now.Add(attachedHeldRecheck))
		return
	}
	if _, _, err := e.closeEndpoint(now, id, nil); err != nil {
		e.classify(err)
		if e.fatal == nil {
			e.log.Warn("an attached session whose controller is gone could not be closed; it is tried again", "endpoint", id.String(), "error", err.Error())
			e.sched.set(key, now.Add(e.p.RetryBackoff))
		}
		return
	}
	e.log.Info("an attached session was closed: its controller has been gone for the whole window", "endpoint", id.String(), "window", window.String())
}

// notifyDetached tells the adapter that the attached endpoint is closed and forgets
// the wake information held for it. It is best effort, like endpoint.pending: nothing
// records it and nothing repeats it, and an adapter that misses it stops at its next
// adapter.start, which no longer lists the endpoint. The session itself is never
// ended.
//
// A late session.attach may reach the adapter after session.detached when a close
// races the call that askToAttach makes after its command; the adapter drops that
// state at its next adapter.start.
func (e *Engine) notifyDetached(rec store.EndpointRecord) {
	if _, ok := e.attached[rec.ID]; !ok {
		return
	}
	delete(e.attached, rec.ID)
	e.sched.remove(attachedAbsenceKey(rec.ID))
	a := e.adapters[rec.Instance]
	if e.d.Adapters == nil || a == nil || !a.online || !a.admitted || !a.adm.Attaches() {
		return
	}
	e.d.Adapters.NotifyDetached(rec.Instance, adapterproto.SessionDetachedParams{Endpoint: adapterproto.EndpointRef(rec.ID.String())})
}

// closeAttached closes the open attached endpoints of the instance, the way the
// owner's close does. It is for an instance whose adapter does not take attached
// sessions (it was replaced by one that does not, or the node has not seen it declare
// them since it started): left open, they would be prompted against an adapter that
// refuses, and listed to nobody.
func (e *Engine) closeAttached(now time.Time, instance string) error {
	var open []ident.EndpointID
	for id, st := range e.view.endpoints {
		if st.rec.Kind == adapterreg.KindSession && st.rec.Instance == instance && st.rec.Status == store.EndpointOpen && st.rec.Origin() == adapterreg.OriginAttached {
			open = append(open, id)
		}
	}
	sort.Slice(open, func(i, j int) bool { return open[i].String() < open[j].String() })
	for _, id := range open {
		if _, _, err := e.closeEndpoint(now, id, nil); err != nil {
			return err
		}
	}
	return nil
}
