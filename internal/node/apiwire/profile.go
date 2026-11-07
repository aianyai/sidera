package apiwire

import (
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// PeerProfileOf words the profile of the engine as the interfaces say it: the one
// shape (adapterproto.PeerProfile, which the SDK defines because it cannot import
// this package) that the Agent interface, the management interface and the
// adapter protocol all give a node's profile. AsOf is when this node got the
// profile, in Unix seconds. A node with no profile is nil, so that the member that
// holds it is left out.
func PeerProfileOf(p *engine.ProfileView) *adapterproto.PeerProfile {
	if p == nil {
		return nil
	}
	return &adapterproto.PeerProfile{
		Name: p.Name, HostName: p.HostName, Platform: p.Platform, System: p.System,
		SystemBuild: p.SystemBuild, SideraVersion: p.SideraVersion, AsOf: p.AsOf.Unix(),
	}
}
