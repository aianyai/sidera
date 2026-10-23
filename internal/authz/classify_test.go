package authz

import (
	"fmt"
	"math/big"
	"math/rand"
	"slices"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/trust"
)

func TestClassify(t *testing.T) {
	member, direct := Members(), DirectTrusted()
	allEps := AllLocalEndpoints()
	send := ActionMessageSend
	none := Conditions{}
	perMin := func(n uint32) Conditions { return Conditions{Rate: RateCap{Count: n, Window: time.Minute}} }
	presets := func(p ...string) Conditions { return Conditions{Presets: p} }
	allow := func(s Subject, o Object, a Action, c Conditions) Rule { return Allow(s, o, a, c) }

	tests := []struct {
		name     string
		old, new []Rule
		want     Change
	}{
		// The examples that decide who may apply a change.
		{"endpoint from only specific nodes back to whole cluster is a loosen",
			[]Rule{allow(AnyEndpointOf(node(2)), LocalEndpoint(ep(1)), send, none)},
			[]Rule{allow(member, LocalEndpoint(ep(1)), send, none)}, Loosen},
		{"endpoint from whole cluster to only specific nodes is a tighten",
			[]Rule{allow(member, LocalEndpoint(ep(1)), send, none)},
			[]Rule{allow(AnyEndpointOf(node(2)), LocalEndpoint(ep(1)), send, none)}, Tighten},
		{"removing a deny rule is a loosen",
			[]Rule{allow(member, allEps, send, none), Deny(AnyEndpointOf(node(2)), allEps, send, none)},
			[]Rule{allow(member, allEps, send, none)}, Loosen},
		{"adding a deny rule is a tighten",
			[]Rule{allow(member, allEps, send, none)},
			[]Rule{allow(member, allEps, send, none), Deny(AnyEndpointOf(node(2)), allEps, send, none)}, Tighten},
		{"widening the preset set is a loosen",
			[]Rule{allow(member, allEps, ActionSessionCreate, presets("a"))},
			[]Rule{allow(member, allEps, ActionSessionCreate, presets("a", "b"))}, Loosen},
		{"narrowing the preset set is a tighten",
			[]Rule{allow(member, allEps, ActionSessionCreate, presets("a", "b"))},
			[]Rule{allow(member, allEps, ActionSessionCreate, presets("a"))}, Tighten},
		{"a preset set replaced by every preset is a loosen",
			[]Rule{allow(member, allEps, ActionSessionCreate, presets("a"))},
			[]Rule{allow(member, allEps, ActionSessionCreate, none)}, Loosen},
		{"every preset replaced by a preset set is a tighten",
			[]Rule{allow(member, allEps, ActionSessionCreate, none)},
			[]Rule{allow(member, allEps, ActionSessionCreate, presets("a"))}, Tighten},
		{"denying one preset on top of allow-all is a tighten",
			[]Rule{allow(member, allEps, ActionSessionCreate, none)},
			[]Rule{allow(member, allEps, ActionSessionCreate, none), Deny(member, allEps, ActionSessionCreate, presets("unsafe"))}, Tighten},
		{"a mixed change counts as a loosen",
			[]Rule{allow(AnyEndpointOf(node(1)), allEps, send, none)},
			[]Rule{allow(AnyEndpointOf(node(2)), allEps, send, none)}, Loosen},

		// Whole policies.
		{"identical policies are unchanged", DefaultPolicy().Rules(), DefaultPolicy().Rules(), Unchanged},
		{"empty to default is a loosen", nil, DefaultPolicy().Rules(), Loosen},
		{"default to empty is a tighten", DefaultPolicy().Rules(), nil, Tighten},
		{"empty to empty is unchanged", nil, nil, Unchanged},

		// Unchanged is about behaviour, not text.
		{"reordering rules is unchanged",
			[]Rule{allow(member, allEps, send, none), Deny(AnyEndpointOf(node(2)), allEps, send, none)},
			[]Rule{Deny(AnyEndpointOf(node(2)), allEps, send, none), allow(member, allEps, send, none)}, Unchanged},
		{"a duplicate rule is unchanged",
			[]Rule{allow(member, allEps, send, none)},
			[]Rule{allow(member, allEps, send, none), allow(member, allEps, send, none)}, Unchanged},
		{"an allow already covered by a broader allow is unchanged",
			[]Rule{allow(member, allEps, send, none)},
			[]Rule{allow(member, allEps, send, none), allow(AnyEndpointOf(node(1)), LocalEndpoint(ep(1)), send, none)}, Unchanged},
		{"an allow that a deny already overrides is unchanged",
			[]Rule{Deny(member, allEps, send, none)},
			[]Rule{Deny(member, allEps, send, none), allow(AnyEndpointOf(node(1)), allEps, send, none)}, Unchanged},
		{"a deny over senders with no allow still tightens: it stops their replies",
			[]Rule{allow(AnyEndpointOf(node(1)), allEps, send, none)},
			[]Rule{allow(AnyEndpointOf(node(1)), allEps, send, none), Deny(AnyEndpointOf(node(2)), allEps, send, none)}, Tighten},
		{"a deny over senders with no allow does not tighten another action: replies exist for message.send only",
			[]Rule{allow(AnyEndpointOf(node(1)), allEps, ActionSessionClose, none)},
			[]Rule{allow(AnyEndpointOf(node(1)), allEps, ActionSessionClose, none), Deny(AnyEndpointOf(node(2)), allEps, ActionSessionClose, none)}, Unchanged},
		{"removing a deny rule that only blocked replies is a loosen",
			[]Rule{Deny(AnyEndpointOf(node(2)), allEps, send, none)},
			nil, Loosen},
		{"adding a deny rule that only blocks replies is a tighten",
			nil,
			[]Rule{Deny(AnyEndpointOf(node(2)), allEps, send, none)}, Tighten},
		{"adding a grant for another action with a validity window is a loosen",
			[]Rule{allow(member, allEps, send, none)},
			[]Rule{allow(member, allEps, send, none), allow(member, allEps, ActionSessionClose, Conditions{NotBefore: t0, NotAfter: t0.Add(time.Hour)})}, Loosen},

		// Subjects and objects.
		{"member to direct only is a tighten",
			[]Rule{allow(member, allEps, send, none)}, []Rule{allow(direct, allEps, send, none)}, Tighten},
		{"direct to member is a loosen",
			[]Rule{allow(direct, allEps, send, none)}, []Rule{allow(member, allEps, send, none)}, Loosen},
		{"node to one endpoint of it is a tighten",
			[]Rule{allow(AnyEndpointOf(node(1)), allEps, send, none)}, []Rule{allow(EndpointOf(addr(1, 1)), allEps, send, none)}, Tighten},
		{"direct and node overlap: swapping one for the other is a loosen",
			[]Rule{allow(direct, allEps, send, none)}, []Rule{allow(AnyEndpointOf(node(1)), allEps, send, none)}, Loosen},
		{"all endpoints to one endpoint is a tighten",
			[]Rule{allow(member, allEps, send, none)}, []Rule{allow(member, LocalEndpoint(ep(1)), send, none)}, Tighten},
		{"a runtime endpoint rule is covered by all endpoints",
			[]Rule{allow(member, RuntimeEndpoint(ep(1)), ActionSessionCreate, none)}, []Rule{allow(member, allEps, ActionSessionCreate, none)}, Loosen},
		{"adding an action is a loosen",
			[]Rule{allow(member, allEps, send, none)}, []Rule{allow(member, allEps, send, none), allow(member, allEps, ActionSessionClose, none)}, Loosen},
		{"node-level relay access removed is a tighten",
			[]Rule{allow(member, ThisNode(), ActionRelayReserve, none)}, nil, Tighten},

		// Conditions.
		{"a longer validity window is a loosen",
			[]Rule{allow(member, allEps, send, Conditions{NotAfter: t0})}, []Rule{allow(member, allEps, send, Conditions{NotAfter: t0.Add(time.Hour)})}, Loosen},
		{"a shorter validity window is a tighten",
			[]Rule{allow(member, allEps, send, Conditions{NotAfter: t0.Add(time.Hour)})}, []Rule{allow(member, allEps, send, Conditions{NotAfter: t0})}, Tighten},
		{"dropping the end of the window is a loosen",
			[]Rule{allow(member, allEps, send, Conditions{NotAfter: t0})}, []Rule{allow(member, allEps, send, none)}, Loosen},
		{"a window that slides forward is a loosen (later times are newly allowed)",
			[]Rule{allow(member, allEps, send, Conditions{NotBefore: t0, NotAfter: t0.Add(time.Hour)})},
			[]Rule{allow(member, allEps, send, Conditions{NotBefore: t0.Add(time.Hour), NotAfter: t0.Add(2 * time.Hour)})}, Loosen},
		{"a deny with a window that ends is a loosen",
			[]Rule{allow(member, allEps, send, none), Deny(member, allEps, send, none)},
			[]Rule{allow(member, allEps, send, none), Deny(member, allEps, send, Conditions{NotAfter: t0})}, Loosen},
		{"a bigger body cap is a loosen",
			[]Rule{allow(member, allEps, send, Conditions{MaxBodyBytes: 100})}, []Rule{allow(member, allEps, send, Conditions{MaxBodyBytes: 101})}, Loosen},
		{"a smaller body cap is a tighten",
			[]Rule{allow(member, allEps, send, Conditions{MaxBodyBytes: 100})}, []Rule{allow(member, allEps, send, Conditions{MaxBodyBytes: 99})}, Tighten},
		{"removing the body cap is a loosen",
			[]Rule{allow(member, allEps, send, Conditions{MaxBodyBytes: 100})}, []Rule{allow(member, allEps, send, none)}, Loosen},
		{"adding a body cap is a tighten",
			[]Rule{allow(member, allEps, send, none)}, []Rule{allow(member, allEps, send, Conditions{MaxBodyBytes: 100})}, Tighten},
		{"a higher rate cap is a loosen",
			[]Rule{allow(member, allEps, send, perMin(10))}, []Rule{allow(member, allEps, send, perMin(11))}, Loosen},
		{"a lower rate cap is a tighten",
			[]Rule{allow(member, allEps, send, perMin(10))}, []Rule{allow(member, allEps, send, perMin(9))}, Tighten},
		{"removing the rate cap is a loosen",
			[]Rule{allow(member, allEps, send, perMin(10))}, []Rule{allow(member, allEps, send, none)}, Loosen},
		{"adding a rate cap is a tighten",
			[]Rule{allow(member, allEps, send, none)}, []Rule{allow(member, allEps, send, perMin(10))}, Tighten},
		{"the same sustained rate with a larger burst is a loosen",
			[]Rule{allow(member, allEps, send, perMin(10))},
			[]Rule{allow(member, allEps, send, Conditions{Rate: RateCap{Count: 600, Window: time.Hour}})}, Loosen},
		{"a lower sustained rate with a larger burst is a loosen",
			[]Rule{allow(member, allEps, send, perMin(10))},
			[]Rule{allow(member, allEps, send, Conditions{Rate: RateCap{Count: 100, Window: time.Hour}})}, Loosen},
		{"a cap that sustains less over a long window can still burst: 100 per hour against 2 per minute is a loosen",
			[]Rule{allow(member, allEps, send, perMin(2))},
			[]Rule{allow(member, allEps, send, Conditions{Rate: RateCap{Count: 100, Window: time.Hour}})}, Loosen},
		{"10 per minute replaced by 10 per hour is a tighten",
			[]Rule{allow(member, allEps, send, perMin(10))},
			[]Rule{allow(member, allEps, send, Conditions{Rate: RateCap{Count: 10, Window: time.Hour}})}, Tighten},
		{"incomparable caps are a loosen in both directions: 2 per 2s against 3 per 3s",
			[]Rule{allow(member, allEps, send, Conditions{Rate: RateCap{Count: 2, Window: 2 * time.Second}})},
			[]Rule{allow(member, allEps, send, Conditions{Rate: RateCap{Count: 3, Window: 3 * time.Second}})}, Loosen},
		{"incomparable caps are a loosen in both directions: 3 per 3s against 2 per 2s",
			[]Rule{allow(member, allEps, send, Conditions{Rate: RateCap{Count: 3, Window: 3 * time.Second}})},
			[]Rule{allow(member, allEps, send, Conditions{Rate: RateCap{Count: 2, Window: 2 * time.Second}})}, Loosen},
		// Objects match by identifier, so a probe target is both kinds.
		{"a deny on an endpoint object also tightens a runtime endpoint with that identifier",
			[]Rule{allow(member, allEps, ActionEndpointStatus, none)},
			[]Rule{allow(member, allEps, ActionEndpointStatus, none), Deny(member, LocalEndpoint(ep(1)), ActionEndpointStatus, none)}, Tighten},

		// Restrictions: only these nodes.
		{"adding a restriction is a tighten",
			[]Rule{allow(member, allEps, send, none)},
			[]Rule{allow(member, allEps, send, none), OnlyFrom(LocalEndpoint(ep(1)), send, node(1), node(2))}, Tighten},
		{"removing a restriction is a loosen",
			[]Rule{allow(member, allEps, send, none), OnlyFrom(LocalEndpoint(ep(1)), send, node(1), node(2))},
			[]Rule{allow(member, allEps, send, none)}, Loosen},
		{"growing a restriction's set is a loosen",
			[]Rule{allow(member, allEps, send, none), OnlyFrom(LocalEndpoint(ep(1)), send, node(1))},
			[]Rule{allow(member, allEps, send, none), OnlyFrom(LocalEndpoint(ep(1)), send, node(1), node(2))}, Loosen},
		{"shrinking a restriction's set is a tighten",
			[]Rule{allow(member, allEps, send, none), OnlyFrom(LocalEndpoint(ep(1)), send, node(1), node(2))},
			[]Rule{allow(member, allEps, send, none), OnlyFrom(LocalEndpoint(ep(1)), send, node(1))}, Tighten},
		{"replacing a restriction's set by another is a loosen",
			[]Rule{allow(member, allEps, send, none), OnlyFrom(LocalEndpoint(ep(1)), send, node(1))},
			[]Rule{allow(member, allEps, send, none), OnlyFrom(LocalEndpoint(ep(1)), send, node(2))}, Loosen},
		{"restricting an endpoint to X and Y, then returning it to the whole cluster, is a loosen",
			[]Rule{allow(member, LocalEndpoint(ep(1)), send, none), OnlyFrom(LocalEndpoint(ep(1)), send, node(1), node(2))},
			[]Rule{allow(member, allEps, send, none)}, Loosen},
		{"a restriction that excludes nobody the allow admits is unchanged",
			[]Rule{allow(AnyEndpointOf(node(1)), allEps, send, none)},
			[]Rule{allow(AnyEndpointOf(node(1)), allEps, send, none), OnlyFrom(allEps, send, node(1), node(2))}, Unchanged},
		{"a restriction on another action is unchanged",
			[]Rule{allow(member, allEps, send, none)},
			[]Rule{allow(member, allEps, send, none), OnlyFrom(allEps, ActionSessionClose, node(1))}, Unchanged},
		{"a restriction does not stop replies, so it does not tighten what no allow grants",
			nil,
			[]Rule{OnlyFrom(allEps, send, node(1))}, Unchanged},

		{"a second capped rule that is wider on a subset is a loosen",
			[]Rule{allow(member, allEps, send, perMin(10))},
			[]Rule{allow(member, allEps, send, perMin(10)), allow(AnyEndpointOf(node(1)), allEps, send, perMin(100))}, Loosen},
		{"a second capped rule that is narrower than the first is unchanged",
			[]Rule{allow(member, allEps, send, perMin(10))},
			[]Rule{allow(member, allEps, send, perMin(10)), allow(AnyEndpointOf(node(1)), allEps, send, perMin(5))}, Unchanged},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			old, new := mustPolicy(t, tc.old...), mustPolicy(t, tc.new...)
			if got := Classify(old, new); got != tc.want {
				t.Fatalf("Classify = %v, want %v", got, tc.want)
			}
		})
	}
}

