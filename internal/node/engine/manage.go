package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/store"
)

// This file is the one door that every administrative operation of the owner
// goes through, and the one place the order the administrative rules fix for a
// side-effecting request is carried out. The management interface has already
// judged who asks (admin.Authorize) and whether the request is well formed
// (admin.ValidateRequest); the loop then does the rest as one command, so that no
// other change comes between the lookup of the identifier and the storing of the
// result, which is what keeps two concurrent resends of a request from both
// running.
//
// An operation is a value of a type of this package: its effect needs the view,
// the store and the configuration, which only the loop may touch, so the type is
// sealed. The package that owns an operation's effect adds its type here; the door
// is the same for all.

// Operation is what a management request asks the node to do. Its effect and the
// storing of its result are one transaction of the transaction layer, whichever
// place keeps the result: the record under state/admin/, the last write of the
// transaction, for an operation that has no file of its own to carry it, and the
// same replacement as the change for an operation that changes a versioned file.
// The two are not equal in a crash: a replacement is whole, but the transaction
// commits with its first write, so a crash after the effect is durable and before
// the record is written leaves the effect without its result. The resend then
// carries the operation out again and gets the answer of an operation that finds
// its effect there (closing an endpoint that is closed says already_closed); an
// operation is written so that doing it again is harmless.
type Operation interface {
	// Admin is the catalogue entry the operation is authorized as. It is
	// side-effecting: what only reads, and the export of the revocation, which
	// must never be stored, do not come through the door.
	Admin() admin.Operation

	// run makes the effect and stores the result with the fingerprint in the one
	// transaction, and returns the result. It runs in the loop and must not wait.
	// The expected version of a versioned setting is checked inside the change
	// that writes it, by the package that owns the file (admin.CheckVersion,
	// config's checkVersion), because the check and the write are one step.
	run(e *Engine, x *opRun) ([]byte, error)
	// stops reports whether the daemon is to shut down once the result is stored.
	stops() bool
}

// ManagementCommand is one side-effecting management request.
type ManagementCommand struct {
	// Key is where the result is filed: admin.KeyFor(actor, id) of an actor that
	// admin.Authorize allowed.
	Key admin.RequestKey
	// Fingerprint is the digest of the whole request. The same key with the same
	// fingerprint is a resend; with another fingerprint, a reuse of the identifier.
	Fingerprint admin.Fingerprint
	// Expected is the version of the setting the sender last saw, for an operation
	// that changes a versioned setting, and zero for one that carries none.
	Expected admin.Version
	// Name is the name of the operation as the stored result carries it.
	Name string
	// Operation is what is asked.
	Operation Operation
}

// StoredResult is the answer to a management request.
type StoredResult struct {
	// Result is the result of the operation: compact JSON, the one stored.
	Result []byte
	// Replayed: the request had been executed before, and Result is the one stored
	// then; nothing was executed.
	Replayed bool
	// Stop: the daemon is to shut down. It follows the operation of the request,
	// not the result: the resend of a request that stops the daemon asks for the
	// stop again, so a daemon that died between storing the result and stopping is
	// stopped by the resend. The engine does not control the process; the
	// interface that gets this calls the stop entry of the composition root.
	Stop bool
}

// opRun is what an operation is told of the command that runs it.
type opRun struct {
	now         time.Time
	key         admin.RequestKey
	fingerprint admin.Fingerprint
	expected    admin.Version
	name        string
}

// record is the stored form of the result of an operation that keeps its result
// under state/admin/, for the transaction of its effect to write.
func (x *opRun) record(result []byte) store.AdminRecord {
	return store.AdminRecord{
		Key: x.key, Fingerprint: x.fingerprint, Op: x.name, Result: result,
		At: x.now, RetainUntil: x.now.Add(store.AdminResultRetention),
	}
}

type cmdExecute struct {
	call[StoredResult]
	cmd ManagementCommand
}

func (c *cmdExecute) gate() gate { return gateWrite }
func (c *cmdExecute) run(e *Engine, now time.Time) {
	res, err := e.execute(now, c.cmd)
	c.done(res, e.classify(err))
}

// Execute carries out a side-effecting management request: the stored result of
// the same request is returned and nothing runs; the identifier of another
// request is refused with ReasonOperationIDReused; otherwise the operation runs,
// its result is stored with the fingerprint in the transaction of its effect, and
// the engine remembers it. The lookup covers every place a result is kept: the
// results under state/admin/ and the operation each versioned file of config/
// records, whose key spaces do not overlap. A request that is refused stores
// nothing, so its identifier stays free.
func (e *Engine) Execute(ctx context.Context, c ManagementCommand) (StoredResult, error) {
	switch {
	case c.Operation == nil:
		return StoredResult{}, errors.New("engine: a management command without an operation")
	case !c.Operation.Admin().SideEffecting():
		return StoredResult{}, errors.New("engine: only an operation that changes state comes through Execute")
	case c.Key.ID.IsZero():
		return StoredResult{}, errors.New("engine: a management command without an operation identifier")
	}
	cmd := &cmdExecute{call: newCall[StoredResult](), cmd: c}
	return roundTrip(ctx, e, submitExternal, cmd, cmd.call)
}

