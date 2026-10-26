package engine

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/handoff"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/adapterhost"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// The creation of a session, from the acceptance of the control signal that asks
// for it to the control reply that tells its sender how it went.
//
// The intent is on disk before the adapter is called (control-intake), so that a
// call whose outcome is not known is made again with the same attempt, which the
// adapter answers idempotently, and never as a second session. The call runs in a
// goroutine the engine owns; its result is a command of the loop (cmdCreateResult),
// which settles the intent in one transaction (control-result): the reply is made
// and stored, the signal's delivery ends, and the session endpoint is opened when
// the session was created and closed when it was not. A call that failed, one the
// adapter answered as indeterminate and an adapter that is not connected are tried
// again until the reply deadline (Params.CreateReplyDeadline from the acceptance),
// when the intent is settled as unconfirmed: the sender is told that a session may
// exist, and the endpoint is closed, as the store's recovery would close it.
//
// The session endpoint is closed for good once the intent is settled any way but
// created, so a session that may exist after an unconfirmed creation is one nobody
// controls: the owner is told with an alert when the creation is settled, as for the
// adapter's own control_unknown, and what the adapter does afterwards changes
// nothing.

// alertControlUnknown is the kind of the alert that says a native session may
// remain that nobody controls.
const alertControlUnknown = "control_unknown"

// creation is what the loop remembers of the call of one creation intent.
type creation struct {
	// inflight says a call is with the adapter: the result schedules the next try,
	// and no second call is made beside it.
	inflight bool
}

// newSessionEndpoint is the endpoint a creation makes: being created, with its
// token, naming the control record that creates it. It is written with the record,
// so it is garbage until the record is there.
func (e *Engine) newSessionEndpoint(runtime *endpointState, c store.CtlRecord) (store.EndpointRecord, error) {
	var id ident.EndpointID
	for {
		var err error
		if id, err = ident.NewEndpointID(e.randomReader()); err != nil {
			return store.EndpointRecord{}, err
		}
		if e.view.endpoints[id] == nil {
			break
		}
	}
	token, err := mintToken(e.randomReader())
	if err != nil {
		return store.EndpointRecord{}, err
	}
	decl, err := encodeDeclaration(authz.Declaration{})
	if err != nil {
		return store.EndpointRecord{}, err
	}
	return store.EndpointRecord{
		ID: id, Kind: adapterreg.KindSession, Instance: runtime.rec.Instance, Status: store.EndpointCreating, Token: token,
		Declaration: decl, CreatedBy: ctlName(c),
		Session: &store.SessionInfo{Preset: c.Preset, ToolAccess: []string{TokenEnv, HomeEnv}},
	}, nil
}

// newAttemptID draws the identifier of a creation attempt: a version 4 UUID from
// the engine's own source.
func (e *Engine) newAttemptID() (adapterproto.AttemptID, error) {
	var b [16]byte
	if _, err := io.ReadFull(e.randomReader(), b[:]); err != nil {
		return "", err
	}
	return attemptOf(b), nil
}

// firstPromptAttempt is the attempt of the first prompt of the session a creation
// attempt makes. It follows from the creation attempt, so that a call made again
// carries the same first prompt, as the adapter requires of a retry, and nothing
// else has to be stored for it. The prompt that announces an initial message is
// recorded under it as a prompt attempt of the new endpoint.
func firstPromptAttempt(a adapterproto.AttemptID) adapterproto.AttemptID {
	sum := sha256.Sum256([]byte("sidera first prompt " + string(a)))
	return attemptOf([16]byte(sum[:16]))
}

// attemptOf writes 16 bytes as a version 4 UUID, in the lower case form the
// protocol requires.
func attemptOf(b [16]byte) adapterproto.AttemptID {
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return adapterproto.AttemptID(fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]))
}

// sessionTokenText is a token as the environment of a session carries it, and as the
// Agent interface reads it.
func sessionTokenText(token []byte) string { return base64.RawURLEncoding.EncodeToString(token) }

// toolAccess is what a session needs to reach the Agent interface: its own token
// and the data directory the interface is found through.
func (e *Engine) toolAccess(token []byte) (adapterproto.ToolAccess, error) {
	if e.d.Home == "" {
		return adapterproto.ToolAccess{}, fmt.Errorf("engine: Deps.Home is not set, and the tool access of a session carries the data directory")
	}
	return adapterproto.ToolAccess{Env: map[string]string{TokenEnv: sessionTokenText(token), HomeEnv: e.d.Home}}, nil
}

