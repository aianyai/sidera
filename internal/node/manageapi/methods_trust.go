package manageapi

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/pairing"
)

// The owner's methods on trust: entering and forgetting an expectation, asking a
// node for a pairing, reading what trust holds, removing a node from trust and
// taking it back. Which nodes may be named, what a refused request is and what a
// pairing does are the engine's and the pairing package's; the rows read the params,
// check the form of what is given and word the results. Pairing identifiers and
// credential digests are written in lower-case hexadecimal, a code in the form an
// owner compares ("482 019").

// maxDialHints is how many addresses a request for a pairing may carry: the bound
// the pairing package keeps for the hints of one pairing. The engine and the
// pairing enforce it again; this one refuses a request before it is planned.
const maxDialHints = pairing.MaxAddresses

// readNodeParams reads the params of a method that names one node and nothing else,
// and gives the node with the canonical form of the params.
func readNodeParams(raw json.RawMessage) (ident.NodeID, []byte, error) {
	var p struct {
		Node string `json:"node"`
	}
	if err := decodeParams(raw, &p); err != nil {
		return ident.NodeID{}, nil, err
	}
	node, err := parseNodeParam(p.Node)
	if err != nil {
		return ident.NodeID{}, nil, err
	}
	p.Node = node.String()
	return node, canonical(p), nil
}

// trust.expect: the owner means to pair with a node, which stands for a
// confirmation made in advance.
var trustExpect = &method{
	name: "trust.expect", op: admin.Operation{Kind: admin.OpConfirmTrust},
	plan: func(_ *Server, raw json.RawMessage) (*plan, error) {
		node, canon, err := readNodeParams(raw)
		if err != nil {
			return nil, err
		}
		return &plan{canonical: canon, operation: engine.ExpectNodeOp(node, expectedResult)}, nil
	},
}

// expectedResult words the entering of an expectation.
func expectedResult(e engine.NodeExpected) ([]byte, error) {
	return json.Marshal(struct {
		Node      string `json:"node"`
		ExpiresAt string `json:"expires_at"`
	}{e.Node.String(), timeText(e.ExpiresAt)})
}

// trust.forget: delete the expectation for a node.
var trustForget = &method{
	name: "trust.forget", op: admin.Operation{Kind: admin.OpRejectTrustRequest},
	plan: func(_ *Server, raw json.RawMessage) (*plan, error) {
		node, canon, err := readNodeParams(raw)
		if err != nil {
			return nil, err
		}
		return &plan{canonical: canon, operation: engine.ForgetNodeOp(node, forgottenResult)}, nil
	},
}

// forgottenResult words the deleting of an expectation.
func forgottenResult(node ident.NodeID) ([]byte, error) {
	return json.Marshal(struct {
		Node      string `json:"node"`
		Forgotten bool   `json:"forgotten"`
	}{node.String(), true})
}

// trust.reject: end a request for a pairing that this node's owner does not want,
// whether the node holds it as a target or started it. A request the node started
// is withdrawn, and the other node learns of it with the next exchange.
var trustReject = &method{
	name: "trust.reject", op: admin.Operation{Kind: admin.OpRejectTrustRequest},
	plan: func(_ *Server, raw json.RawMessage) (*plan, error) {
		var p struct {
			PairingID string `json:"pairing_id"`
		}
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		id, err := parsePairingID(p.PairingID)
		if err != nil {
			return nil, err
		}
		return &plan{canonical: canonical(struct {
			PairingID string `json:"pairing_id"`
		}{pairingIDText(id)}), operation: engine.RejectPairingOp(id, rejectedResult)}, nil
	},
}

// parsePairingID reads a pairing identifier as pairingIDText writes it.
func parsePairingID(s string) (pairing.ID, error) {
	d, err := parseDigest(s, "params.pairing_id")
	if err != nil {
		return pairing.ID{}, apiwire.Malformed("the value is not a pairing identifier: 64 lower-case hexadecimal digits", "params.pairing_id")
	}
	return pairing.ID(d), nil
}

// rejectedResult words the rejection of a request: the request as it is now, in the
// fields and the status names trust.list gives it.
func rejectedResult(r engine.PairingRejected) ([]byte, error) {
	out := struct {
		PairingID string `json:"pairing_id"`
		Node      string `json:"node"`
		Status    string `json:"status"`
	}{}
	switch {
	case r.Incoming != nil:
		out.PairingID, out.Node, out.Status = pairingIDText(r.Incoming.ID()), r.Incoming.Display().Remote.String(), targetStatusName(*r.Incoming)
	case r.Outgoing != nil:
		out.PairingID, out.Node, out.Status = pairingIDText(r.Outgoing.ID()), r.Outgoing.Display().Remote.String(), initiatorStatusName(*r.Outgoing)
	default:
		return nil, errors.New("manageapi: a rejection that names no request")
	}
	return json.Marshal(out)
}

