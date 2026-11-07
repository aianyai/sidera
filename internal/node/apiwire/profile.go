package apiwire

import "github.com/aianyai/sidera/internal/node/engine"

// NodeProfile is a node profile as an interface says it: what a node says about
// itself, so that a person or an agent can tell nodes apart. A member the node did
// not give is left out. AsOf is when this node got the profile, in Unix seconds:
// the time of a fetch, or of the issue for this node itself.
type NodeProfile struct {
	Name          string `json:"name,omitempty"`
	HostName      string `json:"host_name,omitempty"`
	Platform      string `json:"platform,omitempty"`
	System        string `json:"system,omitempty"`
	SystemBuild   string `json:"system_build,omitempty"`
	SideraVersion string `json:"sidera_version,omitempty"`
	AsOf          int64  `json:"as_of"`
}

// NodeProfileOf words the profile of the engine. A node with no profile is nil, so
// that the member that holds it is left out.
func NodeProfileOf(p *engine.ProfileView) *NodeProfile {
	if p == nil {
		return nil
	}
	return &NodeProfile{
		Name: p.Name, HostName: p.HostName, Platform: p.Platform, System: p.System,
		SystemBuild: p.SystemBuild, SideraVersion: p.SideraVersion, AsOf: p.AsOf.Unix(),
	}
}
