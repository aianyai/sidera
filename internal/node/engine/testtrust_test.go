//go:build netacceptance

package engine_test

import (
	"context"
	"testing"

	"github.com/aianyai/sidera/internal/credential"
	"github.com/aianyai/sidera/internal/record"
)

// The two steps that give a test node a credential it did not pair for: the node
// signs its own end and another node's signature completes it, or the other node
// signs first and the node completes and keeps it. Either way the other node is
// trusted afterwards, and a credential between other nodes, or with the node
// itself, is refused.
func TestATestCredentialIsSignedByBothEndsAndKept(t *testing.T) {
	ctx := context.Background()
	for _, offered := range []bool{true, false} {
		r := trustRig(t)
		e := r.start()
		peer := newPeerNode(t, 1)
		to := register(t, e, "notes")
		wantTrusted(t, r.inboundFrom(t, e, peer.node, to, "m1"), false, "no credential")

		var done record.Record
		if offered {
			rec, err := e.TestOfferCredential(ctx, peer.node)
			if err != nil {
				t.Fatal(err)
			}
			if rec, err = record.CoSign(credential.Descriptor, rec, peer.identity); err != nil {
				t.Fatal(err)
			}
			done, err = e.TestInstallCredential(ctx, peer.node, rec)
			if err != nil {
				t.Fatal(err)
			}
		} else {
			c := peer.credentialWith(r.keys.IdentitySigner(), true)
			var err error
			if done, err = e.TestInstallCredential(ctx, peer.node, c.Record); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := record.Verify(credential.Descriptor, done); err != nil {
			t.Fatalf("offered %v: the credential kept is not complete: %v", offered, err)
		}
		wantTrusted(t, r.inboundFrom(t, e, peer.node, to, "m2"), true, "a test credential")

		other := newPeerNode(t, 2)
		if _, err := e.TestInstallCredential(ctx, other.node, done); err == nil {
			t.Error("a credential was kept as one with a node it does not name")
		}
		if _, err := e.TestOfferCredential(ctx, r.node); err == nil {
			t.Error("a credential with the node itself was offered")
		}
	}
}
