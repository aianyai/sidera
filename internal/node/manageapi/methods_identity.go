package manageapi

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/record"
)

// RevocationExportResult is the self-revocation document and the digest used to
// confirm its offline backup. Possession of the document can retire the node.
type RevocationExportResult struct {
	Armored string `json:"armored"`
	Digest  string `json:"digest"`
}

// identity.export_revocation: the statement that retires this node, armored, with
// the digest the backup is confirmed by. It is authorized as an operation of the
// owner's alone and is not stored: it is a read that the catalogue does not let
// anyone but the local owner make, because what it returns can retire the node.
var identityExportRevocation = &method{
	name: "identity.export_revocation", op: admin.Operation{Kind: admin.OpExportRevocation},
	plan: func(_ *Server, raw json.RawMessage) (*plan, error) {
		var p struct{}
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		return &plan{canonical: canonical(p), read: func(ctx context.Context, svc engine.ManageService) (any, error) {
			ex, err := svc.ExportRevocation(ctx)
			if err != nil {
				return nil, err
			}
			return RevocationExportResult{Armored: string(ex.Armor), Digest: ex.Digest.String()}, nil
		}}, nil
	},
}

// parseDigest reads the digest of a record: 64 lower-case hexadecimal digits that
// are not all zero.
func parseDigest(s, path string) (record.Digest, error) {
	var d record.Digest
	bad := func() error {
		return apiwire.Malformed("the value is not a digest: 64 lower-case hexadecimal digits", path)
	}
	if len(s) != 2*len(d) || s != strings.ToLower(s) {
		return d, bad()
	}
	raw, err := hex.DecodeString(s)
	if err != nil {
		return d, bad()
	}
	copy(d[:], raw)
	if d.IsZero() {
		return d, bad()
	}
	return d, nil
}

// identity.confirm_backup: the owner keeps a copy of the statement outside this
// device, and names its digest as the export showed it.
var identityConfirmBackup = &method{
	name: "identity.confirm_backup", op: admin.Operation{Kind: admin.OpConfirmRevocationBackup},
	plan: func(_ *Server, raw json.RawMessage) (*plan, error) {
		var p struct {
			Digest string `json:"digest"`
		}
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		digest, err := parseDigest(p.Digest, "params.digest")
		if err != nil {
			return nil, err
		}
		op := engine.ConfirmRevocationBackupOp(digest, func(c engine.BackupConfirmed) ([]byte, error) {
			return json.Marshal(struct {
				ConfirmedAt string `json:"confirmed_at"`
			}{timeText(c.At)})
		})
		return &plan{canonical: canonical(p), operation: op}, nil
	},
}

// identity.retire: retire the identity of this node for good. The owner names the
// node, so that a retirement meant for another data directory is refused.
var identityRetire = &method{
	name: "identity.retire", op: admin.Operation{Kind: admin.OpRetireIdentity},
	plan: func(_ *Server, raw json.RawMessage) (*plan, error) {
		var p struct {
			Node string `json:"node"`
		}
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		node, err := ident.ParseNodeID(p.Node)
		if err != nil {
			return nil, apiwire.Malformed("the value is not the identifier of a node", "params.node")
		}
		p.Node = node.String()
		op := engine.RetireIdentityOp(node, func() ([]byte, error) {
			return json.Marshal(struct {
				Node     string `json:"node"`
				Retired  bool   `json:"retired"`
				Stopping bool   `json:"stopping"`
			}{node.String(), true, true})
		})
		return &plan{canonical: canonical(p), operation: op}, nil
	},
}

// clock.clear_rollback: the owner has looked at the wall clock that was set back and
// accepts it as it reads; the pause is lifted and the mark follows the clock.
var clockClearRollback = &method{
	name: "clock.clear_rollback", op: admin.Operation{Kind: admin.OpClearClockRollback},
	plan: func(_ *Server, raw json.RawMessage) (*plan, error) {
		var p struct{}
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		op := engine.ClearClockRollbackOp(func(c engine.ClockClear) ([]byte, error) {
			return json.Marshal(struct {
				Cleared   bool   `json:"cleared"`
				HighWater string `json:"high_water"`
				Now       string `json:"now"`
			}{c.Cleared, timeText(c.HighWater), timeText(c.Now)})
		})
		return &plan{canonical: canonical(p), operation: op}, nil
	},
}
