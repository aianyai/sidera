//go:build netacceptance

package manageapi

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/record"
)

func netMethods() []*method { return []*method{testTrustOffer, testTrustCredential} }

// maxTestRecord bounds a credential record handed to a test row: a credential
// with its two signatures is far smaller.
const maxTestRecord = 4096

// credentialService is what the two rows below ask of the engine.
type credentialService interface {
	TestOfferCredential(context.Context, ident.NodeID) (record.Record, error)
	TestInstallCredential(context.Context, ident.NodeID, record.Record) (record.Record, error)
}

// test.trust.offer: the credential between this node and params.node, signed by
// this node's identity key alone and stored nowhere. Its record is what the other
// node completes with test.trust.credential.
var testTrustOffer = &method{name: "test.trust.offer", test: func(s *Server, ctx context.Context, raw json.RawMessage) (any, error) {
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
	svc, ok := s.cfg.Service.(credentialService)
	if !ok {
		return nil, fmt.Errorf("manageapi: test.trust.offer requires a credential service")
	}
	rec, err := svc.TestOfferCredential(ctx, node)
	if err != nil {
		return nil, err
	}
	return credentialResult(node.String(), rec)
}}

// test.trust.credential: this node keeps the credential in params.record, which
// names it and params.node and carries the other node's signature, after adding its
// own when that is missing. It is how a test gives two nodes the direct trust that
// pairing gives them in a deployment.
var testTrustCredential = &method{name: "test.trust.credential", test: func(s *Server, ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Node   string `json:"node"`
		Record string `json:"record"`
	}
	if err := decodeParams(raw, &p); err != nil {
		return nil, err
	}
	node, err := parseNodeParam(p.Node)
	if err != nil {
		return nil, err
	}
	wire, err := hex.DecodeString(p.Record)
	if err != nil {
		return nil, apiwire.Malformed("the value is not hexadecimal", "params.record")
	}
	rec, err := record.Decode(wire, maxTestRecord)
	if err != nil {
		return nil, apiwire.Malformed("the value is not a record", "params.record")
	}
	svc, ok := s.cfg.Service.(credentialService)
	if !ok {
		return nil, fmt.Errorf("manageapi: test.trust.credential requires a credential service")
	}
	done, err := svc.TestInstallCredential(ctx, node, rec)
	if err != nil {
		return nil, err
	}
	return credentialResult(node.String(), done)
}}

// credentialResult is the answer of both rows: the node, the digest of the
// credential and its record, which has the signatures the node put on it.
func credentialResult(node string, rec record.Record) (any, error) {
	wire, err := rec.Encode()
	if err != nil {
		return nil, err
	}
	return struct {
		Node       string `json:"node"`
		Credential string `json:"credential"`
		Record     string `json:"record"`
	}{node, rec.Digest().String(), hex.EncodeToString(wire)}, nil
}