// beginCreate starts a creation whose intent was just written: its reply deadline
// is an entry of the schedule, and the adapter is called at once.
func (e *Engine) beginCreate(now time.Time, c store.CtlRecord) {
	e.sched.set(schedKey{schedCreateDeadline, c.Key.String()}, c.AcceptedAt.Add(e.p.CreateReplyDeadline))
	e.startCreate(now, c.Key)
}

// creatingIntent is the control record of the key when its creation is not settled.
func (e *Engine) creatingIntent(key store.TargetKey) (store.CtlRecord, bool) {
	c, ok := e.view.ctl[key]
	return c, ok && c.Intent != nil && c.Intent.State == store.IntentCreating
}

// instanceOf is the adapter instance a control record is executed by: the one its
// runtime endpoint belongs to.
func (e *Engine) instanceOf(c store.CtlRecord) string {
	if st := e.view.endpoints[c.Key.Recipient]; st != nil {
		return st.rec.Instance
	}
	return ""
}

// startCreate calls the adapter for a creation that is not settled and has no call
// out. An adapter that is not connected is waited for: the next connection of the
// instance starts the creations it owes (redriveCreates).
func (e *Engine) startCreate(now time.Time, key store.TargetKey) {
	c, ok := e.creatingIntent(key)
	if !ok {
		return
	}
	if !now.Before(c.AcceptedAt.Add(e.p.CreateReplyDeadline)) {
		return // its time is up: the deadline settles it, and the adapter is not asked again
	}
	cr := e.creations[key]
	if cr == nil {
		cr = &creation{}
		e.creations[key] = cr
	}
	instance := e.instanceOf(c)
	if a := e.adapters[instance]; cr.inflight || e.d.Adapters == nil || a == nil || !a.online {
		return
	}
	ep := e.view.endpoints[c.Intent.Endpoint]
	if ep == nil {
		e.log.Error("a creation names a session endpoint the node does not hold", "record", key.String())
		return
	}
	params, err := e.createParams(c, ep.rec)
	if err != nil {
		e.log.Error("the parameters of a creation are not valid; it is tried again", "record", key.String(), "error", err.Error())
		e.sched.set(schedKey{schedCreateRetry, key.String()}, now.Add(e.p.RetryBackoff))
		return
	}
	cr.inflight = true
	attempt := c.Intent.Attempt
	if !e.spawn(func(ctx context.Context) {
		e.adapterCalls.Add(1)
		res, err := e.d.Adapters.CreateSession(ctx, instance, params)
		e.adapterCalls.Add(-1)
		r := &cmdCreateResult{call: newCall[struct{}](), key: key, attempt: attempt, res: res, err: err}
		_, _ = roundTrip(ctx, e, submitInternal, r, r.call)
	}) {
		cr.inflight = false
	}
}

// createParams is session.create for a creation, built from the records alone: a
// call made again after a restart carries what the first carried.
func (e *Engine) createParams(c store.CtlRecord, ep store.EndpointRecord) (adapterproto.SessionCreateParams, error) {
	access, err := e.toolAccess(ep.Token)
	if err != nil {
		return adapterproto.SessionCreateParams{}, err
	}
	p := adapterproto.SessionCreateParams{
		Attempt:         c.Intent.Attempt,
		RuntimeEndpoint: adapterproto.EndpointRef(c.Key.Recipient.String()),
		SessionEndpoint: adapterproto.EndpointRef(ep.ID.String()),
		Preset:          c.Preset,
		FirstPrompt:     adapterproto.FirstPrompt{Attempt: firstPromptAttempt(c.Intent.Attempt), Text: handoff.NewSessionPromptText},
		ToolAccess:      access,
	}
	if cp, ok := e.view.checkpoints[store.CheckpointKey{Scope: string(adapterproto.ScopeAttempt), ID: string(c.Intent.Attempt)}]; ok {
		p.Checkpoint = append([]byte(nil), cp...)
	}
	if err := p.Validate(); err != nil {
		return adapterproto.SessionCreateParams{}, err
	}
	return p, nil
}

// redriveCreates calls the adapter again for every creation the instance owes: it
// has just connected, and what it was asked before its connection ended it may or
// may not have done. While the node starts, the creations wait for step 11, which
// checks the initial message of each before its call is made: an adapter that
// answered created sooner would open the endpoint of a creation whose message is gone.
func (e *Engine) redriveCreates(instance string, now time.Time) {
	if e.Phase() == PhaseRecovering && !e.reconciling {
		return
	}
	for _, key := range e.creatingKeys() {
		if c := e.view.ctl[key]; e.instanceOf(c) == instance {
			e.startCreate(now, key)
		}
	}
}

