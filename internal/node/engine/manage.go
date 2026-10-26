package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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
	var wait *waitForDrain
	if errors.As(err, &wait) {
		// The operation cannot run until the records of an endpoint are closed: it is
		// run again from the start when they are, and answered then.
		e.parkUntilDrained(wait.endpoint, c)
		return
	}
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
	if p, ok := c.Operation.(preparedOperation); ok {
		cmd := &cmdPrepare{call: newCall[StoredResult](), cmd: c, op: p}
		return roundTrip(ctx, e, submitExternal, cmd, cmd.call)
	}
	cmd := &cmdExecute{call: newCall[StoredResult](), cmd: c}
	return roundTrip(ctx, e, submitExternal, cmd, cmd.call)
}

// replayOf is the replay check of the administrative rules, made against every
// stored result: the stored result of the same request (nothing is to run), a
// refusal for the identifier of another request, or neither, in which case the
// operation is to run. It is the one place the check is made.
func (e *Engine) replayOf(c ManagementCommand) (res StoredResult, answered bool, err error) {
	var seen *admin.StoredRequest
	stored, ok := e.view.results[c.Key]
	if ok {
		seen = &admin.StoredRequest{Fingerprint: stored.fingerprint}
	}
	switch v := admin.CheckReplay(seen, c.Fingerprint); v.Action {
	case admin.ReplayReturnStored:
		return StoredResult{Result: stored.result, Replayed: true, Stop: c.Operation.stops()}, true, nil
	case admin.ReplayConflict:
		return StoredResult{}, true, refuse(ReasonOperationIDReused)
	}
	return StoredResult{}, false, nil
}

func (e *Engine) execute(now time.Time, c ManagementCommand) (StoredResult, error) {
	if res, answered, err := e.replayOf(c); answered {
		return res, err
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

// waitForDrain is what an operation returns when it cannot run until the records
// queued for an endpoint are closed. The command that carries it is held until
// then and run again; the operation is not carried out in part.
type waitForDrain struct{ endpoint ident.EndpointID }

func (w *waitForDrain) Error() string {
	return "engine: waiting for the records of endpoint " + w.endpoint.String() + " to be closed"
}

// preparedOperation is an operation that has work to do outside the loop before
// the loop may commit it: the copy of an adapter package, which takes as long as
// the package is large and must not hold up the loop (the adapters' notifications
// are held in a buffer that the loop's stall must stay short enough for). The
// engine runs it in this order: the loop looks the request up in the stored results
// (a request that was done is answered without any preparation, so that nothing is
// copied again), takes the one place of the install worker, and starts the worker;
// the worker prepares, then has the loop run the operation as an ordinary one,
// which looks the request up again, because an identical resend may have come
// between the two, and commits; the worker then settles whatever the preparation
// left, whether the commit happened or not, and only then is the request answered.
type preparedOperation interface {
	Operation
	// prepare runs in the install worker, outside the loop. rnd is what the loop
	// drew for it: the worker draws no randomness of its own.
	prepare(ctx context.Context, e *Engine, rnd io.Reader)
	// settle runs in the install worker after the loop has answered the command
	// that carried the operation: it drops what the preparation left that the commit
	// did not take over. It is not called after a fatal failure of the disk: nothing
	// more is written then.
	settle(e *Engine)
}

type cmdPrepare struct {
	call[StoredResult]
	cmd ManagementCommand
	op  preparedOperation
}

func (c *cmdPrepare) gate() gate { return gateWrite }
func (c *cmdPrepare) run(e *Engine, _ time.Time) {
	if res, answered, err := e.replayOf(c.cmd); answered {
		c.done(res, err)
		return
	}
	if !e.installing.CompareAndSwap(false, true) {
		c.fail(refuse(ReasonInstallInProgress))
		return
	}
	var drawn [config.StageSuffixBytes]byte
	if _, err := io.ReadFull(e.randomReader(), drawn[:]); err != nil {
		e.installing.Store(false)
		c.fail(fmt.Errorf("engine: the randomness for a staging directory: %w", err))
		return
	}
	if !e.spawn(func(ctx context.Context) { c.done(e.runPrepared(ctx, c, drawn[:])) }) {
		e.installing.Store(false)
		c.fail(refuse(ReasonShuttingDown))
	}
}

// runPrepared is the install worker's whole task. It holds the place of the
// worker until it has settled, so that the next installation does not start while
// this one's staging is still on the disk. After a fatal failure of the disk it
// writes nothing more, neither the preparation nor the settling: the next start
// removes what is left. A copy that is under way when the failure comes finishes,
// because the staging does not take a context; the settling that follows it is what
// does not happen.
func (e *Engine) runPrepared(ctx context.Context, c *cmdPrepare, drawn []byte) (StoredResult, error) {
	defer e.installing.Store(false)
	if e.diskFailed(nil) {
		return StoredResult{}, refuse(ReasonStorageFatal)
	}
	c.op.prepare(ctx, e, bytes.NewReader(drawn))
	run := &cmdExecute{call: newCall[StoredResult](), cmd: c.cmd}
	res, err := roundTrip(ctx, e, submitInternal, run, run.call)
	if !e.diskFailed(err) {
		c.op.settle(e)
	}
	return res, err
}

// diskFailed says whether the engine has met a fatal failure of the disk, which is
// the end of everything it writes: it is in the failed phase, or err, the answer to
// a command, says the command met it and the loop is about to fail.
func (e *Engine) diskFailed(err error) bool {
	if r, ok := ReasonOf(err); ok && r == ReasonStorageFatal {
		return true
	}
	return e.Phase() == PhaseFailed
}