// Classify of identical policies is Unchanged, and swapping the arguments
// swaps Tighten and Loosen except for mixed changes, which are Loosen both
// ways.
func TestClassifyArgumentSwap(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 200; i++ {
		a, b := randomPolicy(t, rng), randomPolicy(t, rng)
		ab, ba := Classify(a, b), Classify(b, a)
		if Classify(a, a) != Unchanged {
			t.Fatalf("policy not unchanged against itself: %+v", a.rules)
		}
		switch ab {
		case Unchanged:
			if ba != Unchanged {
				t.Fatalf("old->new unchanged but new->old %v", ba)
			}
		case Tighten:
			if ba != Loosen {
				t.Fatalf("old->new tighten but new->old %v", ba)
			}
		case Loosen:
			if ba == Unchanged {
				t.Fatalf("old->new loosen but new->old unchanged")
			}
		}
	}
}

// The value pools the property test draws from. Rules and requests share the
// pools, so a request can land in every equivalence class a rule makes.
var (
	poolNodes     = []ident.NodeID{node(1), node(2), node(3)}
	poolEndpoints = []ident.EndpointID{ep(1), ep(2)}
	poolTimes     = []time.Time{t0, t0.Add(time.Hour), t0.Add(2 * time.Hour), t0.Add(3 * time.Hour)}
	poolBodyCaps  = []int64{10, 100, 1000}
	poolPresets   = []string{"p1", "p2", "p3"}
	// The rates include pairs where neither contains the other (2 per minute and
	// 3 per 3 minutes) and pairs where a lower sustained rate has a larger burst
	// (60 per hour against 2 per minute).
	poolRates = []RateCap{
		{Count: 1, Window: time.Minute}, {Count: 2, Window: time.Minute},
		{Count: 60, Window: time.Hour}, {Count: 10, Window: time.Hour},
		{Count: 3, Window: 3 * time.Minute},
	}
)