// creatingKeys are the keys of the creations that are not settled, in the order of
// their names.
func (e *Engine) creatingKeys() []store.TargetKey {
	var keys []store.TargetKey
	for k := range e.view.ctl {
		if _, ok := e.creatingIntent(k); ok {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
	return keys
}

type cmdCreateResult struct {
	call[struct{}]
	key     store.TargetKey
	attempt adapterproto.AttemptID
	res     adapterproto.SessionCreateResult
	err     error
}

func (c *cmdCreateResult) gate() gate { return gateAlways }
func (c *cmdCreateResult) run(e *Engine, now time.Time) {
	e.createResult(now, c)
	c.done(struct{}{}, nil)
}

// createResult takes in what the call of a creation brought. A result that settles
// the creation settles it; one that does not (a call that failed, an indeterminate
// outcome) is tried again after Params.CreateRetryBackoff, until the reply
// deadline, and a call that did not reach an adapter waits for the next connection.
// A result that comes after the intent was settled is a creation nobody asked the
// node to keep: if it made a session, the owner is told.
func (e *Engine) createResult(now time.Time, r *cmdCreateResult) {
	if cr := e.creations[r.key]; cr != nil {
		cr.inflight = false
	}
	c, ok := e.view.ctl[r.key]
	if !ok {
		return
	}
	if c.Intent == nil || c.Intent.State != store.IntentCreating || c.Intent.Attempt != r.attempt {
		e.lateCreate(c, r)
		return
	}
	if r.err != nil {
		switch {
		case errors.Is(r.err, context.Canceled):
			return // the engine is stopping; the start finds the intent again
		case errors.Is(r.err, adapterhost.ErrNotConnected):
			// The connection may be one that is not in service yet (the host puts it in
			// service after it has told the node that the adapter started), so the call is
			// tried again later as well as at the next connection.
			e.log.Debug("the adapter is not connected; the creation is tried again", "record", r.key.String())
			e.sched.set(schedKey{schedCreateRetry, r.key.String()}, now.Add(e.p.CreateRetryBackoff))
		default:
			e.log.Warn("a call to create a session got no answer; it is made again", "record", r.key.String(), "error", r.err.Error())
			e.sched.set(schedKey{schedCreateRetry, r.key.String()}, now.Add(e.p.CreateRetryBackoff))
		}
		return
	}
	ep := e.view.endpoints[c.Intent.Endpoint]
	if ep == nil {
		return
	}
	reply, final, err := adapterreg.CreateReply(r.res, adapterproto.EndpointRef(ep.rec.ID.String()))
	if err != nil {
		e.log.Error("the adapter's answer to a creation has no reply; it is asked again", "record", r.key.String(), "error", err.Error())
		e.sched.set(schedKey{schedCreateRetry, r.key.String()}, now.Add(e.p.CreateRetryBackoff))
		return
	}
	if !final {
		e.sched.set(schedKey{schedCreateRetry, r.key.String()}, now.Add(e.p.CreateRetryBackoff))
		return
	}
	if err := e.settleCreate(now, c, reply, &r.res); err != nil {
		e.classify(err) // a fatal error stops the loop after this command
		if e.fatal == nil {
			e.log.Warn("the result of a creation could not be recorded; it is tried again", "record", r.key.String(), "error", err.Error())
			e.sched.set(schedKey{schedCreateRetry, r.key.String()}, now.Add(e.p.RetryBackoff))
		}
	}
}

// lateCreate deals with the result of a call that comes after its intent was
// settled. Nothing is left to settle: if the intent was unconfirmed the owner was
// told when it was, and the endpoint is closed for good, whatever the adapter made.
func (e *Engine) lateCreate(c store.CtlRecord, r *cmdCreateResult) {
	e.log.Info("the result of a creation came after the creation was settled; it changes nothing", "record", r.key.String(), "settled", c.Intent != nil)
}

// alertUncontrolled tells the owner of a native session that may remain and that
// the node does not control. The endpoint is named, and nothing of its content. The
// alert is of the control record of the creation and told once: a creation the node
// cannot settle is met again at every start.
func (e *Engine) alertUncontrolled(now time.Time, c store.CtlRecord, why string) {
	subject := c.SessionEndpoint.String()
	if err := e.raiseAlertOnce(now, alertControlUnknown, c.Name(), subject, why+" (session endpoint "+subject+")"); err != nil {
		e.classify(err)
		if e.fatal == nil {
			e.log.Error("the alert of a session nobody controls could not be recorded", "endpoint", subject, "error", err.Error())
		}
	}
}

// createDeadline settles a creation whose reply deadline has come as unconfirmed: a
// session may exist, and the sender is told so with the endpoint the node
// allocated. While the wall clock is in doubt nothing is judged late, and it is
// looked at again.
func (e *Engine) createDeadline(now time.Time, key store.TargetKey) {
	c, ok := e.creatingIntent(key)
	if !ok {
		return
	}
	if e.paused {
		e.sched.set(schedKey{schedCreateDeadline, key.String()}, now.Add(e.p.RetryBackoff))
		return
	}
	a := e.adapters[e.instanceOf(c)]
	reply := adapterreg.CreateUnconfirmed(adapterproto.EndpointRef(c.Intent.Endpoint.String()), a == nil || !a.online)
	if err := e.settleCreate(now, c, reply, nil); err != nil {
		e.classify(err)
		if e.fatal == nil {
			e.log.Warn("an unconfirmed creation could not be recorded; it is tried again", "record", key.String(), "error", err.Error())
			e.sched.set(schedKey{schedCreateDeadline, key.String()}, now.Add(e.p.RetryBackoff))
		}
	}
}

// settleCreate ends a creation in one transaction: the reply is made and stored and
// the delivery of the signal ends, the intent records how it ended, and the session
// endpoint follows (open when the session was created, closed when it was not). An
// endpoint the owner closed meanwhile stays closed: closed is absorbing, and a
// session that was made for it is one nobody controls. native is what the adapter
// answered, nil when the node settles the creation itself.
func (e *Engine) settleCreate(now time.Time, c store.CtlRecord, reply adapterproto.ControlCreateReply, native *adapterproto.SessionCreateResult) error {
	st := e.view.endpoints[c.Intent.Endpoint]
	if st == nil {
		return fmt.Errorf("engine: the creation %s names a session endpoint the node does not hold", c.Key)
	}
	n, err := e.withReply(now, c, envelope.ControlResult{Signal: adapterproto.SignalSessionCreate, Create: createResultOf(reply)})
	if errors.Is(err, errReplyNotCommittable) {
		// The control record was closed while the creation was unsettled, which its own
		// deadline for the reply should have prevented: no reply can be written to it any
		// more, so trying again would never end. The endpoint and the intent stay as they
		// are, and the owner is told that a session may exist.
		e.log.Error("a creation cannot be settled: its control record was closed before it", "record", c.Key.String(), "state", c.Record.State.String(), "error", err.Error())
		e.alertUncontrolled(now, c, "the control record of a creation was closed before the creation settled, so no reply can be stored and a session may exist")
		e.forgetCreation(c.Key)
		return nil
	}
	if err != nil {
		return err
	}
	intent := *n.Intent
	ep := st.rec
	switch reply.Outcome {
	case adapterproto.CreateReplyCreated:
		intent.State, intent.NativeRef, intent.Process = store.IntentCreated, string(native.NativeRef), native.Process
		// A session made for an endpoint the owner closed meanwhile leaves the endpoint
		// closed, with the native session recorded on it: that is what the node releases.
		sess := store.SessionInfo{Preset: c.Preset, ToolAccess: []string{TokenEnv, HomeEnv}}
		if ep.Session != nil {
			sess = *ep.Session
		}
		sess.NativeRef, sess.Process = intent.NativeRef, intent.Process
		ep.Session = &sess
		if ep.Status == store.EndpointCreating {
			ep.Status = store.EndpointOpen
		}
	case adapterproto.CreateReplyUnconfirmed:
		intent.State = store.IntentUnconfirmed
		ep.Status, ep.Token = store.EndpointClosed, nil
	default:
		intent.State = store.IntentFailed
		ep.Status, ep.Token = store.EndpointClosed, nil
	}
	n.Intent = &intent
	var write *store.EndpointRecord
	if ep.Status != st.rec.Status || (reply.Outcome == adapterproto.CreateReplyCreated && ep.Status == store.EndpointClosed) {
		write = &ep
	}
	if err := e.d.Store.ControlResult(n, write); err != nil {
		return err
	}
	e.view.setCtl(n)
	if write != nil {
		e.putEndpoint(ep, st.decl)
		if ep.Status == store.EndpointClosed && e.view.Draining(ep.ID) {
			e.later(&cmdDrain{endpoint: ep.ID})
		}
	}
	e.forgetCreation(c.Key)
	e.dropCreationCheckpoints(intent.Attempt)
	if st.rec.Status == store.EndpointClosed && intent.State == store.IntentCreated {
		e.alertUncontrolled(now, n, "a session was created for an endpoint that had been closed")
	}
	if reply.Outcome == adapterproto.CreateReplyUnconfirmed {
		e.alertUncontrolled(now, n, "a session being created was not confirmed in time and may exist")
	}
	if reply.Reason == adapterproto.CreateReasonControlUnknown {
		if a := e.adapters[ep.Instance]; a != nil {
			a.controlUnknown = true
		}
		e.alertUncontrolled(now, n, "no session will be attached to a creation whose native session may remain")
	}
	return nil
}

// failCreationWithoutEndpoint settles a creation whose session endpoint is not there: the
// file of the endpoint was damaged while the creation was not settled, and settling it
// removed the file. The creation cannot go on, since no token would open the session,
// and the sender must not wait for one: the reply says `failed` with control_unknown (no
// session will be attached, but a call already out may have made one that nobody
// controls), and the owner is told of it.
func (e *Engine) failCreationWithoutEndpoint(now time.Time, c store.CtlRecord) error {
	failed := adapterproto.ControlCreateReply{Outcome: adapterproto.CreateReplyFailed, Reason: adapterproto.CreateReasonControlUnknown}
	n, err := e.withReply(now, c, envelope.ControlResult{Signal: adapterproto.SignalSessionCreate, Create: createResultOf(failed)})
	if errors.Is(err, errReplyNotCommittable) {
		e.log.Error("a creation whose endpoint is lost cannot be settled: its control record was closed before it", "record", c.Key.String())
		e.alertUncontrolled(now, c, "the control record of a creation whose endpoint was lost was closed before the creation settled, so a session may exist")
		e.forgetCreation(c.Key)
		return nil
	}
	if err != nil {
		return err
	}
	intent := *n.Intent
	intent.State = store.IntentFailed
	n.Intent = &intent
	if err := e.d.Store.ControlResult(n, nil); err != nil {
		return err
	}
	e.view.setCtl(n)
	e.forgetCreation(c.Key)
	e.dropCreationCheckpoints(intent.Attempt)
	e.alertUncontrolled(now, n, "the session endpoint of a creation was lost, so no session will be attached and a native session may remain")
	return nil
}

// dropCreationCheckpoints removes the checkpoints the adapter kept for a creation that
// has settled, under its attempt and under the attempt of its first prompt: they are what
// a call made again would be given, and none will be made now.
func (e *Engine) dropCreationCheckpoints(attempt adapterproto.AttemptID) {
	e.dropAttemptCheckpoint(attempt)
	e.dropAttemptCheckpoint(firstPromptAttempt(attempt))
}

// forgetCreation stops the work for a creation: its deadline, its retry and the call
// that was to be made again.
func (e *Engine) forgetCreation(key store.TargetKey) {
	e.sched.remove(schedKey{schedCreateDeadline, key.String()})
	e.sched.remove(schedKey{schedCreateRetry, key.String()})
	delete(e.creations, key)
}

// reconcileCreations settles or goes on with every creation that was not settled when
// the node went down (step 11): one past its reply deadline is settled as
// unconfirmed, one whose time is not up has its deadline set again and, when its
// adapter is connected, its call made again with the same attempt.
func (e *Engine) reconcileCreations(now time.Time) error {
	for _, key := range e.creatingKeys() {
		c := e.view.ctl[key]
		if e.view.endpoints[c.Intent.Endpoint] == nil {
			if err := e.failCreationWithoutEndpoint(now, c); err != nil {
				return err
			}
			continue
		}
		if err := e.reconcileInitialMessage(now, c); err != nil {
			return err
		}
		if _, ok := e.creatingIntent(key); !ok {
			continue // failed: its initial message is gone
		}
		due := c.AcceptedAt.Add(e.p.CreateReplyDeadline)
		if !now.Before(due) && !e.paused {
			e.createDeadline(now, key)
			if e.fatal != nil {
				return e.fatal
			}
			continue
		}
		e.sched.set(schedKey{schedCreateDeadline, key.String()}, due)
		e.startCreate(now, key)
	}
	return nil
}
