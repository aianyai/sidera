package engine

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/publication"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// ---------------------------------------------------------------------------
// Admission of an adapter instance

type cmdAdmit struct {
	call[struct{}]
	instance string
	adm      adapterreg.Admission
}

func (c *cmdAdmit) gate() gate { return gateAlways }
func (c *cmdAdmit) run(e *Engine, _ time.Time) {
	if in, ok := e.d.Config.Instances().Items[c.instance]; !ok || !in.Enabled {
		c.done(struct{}{}, refuse(ReasonInstanceUnknown))
		return
	}
	e.adm[c.instance] = c.adm
	c.done(struct{}{}, nil)
}

// AdapterAdmitted tells the engine that an adapter instance was admitted on a
// connection, with what it is allowed to do. The registration of its direct
// endpoints is judged by that. The instance must be enabled in the
// configuration. It is the entry the adapter host calls, and the one a test
// calls to give an instance, as the host does.
func (e *Engine) AdapterAdmitted(ctx context.Context, instance string, a adapterreg.Admission) error {
	c := &cmdAdmit{call: newCall[struct{}](), instance: instance, adm: a}
	_, err := roundTrip(ctx, e, submitInternal, c, c.call)
	return err
}

// ---------------------------------------------------------------------------
// Registration of a direct endpoint

type cmdRegister struct {
	call[adapterproto.RegisterResult]
	instance string
	params   adapterproto.RegisterParams
}

func (c *cmdRegister) gate() gate { return gateAdapter }
func (c *cmdRegister) run(e *Engine, _ time.Time) {
	res, err := e.registerDirect(c.instance, c.params)
	var d *drainingError
	if errors.As(err, &d) {
		e.parkUntilDrained(d.endpoint, c)
		return
	}
	c.done(res, e.classify(err))
}

// drainingError says that the key of a registration is the endpoint of a closed
// endpoint whose queued records are not all closed yet. The registration is not
// refused: it waits for the drain and then runs.
type drainingError struct{ endpoint ident.EndpointID }

func (d *drainingError) Error() string {
	return "engine: endpoint " + d.endpoint.String() + " is draining"
}

// RegisterDirect registers a direct endpoint of an adapter instance under the
// key the adapter chose, and returns it: the same key always gives the same
// endpoint, which the id kept in the configuration makes true for ever. This is
// the entry for direct endpoints, the one the adapter host calls for the adapter
// protocol's endpoint.register; it waits for the running phase.
//
// A key that gives an endpoint that is open is not registered again: its display
// name and description are what the adapter says now, and nothing else changes.
// A new endpoint, and one that was closed and is opened again with a new token,
// counts against the limit of the instance and the cap of the node. A closed
// endpoint is not opened again while the records queued for it are being closed:
// the registration waits until they are, and then runs, so the adapter's request
// is answered late and never refused for it.
func (e *Engine) RegisterDirect(ctx context.Context, instance string, p adapterproto.RegisterParams) (adapterproto.RegisterResult, error) {
	c := &cmdRegister{call: newCall[adapterproto.RegisterResult](), instance: instance, params: p}
	return roundTrip(ctx, e, submitInternal, c, c.call)
}

func invalidArgument(field string, err error) *Refusal {
	return &Refusal{Reason: ReasonInvalidArgument, Field: field, Err: err}
}

