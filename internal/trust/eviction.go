package trust

import (
	"slices"
	"time"

	"github.com/aianyai/sidera/internal/ident"
)

// PartyCredentials is a direct trust party of an evicted node together with
// the credentials between them that are not yet revoked.
type PartyCredentials struct {
	Party       ident.NodeID
	Credentials []CredentialDigest
}

// Eviction is the progress of evicting a node, as far as the local two-hop
// view shows. Credentials with a retired node carry no trust and are not part
// of it: they are neither outstanding nor a dependency.
type Eviction struct {
	Target ident.NodeID
	// Unrevoked are the direct trust parties of Target (the local node
	// included) that still hold a usable credential with it, ordered by node.
	// Each must revoke its own credential: only the two parties of a
	// credential can revoke it.
	Unrevoked []PartyCredentials
	// Dependent are the inherited paths from the local node that rest on
	// Target, as intermediate or as end, over usable credentials. Vouchers are
	// not considered: a path that is merely suspended still counts, because it
	// recovers when its intermediate returns.
	Dependent []Path
	// Complete is false while Unrevoked is not empty. A suspended path is not
	// an eviction: the intermediate's trust comes back when it is online
	// again unless its credential was revoked.
	Complete bool
}

// EvictionProgress reports how far the eviction of x has come at now. Parties
// beyond the local two-hop view are invisible to it.
//
// A retired x is complete at once, with nothing outstanding. A retired party of
// x is not waited for either: its credential with x cannot carry trust, and a
// void identity will never revoke anything. So a node that was reachable only
// through a retired party reports complete too, which is true: no path to it
// is left.
func EvictionProgress(v View, x ident.NodeID, now time.Time) Eviction {
	e := Eviction{Target: x}

	byParty := map[ident.NodeID][]CredentialDigest{}
	for _, c := range v.credentialsOf(x) {
		party, _ := c.other(x)
		if !v.usable(c, now) || v.retired(x) || v.retired(party) {
			continue
		}
		byParty[party] = append(byParty[party], c.Digest)
	}
	for party, ds := range byParty {
		sortDigests(ds)
		e.Unrevoked = append(e.Unrevoked, PartyCredentials{Party: party, Credentials: ds})
	}
	slices.SortFunc(e.Unrevoked, func(a, b PartyCredentials) int { return compareNode(a.Party, b.Party) })

	e.Dependent = inheritedStructure(v, now, func(via, to ident.NodeID) bool { return via == x || to == x })
	e.Complete = len(e.Unrevoked) == 0
	return e
}

// inheritedStructure lists the two-edge paths local-via-to over usable
// credentials for which keep returns true, ignoring vouchers, settings and
// the locally revoked set. Paths through or to a retired node are left out.
func inheritedStructure(v View, now time.Time, keep func(via, to ident.NodeID) bool) []Path {
	var out []Path
	for _, first := range v.credentialsOf(v.Local) {
		via, _ := first.other(v.Local)
		if !v.usable(first, now) || v.retired(via) {
			continue
		}
		for _, second := range v.credentialsOf(via) {
			to, _ := second.other(via)
			if to == v.Local || v.retired(to) || !v.usable(second, now) || !keep(via, to) {
				continue
			}
			out = append(out, Path{Via: via, To: to, Credentials: []CredentialDigest{first.Digest, second.Digest}})
		}
	}
	sortPaths(out)
	return out
}

// NewEdgeAudience returns the nodes whose owners must be told that a trust
// edge between a and b appeared: a, b and the direct trust parties of both,
// as far as the local view shows, ordered by node. It only informs; it never
// blocks the edge.
func NewEdgeAudience(v View, a, b ident.NodeID, now time.Time) []ident.NodeID {
	set := map[ident.NodeID]struct{}{a: {}, b: {}}
	for _, end := range []ident.NodeID{a, b} {
		for _, c := range v.credentialsOf(end) {
			if v.usable(c, now) {
				party, _ := c.other(end)
				set[party] = struct{}{}
			}
		}
	}
	out := make([]ident.NodeID, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sortNodes(out)
	return out
}
