package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/txn"
	"github.com/aianyai/sidera/internal/pairing"
	"github.com/aianyai/sidera/internal/record"
)

// The owner's operations on trust: entering and forgetting an expectation, asking
// another node for a pairing, removing a node from trust and taking it back. Each
// comes through the one door of manage.go, so the replay check of a request is the
// door's alone: the pairing package is called with no stored results (see
// pairingWorld). The effect of each operation and its result are one transaction
// (transactions.go); an operation whose effect writes nothing stores its result
// alone (declTrustResult), so the identifier is spent either way.

// trustOp is what the owner's operations on trust share.
type trustOp struct{ kind admin.OpKind }

func (o trustOp) Admin() admin.Operation { return admin.Operation{Kind: o.kind} }
func (trustOp) stops() bool              { return false }

// pairingRequest is who asks and under which identifier, for the state machine: the
// actor the management interface made for the request.
func (x *opRun) pairingRequest() pairing.Request {
	return pairing.Request{Actor: x.actor, ID: x.key.ID}
}

// pairingRefusal turns what the state machine said of a command into the refusal of
// the operation, or nil when the command did what it was asked. A command that is
// not an execution is a refusal and stores nothing, so its identifier stays free.
//
// The loop authorized and validated the request before the state machine saw it,
// and gave it no stored results, so a disposition other than executed is a
// disagreement between the two and fails loudly. Of the results, those that say the
// node it names cannot be used are invalid arguments; those that say the node cannot
// do it now (a pause after failed verifications, a pairing under way with that
// node, a full list of expectations) each have a reason of their own. invalid is the
// field a result of ResultInvalid is about.
func (e *Engine) pairingRefusal(out pairing.Outcome, invalid string) error {
	if out.Disposition != pairing.DispositionExecuted {
		return fmt.Errorf("engine: the pairing did not execute a command the engine had authorized and checked (disposition %d, reason %q)", out.Disposition, out.Reason)
	}
	switch out.Result {
	case pairing.ResultOK:
		return nil
	case pairing.ResultInvalid:
		return invalidArgument(invalid, fmt.Errorf("the pairing refused it as %s", out.Result))
	case pairing.ResultNotFound, pairing.ResultNotAccepted, pairing.ResultWrongPeer:
		return invalidArgument("node", fmt.Errorf("the pairing refused it as %s", out.Result))
	case pairing.ResultPaused:
		return &Refusal{Reason: ReasonPairingPaused, RetryAfter: out.RetryAfter, Err: fmt.Errorf("the pairing refused it as %s", out.Result)}
	case pairing.ResultInProgress:
		return &Refusal{Reason: ReasonPairingInProgress, Err: fmt.Errorf("the pairing refused it as %s", out.Result)}
	case pairing.ResultFull:
		return &Refusal{Reason: ReasonExpectationsFull, Err: fmt.Errorf("the pairing refused it as %s", out.Result)}
	}
	return fmt.Errorf("engine: a pairing command ended as %s, which none of the owner's operations on pairing can", out.Result)
}

// ---------------------------------------------------------------------------
// Expectations

// NodeExpected is what entering an expectation did, for the result to say.
type NodeExpected struct {
	Node ident.NodeID
	// ExpiresAt is when the expectation stops standing.
	ExpiresAt time.Time
}

// ExpectNodeOp is the operation that enters an expectation for a node: the owner
// means to pair with it, which stands for a confirmation made in advance. Entering
// the node again replaces the earlier expectation. encode makes the result; it runs
// in the loop and must not wait.
func ExpectNodeOp(node ident.NodeID, encode func(NodeExpected) ([]byte, error)) Operation {
	return &expectNodeOp{trustOp{admin.OpConfirmTrust}, node, encode}
}

type expectNodeOp struct {
	trustOp
	node   ident.NodeID
	encode func(NodeExpected) ([]byte, error)
}

func (o *expectNodeOp) run(e *Engine, x *opRun) ([]byte, error) {
	w := e.pairingWorld()
	return e.changeExpectations(x, &declTrustExpect, func(next pairing.State) ([]byte, error) {
		for _, ex := range next.Expectations() {
			if ex.Node == o.node {
				return o.encode(NodeExpected{Node: o.node, ExpiresAt: ex.ExpiresAt(w.Params)})
			}
		}
		return nil, fmt.Errorf("engine: the expectation for %s is not in the list that was just entered", o.node)
	}, func() (pairing.State, pairing.Outcome, []pairing.Action, error) {
		return e.pairing.state.Expect(w, x.pairingRequest(), o.node, x.now)
	})
}