func pick[T any](rng *rand.Rand, xs []T) T { return xs[rng.Intn(len(xs))] }

func randomRule(rng *rand.Rand) Rule {
	for {
		// Half of the rules use the two actions that carry extra conditions, so
		// that size caps and preset sets meet each other often.
		r := Rule{Effect: EffectAllow, Action: pick(rng, Actions)}
		if rng.Intn(2) == 0 {
			r.Action = pick(rng, []Action{ActionMessageSend, ActionSessionCreate})
		}
		if rng.Intn(3) == 0 {
			r.Effect = EffectDeny
		}
		switch rng.Intn(5) {
		case 0, 4:
			r.Subject = Members()
		case 1:
			r.Subject = DirectTrusted()
		case 2:
			r.Subject = AnyEndpointOf(pick(rng, poolNodes))
		default:
			r.Subject = EndpointOf(ident.Address{Node: pick(rng, poolNodes), Endpoint: pick(rng, poolEndpoints)})
		}
		switch rng.Intn(5) {
		case 0, 4:
			r.Object = AllLocalEndpoints()
		case 1:
			r.Object = LocalEndpoint(pick(rng, poolEndpoints))
		case 2:
			r.Object = RuntimeEndpoint(pick(rng, poolEndpoints))
		default:
			r.Object = ThisNode()
		}
		if rng.Intn(8) == 0 {
			// A restriction has no subject and no conditions, only a node set.
			r.Effect, r.Subject = EffectRestrict, Subject{}
			r.Nodes = randomNodes(rng)
			if len(r.Problems()) == 0 {
				return r
			}
			continue
		}
		if rng.Intn(2) == 0 {
			i, j := rng.Intn(len(poolTimes)), rng.Intn(len(poolTimes))
			switch rng.Intn(3) {
			case 0:
				r.Cond.NotBefore = poolTimes[i]
			case 1:
				r.Cond.NotAfter = poolTimes[j]
			default:
				r.Cond.NotBefore, r.Cond.NotAfter = poolTimes[i], poolTimes[j]
			}
		}
		if r.Effect == EffectAllow {
			if rng.Intn(3) == 0 {
				r.Cond.Rate = pick(rng, poolRates)
			}
			if r.Action == ActionMessageSend && rng.Intn(2) == 0 {
				r.Cond.MaxBodyBytes = pick(rng, poolBodyCaps)
			}
		}
		if r.Action == ActionSessionCreate && rng.Intn(2) == 0 {
			names := append([]string(nil), poolPresets...)
			rng.Shuffle(len(names), func(i, j int) { names[i], names[j] = names[j], names[i] })
			r.Cond.Presets = names[:1+rng.Intn(len(names))]
		}
		if len(r.Problems()) == 0 {
			return r
		}
	}
}

