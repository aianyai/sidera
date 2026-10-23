// Package authz decides whether a node accepts a request from another node:
// a node-local access policy made of a hand-written rule list with a closed
// condition syntax. See README.md for the invariants.
package authz

import (
	"cmp"
	"math/bits"
	"slices"
	"time"

	"github.com/aianyai/sidera/internal/ident"
)

// Action is something a requester asks the node to do.
type Action uint8

// The closed set of policy actions.
const (
	ActionSee Action = iota + 1
	ActionMessageSend
	ActionSessionCreate
	ActionSessionClose
	ActionSessionInterrupt
	ActionEndpointStatus
	ActionRelayReserve
	ActionRelayConnect
)

// Actions lists every action in a stable order.
var Actions = []Action{
	ActionSee, ActionMessageSend, ActionSessionCreate, ActionSessionClose,
	ActionSessionInterrupt, ActionEndpointStatus, ActionRelayReserve, ActionRelayConnect,
}

var actionNames = [...]string{
	ActionSee:              "see",
	ActionMessageSend:      "message.send",
	ActionSessionCreate:    "session.create",
	ActionSessionClose:     "session.close",
	ActionSessionInterrupt: "session.interrupt",
	ActionEndpointStatus:   "endpoint.status",
	ActionRelayReserve:     "relay.reserve",
	ActionRelayConnect:     "relay.connect",
}

func (a Action) valid() bool { return a >= ActionSee && a <= ActionRelayConnect }

// String returns the canonical action name, for example "message.send".
func (a Action) String() string {
	if !a.valid() {
		return "invalid"
	}
	return actionNames[a]
}

// TargetKind says what a request is aimed at.
type TargetKind uint8

// The kinds of thing on this node that a request can target.
const (
	// TargetEndpoint is an ordinary endpoint, such as a session.
	TargetEndpoint TargetKind = iota + 1
	// TargetRuntime is a runtime endpoint, which can create sessions.
	TargetRuntime
	// TargetNode is the node itself.
	TargetNode
)

func (k TargetKind) valid() bool { return k >= TargetEndpoint && k <= TargetNode }

// String returns a readable name for the target kind.
func (k TargetKind) String() string {
	switch k {
	case TargetEndpoint:
		return "endpoint"
	case TargetRuntime:
		return "runtime"
	case TargetNode:
		return "node"
	}
	return "invalid"
}

// appliesTo reports whether the action can be aimed at the target kind. A
// request or rule that pairs an action with a target kind it cannot apply to is
// malformed, because it could never mean anything.
func (a Action) appliesTo(k TargetKind) bool {
	switch a {
	case ActionSee:
		return k.valid()
	case ActionMessageSend, ActionSessionClose, ActionSessionInterrupt:
		return k == TargetEndpoint
	case ActionSessionCreate:
		return k == TargetRuntime
	case ActionEndpointStatus:
		return k == TargetEndpoint || k == TargetRuntime
	case ActionRelayReserve, ActionRelayConnect:
		return k == TargetNode
	}
	return false
}

// SubjectKind selects whom a rule is about.
type SubjectKind uint8

// The closed set of rule subjects.
const (
	// SubjectMember is every cluster member, direct or inherited.
	SubjectMember SubjectKind = iota + 1
	// SubjectDirect is every directly trusted node.
	SubjectDirect
	// SubjectNode is any endpoint of one node (Subject.Node).
	SubjectNode
	// SubjectEndpoint is one endpoint of one node (Subject.Node/Endpoint).
	SubjectEndpoint
)

// Subject says whom a rule is about. Node is set for SubjectNode and
// SubjectEndpoint, Endpoint only for SubjectEndpoint.
type Subject struct {
	Kind     SubjectKind
	Node     ident.NodeID
	Endpoint ident.EndpointID
}

// Members is the subject "every cluster member, including inherited ones".
func Members() Subject { return Subject{Kind: SubjectMember} }

// DirectTrusted is the subject "every directly trusted node".
func DirectTrusted() Subject { return Subject{Kind: SubjectDirect} }

// AnyEndpointOf is the subject "any endpoint of node n".
func AnyEndpointOf(n ident.NodeID) Subject { return Subject{Kind: SubjectNode, Node: n} }

// EndpointOf is the subject "the endpoint at address a".
func EndpointOf(a ident.Address) Subject {
	return Subject{Kind: SubjectEndpoint, Node: a.Node, Endpoint: a.Endpoint}
}

// ObjectKind selects what a rule is about.
type ObjectKind uint8