// ForgetNodeOp is the operation that deletes the expectation for a node. A node with
// no expectation is refused. encode makes the result; it runs in the loop and must
// not wait.
func ForgetNodeOp(node ident.NodeID, encode func(ident.NodeID) ([]byte, error)) Operation {
	return &forgetNodeOp{trustOp{admin.OpRejectTrustRequest}, node, encode}
}

type forgetNodeOp struct {
	trustOp
	node   ident.NodeID
	encode func(ident.NodeID) ([]byte, error)
}

func (o *forgetNodeOp) run(e *Engine, x *opRun) ([]byte, error) {
	w := e.pairingWorld()
	return e.changeExpectations(x, &declTrustForget, func(pairing.State) ([]byte, error) {
		return o.encode(o.node)
	}, func() (pairing.State, pairing.Outcome, []pairing.Action, error) {
		return e.pairing.state.Forget(w, x.pairingRequest(), o.node, x.now)
	})
}

// changeExpectations is the run of both operations on expectations: the state
// machine's command, the list of expectations it asks to persist and the result, one
// transaction; the state is taken only after it is durable. A command that is a
// refusal writes nothing.
func (e *Engine) changeExpectations(x *opRun, decl *txn.Decl, result func(next pairing.State) ([]byte, error),
	command func() (pairing.State, pairing.Outcome, []pairing.Action, error)) ([]byte, error) {
	next, out, acts, err := command()
	if err != nil {
		return nil, err
	}
	if err := e.pairingRefusal(out, "node"); err != nil {
		return nil, err
	}
	if len(acts) != 1 {
		return nil, fmt.Errorf("engine: a command on expectations returned %d actions, want one", len(acts))
	}
	persist, ok := acts[0].(pairing.PersistExpectations)
	if !ok {
		return nil, fmt.Errorf("engine: a command on expectations returned %T, want the list to persist", acts[0])
	}
	body, err := result(next)
	if err != nil {
		return nil, fmt.Errorf("engine: the result of %s: %w", x.name, err)
	}
	list, err := store.PreparePairingExpectations(persist.Expectations)
	if err != nil {
		return nil, err
	}
	pr, err := store.PrepareAdminResult(x.record(body))
	if err != nil {
		return nil, err
	}
	tx := e.d.Writer.Begin(decl)
	defer tx.Close()
	if err := store.PutPairingExpectations(tx, store.RolePairingExpectations, list); err != nil {
		return nil, err
	}
	if err := store.PutAdminResult(tx, store.RoleResult, pr); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	e.pairing.state = next
	return body, nil
}

// ---------------------------------------------------------------------------
// Rejecting a request

// PairingRejected is what rejecting a request did, for the result to say: the
// request as it is now, which is one this node holds as a target or one it started.
type PairingRejected struct {
	// Incoming is the request this node held as a target and rejected; nil when the
	// request was this node's own.
	Incoming *pairing.TargetRecord
	// Outgoing is the request this node started and withdrew; nil when the request
	// was one it held as a target.
	Outgoing *pairing.InitiatorRecord
}

// RejectPairingOp is the operation that ends a request for a pairing because its
// owner does not want it: a request this node holds as a target is rejected, and one
// it started is withdrawn, which the other node learns with the next exchange of the
// result. It is authorized as the state machine authorizes it, as rejecting a trust
// request. A request that no longer waits is refused like one that is not there.
// The node may not sign while its clock or identity is in doubt, and withdrawing a
// request may store a revocation, so the operation is refused then. encode makes
// the result; it runs in the loop and must not wait.
func RejectPairingOp(id pairing.ID, encode func(PairingRejected) ([]byte, error)) Operation {
	return &rejectPairingOp{trustOp{admin.OpRejectTrustRequest}, id, encode}
}

type rejectPairingOp struct {
	trustOp
	id     pairing.ID
	encode func(PairingRejected) ([]byte, error)
}

func (o *rejectPairingOp) run(e *Engine, x *opRun) ([]byte, error) {
	if err := e.canSign(); err != nil {
		return nil, err
	}
	next, out, acts, err := e.pairing.state.Reject(e.pairingWorld(), x.pairingRequest(), o.id, pairing.WithdrawDeclined, x.now)
	if err != nil {
		return nil, err
	}
	if out.Disposition == pairing.DispositionExecuted {
		switch out.Result {
		case pairing.ResultNotFound, pairing.ResultWrongState, pairing.ResultExpired:
			return nil, invalidArgument("pairing_id", fmt.Errorf("the pairing refused it as %s", out.Result))
		}
	}
	if err := e.pairingRefusal(out, "pairing_id"); err != nil {
		return nil, err
	}
	return e.persistRejection(x, o, next, acts)
}

