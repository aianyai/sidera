package revocation

import (
	"testing"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/trust"
)

// The retention rows are sealed and verified statements of the nodes and persons
// that indexedSigner makes: Classify reads kind and node only, so which
// certificate or device key a statement names does not matter to a row.

func nodeSelfOf(t testing.TB, n int) Revocation {
	t.Helper()
	identity := indexedSigner(t, n)
	return mustRevocation(t, must(IssueNodeSelfRevocation(identity)), nil)
}

func certRevocationOf(t testing.TB, n int) Revocation {
	t.Helper()
	identity := indexedSigner(t, n)
	_, cert := networkCertificate(t, identity, seedSigner(t, 0x21), 1, epoch, endTime)
	return mustRevocation(t, must(IssueNetworkKeyCertificateRevocation(cert, identity)), nil)
}

func personSelfOf(t testing.TB, n int) Revocation {
	t.Helper()
	return mustRevocation(t, must(IssuePersonSelfRevocation(indexedSigner(t, n))), nil)
}

func deviceRevocationOf(t testing.TB, n int, k ident.PublicKey) Revocation {
	t.Helper()
	return mustRevocation(t, must(IssueDeviceKeyRevocation(indexedSigner(t, n), k)), nil)
}

func nodeIDOf(t testing.TB, n int) ident.NodeID { return nodeOf(t, indexedSigner(t, n)) }

// Rule table of the retention decision, each row named after the rule.
func TestClassify(t *testing.T) {
	const (
		nLocal, nGraph, nDeclared, nUnrelated, nPairing = 1, 2, 3, 4, 7
		nPerson                                         = 5
	)
	local, graph, declared := nodeIDOf(t, nLocal), nodeIDOf(t, nGraph), nodeIDOf(t, nDeclared)
	pairing := nodeIDOf(t, nPairing)
	device := indexedSigner(t, 6).Public()
	ctx := Context{
		Local:              local,
		TrustGraph:         trust.NodeSet{graph: {}},
		DeclarationSigners: trust.NodeSet{declared: {}},
		PairingNodes:       trust.NodeSet{pairing: {}},
	}
	p := Params{MaxForwardOnce: 3}
	long := func(r Reason) Retention { return Retention{LongTerm, r} }

	rows := []struct {
		rule string
		rev  Revocation
		ctx  Context
		held int
		p    Params
		want Retention
	}{
		{"a person self-revocation is long-term", personSelfOf(t, nPerson), Context{}, 0, p, long(ReasonPersonLevel)},
		{"a person self-revocation is long-term with the cap full", personSelfOf(t, nPerson), Context{}, 1000, p, long(ReasonPersonLevel)},
		{"a person self-revocation is long-term with the cap unset", personSelfOf(t, nPerson), Context{}, 0, Params{}, long(ReasonPersonLevel)},
		{"a device key revocation is long-term", deviceRevocationOf(t, nPerson, device), Context{}, 0, p, long(ReasonDeviceLevel)},
		{"a device key revocation is long-term with the cap full", deviceRevocationOf(t, nPerson, device), Context{}, 1000, p, long(ReasonDeviceLevel)},
		{"the local node's self-revocation is long-term", nodeSelfOf(t, nLocal), ctx, 0, p, long(ReasonLocalNode)},
		{"the local node's certificate revocation is long-term", certRevocationOf(t, nLocal), ctx, 0, p, long(ReasonLocalNode)},
		{"the local node is local although it is also in the graph", nodeSelfOf(t, nLocal), Context{Local: local, TrustGraph: trust.NodeSet{local: {}}}, 0, p, long(ReasonLocalNode)},
		{"a node in the trust graph: its self-revocation is long-term", nodeSelfOf(t, nGraph), ctx, 0, p, long(ReasonTrustGraphNode)},
		{"a node in the trust graph: its certificate revocation is long-term", certRevocationOf(t, nGraph), ctx, 0, p, long(ReasonTrustGraphNode)},
		{"a node in the trust graph stays long-term with the cap full", certRevocationOf(t, nGraph), ctx, 1000, p, long(ReasonTrustGraphNode)},
		{"a node in the trust graph stays long-term with the cap unset", certRevocationOf(t, nGraph), ctx, 0, Params{}, long(ReasonTrustGraphNode)},
		{"a declaration signer: its self-revocation is long-term", nodeSelfOf(t, nDeclared), ctx, 0, p, long(ReasonDeclarationSigner)},
		{"a declaration signer: its certificate revocation is long-term", certRevocationOf(t, nDeclared), ctx, 0, p, long(ReasonDeclarationSigner)},
		{"the trust graph is named before the declaration signers", nodeSelfOf(t, nGraph), Context{TrustGraph: trust.NodeSet{graph: {}}, DeclarationSigners: trust.NodeSet{graph: {}}}, 0, p, long(ReasonTrustGraphNode)},
		{"a node with a pairing expectation or request: its self-revocation is long-term", nodeSelfOf(t, nPairing), ctx, 0, p, long(ReasonPairingNode)},
		{"a node with a pairing expectation or request: its certificate revocation is long-term", certRevocationOf(t, nPairing), ctx, 0, p, long(ReasonPairingNode)},
		{"a pairing node stays long-term with the cap full", nodeSelfOf(t, nPairing), ctx, 1000, p, long(ReasonPairingNode)},
		{"a pairing node stays long-term with the cap unset", nodeSelfOf(t, nPairing), ctx, 0, Params{}, long(ReasonPairingNode)},
		{"a pairing node stays long-term with a negative count held", nodeSelfOf(t, nPairing), ctx, -1, p, long(ReasonPairingNode)},
		{"the declaration signers are named before the pairing nodes", nodeSelfOf(t, nDeclared), Context{DeclarationSigners: trust.NodeSet{declared: {}}, PairingNodes: trust.NodeSet{declared: {}}}, 0, p, long(ReasonDeclarationSigner)},
		{"the trust graph is named before the pairing nodes", nodeSelfOf(t, nGraph), Context{TrustGraph: trust.NodeSet{graph: {}}, PairingNodes: trust.NodeSet{graph: {}}}, 0, p, long(ReasonTrustGraphNode)},
		{"the local node is named before the pairing nodes", nodeSelfOf(t, nLocal), Context{Local: local, PairingNodes: trust.NodeSet{local: {}}}, 0, p, long(ReasonLocalNode)},
		{"a node only in the pairing set is not related through the other sets", nodeSelfOf(t, nPairing), Context{Local: local, TrustGraph: trust.NodeSet{graph: {}}, DeclarationSigners: trust.NodeSet{declared: {}}}, 0, p, Retention{ForwardOnce, ReasonUnrelatedNode}},
		{"a person self-revocation ignores the pairing nodes", personSelfOf(t, nPerson), ctx, 0, p, long(ReasonPersonLevel)},
		{"an unrelated self-revocation is forwarded once", nodeSelfOf(t, nUnrelated), ctx, 0, p, Retention{ForwardOnce, ReasonUnrelatedNode}},
		{"an unrelated certificate revocation is forwarded once", certRevocationOf(t, nUnrelated), ctx, 0, p, Retention{ForwardOnce, ReasonUnrelatedNode}},
		{"one below the cap is still forwarded", nodeSelfOf(t, nUnrelated), ctx, 2, p, Retention{ForwardOnce, ReasonUnrelatedNode}},
		{"at the cap an unrelated revocation is dropped", nodeSelfOf(t, nUnrelated), ctx, 3, p, Retention{Dropped, ReasonCapReached}},
		{"above the cap an unrelated revocation is dropped", nodeSelfOf(t, nUnrelated), ctx, 4, p, Retention{Dropped, ReasonCapReached}},
		{"an unset cap drops an unrelated revocation", nodeSelfOf(t, nUnrelated), ctx, 0, Params{}, Retention{Dropped, ReasonInvalidInput}},
		{"a negative cap drops an unrelated revocation", nodeSelfOf(t, nUnrelated), ctx, 0, Params{MaxForwardOnce: -1}, Retention{Dropped, ReasonInvalidInput}},
		{"a negative count held is refused", nodeSelfOf(t, nUnrelated), ctx, -1, p, Retention{Dropped, ReasonInvalidInput}},
		{"the zero Revocation is dropped", Revocation{}, ctx, 0, p, Retention{Dropped, ReasonInvalidInput}},
		{"the zero Local never matches a node", nodeSelfOf(t, nUnrelated), Context{}, 0, p, Retention{ForwardOnce, ReasonUnrelatedNode}},
		{"nil sets relate nobody", nodeSelfOf(t, nGraph), Context{Local: local}, 0, p, Retention{ForwardOnce, ReasonUnrelatedNode}},
	}
	for _, row := range rows {
		t.Run(row.rule, func(t *testing.T) {
			if got := Classify(row.rev, row.ctx, row.held, row.p); got != row.want {
				t.Fatalf("got %v/%v, want %v/%v", got.Class, got.Reason, row.want.Class, row.want.Reason)
			}
		})
	}
}

