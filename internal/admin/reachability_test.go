package admin

import (
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/ident"
)

func TestMayIssueReachabilityProof(t *testing.T) {
	viewer, manager, expiring, expiredNow, unlisted := person(t, 1), person(t, 2), person(t, 3), person(t, 4), person(t, 5)
	list := mustList(t,
		Entry{Person: viewer, Level: LevelView},
		Entry{Person: manager, Level: LevelManage},
		Entry{Person: expiring, Level: LevelView, ExpiresAt: t0.Add(time.Hour)},
		Entry{Person: expiredNow, Level: LevelFullControl, ExpiresAt: t0},
	)
	later := t0.Add(24 * time.Hour)
	tests := []struct {
		name     string
		list     List
		person   ident.PersonID
		notAfter time.Time
		want     ProofVerdict
	}{
		{"a view-level person on the list may get a proof", list, viewer, later, ProofVerdict{true, ReasonOK}},
		{"a manage-level person on the list may get a proof", list, manager, later, ProofVerdict{true, ReasonOK}},
		{"an entry without an expiry bounds the proof by nothing", list, viewer, later.Add(1000 * time.Hour), ProofVerdict{true, ReasonOK}},
		{"a proof ending before the entry expires is issued", list, expiring, t0.Add(time.Minute), ProofVerdict{true, ReasonOK}},
		{"a proof ending exactly when the entry expires is issued", list, expiring, t0.Add(time.Hour), ProofVerdict{true, ReasonOK}},
		{"a proof ending one instant after the entry expires is refused", list, expiring, t0.Add(time.Hour + time.Nanosecond),
			ProofVerdict{false, ReasonProofOutlivesEntry}},
		{"a person absent from the list gets no proof", list, unlisted, later, ProofVerdict{false, ReasonNotOnList}},
		{"an empty list issues no proof", List{}, viewer, later, ProofVerdict{false, ReasonNotOnList}},
		{"a person whose entry expires at this instant gets no proof", list, expiredNow, t0.Add(-time.Hour),
			ProofVerdict{false, ReasonEntryExpired}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := MayIssueReachabilityProof(tc.list, tc.person, tc.notAfter, t0); got != tc.want {
				t.Fatalf("verdict = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestCheckReachabilityProof(t *testing.T) {
	self, other := node(t, 1), node(t, 2)
	me, someoneElse := person(t, 1), person(t, 2)
	proof := ReachabilityProof{Node: self, Person: me, NotAfter: t0.Add(time.Hour)}
	with := func(mut func(*ReachabilityProof)) ReachabilityProof {
		p := proof
		mut(&p)
		return p
	}
	tests := []struct {
		name      string
		p         ReachabilityProof
		node      ident.NodeID
		requester ident.PersonID
		now       time.Time
		want      ProofVerdict
	}{
		{"a proof for this node and this requester is valid", proof, self, me, t0, ProofVerdict{true, ReasonOK}},
		{"a proof is valid one instant before NotAfter", proof, self, me, proof.NotAfter.Add(-time.Nanosecond), ProofVerdict{true, ReasonOK}},
		{"a proof is expired at the NotAfter instant", proof, self, me, proof.NotAfter, ProofVerdict{false, ReasonProofExpired}},
		{"a proof is expired after NotAfter", proof, self, me, proof.NotAfter.Add(time.Second), ProofVerdict{false, ReasonProofExpired}},
		{"a proof without NotAfter is expired", with(func(p *ReachabilityProof) { p.NotAfter = time.Time{} }), self, me, t0,
			ProofVerdict{false, ReasonProofExpired}},
		{"a proof issued for another node is refused", proof, other, me, t0, ProofVerdict{false, ReasonProofNodeMismatch}},
		{"a proof issued to another person is refused", proof, self, someoneElse, t0, ProofVerdict{false, ReasonProofPersonMismatch}},
		{"an unset node never matches, even a proof with an unset node", with(func(p *ReachabilityProof) { p.Node = ident.NodeID{} }),
			ident.NodeID{}, me, t0, ProofVerdict{false, ReasonProofNodeMismatch}},
		{"an unset requester never matches, even a proof with an unset person", with(func(p *ReachabilityProof) { p.Person = ident.PersonID{} }),
			self, ident.PersonID{}, t0, ProofVerdict{false, ReasonProofPersonMismatch}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CheckReachabilityProof(tc.p, tc.node, tc.requester, tc.now); got != tc.want {
				t.Fatalf("verdict = %+v, want %+v", got, tc.want)
			}
		})
	}
}