func (e *Engine) registerDirect(inst string, p adapterproto.RegisterParams) (adapterproto.RegisterResult, error) {
	var none adapterproto.RegisterResult
	adm, ok := e.adm[inst]
	switch {
	case !ok:
		return none, refuse(ReasonInstanceUnknown)
	case !adm.Declares(adapterreg.KindDirect):
		return none, refuse(ReasonWrongEndpointKind)
	}
	if err := p.Validate(); err != nil {
		field := ""
		var ve *adapterproto.ValidationError
		if errors.As(err, &ve) {
			field = ve.Field
		}
		return none, invalidArgument(field, err)
	}
	ch, err := e.d.Config.DirectID(inst, p.Key, e.randomReader())
	var ce *config.ChangeError
	switch {
	case errors.As(err, &ce) && ce.Reason == config.ChangeInstanceUnknown:
		return none, refuse(ReasonInstanceUnknown)
	case err != nil:
		return none, err
	}
	ref := adapterproto.RegisterResult{Endpoint: adapterproto.EndpointRef(ch.ID.String())}
	meta := endpointMeta{DisplayName: p.DisplayName, Description: p.Description}

	st := e.view.endpoints[ch.ID]
	if st != nil && st.rec.Kind != adapterreg.KindDirect {
		return none, errors.New("engine: a stable id of a direct endpoint is the id of another kind of endpoint")
	}
	if st != nil && st.rec.Status == store.EndpointOpen {
		meta.Declaration = st.meta.Declaration
		if meta.DisplayName == st.meta.DisplayName && meta.Description == st.meta.Description {
			return ref, nil
		}
		rec := st.rec
		if rec.Declaration, err = encodeMeta(meta); err != nil {
			return none, err
		}
		if err := e.d.Store.EndpointUpdate(rec); err != nil {
			return none, err
		}
		e.view.putEndpoint(rec, meta)
		return ref, nil
	}

	if st != nil {
		if e.view.Draining(ch.ID) {
			return none, &drainingError{ch.ID}
		}
		meta.Declaration = st.meta.Declaration
	}
	if e.view.openDirect(inst) >= adm.MaxDirectEndpoints() {
		return none, &Refusal{Reason: ReasonEndpointLimit, Limit: LimitInstance}
	}
	if e.view.OpenEndpoints() >= publication.MaxEndpointsPerNode {
		return none, &Refusal{Reason: ReasonEndpointLimit, Limit: LimitNode}
	}
	token, err := mintToken(e.randomReader())
	if err != nil {
		return none, err
	}
	rec := store.EndpointRecord{ID: ch.ID, Kind: adapterreg.KindDirect, Instance: inst, Status: store.EndpointOpen, Token: token}
	if rec.Declaration, err = encodeMeta(meta); err != nil {
		return none, err
	}
	if err := e.commitDirectRegister(ch, rec); err != nil {
		return none, err
	}
	e.view.putEndpoint(rec, meta)
	return ref, nil
}

// commitDirectRegister is the transaction of the registration: the id in
// config/, when it is new, and the record in state/, in one transaction, which
// the engine opens because it writes the files of two owners. Everything that can
// be refused is checked before Begin.
func (e *Engine) commitDirectRegister(ch config.IDChange, rec store.EndpointRecord) error {
	pe, err := store.PrepareEndpoint(rec)
	if err != nil {
		return err
	}
	tx := e.d.Writer.Begin(&declDirectRegister)
	defer tx.Close()
	after, err := e.d.Config.PutEndpointIDs(tx, ch)
	if err != nil {
		return err
	}
	if err := store.PutEndpoint(tx, store.RoleEndpoint, pe); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	after()
	return nil
}

type cmdUnregister struct {
	call[struct{}]
	instance string
	params   adapterproto.UnregisterParams
}

func (c *cmdUnregister) gate() gate { return gateAdapter }
func (c *cmdUnregister) run(e *Engine, now time.Time) {
	c.done(struct{}{}, e.classify(e.unregisterDirect(now, c.instance, c.params)))
}

// UnregisterDirect closes a direct endpoint of the adapter instance, as the
// adapter protocol's endpoint.unregister does. An endpoint that is not an open
// direct endpoint of that instance is unknown to it.
func (e *Engine) UnregisterDirect(ctx context.Context, instance string, p adapterproto.UnregisterParams) error {
	c := &cmdUnregister{call: newCall[struct{}](), instance: instance, params: p}
	_, err := roundTrip(ctx, e, submitInternal, c, c.call)
	return err
}

func (e *Engine) unregisterDirect(now time.Time, inst string, p adapterproto.UnregisterParams) error {
	if _, ok := e.adm[inst]; !ok {
		return refuse(ReasonInstanceUnknown)
	}
	st, err := e.view.resolve(ByAdapter(inst, p.Endpoint))
	if err != nil {
		return err
	}
	_, err = e.closeEndpoint(now, st.rec.ID, nil)
	return err
}

// ---------------------------------------------------------------------------
// Declaration

