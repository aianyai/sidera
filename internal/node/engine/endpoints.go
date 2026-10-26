package engine

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/txn"
	"github.com/aianyai/sidera/internal/publication"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

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

// invalidParams is the refusal for parameters that failed their own Validate: the
// field is the one the protocol's validation names.
func invalidParams(err error) *Refusal {
	field := ""
	var ve *adapterproto.ValidationError
	if errors.As(err, &ve) {
		field = ve.Field
	}
	return invalidArgument(field, err)
}

func (e *Engine) registerDirect(inst string, p adapterproto.RegisterParams) (adapterproto.RegisterResult, error) {
	var none adapterproto.RegisterResult
	adm, ok := e.admission(inst)
	switch {
	case !ok:
		return none, refuse(ReasonInstanceUnknown)
	case !adm.Declares(adapterreg.KindDirect):
		return none, refuse(ReasonWrongEndpointKind)
	}
	if err := p.Validate(); err != nil {
		return none, invalidParams(err)
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

	st := e.view.endpoints[ch.ID]
	if st != nil && st.rec.Kind != adapterreg.KindDirect {
		return none, errors.New("engine: a stable id of a direct endpoint is the id of another kind of endpoint")
	}
	if st != nil && st.rec.Status == store.EndpointOpen {
		if p.DisplayName == st.rec.DisplayName && p.Description == st.rec.Description {
			return ref, nil
		}
		rec := st.rec
		rec.DisplayName, rec.Description = p.DisplayName, p.Description
		if err := e.d.Store.EndpointUpdate(rec); err != nil {
			return none, err
		}
		e.putEndpoint(rec, st.decl)
		return ref, nil
	}

	var decl authz.Declaration
	if st != nil {
		if e.view.Draining(ch.ID) {
			return none, &drainingError{ch.ID}
		}
		decl = st.decl
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
	rec := store.EndpointRecord{
		ID: ch.ID, Kind: adapterreg.KindDirect, Instance: inst,
		DisplayName: p.DisplayName, Description: p.Description,
		Status: store.EndpointOpen, Token: token,
	}
	if rec.Declaration, err = encodeDeclaration(decl); err != nil {
		return none, err
	}
	if err := e.commitEndpointAndID(&declDirectRegister, ch, rec); err != nil {
		return none, err
	}
	e.putEndpoint(rec, decl)
	return ref, nil
}

// commitEndpointAndID is the transaction that makes an endpoint of an adapter
// instance exist (direct-register, runtime-admit): the id in config/, when it is
// new, and the record in state/, in one transaction, which the engine opens
// because it writes the files of two owners. Everything that can be refused is
// checked before Begin.
func (e *Engine) commitEndpointAndID(decl *txn.Decl, ch config.IDChange, rec store.EndpointRecord) error {
	pe, err := store.PrepareEndpoint(rec)
	if err != nil {
		return err
	}
	tx := e.d.Writer.Begin(decl)
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
	if _, ok := e.admission(inst); !ok {
		return refuse(ReasonInstanceUnknown)
	}
	st, err := e.view.resolve(ByAdapter(inst, p.Endpoint))
	if err != nil {
		return err
	}
	_, _, err = e.closeEndpoint(now, st.rec.ID, nil)
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
	decl := p.apply(st.decl)
	if err := checkAcceptFrom(decl.AcceptFrom); err != nil {
		return authz.Declaration{}, invalidArgument("accept_from", err)
	}
	rec := st.rec
	if rec.Declaration, err = encodeDeclaration(decl); err != nil {
		return authz.Declaration{}, err
	}
	if err := e.d.Store.EndpointUpdate(rec); err != nil {
		return authz.Declaration{}, err
	}
	e.putEndpoint(rec, decl)
	return cloneDeclaration(decl), nil
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

// CloseRequest asks to close an endpoint.
type CloseRequest struct {
	Endpoint ident.EndpointID
}

// CloseResult is the answer to a close.
type CloseResult struct {
	// Outcome is what the close did.
	Outcome CloseOutcome
}

type cmdClose struct {
	call[CloseResult]
	req CloseRequest
}

func (c *cmdClose) gate() gate { return gateWrite }
func (c *cmdClose) run(e *Engine, now time.Time) {
	outcome, _, err := e.closeEndpoint(now, c.req.Endpoint, nil)
	c.done(CloseResult{Outcome: outcome}, e.classify(err))
}

// CloseEndpoint closes an endpoint for the engine's own features, with no
// administrative request behind it; the owner's request to close is the
// operation CloseEndpointOp, which comes through Execute so that its result is
// stored. The head record, which closes the endpoint and takes its token away, is
// committed before this returns; the records queued for the endpoint are closed
// after it, a chunk at a time. An endpoint that is not one of the node's is
// unknown, the system endpoint among them; a runtime endpoint stands for its
// adapter instance and is closed by disabling the instance.
func (e *Engine) CloseEndpoint(ctx context.Context, r CloseRequest) (CloseResult, error) {
	c := &cmdClose{call: newCall[CloseResult](), req: r}
	return roundTrip(ctx, e, submitExternal, c, c.call)
}

// closeVia is how a close that an administrative operation asks for stores its
// result: the result is built from the outcome the close reached, and written in
// the transaction of the head record.
type closeVia struct {
	x      *opRun
	encode func(CloseOutcome) ([]byte, error)
}

// closeEndpoint closes the endpoint and says what it did. With via, the result of
// the operation is stored in the transaction of the head record, as its last
// write, whatever the outcome; the result is returned too. A crash between the
// two leaves the endpoint closed without the result, and the resend then says it
// was closed already.
func (e *Engine) closeEndpoint(now time.Time, id ident.EndpointID, via *closeVia) (CloseOutcome, []byte, error) {
	st := e.view.endpoints[id]
	switch {
	case st == nil:
		return 0, nil, refuse(ReasonEndpointUnknown)
	case st.rec.Kind == adapterreg.KindRuntime:
		return 0, nil, refuse(ReasonWrongEndpointKind)
	}
	rec := st.rec
	outcome := CloseOutcomeAlreadyClosed
	if rec.Status != store.EndpointClosed {
		rec.Status, rec.Token = store.EndpointClosed, nil
		outcome = closeOutcomeOf(rec.Kind)
	}
	var result []byte
	switch {
	case via != nil:
		var err error
		if result, err = e.closeWithResult(rec, via, outcome); err != nil {
			return 0, nil, err
		}
	case outcome != CloseOutcomeAlreadyClosed:
		if err := e.d.Store.EndpointClose(rec, nil); err != nil {
			return 0, nil, err
		}
	}
	if outcome != CloseOutcomeAlreadyClosed {
		e.putEndpoint(rec, st.decl)
		if e.view.Draining(id) {
			e.later(&cmdDrain{endpoint: id})
		}
	}
	return outcome, result, nil
}

// closeWithResult writes the closed record of an endpoint together with the result
// of the operation, built from what the close did, and returns the result. An
// endpoint that was closed already is written again as it is, so that the result
// and its fingerprint are stored all the same.
func (e *Engine) closeWithResult(rec store.EndpointRecord, via *closeVia, outcome CloseOutcome) ([]byte, error) {
	result, err := via.encode(outcome)
	if err != nil {
		return nil, fmt.Errorf("engine: the result of %s: %w", via.x.name, err)
	}
	a := via.x.record(result)
	if err := e.d.Store.EndpointClose(rec, &a); err != nil {
		return nil, err
	}
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

func (c *cmdEndpoints) gate() gate                 { return gateRead }
func (c *cmdEndpoints) run(e *Engine, _ time.Time) { c.done(e.endpointViews(), nil) }

// endpointViews are the endpoints of the node as the owner sees them, in the
// order of their identifiers.
func (e *Engine) endpointViews() []EndpointView {
	out := make([]EndpointView, 0, len(e.view.endpoints))
	for id, st := range e.view.endpoints {
		out = append(out, EndpointView{
			ID: id, Address: ident.Address{Node: e.d.Node, Endpoint: id},
			Kind: st.rec.Kind, Instance: st.rec.Instance, Status: st.rec.Status,
			DisplayName: st.rec.DisplayName, Description: st.rec.Description,
			Declaration: cloneDeclaration(st.decl), Queued: len(e.view.queued[id]), Draining: e.view.Draining(id),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID.String() < out[j].ID.String() })
	return out
}

// Endpoints lists every endpoint of the node that has a record, closed ones
// included, in the order of their identifiers. The system endpoint has no record
// and is not in it.
func (e *Engine) Endpoints(ctx context.Context) ([]EndpointView, error) {
	c := &cmdEndpoints{call: newCall[[]EndpointView]()}
	return roundTrip(ctx, e, submitExternal, c, c.call)
}

// putEndpoint records an endpoint as it is on disk now. A session endpoint that is
// closed holds no state of its session any more: the checkpoint the adapter kept
// for it is removed after the command that closed the endpoint, whichever of the
// ways to close one it was, and a checkpoint a crash left is removed by the scan
// entry (store.ClosedSessionCheckpoints).
func (e *Engine) putEndpoint(rec store.EndpointRecord, decl authz.Declaration) {
	e.view.putEndpoint(rec, decl)
	for key := range e.view.checkpoints {
		if store.CheckpointOfClosedSession(key, rec) {
			e.later(&cmdCheckpointDrop{key: key})
		}
	}
}

// cmdCheckpointDrop removes the checkpoint of a closed session.
type cmdCheckpointDrop struct{ key store.CheckpointKey }

func (c *cmdCheckpointDrop) gate() gate { return gateAlways }
func (c *cmdCheckpointDrop) fail(error) {}
func (c *cmdCheckpointDrop) run(e *Engine, _ time.Time) {
	if _, ok := e.view.checkpoints[c.key]; !ok {
		return
	}
	if err := e.dropCheckpoint(c.key); err != nil && e.fatal == nil {
		e.log.Warn("the state kept for a closed session could not be removed; the next start removes it", "error", err.Error())
	}
}

// dropCheckpoint removes a checkpoint from the disk and from the view: the one place the
// engine removes one, for the state of a closed session and for the checkpoint of an
// attempt that has settled.
func (e *Engine) dropCheckpoint(key store.CheckpointKey) error {
	if err := e.classify(e.d.Store.CheckpointDrop([]store.CheckpointKey{key})); err != nil {
		return err
	}
	delete(e.view.checkpoints, key)
	return nil
}
