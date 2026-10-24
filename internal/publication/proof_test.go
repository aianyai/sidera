package publication

import (
	"bytes"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
)

func sampleProof(t testing.TB) ReachabilityProof {
	t.Helper()
	return ReachabilityProof{Person: personOf(t, keyRoot), Node: nodeOf(t, keyA), NotAfter: t0.Add(24 * time.Hour)}
}

func TestGoldenProof(t *testing.T) {
	v, err := record.Verify(ReachabilityProofDescriptor, mustDecode(t, goldenProof))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if v.Payload() != sampleProof(t) {
		t.Errorf("payload = %+v", v.Payload())
	}
	if v.Digest().String() != goldenProofDigest {
		t.Errorf("digest = %s", v.Digest())
	}
	if v.Signers()[0] != signerOf(t, keyA).Public() {
		t.Error("not signed by the node's identity key")
	}
	r, err := record.Seal(ReachabilityProofDescriptor, sampleProof(t), signerOf(t, keyA))
	if err != nil {
		t.Fatal(err)
	}
	if got := mustEncode(t, r); !bytes.Equal(got, goldenProof) {
		t.Errorf("proof differs from the vector:\n got %x\nwant %x", got, goldenProof)
	}
}

func TestProofSigners(t *testing.T) {
	payload := mustDecode(t, goldenProof).Payload
	tests := []struct {
		name    string
		typ     string
		signers []int
		want    error
	}{
		{"the node's network key", ReachabilityProofType, []int{keyNetA}, record.ErrSignerNotAllowed},
		{"another node's identity key", ReachabilityProofType, []int{keyOther}, record.ErrSignerNotAllowed},
		{"the person's root key", ReachabilityProofType, []int{keyRoot}, record.ErrSignerNotAllowed},
		{"extra signature", ReachabilityProofType, []int{keyA, keyRoot}, record.ErrSignerNotAllowed},
		{"wrong type string", ReachabilityType, []int{keyA}, record.ErrType},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var ss []record.Signer
			for _, n := range tc.signers {
				ss = append(ss, signerOf(t, n))
			}
			r, err := record.Sign(tc.typ, payload, ss...)
			if err != nil {
				t.Fatal(err)
			}
			_, err = record.Verify(ReachabilityProofDescriptor, r)
			requireErr(t, err, tc.want)
		})
	}
	t.Run("missing signature", func(t *testing.T) {
		r := mustDecode(t, goldenProof)
		r.Signatures = nil
		if _, err := record.Verify(ReachabilityProofDescriptor, r); err == nil {
			t.Error("a proof without a signature verified")
		}
	})
}

func TestProofPayloadRules(t *testing.T) {
	good := reachabilityProofWire{Person: personOf(t, keyRoot).Bytes(), Node: nodeOf(t, keyA).Bytes(), NotAfter: unixT0 + 86400}
	tests := []struct {
		name   string
		mutate func(w *reachabilityProofWire)
		want   error
	}{
		{"valid", func(w *reachabilityProofWire) {}, nil},
		{"person unset", func(w *reachabilityProofWire) { w.Person = make([]byte, 32) }, ident.ErrZero},
		{"node unset", func(w *reachabilityProofWire) { w.Node = make([]byte, 32) }, ident.ErrZero},
		{"node of the wrong length", func(w *reachabilityProofWire) { w.Node = w.Node[:16] }, ident.ErrLength},
		{"not_after zero", func(w *reachabilityProofWire) { w.NotAfter = 0 }, record.ErrTime},
		{"not_after negative", func(w *reachabilityProofWire) { w.NotAfter = -1 }, record.ErrTime},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := good
			tc.mutate(&w)
			payload, err := record.MarshalCanonical(w)
			if err != nil {
				t.Fatal(err)
			}
			r, err := record.Sign(ReachabilityProofType, payload, signerOf(t, keyA))
			if err != nil {
				t.Fatal(err)
			}
			_, err = record.Verify(ReachabilityProofDescriptor, r)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("rejected: %v", err)
				}
				return
			}
			requireErr(t, err, record.ErrPayload, tc.want)
		})
	}
}

// TestProofThroughAdmin: the proof's expiry, node and person are admin's to
// judge, over the facts this package hands it.
func TestProofThroughAdmin(t *testing.T) {
	v, err := record.Verify(ReachabilityProofDescriptor, mustDecode(t, goldenProof))
	if err != nil {
		t.Fatal(err)
	}
	facts := v.Payload().Admin()
	node, person := nodeOf(t, keyA), personOf(t, keyRoot)
	tests := []struct {
		name      string
		node      ident.NodeID
		requester ident.PersonID
		now       time.Time
		want      admin.Reason
	}{
		{"before the end", node, person, t0.Add(time.Hour), admin.ReasonOK},
		{"one second before the end", node, person, t0.Add(24*time.Hour - time.Second), admin.ReasonOK},
		{"at the end", node, person, t0.Add(24 * time.Hour), admin.ReasonProofExpired},
		{"another node", nodeOf(t, keyOther), person, t0, admin.ReasonProofNodeMismatch},
		{"another person", node, personOf(t, keyOther), t0, admin.ReasonProofPersonMismatch},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := admin.CheckReachabilityProof(facts, tc.node, tc.requester, tc.now)
			if got.Reason != tc.want || got.Allowed != (tc.want == admin.ReasonOK) {
				t.Errorf("verdict = %+v, want %v", got, tc.want)
			}
		})
	}
	// A proof that outlives the list entry is the issuer's to avoid, and admin
	// says so: the facts carried here are what it compares.
	list, _ := admin.NewList([]admin.Entry{{Person: person, Level: admin.LevelView, ExpiresAt: t0.Add(time.Hour)}})
	if got := admin.MayIssueReachabilityProof(list, person, v.Payload().NotAfter, t0); got.Reason != admin.ReasonProofOutlivesEntry {
		t.Errorf("issue verdict = %+v", got)
	}
}