// The names of classes, reasons and kinds are distinct, and the zero values are
// "invalid".
func TestEnumNames(t *testing.T) {
	seen := map[string]string{}
	add := func(kind, name string) {
		if prev, dup := seen[name]; dup && prev == kind {
			t.Fatalf("%s name %q repeated", kind, name)
		}
		seen[name] = kind
	}
	for _, c := range []Class{ClassInvalid, LongTerm, ForwardOnce, Dropped} {
		add("class", c.String())
	}
	for r := ReasonInvalid; r <= ReasonInvalidInput; r++ {
		add("reason", r.String())
	}
	for k := KindInvalid; k <= KindDeviceKey; k++ {
		add("kind", k.String())
	}
	if ClassInvalid.String() != "invalid" || ReasonInvalid.String() != "invalid" || KindInvalid.String() != "invalid" {
		t.Fatal("zero values are not invalid")
	}
	if (Retention{}).Class != ClassInvalid {
		t.Fatal("the zero Retention is a decision")
	}
	for _, k := range []Kind{KindNodeSelf, KindNetworkKeyCertificate} {
		if !k.NodeLevel() {
			t.Fatalf("%v is node-level", k)
		}
	}
	for _, k := range []Kind{KindInvalid, KindPersonSelf, KindDeviceKey} {
		if k.NodeLevel() {
			t.Fatalf("%v is not node-level", k)
		}
	}
}

// Classify reads the context and nothing else: the same inputs give the same
// answer however the sets were built.
func TestClassifyDoesNotWriteItsContext(t *testing.T) {
	g := nodeIDOf(t, 2)
	ctx := Context{TrustGraph: trust.NodeSet{g: {}}, DeclarationSigners: trust.NodeSet{}}
	before := len(ctx.TrustGraph) + len(ctx.DeclarationSigners)
	for i := 0; i < 3; i++ {
		_ = Classify(nodeSelfOf(t, 9), ctx, i, Params{MaxForwardOnce: 5})
	}
	if len(ctx.TrustGraph)+len(ctx.DeclarationSigners) != before {
		t.Fatal("the context changed")
	}
}