// The closed set of rule objects.
const (
	// ObjectEndpoint is the local endpoint with identifier Object.Endpoint. It
	// matches by identifier whatever the target kind, so it also covers a runtime
	// endpoint with that identifier.
	ObjectEndpoint ObjectKind = iota + 1
	// ObjectAllEndpoints is every local endpoint, runtime endpoints included.
	ObjectAllEndpoints
	// ObjectRuntime is the local runtime endpoint with identifier Object.Endpoint.
	// It matches by identifier like ObjectEndpoint; it differs in the actions a
	// rule may pair it with, which are the ones a runtime endpoint accepts.
	ObjectRuntime
	// ObjectNode is the node itself.
	ObjectNode
)

// Object says what a rule is about. Endpoint is set for ObjectEndpoint and
// ObjectRuntime only.
type Object struct {
	Kind     ObjectKind
	Endpoint ident.EndpointID
}

// LocalEndpoint is the object "the local endpoint e".
func LocalEndpoint(e ident.EndpointID) Object { return Object{Kind: ObjectEndpoint, Endpoint: e} }

// AllLocalEndpoints is the object "every local endpoint".
func AllLocalEndpoints() Object { return Object{Kind: ObjectAllEndpoints} }

// RuntimeEndpoint is the object "the local runtime endpoint e".
func RuntimeEndpoint(e ident.EndpointID) Object { return Object{Kind: ObjectRuntime, Endpoint: e} }

// ThisNode is the object "the node itself".
func ThisNode() Object { return Object{Kind: ObjectNode} }

// Effect is what a matching rule does.
type Effect uint8

// The three effects. Deny wins over allow. A restriction narrows the senders an
// allow admits but, unlike a deny, lets a verified reply through.
const (
	EffectAllow Effect = iota + 1
	EffectDeny
	// EffectRestrict makes an object and action accept requests only from the
	// nodes in Rule.Nodes. See OnlyFrom.
	EffectRestrict
)

// RateCap limits how often something may happen: at most Count times in any
// span of Window. The zero value means no cap.
type RateCap struct {
	Count  uint32
	Window time.Duration
}

func (c RateCap) unlimited() bool { return c == RateCap{} }

// rateCmp is the deterministic order used to choose among several allow rules
// that grant the same request: it is negative when a ranks below b, zero when
// they are the same cap, and positive when a ranks above. A higher sustained
// rate (Count/Window) ranks above a lower one; at equal sustained rate the larger
// Count (burst) ranks above; no cap ranks above every cap. It is a selection
// order only. It does not define what is wider or narrower: that is rateWithin,
// because a low sustained rate over a huge window can still allow an unbounded
// burst.
func rateCmp(a, b RateCap) int {
	switch {
	case a.unlimited() && b.unlimited():
		return 0
	case a.unlimited():
		return 1
	case b.unlimited():
		return -1
	}
	ah, al := bits.Mul64(uint64(a.Count), uint64(b.Window))
	bh, bl := bits.Mul64(uint64(b.Count), uint64(a.Window))
	if c := cmp.Compare(ah, bh); c != 0 {
		return c
	}
	if c := cmp.Compare(al, bl); c != 0 {
		return c
	}
	return cmp.Compare(a.Count, b.Count)
}

// rateWithin reports whether every sequence of requests that cap a admits is
// also admitted by cap b, that is, whether a is no wider than b. This is the
// definition Classify uses; two caps that neither contains the other are
// incomparable and the caller must treat that as a loosening.
//
// Proof. Let k = ceil(b.Window / a.Window). Any span of length b.Window is
// covered by k spans of length a.Window, and a admits at most a.Count requests in
// each, so a admits at most a.Count*k in any span of b.Window; when that is at
// most b.Count, b admits everything a does. Conversely, a admits a burst of
// a.Count at each of the instants 0, a.Window, ..., (k-1)*a.Window, and these
// all lie within one span of b.Window because (k-1)*a.Window < b.Window, so a
// can reach exactly a.Count*k there; when that exceeds b.Count, b refuses a
// sequence that a admits. No cap is the widest.
func rateWithin(a, b RateCap) bool {
	switch {
	case b.unlimited():
		return true
	case a.unlimited():
		return false
	}
	aw, bw := uint64(a.Window), uint64(b.Window)
	k := bw / aw
	if bw%aw != 0 {
		k++
	}
	hi, lo := bits.Mul64(uint64(a.Count), k)
	return hi == 0 && lo <= uint64(b.Count)
}

