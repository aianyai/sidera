package transport

import "github.com/aianyai/sidera/internal/ident"

// Origin says where an inbound request came from: the node it names and whether
// that node is this one, reached through the loopback. The zero Origin is a
// remote one that names no node.
//
// Local is the only property the engine's local-source rules may rest on. It is
// not read from the request and not claimed by the peer: it is set where the
// request enters the node, and only the loopback sets it. A transport of
// another package has no way to make a local origin, because the field is
// unexported and the one constructor that sets it is not exported either, so a
// connection that shows this node's own id (a second instance, a stolen key) is
// a remote origin like any other.
type Origin struct {
	node  ident.NodeID
	local bool
}

// Node returns the node the request came from.
func (o Origin) Node() ident.NodeID { return o.node }

// Local reports whether the request came from an endpoint of this node,
// through the loopback.
func (o Origin) Local() bool { return o.local }

// RemoteOrigin returns the origin of a request that arrived from node over a
// transport other than the loopback. It is never local, whatever node it names,
// this one included.
func RemoteOrigin(node ident.NodeID) Origin { return Origin{node: node} }

// localOrigin returns the origin of a request that the loopback carried on this
// node's behalf. The loopback is its only caller.
func localOrigin(node ident.NodeID) Origin { return Origin{node: node, local: true} }
