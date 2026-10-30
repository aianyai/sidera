package diskfacts

import (
	"bytes"
	"encoding/json"
	"path"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/revocation"
)

// Terminal contains stopped-disk observations, independent of any response or
// replay. Persisted stays nil when the local owner's operation has no result.
type Terminal struct {
	Persisted json.RawMessage
	Retired   bool
}

// InspectTerminal reads the local owner's result from the binding's actual
// stopped-disk scan and verifies every kept revocation. Only a self-revocation
// bound to node establishes retirement; even after finding one, a later invalid
// record is an error. The caller supplies a non-nil scan of the same stopped disk.
func InspectTerminal(r fsx.Reader, scan *store.Scan, node ident.NodeID, id admin.OperationID) (Terminal, error) {
	var out Terminal
	key := admin.KeyFor(admin.LocalOwner(), id)
	for _, record := range scan.Admin {
		if record.Key == key {
			out.Persisted = bytes.Clone(record.Result)
		}
	}
	entries, err := r.ReadDir(config.RevocationsDir)
	if err != nil {
		return Terminal{}, err
	}
	for _, entry := range entries {
		raw, err := r.ReadFile(path.Join(config.RevocationsDir, entry.Name))
		if err != nil {
			return Terminal{}, err
		}
		rev, _, err := config.DecodeRevocation(entry.Name, raw)
		if err != nil {
			return Terminal{}, err
		}
		out.Retired = out.Retired || (rev.Kind() == revocation.KindNodeSelf && rev.Node() == node)
	}
	return out, nil
}