// DeclarationPatch is a change of a declaration: what it names changes, and the
// rest stays. A list of senders is replaced as a whole.
type DeclarationPatch struct {
	Paused               *bool
	RefuseMultiRecipient *bool
	RestrictSenders      *bool
	HiddenFromDirectory  *bool
	AcceptFrom           *[]ident.Address
}

// IsEmpty reports whether the patch names nothing.
func (p DeclarationPatch) IsEmpty() bool {
	return p.Paused == nil && p.RefuseMultiRecipient == nil && p.RestrictSenders == nil &&
		p.HiddenFromDirectory == nil && p.AcceptFrom == nil
}

func (p DeclarationPatch) apply(d authz.Declaration) authz.Declaration {
	if p.Paused != nil {
		d.Paused = *p.Paused
	}
	if p.RefuseMultiRecipient != nil {
		d.RefuseMultiRecipient = *p.RefuseMultiRecipient
	}
	if p.RestrictSenders != nil {
		d.RestrictSenders = *p.RestrictSenders
	}
	if p.HiddenFromDirectory != nil {
		d.HiddenFromDirectory = *p.HiddenFromDirectory
	}
	if p.AcceptFrom != nil {
		d.AcceptFrom = append([]ident.Address(nil), *p.AcceptFrom...)
	}
	return d
}

type cmdDeclare struct {
	call[authz.Declaration]
	caller Caller
	patch  DeclarationPatch
}

func (c *cmdDeclare) gate() gate { return gateWrite }
func (c *cmdDeclare) run(e *Engine, _ time.Time) {
	d, err := e.declare(c.caller, c.patch)
	c.done(d, e.classify(err))
}

// Declare changes the declaration of the endpoint the caller is, and returns the
// declaration as it is then. It is the same for every kind of endpoint: it is the
// caller's statement about what its own endpoint takes in, and who may make it is
// for the front end to decide (the Agent interface only for the session endpoint
// of its token).
func (e *Engine) Declare(ctx context.Context, c Caller, p DeclarationPatch) (authz.Declaration, error) {
	cmd := &cmdDeclare{call: newCall[authz.Declaration](), caller: c, patch: p}
	return roundTrip(ctx, e, submitExternal, cmd, cmd.call)
}

func (e *Engine) declare(c Caller, p DeclarationPatch) (authz.Declaration, error) {
	st, err := e.view.resolve(c)
	if err != nil {
		return authz.Declaration{}, err
	}
	if p.IsEmpty() {
		return authz.Declaration{}, &Refusal{Reason: ReasonInvalidArgument, Field: "patch", Detail: DetailEmptyPatch}
	}
	meta := st.meta
	meta.Declaration = p.apply(meta.Declaration)
	if err := checkAcceptFrom(meta.Declaration.AcceptFrom); err != nil {
		return authz.Declaration{}, invalidArgument("accept_from", err)
	}
	rec := st.rec
	if rec.Declaration, err = encodeMeta(meta); err != nil {
		return authz.Declaration{}, err
	}
	if err := e.d.Store.EndpointUpdate(rec); err != nil {
		return authz.Declaration{}, err
	}
	e.view.putEndpoint(rec, meta)
	return cloneDeclaration(meta.Declaration), nil
}

func cloneDeclaration(d authz.Declaration) authz.Declaration {
	d.AcceptFrom = append([]ident.Address(nil), d.AcceptFrom...)
	return d
}

// ---------------------------------------------------------------------------
// Closing an endpoint

// CloseOutcome says what a close did.
type CloseOutcome uint8

// The outcomes.
const (
	// CloseOutcomeClosed: the endpoint is closed; the records queued for it are
	// being closed.
	CloseOutcomeClosed CloseOutcome = iota + 1
	// CloseOutcomeClosing: a session endpoint is closed and its token no longer
	// opens anything; the native session behind it is still to be released.
	CloseOutcomeClosing
	// CloseOutcomeAlreadyClosed: the endpoint was closed before; nothing changed.
	CloseOutcomeAlreadyClosed
)

// String returns the name of the outcome.
func (o CloseOutcome) String() string {
	switch o {
	case CloseOutcomeClosed:
		return "closed"
	case CloseOutcomeClosing:
		return "closing"
	case CloseOutcomeAlreadyClosed:
		return "already_closed"
	}
	return "invalid"
}