// trust.request: ask a node for a pairing, dialing the addresses given for this
// request only, or else those of the address book. It is answered when the online
// segment of the pairing ends.
var trustRequest = &method{
	name: "trust.request", op: admin.Operation{Kind: admin.OpConfirmTrust},
	plan: func(_ *Server, raw json.RawMessage) (*plan, error) {
		var p struct {
			Node      string    `json:"node"`
			Addresses *[]string `json:"addresses,omitempty"`
		}
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		node, err := parseNodeParam(p.Node)
		if err != nil {
			return nil, err
		}
		var hints []string
		if p.Addresses != nil {
			hints = *p.Addresses
		}
		if len(hints) > maxDialHints {
			return nil, apiwire.Malformed(fmt.Sprintf("a request carries at most %d addresses", maxDialHints), "params.addresses")
		}
		for i, addr := range hints {
			if err := config.ValidatePeerAddress(addr); err != nil {
				return nil, apiwire.Malformed(err.Error(), fmt.Sprintf("params.addresses[%d]", i))
			}
		}
		// An empty list and an absent one are one request: the canonical form leaves
		// out both. The member is a pointer for the decoder, which refuses an
		// explicit empty value for an optional member that is not one.
		canon := canonical(struct {
			Node      string   `json:"node"`
			Addresses []string `json:"addresses,omitempty"`
		}{node.String(), hints})
		return &plan{canonical: canon, operation: engine.RequestPairingOp(node, hints, requestedResult)}, nil
	},
}

// requestedResult words what a request for a pairing came to: the request the node
// saved, with the code to compare, or the target's refusal, which has no request and
// so no identifier and no code.
func requestedResult(r engine.PairingRequested) ([]byte, error) {
	out := struct {
		PairingID string `json:"pairing_id"`
		Node      string `json:"node"`
		Status    string `json:"status"`
		Code      string `json:"code"`
		Refusal   string `json:"refusal,omitempty"`
	}{Node: r.Node.String()}
	if r.Saved == nil {
		out.Status, out.Refusal = "refused", r.Refusal.String()
	} else {
		out.PairingID, out.Code, out.Status = pairingIDText(r.Saved.ID()), r.Saved.Display().Code.String(), initiatorStatusName(*r.Saved)
	}
	return json.Marshal(out)
}

// initiatorStatusName is the state of a request this node started, as the owner
// reads it: a request that lives is waiting for this node's owner to confirm, or
// for the target; the others are named as the pairing package names them.
func initiatorStatusName(rec pairing.InitiatorRecord) string {
	switch rec.Status {
	case pairing.InitiatorActive:
		if rec.Confirmation != nil && rec.Signature != nil {
			return "awaiting_target"
		}
		return "awaiting_initiator_signature"
	case pairing.InitiatorComplete:
		return "complete"
	}
	return rec.Status.String()
}

// targetStatusName is the state of a request this node holds as a target, as the
// owner reads it: a pending request is waiting for the initiator's signature, or,
// with it in, for this node's owner; the others are named as the pairing package
// names them.
func targetStatusName(rec pairing.TargetRecord) string {
	if rec.Status != pairing.TargetPending {
		return rec.Status.String()
	}
	if rec.InitiatorSignature == nil {
		return "awaiting_initiator_signature"
	}
	return "awaiting_target"
}

func pairingIDText(id pairing.ID) string { return hex.EncodeToString(id[:]) }

// trust.list: the nodes this node trusts, the expectations that stand, the requests
// it holds as a target and those it started, and the nodes it removed from its
// trust.
var trustList = &method{
	name: "trust.list", op: admin.Operation{Kind: admin.OpRead},
	plan: func(_ *Server, raw json.RawMessage) (*plan, error) {
		var p struct{}
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		return &plan{canonical: canonical(p), direct: func(ctx context.Context, svc engine.ManageService) (any, error) {
			v, err := svc.TrustView(ctx)
			if err != nil {
				return nil, err
			}
			return trustListResult(v), nil
		}}, nil
	},
}