// randomNodes returns a non-empty random subset of the node pool.
func randomNodes(rng *rand.Rand) []ident.NodeID {
	nodes := slices.Clone(poolNodes)
	rng.Shuffle(len(nodes), func(i, j int) { nodes[i], nodes[j] = nodes[j], nodes[i] })
	return nodes[:1+rng.Intn(len(nodes))]
}

func randomPolicy(t testing.TB, rng *rand.Rand) Policy {
	n := rng.Intn(7)
	rules := make([]Rule, n)
	for i := range rules {
		rules[i] = randomRule(rng)
	}
	return mustPolicy(t, rules...)
}

// mutate edits a policy a little, so that old and new often differ in just one
// way and the interesting Tighten and Unchanged answers come up.
func mutate(t testing.TB, rng *rand.Rand, p Policy) Policy {
	rules := p.Rules()
	switch rng.Intn(5) {
	case 4:
		// Change the node set of a restriction: grow it, shrink it or replace it.
		for i := rng.Perm(len(rules)); len(i) > 0; i = i[1:] {
			if rules[i[0]].Effect == EffectRestrict {
				rules[i[0]].Nodes = randomNodes(rng)
				break
			}
		}
	case 0:
		rules = append(rules, randomRule(rng))
	case 1:
		if len(rules) > 0 {
			i := rng.Intn(len(rules))
			rules = append(rules[:i], rules[i+1:]...)
		}
	case 2:
		if len(rules) > 0 {
			i := rng.Intn(len(rules))
			r := randomRule(rng)
			r.Subject, r.Object, r.Action, r.Effect, r.Nodes = rules[i].Subject, rules[i].Object, rules[i].Action, rules[i].Effect, rules[i].Nodes
			if len(r.Problems()) == 0 {
				rules[i] = r
			}
		}
	default:
		rng.Shuffle(len(rules), func(i, j int) { rules[i], rules[j] = rules[j], rules[i] })
	}
	return mustPolicy(t, rules...)
}

