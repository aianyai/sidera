package authz

import (
	"encoding/binary"
	"fmt"
	"slices"
	"time"

	"github.com/aianyai/sidera/internal/ident"
)

// Change says how a policy change moves what the node permits.
type Change uint8

// The classes of change.
const (
	// Unchanged: every request gets the same answer under both policies.
	Unchanged Change = iota + 1
	// Tighten: nothing is permitted that was not, and something is no longer.
	Tighten
	// Loosen: some request is permitted, or permitted under a rate cap that is
	// not within the old one, that was not before; or some verified reply is
	// admitted that was not. A change that tightens one thing and loosens another
	// counts as Loosen.
	Loosen
)

// String returns a readable name for the change.
func (c Change) String() string {
	switch c {
	case Unchanged:
		return "unchanged"
	case Tighten:
		return "tighten"
	case Loosen:
		return "loosen"
	}
	return "invalid"
}

// Classify says whether replacing old by new tightens, loosens or leaves
// unchanged what the node permits. Only the owner or a person with full control
// may apply a Loosen; tightening needs less.
//
// The answer is exact for the closed rule syntax, not a sample. A request is
// described by who asks, what it targets, the action, the time, the body size
// and the preset. Rules only ever test those against fixed values (node and
// endpoint identifiers, window bounds, size caps, preset names), so two
// requests that agree on every value that appears in either policy are treated
// the same by both. Classify therefore builds one representative request for
// every combination of those equivalence classes and compares the two policies
// on each with Evaluate's own rule logic. The work grows with the product of
// the numbers of distinct senders, targets and condition values in the two
// rule lists.
//
// Each probe is compared twice for message.send: as an ordinary request, and as
// a verified reply, which a deny stops and nothing else does. Rate caps are
// compared by containment, see rateWithin: a new cap that is not within the old
// one, including one that is merely incomparable with it, is a loosening,
// because the gate decides who may widen the node's exposure and must never
// guess in favour of the change.
func Classify(old, new Policy) Change {
	rules := slices.Concat(old.rules, new.rules)
	u := newUniverse(rules)

	narrower := false
	for _, s := range u.senders() {
		for _, t := range u.targets() {
			for _, a := range Actions {
				if !a.appliesTo(t.Kind) {
					continue
				}
				for _, q := range u.probes(rules, s, t, a) {
					o, n := old.decide(q), new.decide(q)
					switch {
					case !n.permits() && !o.permits():
					case n.permits() && !o.permits():
						return Loosen
					case !n.permits() && o.permits():
						narrower = true
					default:
						if !rateWithin(n.rate, o.rate) {
							return Loosen
						}
						if n.rate != o.rate {
							narrower = true
						}
					}
					if a == ActionMessageSend {
						switch {
						case n.admitsReply() && !o.admitsReply():
							return Loosen
						case !n.admitsReply() && o.admitsReply():
							narrower = true
						}
					}
				}
			}
		}
	}
	if narrower {
		return Tighten
	}
	return Unchanged
}

// universe holds the distinct values that the rules mention, from which the
// representative requests are built.
type universe struct {
	nodes     map[ident.NodeID]bool
	endpoints map[ident.NodeID]map[ident.EndpointID]bool // sender endpoints per node
	targetIDs map[ident.EndpointID]bool                  // target identifiers named by any object
}

func newUniverse(rules []Rule) universe {
	u := universe{
		nodes:     map[ident.NodeID]bool{},
		endpoints: map[ident.NodeID]map[ident.EndpointID]bool{},
		targetIDs: map[ident.EndpointID]bool{},
	}
	for _, r := range rules {
		switch r.Subject.Kind {
		case SubjectNode:
			u.nodes[r.Subject.Node] = true
		case SubjectEndpoint:
			u.nodes[r.Subject.Node] = true
			if u.endpoints[r.Subject.Node] == nil {
				u.endpoints[r.Subject.Node] = map[ident.EndpointID]bool{}
			}
			u.endpoints[r.Subject.Node][r.Subject.Endpoint] = true
		}
		for _, n := range r.Nodes {
			u.nodes[n] = true
		}
		switch r.Object.Kind {
		case ObjectEndpoint, ObjectRuntime:
			u.targetIDs[r.Object.Endpoint] = true
		}
	}
	return u
}

type senderProbe struct {
	sender Sender
	direct bool
}

