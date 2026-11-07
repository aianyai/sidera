package apiwire

import (
	"github.com/aianyai/sidera/internal/node/transport"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// PeerProfileOf is a node's profile as the adapter protocol writes it. It is
// [NodeProfile] under the protocol's own type, which the SDK cannot take from here:
// the two have the same members, so that an agent and an adapter read one
// description of a node. A node with no profile is nil.
func PeerProfileOf(p *NodeProfile) *adapterproto.PeerProfile {
	if p == nil {
		return nil
	}
	out := adapterproto.PeerProfile(*p)
	return &out
}

// DirectoryAdapter is the adapter behind a directory entry as the Agent interface
// and the adapter protocol write it, in the one shape both share; nil when the
// entry has none.
func DirectoryAdapter(a *transport.DirectoryAdapter) *adapterproto.DirectoryAdapter {
	if a == nil {
		return nil
	}
	out := &adapterproto.DirectoryAdapter{Instance: a.Instance, ID: a.ID, Version: a.Version}
	if a.Runtime != nil {
		rt := *a.Runtime
		out.Runtime = &rt
	}
	return out
}
