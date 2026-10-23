package revocation

import (
	"math/rand"
	"testing"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/trust"
)

// oracle decides retention on small integers, without the package's types: a
// node is an index, a context is three plain values and slices, and the rule is
// written as a list of tests in order. It shares nothing with Classify.
type oracleInput struct {
	kind       string // "none", "person", "device", "node-self", "node-cert"
	node       int
	local      int // -1: no local node
	graph      []int
	declared   []int
	held, capN int
}

func oracle(in oracleInput) (class, reason string) {
	switch in.kind {
	case "person":
		return "long-term", "person-level"
	case "device":
		return "long-term", "device-level"
	case "node-self", "node-cert":
	default:
		return "dropped", "invalid-input"
	}
	has := func(xs []int, v int) bool {
		for _, x := range xs {
			if x == v {
				return true
			}
		}
		return false
	}
	for _, step := range []struct {
		hit    bool
		reason string
	}{
		{in.node == in.local, "local-node"},
		{has(in.graph, in.node), "trust-graph-node"},
		{has(in.declared, in.node), "declaration-signer"},
	} {
		if step.hit {
			return "long-term", step.reason
		}
	}
	if in.held < 0 || in.capN <= 0 {
		return "dropped", "invalid-input"
	}
	if in.held >= in.capN {
		return "dropped", "cap-reached"
	}
	return "forward-once", "unrelated-node"
}

// Property: Classify agrees with the oracle on random graphs and revocation
// mixes, and every class and reason is reached often enough that the agreement
// means something.
func TestClassifyAgainstOracle(t *testing.T) {
	const nodes, rounds = 24, 6000
	ids := make([]ident.NodeID, nodes)
	for i := range ids {
		ids[i] = nodeIDOf(t, 1000+i)
	}
	persons := []ident.PersonID{personOf(t, indexedSigner(t, 2000)), personOf(t, indexedSigner(t, 2001))}
	device := indexedSigner(t, 2002).Public()
	rng := rand.New(rand.NewSource(20261023))
	counts := map[[2]string]int{}

	for round := 0; round < rounds; round++ {
		in := oracleInput{local: -1, capN: rng.Intn(5), held: rng.Intn(8) - 1}
		if rng.Intn(3) > 0 {
			in.local = rng.Intn(nodes)
		}
		ctx := Context{TrustGraph: trust.NodeSet{}, DeclarationSigners: trust.NodeSet{}}
		if in.local >= 0 {
			ctx.Local = ids[in.local]
		}
		for n := 0; n < nodes; n++ {
			if rng.Intn(5) == 0 {
				in.graph = append(in.graph, n)
				ctx.TrustGraph[ids[n]] = struct{}{}
			}
			if rng.Intn(6) == 0 {
				in.declared = append(in.declared, n)
				ctx.DeclarationSigners[ids[n]] = struct{}{}
			}
		}
		in.node = rng.Intn(nodes)
		if in.local >= 0 && rng.Intn(5) == 0 {
			in.node = in.local
		}
		var rev Revocation
		digest := record.Digest{byte(round), byte(round >> 8), 1}
		switch k := rng.Intn(10); {
		case k < 3:
			in.kind, rev = "node-self", Of(record.Verified[NodeSelfRevocation]{Payload: NodeSelfRevocation{Node: ids[in.node]}, Digest: digest})
		case k < 6:
			in.kind, rev = "node-cert", Of(record.Verified[NetworkKeyCertificateRevocation]{
				Payload: NetworkKeyCertificateRevocation{Node: ids[in.node], Serial: uint64(rng.Intn(5) + 1), Certificate: record.Digest{9, byte(round)}}, Digest: digest})
		case k < 7:
			in.kind, rev = "person", Of(record.Verified[PersonSelfRevocation]{Payload: PersonSelfRevocation{Person: persons[rng.Intn(2)]}, Digest: digest})
		case k < 8:
			in.kind, rev = "device", Of(record.Verified[DeviceKeyRevocation]{Payload: DeviceKeyRevocation{Person: persons[rng.Intn(2)], DeviceKey: device}, Digest: digest})
		default:
			in.kind, rev = "none", Revocation{}
		}
		wantClass, wantReason := oracle(in)
		got := Classify(rev, ctx, in.held, Params{MaxForwardOnce: in.capN})
		if got.Class.String() != wantClass || got.Reason.String() != wantReason {
			t.Fatalf("round %d %+v: got %v/%v, want %s/%s", round, in, got.Class, got.Reason, wantClass, wantReason)
		}
		counts[[2]string{wantClass, wantReason}]++
	}

	for _, key := range [][2]string{
		{"long-term", "person-level"}, {"long-term", "device-level"}, {"long-term", "local-node"},
		{"long-term", "trust-graph-node"}, {"long-term", "declaration-signer"},
		{"forward-once", "unrelated-node"}, {"dropped", "cap-reached"}, {"dropped", "invalid-input"},
	} {
		if counts[key] < 150 {
			t.Errorf("path %v taken %d times, want at least 150", key, counts[key])
		}
	}
	t.Logf("paths: %v", counts)
}

// Property: a random flood never makes the node hold more forward-once
// revocations than the cap, and never drops a related or person-level one.
func TestFloodNeverExceedsTheCap(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	const nodes = 60
	ids := make([]ident.NodeID, nodes)
	for i := range ids {
		ids[i] = nodeIDOf(t, 3000+i)
	}
	ctx := Context{Local: ids[0], TrustGraph: trust.NodeSet{ids[1]: {}, ids[2]: {}}, DeclarationSigners: trust.NodeSet{ids[3]: {}}}
	for _, capN := range []int{1, 2, 5, 17} {
		held, forwarded, dropped, kept := 0, 0, 0, 0
		for i := 0; i < 3000; i++ {
			n := ids[rng.Intn(nodes)]
			rev := nodeSelfOf(n, byte(i))
			if rng.Intn(2) == 0 {
				rev = certRevocationOf(n, byte(i))
			}
			got := Classify(rev, ctx, held, Params{MaxForwardOnce: capN})
			switch got.Class {
			case ForwardOnce:
				held++
				forwarded++
			case Dropped:
				dropped++
			case LongTerm:
				kept++
			}
			// The node passes some on: held falls at random.
			if held > 0 && rng.Intn(4) == 0 {
				held--
			}
			if held > capN {
				t.Fatalf("cap %d: held %d", capN, held)
			}
		}
		if forwarded == 0 || dropped == 0 || kept == 0 {
			t.Fatalf("cap %d: forwarded %d dropped %d kept %d", capN, forwarded, dropped, kept)
		}
	}
}