// trustListResult words the view of trust. Every list is a list, empty or not.
func trustListResult(v engine.TrustView) any {
	type trusted struct {
		Node       string `json:"node"`
		Credential string `json:"credential"`
		CreatedAt  string `json:"created_at"`
		NotAfter   string `json:"not_after,omitempty"`
		Connected  bool   `json:"connected"`
		// Profile is what the node says about itself; left out when none is known.
		Profile *apiwire.NodeProfile `json:"profile,omitempty"`
	}
	type expectation struct {
		Node      string `json:"node"`
		ExpiresAt string `json:"expires_at"`
	}
	type incoming struct {
		PairingID string `json:"pairing_id"`
		Node      string `json:"node"`
		Status    string `json:"status"`
		Code      string `json:"code"`
		Expected  bool   `json:"expected"`
		ExpiresAt string `json:"expires_at"`
	}
	type outgoing struct {
		PairingID string `json:"pairing_id"`
		Node      string `json:"node"`
		Status    string `json:"status"`
		Code      string `json:"code"`
		Reason    string `json:"reason,omitempty"`
	}
	out := struct {
		Trusted        []trusted     `json:"trusted"`
		Expectations   []expectation `json:"expectations"`
		Incoming       []incoming    `json:"incoming"`
		Outgoing       []outgoing    `json:"outgoing"`
		LocallyRevoked []string      `json:"locally_revoked"`
	}{[]trusted{}, []expectation{}, []incoming{}, []outgoing{}, []string{}}
	for _, t := range v.Trusted {
		row := trusted{Node: t.Node.String(), Credential: t.Credential.String(), CreatedAt: timeText(t.CreatedAt), Connected: t.Connected, Profile: apiwire.NodeProfileOf(t.Profile)}
		if !t.NotAfter.IsZero() {
			row.NotAfter = timeText(t.NotAfter)
		}
		out.Trusted = append(out.Trusted, row)
	}
	for _, e := range v.Expectations {
		out.Expectations = append(out.Expectations, expectation{e.Node.String(), timeText(e.ExpiresAt)})
	}
	for _, rec := range v.Incoming {
		d := rec.Display()
		out.Incoming = append(out.Incoming, incoming{
			PairingID: pairingIDText(rec.ID()), Node: d.Remote.String(), Status: targetStatusName(rec),
			Code: d.Code.String(), Expected: rec.Expected != (pairing.Author{}), ExpiresAt: timeText(rec.ExpiresAt),
		})
	}
	for _, rec := range v.Outgoing {
		row := outgoing{PairingID: pairingIDText(rec.ID()), Node: rec.Display().Remote.String(), Status: initiatorStatusName(rec), Code: rec.Display().Code.String()}
		if rec.Reason != pairing.WithdrawNone {
			row.Reason = rec.Reason.String()
		}
		out.Outgoing = append(out.Outgoing, row)
	}
	for _, n := range v.LocallyRevoked {
		out.LocallyRevoked = append(out.LocallyRevoked, n.String())
	}
	return out
}

// trust.remove: remove a node from this node's trust. The other end is not told.
var trustRemove = &method{
	name: "trust.remove", op: admin.Operation{Kind: admin.OpLocallyRevokeNode},
	plan: func(_ *Server, raw json.RawMessage) (*plan, error) {
		node, canon, err := readNodeParams(raw)
		if err != nil {
			return nil, err
		}
		return &plan{canonical: canon, operation: engine.RemoveNodeOp(node, removedResult)}, nil
	},
}

// removedResult words the removal of a node: the credentials it revoked now.
func removedResult(r engine.NodeRemoved) ([]byte, error) {
	revoked := []string{}
	for _, d := range r.Credentials {
		revoked = append(revoked, d.String())
	}
	return json.Marshal(struct {
		Node    string   `json:"node"`
		Revoked []string `json:"revoked_credentials"`
	}{r.Node.String(), revoked})
}

// trust.clear: take a removed node back into this node's trust.
var trustClear = &method{
	name: "trust.clear", op: admin.Operation{Kind: admin.OpClearLocallyRevoked},
	plan: func(_ *Server, raw json.RawMessage) (*plan, error) {
		node, canon, err := readNodeParams(raw)
		if err != nil {
			return nil, err
		}
		return &plan{canonical: canon, operation: engine.ClearNodeOp(node, clearedResult)}, nil
	},
}

// clearedResult words the taking back of a node.
func clearedResult(r engine.NodeCleared) ([]byte, error) {
	return json.Marshal(struct {
		Node               string `json:"node"`
		NewPairingRequired bool   `json:"new_pairing_required"`
	}{r.Node.String(), r.NewPairingRequired})
}
