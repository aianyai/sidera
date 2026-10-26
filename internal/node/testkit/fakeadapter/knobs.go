package fakeadapter

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// OutcomeKind is what a knob makes a request come to.
type OutcomeKind int

// The outcomes. Which of them a knob takes is listed on its Set method.
const (
	// OutcomeFail: a creation fails with Reason, a CreateFailure.
	OutcomeFail OutcomeKind = iota + 1
	// OutcomeIndeterminate: the answer is "cannot tell"; Reason is a grounds code
	// (turn_running, record_unreadable, native_unreachable or
	// runtime_version_untested; native_unreachable when empty).
	OutcomeIndeterminate
	// OutcomeRefuse: the request is refused with Reason, a PromptRefusal or an
	// InterruptRefusal.
	OutcomeRefuse
	// OutcomeAccepted: a prompt is accepted but not yet durable; the test makes
	// it durable later with Persist.
	OutcomeAccepted
	// OutcomeRequested: an interrupt is requested but the turn is left running;
	// the script ends it.
	OutcomeRequested
	// OutcomeNotRunning: an interrupt finds nothing to stop, whatever the runtime
	// says.
	OutcomeNotRunning
	// OutcomeUnconfirmed: a close cannot be confirmed; Reason is a CloseReason.
	OutcomeUnconfirmed
	// OutcomeUnreachable: adapter.start reports every session unreachable.
	OutcomeUnreachable
	// OutcomeError: the request is answered with the error Code.
	OutcomeError
	// OutcomeHold: the request waits until ReleaseHeld, then goes its normal way.
	OutcomeHold
)

// Outcome is one setting of a knob.
type Outcome struct {
	Kind   OutcomeKind
	Reason string
	// Code is the error code of OutcomeError.
	Code int
	// AfterEffect makes OutcomeError, and Indeterminate and the failure
	// control_unknown of a creation, happen after the effect: the native session
	// is created (or the prompt delivered) and then the answer is the error. An
	// error with no effect is the default, because the protocol's codes below
	// -32603 say nothing was done.
	AfterEffect bool
}

// Request kinds a knob can govern.
const (
	forCreate    = "create"
	forPrompt    = "prompt"
	forInterrupt = "interrupt"
	forClose     = "close"
	forReconcile = "reconcile"
	forStart     = "start"
)

// valid lists the outcome kinds each request takes.
var validKinds = map[string][]OutcomeKind{
	forCreate:    {OutcomeFail, OutcomeIndeterminate, OutcomeError, OutcomeHold},
	forPrompt:    {OutcomeAccepted, OutcomeRefuse, OutcomeError, OutcomeHold},
	forInterrupt: {OutcomeRequested, OutcomeRefuse, OutcomeNotRunning, OutcomeError, OutcomeHold},
	forClose:     {OutcomeUnconfirmed, OutcomeError, OutcomeHold},
	forReconcile: {OutcomeIndeterminate, OutcomeError, OutcomeHold},
	forStart:     {OutcomeUnreachable, OutcomeError, OutcomeHold},
}

func (o Outcome) check(request string) error {
	if !slices.Contains(validKinds[request], o.Kind) {
		return fmt.Errorf("fakeadapter: outcome kind %d is not one a %s request takes", o.Kind, request)
	}
	if o.Kind == OutcomeError {
		if o.Code == 0 {
			return errors.New("fakeadapter: OutcomeError needs a code")
		}
		// The adapter never writes a message the protocol would refuse: a code the
		// protocol does not define, or one that must carry data, is refused here and
		// not discovered on the wire.
		if err := (adapterproto.ErrorObject{Code: o.Code, Message: injectedMessage}).Validate(); err != nil {
			return fmt.Errorf("fakeadapter: OutcomeError code %d is not one the adapter can answer with: %w", o.Code, err)
		}
	}
	return nil
}

// injectedMessage is the text of every error a knob makes the adapter answer with.
const injectedMessage = "injected by a knob"

