package revocation

import (
	"testing"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/trust"
)

// Statements for the retention rows are built from a payload and a synthetic
// digest: Classify reads kind and node only.
func nodeSelfOf(n ident.NodeID, digest byte) Revocation {
	return Of(record.Verified[NodeSelfRevocation]{Payload: NodeSelfRevocation{Node: n}, Digest: record.Digest{digest}})
}

func certRevocationOf(n ident.NodeID, digest byte) Revocation {
	return Of(record.Verified[NetworkKeyCertificateRevocation]{
		Payload: NetworkKeyCertificateRevocation{Node: n, Serial: 1, Certificate: record.Digest{0xc0, digest}},
		Digest:  record.Digest{digest},
	})
}

func personSelfOf(p ident.PersonID) Revocation {
	return Of(record.Verified[PersonSelfRevocation]{Payload: PersonSelfRevocation{Person: p}, Digest: record.Digest{0x70}})
}

func deviceRevocationOf(p ident.PersonID, k ident.PublicKey) Revocation {
	return Of(record.Verified[DeviceKeyRevocation]{Payload: DeviceKeyRevocation{Person: p, DeviceKey: k}, Digest: record.Digest{0x71}})
}

func nodeIDOf(t testing.TB, n int) ident.NodeID { return nodeOf(t, indexedSigner(t, n)) }