// requestGrid lists every request the pools can make: each sender class, each
// target, each applicable action, the times around every window bound, and the
// sizes and presets around every cap and set. Every message.send appears both as
// an ordinary request and as a verified reply. All of them are intake checks
// with no endpoint declaration.
func requestGrid() []Request {
	nodes := append(append([]ident.NodeID(nil), poolNodes...), node(4)) // node 4 is in no rule
	endpoints := append(append([]ident.EndpointID(nil), poolEndpoints...), ep(3), ident.EndpointID{})
	times := []time.Time{t0.Add(-time.Hour)}
	for _, x := range poolTimes {
		times = append(times, x, x.Add(30*time.Minute))
	}
	sizes := []int64{0}
	for _, c := range poolBodyCaps {
		sizes = append(sizes, c, c+1)
	}
	presets := append(append([]string(nil), poolPresets...), "p4")
	targets := []Target{{Kind: TargetNode}}
	for _, e := range append(append([]ident.EndpointID(nil), poolEndpoints...), ep(3)) {
		targets = append(targets, Target{Kind: TargetEndpoint, Endpoint: e}, Target{Kind: TargetRuntime, Endpoint: e})
	}

	var out []Request
	for _, n := range nodes {
		for _, e := range endpoints {
			for _, tr := range []trust.Outcome{trust.AllowedInherited, trust.AllowedDirect} {
				for _, tg := range targets {
					for _, a := range Actions {
						if !a.appliesTo(tg.Kind) {
							continue
						}
						for _, now := range times {
							r := Request{Now: now, Trust: tr, Stage: StageIntake,
								Sender: Sender{Node: n, Endpoint: e}, Action: a, Target: tg, Preset: "p1"}
							switch a {
							case ActionMessageSend:
								for _, s := range sizes {
									r.BodySize = s
									for _, reply := range []bool{false, true} {
										r.Reply = reply
										out = append(out, r)
									}
								}
							case ActionSessionCreate:
								for _, p := range presets {
									r.Preset = p
									out = append(out, r)
								}
							default:
								out = append(out, r)
							}
						}
					}
				}
			}
		}
	}
	return out
}

