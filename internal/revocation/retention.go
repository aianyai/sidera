package revocation

import (
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/trust"
)

// Params are the values of the retention rule whose value is not decided.
type Params struct {
	// MaxForwardOnce is the most forward-once revocations a node holds at one
	// time, counted over all of them, not per node or per peer. It bounds the
	// memory that revocations of nodes the local node has no relation to can
	// use. No value is decided yet, so there is no default; zero means unset and
	// every forward-once revocation is dropped (ReasonInvalidInput).
	MaxForwardOnce int
}

// Context is what the local node knows that decides whether a node-level
// revocation concerns it. The caller assembles it from its own state.
type Context struct {
	// Local is the local node. Its own revocations always concern it, whether or
	// not the caller lists it in TrustGraph. The zero NodeID is no node.
	Local ident.NodeID
	// TrustGraph holds the nodes in the local trust graph, the nodes it has
	// credentials with directly or through one intermediate.
	TrustGraph trust.NodeSet
	// DeclarationSigners holds the nodes whose declarations the local node has
	// cached.
	DeclarationSigners trust.NodeSet
}

// Class is how long a node keeps a revocation.
type Class int

const (
	// ClassInvalid is the zero value; it is never returned.
	ClassInvalid Class = iota
	// LongTerm: store it for good and send it to every peer that asks.
	LongTerm
	// ForwardOnce: pass it on once and do not store it long term; it counts
	// against Params.MaxForwardOnce until it has been passed on.
	ForwardOnce
	// Dropped: neither store nor forward it. The reason says why.
	Dropped
)

func (c Class) String() string {
	switch c {
	case LongTerm:
		return "long-term"
	case ForwardOnce:
		return "forward-once"
	case Dropped:
		return "dropped"
	}
	return "invalid"
}

// Reason is the machine-readable cause of a retention decision.
type Reason int

const (
	// ReasonInvalid is the zero value; it is never returned.
	ReasonInvalid Reason = iota
	// ReasonPersonLevel: a person self-revocation. There are few of them and a
	// node cannot tell which concern its peers, so every node keeps them all.
	ReasonPersonLevel
	// ReasonDeviceLevel: a device key revocation, kept for the same reasons.
	ReasonDeviceLevel
	// ReasonLocalNode: the revocation concerns the local node itself.
	ReasonLocalNode
	// ReasonTrustGraphNode: it concerns a node in the local trust graph.
	ReasonTrustGraphNode
	// ReasonDeclarationSigner: it concerns a node whose declaration the local
	// node has cached.
	ReasonDeclarationSigner
	// ReasonUnrelatedNode: a node-level revocation of a node the local node has
	// no relation to; it is passed on once, within the cap.
	ReasonUnrelatedNode
	// ReasonCapReached: an unrelated node-level revocation arrived while the
	// local node already holds the most forward-once revocations it may.
	ReasonCapReached
	// ReasonInvalidInput: the revocation is the zero value, the count held is
	// negative, or the cap is unset. Nothing is kept on an input the rule cannot
	// judge.
	ReasonInvalidInput
)

func (r Reason) String() string {
	switch r {
	case ReasonPersonLevel:
		return "person-level"
	case ReasonDeviceLevel:
		return "device-level"
	case ReasonLocalNode:
		return "local-node"
	case ReasonTrustGraphNode:
		return "trust-graph-node"
	case ReasonDeclarationSigner:
		return "declaration-signer"
	case ReasonUnrelatedNode:
		return "unrelated-node"
	case ReasonCapReached:
		return "cap-reached"
	case ReasonInvalidInput:
		return "invalid-input"
	}
	return "invalid"
}

// Retention is the decision for one revocation.
type Retention struct {
	Class  Class
	Reason Reason
}

// Classify decides how long the local node keeps r.
//
//   - Person self-revocations and device key revocations are LongTerm. They are
//     few, and a node cannot tell which concern its peers, so none is filtered.
//   - A node-level revocation (self-revocation, certificate revocation) is
//     LongTerm when its node is the local node, a node in the trust graph or the
//     signer of a declaration the local node has cached, and in that order of
//     precedence for the reason.
//   - Any other node-level revocation is ForwardOnce while forwardOnceHeld, the
//     number of forward-once revocations the node holds now, is below
//     p.MaxForwardOnce, and Dropped after that. The cap bounds what unrelated
//     revocations can cost, whoever floods them.
//
// The decision never depends on the order revocations arrive in, except through
// forwardOnceHeld, which the caller counts and lowers when a revocation has been
// passed on. Revocations of related nodes are never refused for the cap.
func Classify(r Revocation, ctx Context, forwardOnceHeld int, p Params) Retention {
	if r.check() != nil {
		return Retention{Dropped, ReasonInvalidInput}
	}
	switch r.kind {
	case KindPersonSelf:
		return Retention{LongTerm, ReasonPersonLevel}
	case KindDeviceKey:
		return Retention{LongTerm, ReasonDeviceLevel}
	}
	// What is left is node-level: a self-revocation or a certificate revocation.
	switch {
	case r.node == ctx.Local:
		return Retention{LongTerm, ReasonLocalNode}
	case inSet(ctx.TrustGraph, r.node):
		return Retention{LongTerm, ReasonTrustGraphNode}
	case inSet(ctx.DeclarationSigners, r.node):
		return Retention{LongTerm, ReasonDeclarationSigner}
	}
	if forwardOnceHeld < 0 || p.MaxForwardOnce < 1 {
		return Retention{Dropped, ReasonInvalidInput}
	}
	if forwardOnceHeld >= p.MaxForwardOnce {
		return Retention{Dropped, ReasonCapReached}
	}
	return Retention{ForwardOnce, ReasonUnrelatedNode}
}

func inSet(s trust.NodeSet, n ident.NodeID) bool {
	_, ok := s[n]
	return ok
}
