package manageapi

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/engine"
)

// The version of every method here is that of network.json: network.show gives it
// and a change names it as the expected version, as node.show and node.set do.
// What an address or a listen address may be is `config`'s rule, and whether an
// address suits the certificate held for its node is the network's, judged by the
// engine; the rows only read the params and word the results.

// parseNodeParam reads the node of a request as the text form of a node id.
func parseNodeParam(text string) (ident.NodeID, error) {
	node, err := ident.ParseNodeID(text)
	if err != nil {
		return ident.NodeID{}, apiwire.Malformed("the value is not a node identifier", "params.node")
	}
	return node, nil
}

// nonNil makes a list that is empty encode as [] and not as null.
func nonNil(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

// network.show: where the node listens (as configured and as open now), the line
// another owner enters to reach this node, the address book with the state of each
// node, and the version a change names.
var networkShow = &method{
	name: "network.show", op: admin.Operation{Kind: admin.OpRead},
	plan: func(s *Server, raw json.RawMessage) (*plan, error) {
		var p struct{}
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		return &plan{canonical: canonical(p), direct: func(ctx context.Context, svc engine.ManageService) (any, error) {
			v, err := svc.NetworkView(ctx)
			if err != nil {
				return nil, err
			}
			type listen struct {
				Configured []string `json:"configured"`
				Actual     []string `json:"actual"`
			}
			type peerJSON struct {
				Node            string   `json:"node"`
				Addresses       []string `json:"addresses"`
				CertificateHeld bool     `json:"certificate_held"`
				Connected       bool     `json:"connected"`
			}
			out := struct {
				Node    string     `json:"node"`
				PeerID  string     `json:"peer_id"`
				Version uint64     `json:"version"`
				Listen  listen     `json:"listen"`
				Contact string     `json:"contact"`
				Peers   []peerJSON `json:"peers"`
			}{
				Node: s.cfg.Node.String(), PeerID: v.PeerID, Version: uint64(v.Config.Version),
				Listen:  listen{Configured: nonNil(v.Config.Listen), Actual: nonNil(v.Listening)},
				Contact: contactLine(s.cfg.Node, v.Contact),
				Peers:   []peerJSON{},
			}
			for _, p := range v.Peers {
				out.Peers = append(out.Peers, peerJSON{p.Node.String(), nonNil(p.Addresses), p.CertificateHeld, p.Connected})
			}
			return out, nil
		}}, nil
	},
}

// contactLine is what one owner gives another: the node and the addresses that
// reach it, each ending in /p2p/<peer id>; empty when the node has no address to
// give.
func contactLine(node ident.NodeID, addrs []string) string {
	if len(addrs) == 0 {
		return ""
	}
	return strings.Join(slices.Insert(slices.Clone(addrs), 0, node.String()), " ")
}

// network.address.set: replace the addresses kept for one node.
var networkAddressSet = &method{
	name: "network.address.set", op: admin.Operation{Kind: admin.OpChangeNodeSettings},
	plan: func(_ *Server, raw json.RawMessage) (*plan, error) {
		var p struct {
			Node      string   `json:"node"`
			Addresses []string `json:"addresses"`
		}
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		node, err := parseNodeParam(p.Node)
		if err != nil {
			return nil, err
		}
		p.Node, p.Addresses = node.String(), nonNil(p.Addresses)
		op := engine.SetPeerAddressesOp(node, p.Addresses, func(n config.Network) ([]byte, error) {
			return json.Marshal(struct {
				Node      string   `json:"node"`
				Addresses []string `json:"addresses"`
				Version   uint64   `json:"version"`
			}{p.Node, nonNil(n.Peers[node]), uint64(n.Version)})
		})
		return &plan{canonical: canonical(p), operation: op}, nil
	},
}

// network.address.remove: remove one node's addresses from the book.
var networkAddressRemove = &method{
	name: "network.address.remove", op: admin.Operation{Kind: admin.OpChangeNodeSettings},
	plan: func(_ *Server, raw json.RawMessage) (*plan, error) {
		var p struct {
			Node string `json:"node"`
		}
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		node, err := parseNodeParam(p.Node)
		if err != nil {
			return nil, err
		}
		p.Node = node.String()
		op := engine.RemovePeerOp(node, func(n config.Network) ([]byte, error) {
			return json.Marshal(struct {
				Node    string `json:"node"`
				Version uint64 `json:"version"`
			}{p.Node, uint64(n.Version)})
		})
		return &plan{canonical: canonical(p), operation: op}, nil
	},
}

// network.listen.set: replace the addresses the node listens on. The listeners
// open once, at the end of a start, so the result says that a restart is needed.
// The list is required: leaving it out must not be taken for a node that stops
// listening, which is the empty list.
var networkListenSet = &method{
	name: "network.listen.set", op: admin.Operation{Kind: admin.OpChangeNodeSettings},
	plan: func(_ *Server, raw json.RawMessage) (*plan, error) {
		var p struct {
			Addresses *[]string `json:"addresses"`
		}
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		if p.Addresses == nil {
			return nil, apiwire.Malformed("a change of the listen list carries the list", "params.addresses")
		}
		listen := nonNil(*p.Addresses)
		op := engine.SetListenOp(listen, func(n config.Network) ([]byte, error) {
			return json.Marshal(struct {
				Listen          []string `json:"listen"`
				Version         uint64   `json:"version"`
				RestartRequired bool     `json:"restart_required"`
			}{nonNil(n.Listen), uint64(n.Version), true})
		})
		return &plan{canonical: canonical(struct {
			Addresses []string `json:"addresses"`
		}{listen}), operation: op}, nil
	},
}