// wider reports whether the verdict v permits strictly more than w. Two allows
// differ only by their rate obligation. A cap is wider than another when, in a
// span of some length L, it admits more requests; a cap admits at most
// Count*ceil(L/Window) in a span of L. The spans tried are every window length
// in the pool, which include each cap's own window, the span that decides
// containment.
func wider(v, w Verdict) bool {
	switch {
	case v.Outcome != OutcomeAllow:
		return false
	case w.Outcome != OutcomeAllow:
		return true
	}
	rv, rw := verdictRate(v), verdictRate(w)
	for _, l := range poolSpans() {
		if mostIn(rv, l).Cmp(mostIn(rw, l)) > 0 {
			return true
		}
	}
	return false
}

// poolSpans lists the window length of every cap in the pool.
func poolSpans() []time.Duration {
	var out []time.Duration
	for _, c := range poolRates {
		if !slices.Contains(out, c.Window) {
			out = append(out, c.Window)
		}
	}
	return out
}

// mostIn is the most requests a cap admits in a span of length l; no cap admits
// without bound.
func mostIn(c RateCap, l time.Duration) *big.Int {
	if c.unlimited() {
		return new(big.Int).Lsh(big.NewInt(1), 200)
	}
	k := (int64(l) + int64(c.Window) - 1) / int64(c.Window)
	return new(big.Int).Mul(big.NewInt(int64(c.Count)), big.NewInt(k))
}