// closeOutcomeOf is what closing an endpoint of the kind says.
func closeOutcomeOf(k adapterreg.EndpointKind) CloseOutcome {
	if k == adapterreg.KindSession {
		return CloseOutcomeClosing
	}
	return CloseOutcomeClosed
}

// CloseOperation is the administrative operation that asks for a close. The loop
// makes the record of its result, so the result says what the close did and not
// what the caller expected, and stores it in the transaction of the close.
type CloseOperation struct {
	// Key and Fingerprint identify the request: the same key and fingerprint is a
	// resend, which gets the stored result; the same key with another fingerprint
	// is refused with ReasonOperationIDReused.
	Key         admin.RequestKey
	Fingerprint admin.Fingerprint
	// Name is the name of the operation, as the stored record carries it.
	Name string
	// Encode makes the result, compact JSON, from the outcome of the close. It
	// runs in the loop and must not wait.
	Encode func(CloseOutcome) ([]byte, error)
}

// CloseRequest asks to close an endpoint at the owner's request.
type CloseRequest struct {
	Endpoint ident.EndpointID
	// Operation is the administrative operation that asks for it; its result is
	// stored in the same transaction as the head record of the closing, whatever
	// the outcome, so a crash leaves both or neither. Nil when no operation asks.
	Operation *CloseOperation
}

// CloseResult is the answer to a close.
type CloseResult struct {
	// Outcome is what the close did. It is the zero value when Replayed.
	Outcome CloseOutcome
	// Result is the result of the operation as stored, nil when the request had no
	// operation.
	Result []byte
	// Replayed: the operation had been executed before, and Result is the one
	// stored then; nothing was executed.
	Replayed bool
}

type cmdClose struct {
	call[CloseResult]
	req CloseRequest
}

func (c *cmdClose) gate() gate { return gateWrite }
func (c *cmdClose) run(e *Engine, now time.Time) {
	res, err := e.closeEndpoint(now, c.req.Endpoint, c.req.Operation)
	c.done(res, e.classify(err))
}

// CloseEndpoint closes an endpoint at the owner's request. The head record, which
// closes the endpoint and takes its token away, is committed before this
// returns; the records queued for the endpoint are closed after it, a chunk at a
// time. An endpoint that is not one of the node's is unknown, the system endpoint
// among them; a runtime endpoint stands for its adapter instance and is closed by
// disabling the instance.
//
// With an Operation the command is the one step the administrative rules ask for:
// a resend of a request that was executed returns its stored result, otherwise
// the close is made and the result, built from its outcome, is stored with the
// fingerprint in the same transaction. That holds for every outcome, an endpoint
// that was closed already included.
func (e *Engine) CloseEndpoint(ctx context.Context, r CloseRequest) (CloseResult, error) {
	c := &cmdClose{call: newCall[CloseResult](), req: r}
	return roundTrip(ctx, e, submitExternal, c, c.call)
}

func (e *Engine) closeEndpoint(now time.Time, id ident.EndpointID, op *CloseOperation) (CloseResult, error) {
	if op != nil {
		stored, seen := e.view.results[op.Key]
		var s *admin.StoredRequest
		if seen {
			s = &admin.StoredRequest{Fingerprint: stored.fingerprint}
		}
		switch v := admin.CheckReplay(s, op.Fingerprint); v.Action {
		case admin.ReplayReturnStored:
			return CloseResult{Result: stored.result, Replayed: true}, nil
		case admin.ReplayConflict:
			return CloseResult{}, refuse(ReasonOperationIDReused)
		}
	}
	st := e.view.endpoints[id]
	switch {
	case st == nil:
		return CloseResult{}, refuse(ReasonEndpointUnknown)
	case st.rec.Kind == adapterreg.KindRuntime:
		return CloseResult{}, refuse(ReasonWrongEndpointKind)
	}
	rec := st.rec
	outcome := CloseOutcomeAlreadyClosed
	if rec.Status != store.EndpointClosed {
		rec.Status, rec.Token = store.EndpointClosed, nil
		outcome = closeOutcomeOf(rec.Kind)
	}
	res := CloseResult{Outcome: outcome}
	switch {
	case op != nil:
		stored, err := e.closeWithResult(now, rec, op, outcome)
		if err != nil {
			return CloseResult{}, err
		}
		res.Result = stored
	case outcome != CloseOutcomeAlreadyClosed:
		if err := e.d.Store.EndpointClose(rec, nil); err != nil {
			return CloseResult{}, err
		}
	}
	if outcome != CloseOutcomeAlreadyClosed {
		e.view.putEndpoint(rec, st.meta)
		if e.view.Draining(id) {
			e.later(&cmdDrain{endpoint: id})
		}
	}
	return res, nil
}