// senders returns one sender per class: each mentioned node (in a subject or in
// a restriction's set) plus one node no rule mentions, each with every endpoint a rule mentions for it plus one
// endpoint no rule mentions (which also stands for "no endpoint"), each both
// directly trusted and inherited.
func (u universe) senders() []senderProbe {
	nodes := make([]ident.NodeID, 0, len(u.nodes)+1)
	for n := range u.nodes {
		nodes = append(nodes, n)
	}
	nodes = append(nodes, freshNode(u.nodes))

	var out []senderProbe
	for _, n := range nodes {
		eps := []ident.EndpointID{freshEndpoint(u.endpoints[n])}
		for e := range u.endpoints[n] {
			eps = append(eps, e)
		}
		for _, e := range eps {
			for _, direct := range []bool{false, true} {
				out = append(out, senderProbe{Sender{Node: n, Endpoint: e}, direct})
			}
		}
	}
	return out
}

// targets returns one target per class. Objects match endpoints by identifier
// whatever the kind of the target, so each mentioned identifier is probed both as
// an endpoint and as a runtime, as is one identifier no rule mentions; the node
// itself is the last target.
func (u universe) targets() []Target {
	out := []Target{{Kind: TargetNode}}
	ids := []ident.EndpointID{freshEndpoint(u.targetIDs)}
	for e := range u.targetIDs {
		ids = append(ids, e)
	}
	for _, e := range ids {
		out = append(out, Target{Kind: TargetEndpoint, Endpoint: e}, Target{Kind: TargetRuntime, Endpoint: e})
	}
	return out
}

// probes returns the representative queries for one sender, target and action.
// Only rules that cover the sender, target and action can make a difference, so
// the time, size and preset classes are cut from their conditions alone.
func (u universe) probes(rules []Rule, s senderProbe, t Target, a Action) []query {
	base := query{direct: s.direct, sender: s.sender, action: a, target: t}
	var covering []Rule
	for _, r := range rules {
		if r.matches(base) {
			covering = append(covering, r)
		}
	}
	if len(covering) == 0 {
		return nil
	}

	// A window is half open, so the answer is constant from one bound to the
	// next: probe each bound itself, and the instant before the earliest one.
	var times []time.Time
	for _, r := range covering {
		for _, b := range []time.Time{r.Cond.NotBefore, r.Cond.NotAfter} {
			if !b.IsZero() {
				times = append(times, b)
			}
		}
	}
	if len(times) == 0 {
		times = []time.Time{time.Unix(0, 0)}
	} else {
		times = append(times, slices.MinFunc(times, time.Time.Compare).Add(-time.Nanosecond))
	}

	// A body size cap splits sizes at the cap: probe the cap and one past it.
	sizes := []int64{0}
	if a == ActionMessageSend {
		for _, r := range covering {
			if c := r.Cond.MaxBodyBytes; c > 0 {
				sizes = append(sizes, c, c+1)
			}
		}
	}

	// A preset set splits presets into listed names and everything else.
	presets := []string{""}
	if a == ActionSessionCreate {
		mentioned := map[string]bool{}
		for _, r := range covering {
			for _, p := range r.Cond.Presets {
				mentioned[p] = true
			}
		}
		presets = presets[:0]
		for p := range mentioned {
			presets = append(presets, p)
		}
		presets = append(presets, freshName(mentioned))
	}

	var out []query
	for _, now := range times {
		for _, size := range sizes {
			for _, preset := range presets {
				q := base
				q.now, q.size, q.preset = now, size, preset
				out = append(out, q)
			}
		}
	}
	return out
}

// The fresh helpers return a value that is not in the used set. They are
// deterministic: the probe set depends only on the rules.

func freshNode(used map[ident.NodeID]bool) ident.NodeID {
	for i := uint64(1); ; i++ {
		var b [ident.NodeIDLen]byte
		binary.BigEndian.PutUint64(b[:8], i)
		id, err := ident.NodeIDFromBytes(b[:])
		if err != nil {
			panic(err) // unreachable: i is never zero
		}
		if !used[id] {
			return id
		}
	}
}

func freshEndpoint(used map[ident.EndpointID]bool) ident.EndpointID {
	for i := uint64(1); ; i++ {
		var b [ident.EndpointIDLen]byte
		binary.BigEndian.PutUint64(b[:8], i)
		id, err := ident.EndpointIDFromBytes(b[:])
		if err != nil {
			panic(err) // unreachable: i is never zero
		}
		if !used[id] {
			return id
		}
	}
}

func freshName(used map[string]bool) string {
	for i := 0; ; i++ {
		if name := fmt.Sprintf("preset-%d", i); !used[name] {
			return name
		}
	}
}