var (
	// ErrNotServing: the knob acts on a running adapter and none is serving.
	ErrNotServing = errors.New("fakeadapter: no adapter is serving")
	// ErrNotDeclared: the adapter did not declare the capability the knob uses.
	ErrNotDeclared = errors.New("fakeadapter: the adapter did not declare the capability")
	// ErrDisconnected is what Serve returns after the Disconnect knob.
	ErrDisconnected = errors.New("fakeadapter: disconnected by a knob")
)

type outcomeRule struct {
	sticky *Outcome
	queue  []Outcome
}

// Knobs are the handles a test holds on a fake adapter. They are safe for
// concurrent use and may be set before Serve starts and between connections: a
// restarted adapter, served with the same Knobs, keeps its settings. Every use of
// a knob that changes what the adapter does writes a knob.hit entry to the
// ledger, so a test can assert that its injection point was reached.
type Knobs struct {
	mu       sync.Mutex
	adapter  *adapter
	rules    map[string]*outcomeRule
	pauses   map[string]*pausePoint // armed, nobody has stopped here yet
	stopped  map[string]*pausePoint // a request is stopped here, waiting for Release
	holds    []heldRequest
	holdOpen chan struct{} // closed by ReleaseHeld, replaced afterwards
	hang     bool
	observed ObservedMode
	onPause  func(point string)
}

// heldRequest is a request of the daemon that the adapter is holding on purpose:
// the method it came with, and the pause point it stopped at, empty when an
// outcome holds it.
type heldRequest struct {
	method, point string
}

type pausePoint struct {
	reached chan struct{} // closed when an adapter has stopped here
	release chan struct{} // closed to let it go
	once    sync.Once
}

func (p *pausePoint) let() { p.once.Do(func() { close(p.release) }) }

// NewKnobs returns knobs that change nothing.
func NewKnobs() *Knobs {
	return &Knobs{rules: map[string]*outcomeRule{}, pauses: map[string]*pausePoint{}, stopped: map[string]*pausePoint{}, holdOpen: make(chan struct{})}
}

func (k *Knobs) rule(request string) *outcomeRule {
	r, ok := k.rules[request]
	if !ok {
		r = &outcomeRule{}
		k.rules[request] = r
	}
	return r
}

