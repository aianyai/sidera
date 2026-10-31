package network

import (
	"context"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/transport"
)

// Remote is the part of the network layer the router calls for another node. The
// router consumes it, so a test can stand in for the network.
type Remote interface {
	Send(ctx context.Context, target ident.NodeID, sender ident.EndpointID, data []byte) (transport.Receipt, error)
	Pull(ctx context.Context, target ident.NodeID, req transport.PullRequest) (transport.PullAnswer, error)
	QueryDirectory(ctx context.Context, target ident.NodeID, sender ident.EndpointID, req transport.DirectoryRequest) (transport.DirectoryPage, error)
}

// Router is the transport the engine calls: a call to this node goes to the
// loopback, which makes the local origin, and a call to any other node goes to
// the remote client. It has the calls of the loopback, so the engine's own
// interface is satisfied by either.
type Router struct {
	self   ident.NodeID
	local  *transport.Loopback
	remote Remote
}

// NewRouter returns the router of node self. It panics when self is the zero
// node identifier or either transport is nil: a router that could not tell a
// local call or could not carry a call would fail only on its first use.
func NewRouter(self ident.NodeID, local *transport.Loopback, remote Remote) *Router {
	if self.IsZero() || local == nil || remote == nil {
		panic("network: NewRouter needs the node, the loopback and the remote client")
	}
	return &Router{self: self, local: local, remote: remote}
}

// Send carries an envelope to the target node.
func (r *Router) Send(ctx context.Context, target ident.NodeID, sender ident.EndpointID, data []byte) (transport.Receipt, error) {
	if target == r.self {
		return r.local.Send(ctx, target, sender, data)
	}
	return r.remote.Send(ctx, target, sender, data)
}

// Pull asks the target node for the reports of a message.
func (r *Router) Pull(ctx context.Context, target ident.NodeID, req transport.PullRequest) (transport.PullAnswer, error) {
	if target == r.self {
		return r.local.Pull(ctx, target, req)
	}
	return r.remote.Pull(ctx, target, req)
}

// QueryDirectory asks the target node for the endpoints it shows the sender.
func (r *Router) QueryDirectory(ctx context.Context, target ident.NodeID, sender ident.EndpointID, req transport.DirectoryRequest) (transport.DirectoryPage, error) {
	if target == r.self {
		return r.local.QueryDirectory(ctx, target, sender, req)
	}
	return r.remote.QueryDirectory(ctx, target, sender, req)
}

// Waiting is the loopback's count of calls waiting out a delay; the remote
// client has no knobs, so it has nothing to add.
func (r *Router) Waiting() int { return r.local.Waiting() }