func verdictRate(v Verdict) RateCap {
	if len(v.Obligations) == 0 {
		return RateCap{}
	}
	return RateCap{Count: v.Obligations[0].Limit, Window: v.Obligations[0].Window}
}

// TestClassifyAgainstExhaustiveOracle checks Classify against a brute-force
// comparison over every request the value pools can make, ordinary and verified
// reply alike. Soundness: when Classify says Unchanged or Tighten, no request is
// allowed (or allowed under a rate cap that admits more in some span) by the new
// policy and not by the old. Completeness: when it says Loosen there is such a
// request, and when it says Unchanged nothing at all differs. Because rules and
// requests draw from the same pools, the grid reaches every equivalence class,
// so the oracle is exact. It shares no code with Classify's rate comparison:
// wider counts what each cap admits in each span.
func TestClassifyAgainstExhaustiveOracle(t *testing.T) {
	grid := requestGrid()
	rng := rand.New(rand.NewSource(20261023))
	trials := 1000
	if testing.Short() {
		trials = 20
	}
	counts := map[Change]int{}
	for i := 0; i < trials; i++ {
		old := randomPolicy(t, rng)
		var new Policy
		if rng.Intn(4) == 0 {
			new = randomPolicy(t, rng)
		} else {
			new = mutate(t, rng, old)
		}

		var looser, tighter *Request
		for j := range grid {
			vo, vn := Evaluate(old, grid[j]), Evaluate(new, grid[j])
			if looser == nil && wider(vn, vo) {
				looser = &grid[j]
			}
			if tighter == nil && wider(vo, vn) {
				tighter = &grid[j]
			}
		}
		want := Unchanged
		switch {
		case looser != nil:
			want = Loosen
		case tighter != nil:
			want = Tighten
		}
		counts[want]++
		if got := Classify(old, new); got != want {
			t.Fatalf("trial %d: Classify = %v, oracle = %v\nold: %+v\nnew: %+v\nloosening request: %+v\ntightening request: %+v",
				i, got, want, old.rules, new.rules, looser, tighter)
		}
	}
	t.Log(fmt.Sprintf("oracle outcomes over %d trials: %v", trials, counts))
	for _, c := range []Change{Unchanged, Tighten, Loosen} {
		if counts[c] == 0 && !testing.Short() {
			t.Errorf("the generator never produced a %v pair; the property test is not exercising it", c)
		}
	}
}