// Rule table of the retention decision, each row named after the rule.
func TestClassify(t *testing.T) {
	local, graph, declared, unrelated := nodeIDOf(t, 1), nodeIDOf(t, 2), nodeIDOf(t, 3), nodeIDOf(t, 4)
	pairing := nodeIDOf(t, 7)
	person := personOf(t, indexedSigner(t, 5))
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
		{"a person self-revocation is long-term", personSelfOf(person), Context{}, 0, p, long(ReasonPersonLevel)},
		{"a person self-revocation is long-term with the cap full", personSelfOf(person), Context{}, 1000, p, long(ReasonPersonLevel)},
		{"a person self-revocation is long-term with the cap unset", personSelfOf(person), Context{}, 0, Params{}, long(ReasonPersonLevel)},
		{"a device key revocation is long-term", deviceRevocationOf(person, device), Context{}, 0, p, long(ReasonDeviceLevel)},
		{"a device key revocation is long-term with the cap full", deviceRevocationOf(person, device), Context{}, 1000, p, long(ReasonDeviceLevel)},
		{"the local node's self-revocation is long-term", nodeSelfOf(local, 1), ctx, 0, p, long(ReasonLocalNode)},
		{"the local node's certificate revocation is long-term", certRevocationOf(local, 1), ctx, 0, p, long(ReasonLocalNode)},
		{"the local node is local although it is also in the graph", nodeSelfOf(local, 1), Context{Local: local, TrustGraph: trust.NodeSet{local: {}}}, 0, p, long(ReasonLocalNode)},
		{"a node in the trust graph: its self-revocation is long-term", nodeSelfOf(graph, 1), ctx, 0, p, long(ReasonTrustGraphNode)},
		{"a node in the trust graph: its certificate revocation is long-term", certRevocationOf(graph, 1), ctx, 0, p, long(ReasonTrustGraphNode)},
		{"a node in the trust graph stays long-term with the cap full", certRevocationOf(graph, 1), ctx, 1000, p, long(ReasonTrustGraphNode)},
		{"a node in the trust graph stays long-term with the cap unset", certRevocationOf(graph, 1), ctx, 0, Params{}, long(ReasonTrustGraphNode)},
		{"a declaration signer: its self-revocation is long-term", nodeSelfOf(declared, 1), ctx, 0, p, long(ReasonDeclarationSigner)},
		{"a declaration signer: its certificate revocation is long-term", certRevocationOf(declared, 1), ctx, 0, p, long(ReasonDeclarationSigner)},
		{"the trust graph is named before the declaration signers", nodeSelfOf(graph, 1), Context{TrustGraph: trust.NodeSet{graph: {}}, DeclarationSigners: trust.NodeSet{graph: {}}}, 0, p, long(ReasonTrustGraphNode)},
		{"a node with a pairing expectation or request: its self-revocation is long-term", nodeSelfOf(pairing, 1), ctx, 0, p, long(ReasonPairingNode)},
		{"a node with a pairing expectation or request: its certificate revocation is long-term", certRevocationOf(pairing, 1), ctx, 0, p, long(ReasonPairingNode)},
		{"a pairing node stays long-term with the cap full", nodeSelfOf(pairing, 1), ctx, 1000, p, long(ReasonPairingNode)},
		{"a pairing node stays long-term with the cap unset", nodeSelfOf(pairing, 1), ctx, 0, Params{}, long(ReasonPairingNode)},
		{"a pairing node stays long-term with a negative count held", nodeSelfOf(pairing, 1), ctx, -1, p, long(ReasonPairingNode)},
		{"the declaration signers are named before the pairing nodes", nodeSelfOf(declared, 1), Context{DeclarationSigners: trust.NodeSet{declared: {}}, PairingNodes: trust.NodeSet{declared: {}}}, 0, p, long(ReasonDeclarationSigner)},
		{"the trust graph is named before the pairing nodes", nodeSelfOf(graph, 1), Context{TrustGraph: trust.NodeSet{graph: {}}, PairingNodes: trust.NodeSet{graph: {}}}, 0, p, long(ReasonTrustGraphNode)},
		{"the local node is named before the pairing nodes", nodeSelfOf(local, 1), Context{Local: local, PairingNodes: trust.NodeSet{local: {}}}, 0, p, long(ReasonLocalNode)},
		{"a node only in the pairing set is not related through the other sets", nodeSelfOf(pairing, 1), Context{Local: local, TrustGraph: trust.NodeSet{graph: {}}, DeclarationSigners: trust.NodeSet{declared: {}}}, 0, p, Retention{ForwardOnce, ReasonUnrelatedNode}},
		{"a person self-revocation ignores the pairing nodes", personSelfOf(personOf(t, indexedSigner(t, 5))), ctx, 0, p, long(ReasonPersonLevel)},
		{"an unrelated self-revocation is forwarded once", nodeSelfOf(unrelated, 1), ctx, 0, p, Retention{ForwardOnce, ReasonUnrelatedNode}},
		{"an unrelated certificate revocation is forwarded once", certRevocationOf(unrelated, 1), ctx, 0, p, Retention{ForwardOnce, ReasonUnrelatedNode}},
		{"one below the cap is still forwarded", nodeSelfOf(unrelated, 1), ctx, 2, p, Retention{ForwardOnce, ReasonUnrelatedNode}},
		{"at the cap an unrelated revocation is dropped", nodeSelfOf(unrelated, 1), ctx, 3, p, Retention{Dropped, ReasonCapReached}},
		{"above the cap an unrelated revocation is dropped", nodeSelfOf(unrelated, 1), ctx, 4, p, Retention{Dropped, ReasonCapReached}},
		{"an unset cap drops an unrelated revocation", nodeSelfOf(unrelated, 1), ctx, 0, Params{}, Retention{Dropped, ReasonInvalidInput}},
		{"a negative cap drops an unrelated revocation", nodeSelfOf(unrelated, 1), ctx, 0, Params{MaxForwardOnce: -1}, Retention{Dropped, ReasonInvalidInput}},
		{"a negative count held is refused", nodeSelfOf(unrelated, 1), ctx, -1, p, Retention{Dropped, ReasonInvalidInput}},
		{"the zero Revocation is dropped", Revocation{}, ctx, 0, p, Retention{Dropped, ReasonInvalidInput}},
		{"a statement with no digest is dropped", Of(record.Verified[NodeSelfRevocation]{Payload: NodeSelfRevocation{Node: graph}}), ctx, 0, p, Retention{Dropped, ReasonInvalidInput}},
		{"the zero Local never matches a node", nodeSelfOf(unrelated, 1), Context{}, 0, p, Retention{ForwardOnce, ReasonUnrelatedNode}},
		{"nil sets relate nobody", nodeSelfOf(graph, 1), Context{Local: local}, 0, p, Retention{ForwardOnce, ReasonUnrelatedNode}},
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
		_ = Classify(nodeSelfOf(nodeIDOf(t, 9), 1), ctx, i, Params{MaxForwardOnce: 5})
	}
	if len(ctx.TrustGraph)+len(ctx.DeclarationSigners) != before {
		t.Fatal("the context changed")
	}
}