// Conditions narrow when a rule applies. The zero value adds no condition.
//
// Allow rules accept every condition. Deny rules accept only a validity window
// and a preset set, because a cap on a refusal has no meaning.
type Conditions struct {
	// NotBefore is the first instant the rule is in force (inclusive). The zero
	// time means no lower bound.
	NotBefore time.Time
	// NotAfter is the first instant the rule is no longer in force (exclusive).
	// The zero time means no upper bound.
	NotAfter time.Time
	// Rate caps how often one sender may use the grant against one target. It
	// is not counted here; see RateObligation. The zero value means no cap.
	Rate RateCap
	// MaxBodyBytes caps the body size of a message.send. Zero means no cap.
	MaxBodyBytes int64
	// Presets restricts session.create to these runtime presets. For an allow
	// rule they are the presets it grants; for a deny rule they are the presets
	// it refuses. Empty means every registered preset.
	Presets []string
}

// activeAt reports whether the validity window contains now.
func (c Conditions) activeAt(now time.Time) bool {
	if !c.NotBefore.IsZero() && now.Before(c.NotBefore) {
		return false
	}
	if !c.NotAfter.IsZero() && !now.Before(c.NotAfter) {
		return false
	}
	return true
}

// allowsPreset reports whether the preset condition admits the preset.
func (c Conditions) allowsPreset(preset string) bool {
	if len(c.Presets) == 0 {
		return true
	}
	for _, p := range c.Presets {
		if p == preset {
			return true
		}
	}
	return false
}

// isZero reports whether no condition is set.
func (c Conditions) isZero() bool {
	return c.NotBefore.IsZero() && c.NotAfter.IsZero() && c.Rate.unlimited() &&
		c.MaxBodyBytes == 0 && len(c.Presets) == 0
}

// Rule is one policy statement. An allow or deny rule is subject x object x
// action -> allow or deny, under conditions. A restriction has no subject and no
// conditions: it names an object, an action and the set of nodes allowed to use
// it.
type Rule struct {
	Effect  Effect
	Subject Subject
	Object  Object
	Action  Action
	Cond    Conditions
	// Nodes is the set of nodes a restriction admits. It is set for
	// EffectRestrict and empty otherwise.
	Nodes []ident.NodeID
}

// Allow builds an allow rule.
func Allow(s Subject, o Object, a Action, c Conditions) Rule {
	return Rule{Effect: EffectAllow, Subject: s, Object: o, Action: a, Cond: c}
}

// Deny builds a deny rule.
func Deny(s Subject, o Object, a Action, c Conditions) Rule {
	return Rule{Effect: EffectDeny, Subject: s, Object: o, Action: a, Cond: c}
}

// OnlyFrom builds a restriction: the object accepts the action only from the
// listed nodes. A request from any other node is not authorized, whatever allow
// rules say. Unlike a deny, a restriction does not stop a verified reply, so an
// endpoint can be narrowed to a set of nodes and still receive the answers to
// its own messages.
func OnlyFrom(o Object, a Action, nodes ...ident.NodeID) Rule {
	return Rule{Effect: EffectRestrict, Object: o, Action: a, Nodes: slices.Clone(nodes)}
}

// matches reports whether the rule's object and action cover the query and, for
// an allow or deny rule, its subject too. Conditions are not looked at. A
// restriction has no subject: whether the sender is in its node set is a
// separate test, see restricts.
func (r Rule) matches(q query) bool {
	if r.Action != q.action || !r.Object.matches(q.target) {
		return false
	}
	return r.Effect == EffectRestrict || r.Subject.matches(q)
}

// restricts reports whether a restriction that matches the query refuses its
// sender, because the sender's node is not in the set.
func (r Rule) restricts(q query) bool {
	return r.Effect == EffectRestrict && !slices.Contains(r.Nodes, q.sender.Node)
}

func (s Subject) matches(q query) bool {
	switch s.Kind {
	case SubjectMember:
		return true
	case SubjectDirect:
		return q.direct
	case SubjectNode:
		return s.Node == q.sender.Node
	case SubjectEndpoint:
		return s.Node == q.sender.Node && s.Endpoint == q.sender.Endpoint
	}
	return false
}

func (o Object) matches(t Target) bool {
	switch o.Kind {
	case ObjectEndpoint, ObjectRuntime:
		// Endpoint identifiers are unique on a node, so the identifier decides.
		// Checking the target kind as well would let a deny that names the wrong
		// kind silently match nothing.
		return t.Endpoint == o.Endpoint
	case ObjectAllEndpoints:
		return t.Kind == TargetEndpoint || t.Kind == TargetRuntime
	case ObjectNode:
		return t.Kind == TargetNode
	}
	return false
}