// persistRejection stores the rejected request and the result in one transaction
// and then adopts the state and tells what the state machine says to tell. A
// revocation that a withdrawal makes is stored first, as the state machine orders:
// it is idempotent, so a crash before the transaction leaves a request the resend
// rejects again.
func (e *Engine) persistRejection(x *opRun, o *rejectPairingOp, next pairing.State, acts []pairing.Action) ([]byte, error) {
	var rejected PairingRejected
	var notices []pairing.Action
	for _, a := range acts {
		switch a := a.(type) {
		case pairing.StoreRevocation:
			if err := e.pairingPersist(a, nil); err != nil {
				return nil, err
			}
		case pairing.PersistTarget:
			rec := a.Record
			rejected.Incoming = &rec
		case pairing.PersistInitiator:
			rec := a.Record
			rejected.Outgoing = &rec
		case pairing.Notify:
			notices = append(notices, a)
		default:
			return nil, fmt.Errorf("engine: rejecting a request returned %T, which it cannot", a)
		}
	}
	if (rejected.Incoming == nil) == (rejected.Outgoing == nil) {
		return nil, errors.New("engine: rejecting a request must persist exactly one request")
	}
	body, err := o.encode(rejected)
	if err != nil {
		return nil, fmt.Errorf("engine: the result of %s: %w", x.name, err)
	}
	pr, err := store.PrepareAdminResult(x.record(body))
	if err != nil {
		return nil, err
	}
	tx := e.d.Writer.Begin(&declTrustReject)
	defer tx.Close()
	if rec := rejected.Incoming; rec != nil {
		prepared, err := store.PreparePairingTarget(*rec)
		if err != nil {
			return nil, err
		}
		if err := store.PutPairingTarget(tx, store.RolePairingTarget, prepared); err != nil {
			return nil, err
		}
	} else {
		prepared, err := store.PreparePairingInitiator(*rejected.Outgoing)
		if err != nil {
			return nil, err
		}
		if err := store.PutPairingInitiator(tx, store.RolePairingInitiator, prepared); err != nil {
			return nil, err
		}
	}
	if err := store.PutAdminResult(tx, store.RoleResult, pr); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	e.pairing.state = next
	for _, n := range notices {
		e.pairingIO(n)
	}
	// A withdrawal is told to the target by the next exchange, which a tick asks for.
	e.pairingSchedule(x.now)
	return body, nil
}

// ---------------------------------------------------------------------------
// Removing a node from trust and taking it back

// NodeRemoved is what removing a node from this node's trust did, for the result to
// say.
type NodeRemoved struct {
	Node ident.NodeID
	// Already: the node was removed before, and its entry is as it was.
	Already bool
	// Credentials are the credentials this node revoked now, by digest.
	Credentials []record.Digest
}

// RemoveNodeOp is the operation that removes a node from this node's trust: the
// node is refused whatever path leads to it, and the credentials this node holds
// with it are revoked with its identity key. The other end is not told. encode makes
// the result; it runs in the loop and must not wait.
func RemoveNodeOp(node ident.NodeID, encode func(NodeRemoved) ([]byte, error)) Operation {
	return &removeNodeOp{trustOp{admin.OpLocallyRevokeNode}, node, encode}
}

type removeNodeOp struct {
	trustOp
	node   ident.NodeID
	encode func(NodeRemoved) ([]byte, error)
}

func (o *removeNodeOp) run(e *Engine, x *opRun) ([]byte, error) {
	return e.revokeNodeLocally(x, o.node, o.encode)
}

// NodeCleared is what taking a node back into this node's trust did, for the result
// to say.
type NodeCleared struct {
	Node ident.NodeID
	// NewPairingRequired: the removal had revoked credentials with the node, and they
	// stay revoked, so it is trusted again only through a new pairing.
	NewPairingRequired bool
}

// ClearNodeOp is the operation that takes a node back into this node's trust, so
// that it can be paired with again. A node that was not removed changes nothing.
// encode makes the result; it runs in the loop and must not wait.
func ClearNodeOp(node ident.NodeID, encode func(NodeCleared) ([]byte, error)) Operation {
	return &clearNodeOp{trustOp{admin.OpClearLocallyRevoked}, node, encode}
}

type clearNodeOp struct {
	trustOp
	node   ident.NodeID
	encode func(NodeCleared) ([]byte, error)
}

func (o *clearNodeOp) run(e *Engine, x *opRun) ([]byte, error) {
	return e.clearLocallyRevoked(x, o.node, o.encode)
}

// storeResultOnly stores the result of an operation whose effect wrote nothing, in
// a transaction of its own.
func (e *Engine) storeResultOnly(x *opRun, result []byte) error {
	pr, err := store.PrepareAdminResult(x.record(result))
	if err != nil {
		return err
	}
	tx := e.d.Writer.Begin(&declTrustResult)
	defer tx.Close()
	if err := store.PutAdminResult(tx, store.RoleResult, pr); err != nil {
		return err
	}
	return tx.Commit()
}

