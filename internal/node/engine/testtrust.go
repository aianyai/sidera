//go:build netacceptance

package engine

import (
	"context"
	"errors"
	"time"

	"github.com/aianyai/sidera/internal/credential"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
)

// This file is present only in builds that carry the netacceptance tag. It gives a
// test of two real nodes the one thing the pairing will give a deployed node: a
// credential between them, signed by both identity keys, held by both. Each node
// signs with its own key and the signed record travels between the nodes through
// the test, so no key leaves the node.

// TestOfferCredential is the credential between this node and node with this
// node's signature on it, for the other node to complete. Nothing is stored.
func (e *Engine) TestOfferCredential(ctx context.Context, node ident.NodeID) (record.Record, error) {
	c := &cmdTestCredential{call: newCall[record.Record](), node: node}
	return roundTrip(ctx, e, submitExternal, c, c.call)
}

// TestInstallCredential keeps the credential rec, which names this node and node:
// signed by the other node only, it is completed with this node's signature
// first. It returns the record that was stored.
func (e *Engine) TestInstallCredential(ctx context.Context, node ident.NodeID, rec record.Record) (record.Record, error) {
	c := &cmdTestCredential{call: newCall[record.Record](), node: node, rec: &rec}
	return roundTrip(ctx, e, submitExternal, c, c.call)
}

type cmdTestCredential struct {
	call[record.Record]
	node ident.NodeID
	rec  *record.Record // nil: offer a new credential
}

func (c *cmdTestCredential) gate() gate { return gateWrite }
func (c *cmdTestCredential) run(e *Engine, now time.Time) {
	c.done(e.testCredential(c.node, c.rec, now))
}

func (e *Engine) testCredential(node ident.NodeID, rec *record.Record, now time.Time) (record.Record, error) {
	if e.d.Keys == nil {
		return record.Record{}, errNoKeys
	}
	if node == e.d.Node {
		return record.Record{}, invalidArgument("node", errors.New("a credential needs two nodes"))
	}
	signer := e.d.Keys.IdentitySigner()
	if rec == nil {
		nonce, err := credential.NewNonce(e.randomReader())
		if err != nil {
			return record.Record{}, err
		}
		return record.Seal(credential.Descriptor, credential.New(e.d.Node, node, now, time.Time{}, nonce), signer)
	}
	partial, err := record.VerifyPartial(credential.Descriptor, *rec)
	if err != nil {
		return record.Record{}, invalidArgument("credential", err)
	}
	if ends := partial.Payload(); !(ends.A == e.d.Node && ends.B == node) && !(ends.A == node && ends.B == e.d.Node) {
		return record.Record{}, invalidArgument("credential", errors.New("the credential is not between this node and the node named"))
	}
	done := *rec
	if !partial.Complete() {
		if done, err = record.CoSign(credential.Descriptor, *rec, signer); err != nil {
			return record.Record{}, invalidArgument("credential", err)
		}
	}
	if err := e.installCredential(done, nil); err != nil {
		return record.Record{}, err
	}
	return done, nil
}