func (e *Engine) execute(now time.Time, c ManagementCommand) (StoredResult, error) {
	var seen *admin.StoredRequest
	stored, ok := e.view.results[c.Key]
	if ok {
		seen = &admin.StoredRequest{Fingerprint: stored.fingerprint}
	}
	switch v := admin.CheckReplay(seen, c.Fingerprint); v.Action {
	case admin.ReplayReturnStored:
		return StoredResult{Result: stored.result, Replayed: true, Stop: c.Operation.stops()}, nil
	case admin.ReplayConflict:
		return StoredResult{}, refuse(ReasonOperationIDReused)
	}
	x := &opRun{now: now, key: c.Key, fingerprint: c.Fingerprint, expected: c.Expected, name: c.Name}
	result, err := c.Operation.run(e, x)
	if err != nil {
		return StoredResult{}, err
	}
	e.view.putStored(c.Key, c.Fingerprint, result)
	return StoredResult{Result: result, Stop: c.Operation.stops()}, nil
}

type cmdOperationResult struct {
	call[operationResult]
	key admin.RequestKey
}

type operationResult struct {
	result []byte
	found  bool
}

func (c *cmdOperationResult) gate() gate { return gateRead }
func (c *cmdOperationResult) run(e *Engine, _ time.Time) {
	stored, ok := e.view.results[c.key]
	c.done(operationResult{result: stored.result, found: ok}, nil)
}

// OperationResult is the result stored for the request key, wherever it is kept
// (state/admin/ or a versioned file of config/), and whether there is one. The
// key is the one the request was filed under, admin.KeyFor of the requester: a
// result of another requester is under another key and is never found. It reads
// and writes nothing, and is taken while the node starts.
func (e *Engine) OperationResult(ctx context.Context, key admin.RequestKey) ([]byte, bool, error) {
	c := &cmdOperationResult{call: newCall[operationResult](), key: key}
	r, err := roundTrip(ctx, e, submitExternal, c, c.call)
	return r.result, r.found, err
}

// addConfigResults puts the results that the versioned files of config/ record
// into the table the lookup reads, beside those of state/admin/.
func (v *view) addConfigResults(ops []config.Operation) {
	for _, op := range ops {
		v.putStored(op.Key(), op.Fingerprint, op.Result)
	}
}

// CloseEndpointOp is the operation that closes an endpoint at the owner's
// request. encode makes the result from what the close did, so the words of the
// wire stay with the interface; it runs in the loop and must not wait. The head
// record of the closing and the result are written in one transaction, in every
// outcome, an endpoint that was closed already included.
func CloseEndpointOp(endpoint ident.EndpointID, encode func(CloseOutcome) ([]byte, error)) Operation {
	return &closeEndpointOp{endpoint: endpoint, encode: encode}
}

type closeEndpointOp struct {
	endpoint ident.EndpointID
	encode   func(CloseOutcome) ([]byte, error)
}

func (o *closeEndpointOp) Admin() admin.Operation {
	return admin.Operation{Kind: admin.OpManageEndpointSession}
}
func (o *closeEndpointOp) stops() bool { return false }
func (o *closeEndpointOp) run(e *Engine, x *opRun) ([]byte, error) {
	_, result, err := e.closeEndpoint(x.now, o.endpoint, &closeVia{x: x, encode: o.encode})
	return result, err
}

// StopDaemonOp is the operation that asks the daemon to shut down normally. Its
// only effect is its stored result; the stop itself is the composition root's,
// asked for through StoredResult.Stop once the result is stored. encode makes the
// result; it runs in the loop and must not wait.
func StopDaemonOp(encode func() ([]byte, error)) Operation { return &stopDaemonOp{encode: encode} }

type stopDaemonOp struct{ encode func() ([]byte, error) }

func (o *stopDaemonOp) Admin() admin.Operation { return admin.Operation{Kind: admin.OpStopDaemon} }
func (o *stopDaemonOp) stops() bool            { return true }
func (o *stopDaemonOp) run(e *Engine, x *opRun) ([]byte, error) {
	result, err := o.encode()
	if err != nil {
		return nil, fmt.Errorf("engine: the result of %s: %w", x.name, err)
	}
	if err := e.d.Store.DaemonStop(x.record(result)); err != nil {
		return nil, err
	}
	return result, nil
}