func (k *Knobs) set(request string, o Outcome) error {
	if err := o.check(request); err != nil {
		return err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.rule(request).sticky = &o
	return nil
}

func (k *Knobs) queue(request string, o Outcome) error {
	if err := o.check(request); err != nil {
		return err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	r := k.rule(request)
	r.queue = append(r.queue, o)
	return nil
}

// next returns the outcome for the next request of a kind: the oldest queued one,
// else the sticky one, else none.
func (k *Knobs) next(request string) (Outcome, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	r := k.rules[request]
	if r == nil {
		return Outcome{}, false
	}
	if len(r.queue) > 0 {
		o := r.queue[0]
		r.queue = r.queue[1:]
		return o, true
	}
	if r.sticky != nil {
		return *r.sticky, true
	}
	return Outcome{}, false
}

// SetCreate makes every later session.create come to the outcome (OutcomeFail,
// OutcomeIndeterminate, OutcomeError or OutcomeHold); QueueCreate makes only the
// next one. The queue is used before the sticky setting.
func (k *Knobs) SetCreate(o Outcome) error   { return k.set(forCreate, o) }
func (k *Knobs) QueueCreate(o Outcome) error { return k.queue(forCreate, o) }

// SetPrompt and QueuePrompt govern prompt.deliver: OutcomeAccepted, OutcomeRefuse,
// OutcomeError or OutcomeHold. The default is a prompt that is persisted.
func (k *Knobs) SetPrompt(o Outcome) error   { return k.set(forPrompt, o) }
func (k *Knobs) QueuePrompt(o Outcome) error { return k.queue(forPrompt, o) }

// SetInterrupt and QueueInterrupt govern session.interrupt: OutcomeRequested,
// OutcomeRefuse, OutcomeNotRunning, OutcomeError or OutcomeHold.
func (k *Knobs) SetInterrupt(o Outcome) error   { return k.set(forInterrupt, o) }
func (k *Knobs) QueueInterrupt(o Outcome) error { return k.queue(forInterrupt, o) }

// SetClose and QueueClose govern session.close: OutcomeUnconfirmed, OutcomeError
// or OutcomeHold.
func (k *Knobs) SetClose(o Outcome) error   { return k.set(forClose, o) }
func (k *Knobs) QueueClose(o Outcome) error { return k.queue(forClose, o) }

// SetReconcile and QueueReconcile govern prompt.reconcile: OutcomeIndeterminate,
// OutcomeError or OutcomeHold. Without them the adapter answers from the record,
// as a real one does.
func (k *Knobs) SetReconcile(o Outcome) error   { return k.set(forReconcile, o) }
func (k *Knobs) QueueReconcile(o Outcome) error { return k.queue(forReconcile, o) }

// SetStart and QueueStart govern adapter.start: OutcomeUnreachable, OutcomeError
// or OutcomeHold.
func (k *Knobs) SetStart(o Outcome) error   { return k.set(forStart, o) }
func (k *Knobs) QueueStart(o Outcome) error { return k.queue(forStart, o) }

// ClearOutcomes drops every sticky setting and every queued outcome.
func (k *Knobs) ClearOutcomes() {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.rules = map[string]*outcomeRule{}
}

// The named pause points. At each the adapter has done an effect and not yet
// answered, which is where a crash or a lost connection is worst for the daemon.
const (
	// PauseCreateAfterNative: the native session exists, created has not been
	// answered.
	PauseCreateAfterNative = "create.after-native"
	// PausePromptAfterForward: the prompt reached the runtime, the result has not
	// been answered.
	PausePromptAfterForward = "prompt.after-forward"
	// PauseInterruptAfterForward: the interrupt reached the runtime, no answer yet.
	PauseInterruptAfterForward = "interrupt.after-forward"
	// PauseCloseAfterForward: the close reached the runtime, no answer yet.
	PauseCloseAfterForward = "close.after-forward"
	// PauseStartBeforeReply: adapter.start was received and has not been answered.
	PauseStartBeforeReply = "start.before-reply"
)

var pauseNames = []string{PauseCreateAfterNative, PausePromptAfterForward, PauseInterruptAfterForward, PauseCloseAfterForward, PauseStartBeforeReply}

// pauseMethods is the method of the request that stops at each pause point.
var pauseMethods = map[string]string{
	PauseCreateAfterNative:     adapterproto.MethodSessionCreate,
	PausePromptAfterForward:    adapterproto.MethodPromptDeliver,
	PauseInterruptAfterForward: adapterproto.MethodSessionInterrupt,
	PauseCloseAfterForward:     adapterproto.MethodSessionClose,
	PauseStartBeforeReply:      adapterproto.MethodAdapterStart,
}

// OnPause sets a function that is called, outside any lock, each time a request
// stops at a pause point, with the name of the point. The adapter program uses it to
// tell the fixture over the control channel.
func (k *Knobs) OnPause(f func(point string)) {
	k.mu.Lock()
	k.onPause = f
	k.mu.Unlock()
}

// PauseAt arms the pause points: the next request to reach one stops there until
// Release. A pause point stops one request: it is spent once it has, and a
// request that reaches it again, a retry on a restarted adapter for instance, goes
// through unless the test arms it again.
func (k *Knobs) PauseAt(points ...string) error {
	for _, p := range points {
		if !slices.Contains(pauseNames, p) {
			return fmt.Errorf("fakeadapter: %q is not a pause point (%v)", p, pauseNames)
		}
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	for _, p := range points {
		if _, armed := k.pauses[p]; !armed {
			k.pauses[p] = &pausePoint{reached: make(chan struct{}), release: make(chan struct{})}
		}
	}
	return nil
}

// WaitPaused returns when an adapter has stopped at the pause point, or when ctx
// ends.
func (k *Knobs) WaitPaused(ctx context.Context, point string) error {
	k.mu.Lock()
	pp := k.pauses[point]
	if pp == nil {
		pp = k.stopped[point]
	}
	k.mu.Unlock()
	if pp == nil {
		return fmt.Errorf("fakeadapter: %q is not armed", point)
	}
	select {
	case <-pp.reached:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Release lets the request that stopped at the pause point go on, and disarms the
// point if nothing has stopped there yet.
func (k *Knobs) Release(point string) {
	k.mu.Lock()
	pp := k.stopped[point]
	if pp == nil {
		pp = k.pauses[point]
	}
	delete(k.stopped, point)
	delete(k.pauses, point)
	k.mu.Unlock()
	if pp != nil {
		pp.let()
	}
}

// pause stops the calling handler at the point if it is armed.
func (k *Knobs) pause(ctx context.Context, a *adapter, point string) error {
	k.mu.Lock()
	pp := k.pauses[point]
	if pp != nil {
		delete(k.pauses, point)
		k.stopped[point] = pp
	}
	k.mu.Unlock()
	if pp == nil {
		return nil
	}
	if err := a.hit("pause."+point, ""); err != nil {
		return err
	}
	held := heldRequest{method: pauseMethods[point], point: point}
	k.addHeld(held)
	defer k.removeHeld(held)
	close(pp.reached)
	k.mu.Lock()
	hook := k.onPause
	k.mu.Unlock()
	if hook != nil {
		hook(point)
	}
	select {
	case <-pp.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (k *Knobs) addHeld(h heldRequest) {
	k.mu.Lock()
	k.holds = append(k.holds, h)
	k.mu.Unlock()
}

func (k *Knobs) removeHeld(h heldRequest) {
	k.mu.Lock()
	if i := slices.Index(k.holds, h); i >= 0 {
		k.holds = slices.Delete(k.holds, i, i+1)
	}
	k.mu.Unlock()
}

// Held lists the methods of the requests the adapter is holding on purpose, one
// entry per request, in the order they were stopped: a multiset of methods, which
// a fixture compares with the multiset of methods of the requests the daemon has
// in flight to this adapter when it asks whether the system is quiet. A request is
// held at a pause point, by OutcomeHold or by Hang; either way it appears here
// under its method (session.create, prompt.deliver, …) and never under the name of
// a point.
func (k *Knobs) Held() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	out := make([]string, len(k.holds))
	for i, h := range k.holds {
		out[i] = h.method
	}
	return out
}

// HeldPoints lists the pause points at which a request is stopped now: the
// requests of Held that stopped at a point and not by an outcome.
func (k *Knobs) HeldPoints() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	var out []string
	for _, h := range k.holds {
		if h.point != "" {
			out = append(out, h.point)
		}
	}
	return out
}

// hold waits for ReleaseHeld or the end of ctx; method labels the request.
func (k *Knobs) hold(ctx context.Context, method string) error {
	k.mu.Lock()
	open := k.holdOpen
	k.mu.Unlock()
	held := heldRequest{method: method}
	k.addHeld(held)
	defer k.removeHeld(held)
	select {
	case <-open:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ReleaseHeld lets every request held by OutcomeHold go on.
func (k *Knobs) ReleaseHeld() {
	k.mu.Lock()
	close(k.holdOpen)
	k.holdOpen = make(chan struct{})
	k.mu.Unlock()
}

// Hang makes the adapter stop answering every request, ping included, while its
// process and its instance lock stay: the picture of an adapter that is stuck.
func (k *Knobs) Hang() {
	k.mu.Lock()
	k.hang = true
	k.mu.Unlock()
}

func (k *Knobs) hanging() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.hang
}

// Disconnect closes the protocol stream: Serve returns ErrDisconnected. In a test
// process this is the adapter crashing; a binary exits.
func (k *Knobs) Disconnect() error {
	a := k.bound()
	if a == nil {
		return ErrNotServing
	}
	if err := a.hit("disconnect", ""); err != nil {
		return err
	}
	a.fail(ErrDisconnected)
	return nil
}

func (k *Knobs) bind(a *adapter) {
	k.mu.Lock()
	k.adapter = a
	k.mu.Unlock()
}

func (k *Knobs) unbind(a *adapter) {
	k.mu.Lock()
	if k.adapter == a {
		k.adapter = nil
	}
	k.mu.Unlock()
}

func (k *Knobs) bound() *adapter {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.adapter
}

// NotificationsSent is how many notifications the adapter that is serving has
// handed to the protocol, counting only those the daemon will see. A fixture that
// asks whether the system is quiet compares it with how many the daemon handled.
func (k *Knobs) NotificationsSent() int {
	if a := k.bound(); a != nil {
		return a.sentCount()
	}
	return 0
}

// Outstanding is how many requests the adapter has made of the daemon that are
// not answered yet.
func (k *Knobs) Outstanding() int {
	if a := k.bound(); a != nil {
		return a.outstanding()
	}
	return 0
}

// stats is what the stats knob answers.
func (k *Knobs) stats() StatsReply {
	return StatsReply{NotificationsSent: k.NotificationsSent(), Outstanding: k.Outstanding(), Held: k.Held(), HeldPoints: k.HeldPoints()}
}

// Ping pings the daemon, as an adapter checks that it is still there.
func (k *Knobs) Ping(ctx context.Context) error {
	a := k.bound()
	if a == nil {
		return ErrNotServing
	}
	return a.call(ctx, adapterproto.MethodPing, &adapterproto.Empty{}, &adapterproto.Empty{})
}

// Availability sends an adapter.availability notification: the full snapshot of
// what keeps the adapter from working; empty when all is well.
func (k *Knobs) Availability(ctx context.Context, conditions []adapterproto.Availability) error {
	a := k.bound()
	if a == nil {
		return ErrNotServing
	}
	if conditions == nil {
		conditions = []adapterproto.Availability{}
	}
	if err := a.hit("availability", fmt.Sprintf("%d conditions", len(conditions))); err != nil {
		return err
	}
	return a.notify(ctx, adapterproto.MethodAdapterAvailability, adapterproto.AvailabilityParams{Conditions: conditions})
}

// Persist sends prompt.persisted for an attempt the adapter answered as only
// accepted.
func (k *Knobs) Persist(ctx context.Context, endpoint adapterproto.EndpointRef, attempt adapterproto.AttemptID) error {
	a := k.bound()
	if a == nil {
		return ErrNotServing
	}
	if err := a.hit("persist", string(attempt)); err != nil {
		return err
	}
	return a.notify(ctx, adapterproto.MethodPromptPersisted, adapterproto.PromptPersistedParams{Endpoint: endpoint, Attempt: attempt})
}

// NativeRequest reports a request of the native runtime that the adapter answered
// itself. The adapter must have declared server requests.
func (k *Knobs) NativeRequest(ctx context.Context, endpoint adapterproto.EndpointRef, method string, d adapterproto.Disposition) error {
	a := k.bound()
	if a == nil {
		return ErrNotServing
	}
	if !a.opts.ServerRequests {
		return fmt.Errorf("%w: server requests", ErrNotDeclared)
	}
	if err := a.hit("native-request", method); err != nil {
		return err
	}
	return a.notify(ctx, adapterproto.MethodNativeRequestHandled, adapterproto.NativeRequestHandledParams{
		Endpoint: endpoint, Method: method, Disposition: d, AtMS: a.cfg.Now().UnixMilli(),
	})
}

// Register, Recv, Ack, Send, MessageStatus and Unregister are what a chat bridge
// does with a direct endpoint: the adapter calls the daemon's endpoint methods and
// returns what it answered. The adapter must have declared direct endpoints.

// Register registers a direct endpoint under a key.
func (k *Knobs) Register(ctx context.Context, p adapterproto.RegisterParams) (adapterproto.RegisterResult, error) {
	var res adapterproto.RegisterResult
	err := k.direct(ctx, "register", adapterproto.MethodEndpointRegister, p, &res)
	return res, err
}

// Recv reads the messages waiting for a direct endpoint.
func (k *Knobs) Recv(ctx context.Context, p adapterproto.RecvParams) (adapterproto.RecvResult, error) {
	var res adapterproto.RecvResult
	err := k.direct(ctx, "recv", adapterproto.MethodEndpointRecv, p, &res)
	return res, err
}

// Ack acknowledges parts of a page.
func (k *Knobs) Ack(ctx context.Context, p adapterproto.AckParams) (adapterproto.AckResult, error) {
	var res adapterproto.AckResult
	err := k.direct(ctx, "ack", adapterproto.MethodEndpointAck, p, &res)
	return res, err
}

// Send sends a message from a direct endpoint.
func (k *Knobs) Send(ctx context.Context, p adapterproto.SendParams) (adapterproto.SendResult, error) {
	var res adapterproto.SendResult
	err := k.direct(ctx, "send", adapterproto.MethodEndpointSend, p, &res)
	return res, err
}

// MessageStatus asks for the delivery state of a message a direct endpoint sent.
func (k *Knobs) MessageStatus(ctx context.Context, p adapterproto.MessageStatusParams) (adapterproto.MessageStatusResult, error) {
	var res adapterproto.MessageStatusResult
	err := k.direct(ctx, "message-status", adapterproto.MethodEndpointMessageStatus, p, &res)
	return res, err
}

// Unregister closes a direct endpoint.
func (k *Knobs) Unregister(ctx context.Context, p adapterproto.UnregisterParams) error {
	var res adapterproto.Empty
	return k.direct(ctx, "unregister", adapterproto.MethodEndpointUnregister, p, &res)
}

func (k *Knobs) direct(ctx context.Context, name, method string, params adapterproto.Message, result adapterproto.Message) error {
	a := k.bound()
	if a == nil {
		return ErrNotServing
	}
	if !a.opts.Direct {
		return fmt.Errorf("%w: direct endpoints", ErrNotDeclared)
	}
	if err := a.hit("direct."+name, ""); err != nil {
		return err
	}
	return a.call(ctx, method, params, result)
}

// ObservedMode is what the adapter hands the daemon as the text the model saw
// when it reports a recv result with recv.observed.
type ObservedMode string

// The modes of the observed knob.
const (
	// ObservedFaithful reports the result exactly as the model received it.
	ObservedFaithful ObservedMode = "faithful"
	// ObservedTruncated reports the first half of it followed by the manifest's
	// truncation notice, as a runtime that cut the result short would show it.
	ObservedTruncated ObservedMode = "truncated"
	// ObservedReplaced reports the manifest's replacement notice in its place, as a
	// runtime that saved the result to a file would show it.
	ObservedReplaced ObservedMode = "replaced"
)

// SetObserved sets what later recv results are reported as. The default is
// ObservedFaithful.
func (k *Knobs) SetObserved(m ObservedMode) error {
	switch m {
	case ObservedFaithful, ObservedTruncated, ObservedReplaced:
	default:
		return fmt.Errorf("fakeadapter: %q is not an observed mode", m)
	}
	k.mu.Lock()
	k.observed = m
	k.mu.Unlock()
	return nil
}

func (k *Knobs) observedMode() ObservedMode {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.observed == "" {
		return ObservedFaithful
	}
	return k.observed
}