// closeWithResult writes the closed record of an endpoint together with the result
// of the operation, built from what the close did, and returns the result. An
// endpoint that was closed already is written again as it is, so that the result
// and its fingerprint are stored all the same.
func (e *Engine) closeWithResult(now time.Time, rec store.EndpointRecord, op *CloseOperation, outcome CloseOutcome) ([]byte, error) {
	result, err := op.Encode(outcome)
	if err != nil {
		return nil, fmt.Errorf("engine: the result of %s: %w", op.Name, err)
	}
	a := store.AdminRecord{
		Key: op.Key, Fingerprint: op.Fingerprint, Op: op.Name, Result: result,
		At: now, RetainUntil: now.Add(store.AdminResultRetention),
	}
	if err := e.d.Store.EndpointClose(rec, &a); err != nil {
		return nil, err
	}
	e.view.putResult(a)
	return result, nil
}

// ---------------------------------------------------------------------------
// Reading

// EndpointView is one endpoint as the owner sees it.
type EndpointView struct {
	ID          ident.EndpointID
	Address     ident.Address
	Kind        adapterreg.EndpointKind
	Instance    string
	Status      store.EndpointStatus
	DisplayName string
	Description string
	Declaration authz.Declaration
	// Queued is how many messages and control signals wait for the endpoint:
	// accepted, and not yet closed or taken.
	Queued int
	// Draining: the endpoint is closed and the records queued for it are not all
	// closed yet.
	Draining bool
}

type cmdEndpoints struct{ call[[]EndpointView] }

func (c *cmdEndpoints) gate() gate { return gateRead }
func (c *cmdEndpoints) run(e *Engine, _ time.Time) {
	out := make([]EndpointView, 0, len(e.view.endpoints))
	for id, st := range e.view.endpoints {
		out = append(out, EndpointView{
			ID: id, Address: ident.Address{Node: e.d.Node, Endpoint: id},
			Kind: st.rec.Kind, Instance: st.rec.Instance, Status: st.rec.Status,
			DisplayName: st.meta.DisplayName, Description: st.meta.Description,
			Declaration: cloneDeclaration(st.meta.Declaration), Queued: len(e.view.queued[id]), Draining: e.view.Draining(id),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID.String() < out[j].ID.String() })
	c.done(out, nil)
}

// Endpoints lists every endpoint of the node that has a record, closed ones
// included, in the order of their identifiers. The system endpoint has no record
// and is not in it.
func (e *Engine) Endpoints(ctx context.Context) ([]EndpointView, error) {
	c := &cmdEndpoints{call: newCall[[]EndpointView]()}
	return roundTrip(ctx, e, submitExternal, c, c.call)
}

// Counts are the numbers the owner's status shows.
type Counts struct {
	// Endpoints is how many endpoints are open or being created, the ones that
	// count against the cap.
	Endpoints int
	// Unfinished is how many deliveries the node holds that still have work behind
	// them: messages waiting to be handed out, accepted control signals and sent
	// messages that no receipt answered.
	Unfinished int
	// Alerts is how many alerts the owner has not acknowledged.
	Alerts int
}

type cmdCounts struct{ call[Counts] }

func (c *cmdCounts) gate() gate { return gateRead }
func (c *cmdCounts) run(e *Engine, _ time.Time) {
	n := 0
	for _, a := range e.view.alerts {
		if a.Acked == nil {
			n++
		}
	}
	c.done(Counts{Endpoints: e.view.OpenEndpoints(), Unfinished: e.view.Unfinished(), Alerts: n}, nil)
}

// Counts returns the numbers of the node's status.
func (e *Engine) Counts(ctx context.Context) (Counts, error) {
	c := &cmdCounts{call: newCall[Counts]()}
	return roundTrip(ctx, e, submitExternal, c, c.call)
}
