package apiwire

import (
	"github.com/aianyai/sidera/internal/node/transport"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

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