// ---------------------------------------------------------------------------
// The view

// TrustedNode is a node this node trusts directly, through a credential it holds.
type TrustedNode struct {
	Node ident.NodeID
	// Credential is the digest of the credential, CreatedAt when the trust was
	// established and NotAfter when it ends, zero for never.
	Credential record.Digest
	CreatedAt  time.Time
	NotAfter   time.Time
	// Connected: a member connection to the node is up now.
	Connected bool
}

// ExpectedNode is an expectation that stands: a node the owner means to pair with.
type ExpectedNode struct {
	Node      ident.NodeID
	ExpiresAt time.Time
}

// TrustView is what the engine keeps of trust and pairing, for the owner to read:
// the nodes it trusts, the expectations that stand, the requests it holds as a
// target, the requests it started and the nodes it removed from its trust.
type TrustView struct {
	// Trusted are the credentials that count now (not revoked, not expired, and not
	// of a node that was removed), in the order of the nodes and then the digests.
	Trusted      []TrustedNode
	Expectations []ExpectedNode
	// Incoming are the requests this node holds as a target, and Outgoing those it
	// started, ended ones included, in the order of their identifiers.
	Incoming []pairing.TargetRecord
	Outgoing []pairing.InitiatorRecord
	// LocallyRevoked are the nodes removed from this node's trust, in order.
	LocallyRevoked []ident.NodeID
}

type cmdTrustView struct{ call[TrustView] }

func (c *cmdTrustView) gate() gate { return gateRead }
func (c *cmdTrustView) run(e *Engine, now time.Time) {
	c.done(e.trustViewAt(now), nil)
}

// TrustView is what the engine keeps of trust and pairing, as the loop sees it. It
// reads and writes nothing.
func (e *Engine) TrustView(ctx context.Context) (TrustView, error) {
	c := &cmdTrustView{call: newCall[TrustView]()}
	return roundTrip(ctx, e, submitExternal, c, c.call)
}

func byID[R any](id func(R) pairing.ID) func(a, b R) int {
	return func(a, b R) int { x, y := id(a), id(b); return strings.Compare(string(x[:]), string(y[:])) }
}

func (e *Engine) trustViewAt(now time.Time) TrustView {
	var v TrustView
	for digest, h := range e.trust.credentials {
		c := h.cred.Payload()
		node := c.A
		if node == e.d.Node {
			node = c.B
		}
		_, revoked := e.trust.revocations[digest]
		_, removed := e.trust.locallyRevoked[node]
		if revoked || removed || (!c.NotAfter.IsZero() && !now.Before(c.NotAfter)) {
			continue
		}
		v.Trusted = append(v.Trusted, TrustedNode{
			Node: node, Credential: digest, CreatedAt: c.CreatedAt, NotAfter: c.NotAfter,
			Connected: e.net != nil && e.net.Connected(node),
		})
	}
	slices.SortFunc(v.Trusted, func(a, b TrustedNode) int {
		if c := strings.Compare(a.Node.String(), b.Node.String()); c != 0 {
			return c
		}
		return strings.Compare(a.Credential.String(), b.Credential.String())
	})
	p := e.pairingWorld().Params
	for _, ex := range e.pairing.state.Expectations() {
		if !ex.ExpiredAt(p, now) {
			v.Expectations = append(v.Expectations, ExpectedNode{Node: ex.Node, ExpiresAt: ex.ExpiresAt(p)})
		}
	}
	slices.SortFunc(v.Expectations, func(a, b ExpectedNode) int { return strings.Compare(a.Node.String(), b.Node.String()) })
	v.Incoming = slices.Clone(e.pairing.state.Targets())
	slices.SortFunc(v.Incoming, byID(pairing.TargetRecord.ID))
	v.Outgoing = slices.Clone(e.pairing.state.Initiators())
	slices.SortFunc(v.Outgoing, byID(pairing.InitiatorRecord.ID))
	for node := range e.trust.locallyRevoked {
		v.LocallyRevoked = append(v.LocallyRevoked, node)
	}
	slices.SortFunc(v.LocallyRevoked, func(a, b ident.NodeID) int { return strings.Compare(a.String(), b.String()) })
	return v
}

// holdsRevokedCredential says whether this node holds a credential with the node
// that is revoked.
func (e *Engine) holdsRevokedCredential(node ident.NodeID) bool {
	for digest, h := range e.trust.credentials {
		if c := h.cred.Payload(); c.A != node && c.B != node {
			continue
		}
		if _, revoked := e.trust.revocations[digest]; revoked {
			return true
		}
	}
	return false
}
