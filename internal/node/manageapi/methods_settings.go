package manageapi

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/engine"
)

// canonicalSettings is a patch of settings in the one form that equal patches
// share: the members in order, no spaces, numbers as written. The patch is read
// strictly by `config`, which owns the names and the rules of the settings; this
// is only the form the fingerprint is made of, so that a resend written with
// another order of members is still a resend.
func canonicalSettings(raw json.RawMessage) json.RawMessage {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return raw // not JSON: the strict decoder of config refuses it with its own words
	}
	b, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return b
}

// node.show: the settings of the node and the version a change of them names.
var nodeShow = &method{
	name: "node.show", op: admin.Operation{Kind: admin.OpRead},
	plan: func(s *Server, raw json.RawMessage) (*plan, error) {
		var p struct{}
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		return &plan{canonical: canonical(p), read: func(ctx context.Context, svc engine.ManageService) (any, error) {
			n, err := svc.NodeSettings(ctx)
			if err != nil {
				return nil, err
			}
			settings, err := n.Settings.WireJSON()
			if err != nil {
				return nil, err
			}
			return struct {
				Node     string          `json:"node"`
				Version  uint64          `json:"version"`
				Settings json.RawMessage `json:"settings"`
			}{s.cfg.Node.String(), uint64(n.Version), settings}, nil
		}}, nil
	},
}

// node.set: change settings of the node with a patch. The names, the values and the
// rules are `config`'s; the version is the one `node.show` gave.
var nodeSet = &method{
	name: "node.set", op: admin.Operation{Kind: admin.OpChangeNodeSettings},
	plan: func(_ *Server, raw json.RawMessage) (*plan, error) {
		var p struct {
			Settings json.RawMessage `json:"settings"`
		}
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		if len(p.Settings) == 0 {
			return nil, apiwire.Malformed("a change of settings carries the settings to change", "params.settings")
		}
		p.Settings = canonicalSettings(p.Settings)
		op := engine.SetNodeSettingsOp(p.Settings, func(n config.Node) ([]byte, error) {
			settings, err := n.Settings.WireJSON()
			if err != nil {
				return nil, err
			}
			return json.Marshal(struct {
				Settings json.RawMessage `json:"settings"`
				Version  uint64          `json:"version"`
			}{settings, uint64(n.Version)})
		})
		return &plan{canonical: canonical(p), operation: op}, nil
	},
}

// endpoints.set: change the settings of one endpoint of this node with a patch.
var endpointsSet = &method{
	name: "endpoints.set", op: admin.Operation{Kind: admin.OpChangeEndpointSettings},
	plan: func(s *Server, raw json.RawMessage) (*plan, error) {
		var p struct {
			Endpoint string          `json:"endpoint"`
			Settings json.RawMessage `json:"settings"`
		}
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		addr, err := ident.ParseAddress(p.Endpoint)
		if err != nil {
			return nil, apiwire.Malformed("the value is not the address of an endpoint", "params.endpoint")
		}
		if addr.Node != s.cfg.Node {
			return nil, apiwire.EndpointUnknown()
		}
		if len(p.Settings) == 0 {
			return nil, apiwire.Malformed("a change of settings carries the settings to change", "params.settings")
		}
		p.Endpoint, p.Settings = addr.String(), canonicalSettings(p.Settings)
		op := engine.SetEndpointSettingsOp(addr.Endpoint, p.Settings, func(c engine.EndpointChange) ([]byte, error) {
			settings, err := c.Settings.WireJSON()
			if err != nil {
				return nil, err
			}
			return json.Marshal(struct {
				Endpoint string          `json:"endpoint"`
				Settings json.RawMessage `json:"settings"`
				Version  uint64          `json:"version"`
			}{addr.String(), settings, uint64(c.Version)})
		})
		return &plan{canonical: canonical(p), operation: op}, nil
	},
}
