package authz

import (
	"errors"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/ident"
)

func TestRuleProblems(t *testing.T) {
	ok := Allow(Members(), AllLocalEndpoints(), ActionMessageSend, Conditions{})
	mod := func(f func(*Rule)) Rule { r := ok; f(&r); return r }

	tests := []struct {
		name string
		rule Rule
		want []Problem
	}{
		{"well-formed allow", ok, nil},
		{"well-formed deny with window and presets",
			Deny(Members(), RuntimeEndpoint(ep(1)), ActionSessionCreate, Conditions{NotBefore: t0, NotAfter: t0.Add(time.Hour), Presets: []string{"x"}}), nil},
		{"well-formed allow with every condition",
			Allow(EndpointOf(addr(1, 1)), LocalEndpoint(ep(2)), ActionMessageSend,
				Conditions{NotAfter: t0, Rate: RateCap{Count: 5, Window: time.Second}, MaxBodyBytes: 10}), nil},
		{"effect unset", mod(func(r *Rule) { r.Effect = 0 }), []Problem{ProblemEffect}},
		{"subject kind unset", mod(func(r *Rule) { r.Subject = Subject{} }), []Problem{ProblemSubject}},
		{"member subject with a node", mod(func(r *Rule) { r.Subject.Node = node(1) }), []Problem{ProblemSubjectIDs}},
		{"direct subject with an endpoint", mod(func(r *Rule) { r.Subject = Subject{Kind: SubjectDirect, Endpoint: ep(1)} }), []Problem{ProblemSubjectIDs}},
		{"node subject without a node", mod(func(r *Rule) { r.Subject = Subject{Kind: SubjectNode} }), []Problem{ProblemSubjectIDs}},
		{"node subject with an endpoint", mod(func(r *Rule) { r.Subject = Subject{Kind: SubjectNode, Node: node(1), Endpoint: ep(1)} }), []Problem{ProblemSubjectIDs}},
		{"endpoint subject without an endpoint", mod(func(r *Rule) { r.Subject = Subject{Kind: SubjectEndpoint, Node: node(1)} }), []Problem{ProblemSubjectIDs}},
		{"object kind unset", mod(func(r *Rule) { r.Object = Object{} }), []Problem{ProblemObject}},
		{"endpoint object without an endpoint", mod(func(r *Rule) { r.Object = Object{Kind: ObjectEndpoint} }), []Problem{ProblemObjectIDs}},
		{"all-endpoints object with an endpoint", mod(func(r *Rule) { r.Object = Object{Kind: ObjectAllEndpoints, Endpoint: ep(1)} }), []Problem{ProblemObjectIDs}},
		{"action unset", mod(func(r *Rule) { r.Action = 0 }), []Problem{ProblemAction}},
		{"relay action on endpoints", mod(func(r *Rule) { r.Action = ActionRelayReserve }), []Problem{ProblemActionObject}},
		{"message.send on the node", mod(func(r *Rule) { r.Object = ThisNode() }), []Problem{ProblemActionObject}},
		{"session.create on an endpoint object names a runtime by its identifier", mod(func(r *Rule) { r.Action = ActionSessionCreate; r.Object = LocalEndpoint(ep(1)) }), nil},
		{"message.send on a runtime object", mod(func(r *Rule) { r.Object = RuntimeEndpoint(ep(1)) }), []Problem{ProblemActionObject}},
		{"window ends before it starts", mod(func(r *Rule) { r.Cond = Conditions{NotBefore: t0, NotAfter: t0.Add(-time.Second)} }), []Problem{ProblemWindowEmpty}},
		{"window of zero length", mod(func(r *Rule) { r.Cond = Conditions{NotBefore: t0, NotAfter: t0} }), []Problem{ProblemWindowEmpty}},
		{"rate count without a window", mod(func(r *Rule) { r.Cond.Rate = RateCap{Count: 1} }), []Problem{ProblemRateShape}},
		{"rate window without a count", mod(func(r *Rule) { r.Cond.Rate = RateCap{Window: time.Second} }), []Problem{ProblemRateShape}},
		{"rate with a negative window", mod(func(r *Rule) { r.Cond.Rate = RateCap{Count: 1, Window: -time.Second} }), []Problem{ProblemRateShape}},
		{"rate on a deny", mod(func(r *Rule) { r.Effect = EffectDeny; r.Cond.Rate = RateCap{Count: 1, Window: time.Second} }), []Problem{ProblemRateOnDeny}},
		{"negative body cap", mod(func(r *Rule) { r.Cond.MaxBodyBytes = -1 }), []Problem{ProblemBodyCapRange}},
		{"body cap at the maximum value", mod(func(r *Rule) { r.Cond.MaxBodyBytes = math.MaxInt64 }), []Problem{ProblemBodyCapRange}},
		{"body cap on a deny", mod(func(r *Rule) { r.Effect = EffectDeny; r.Cond.MaxBodyBytes = 5 }), []Problem{ProblemBodyCapOnDeny}},
		{"body cap on an action without a body", mod(func(r *Rule) { r.Action = ActionSee; r.Cond.MaxBodyBytes = 5 }), []Problem{ProblemBodyCapAction}},
		{"presets on message.send", mod(func(r *Rule) { r.Cond.Presets = []string{"x"} }), []Problem{ProblemPresetsAction}},
		{"empty preset name", mod(func(r *Rule) {
			r.Action = ActionSessionCreate
			r.Cond.Presets = []string{""}
		}), []Problem{ProblemPresetName}},
		{"duplicate preset name", mod(func(r *Rule) {
			r.Action = ActionSessionCreate
			r.Cond.Presets = []string{"a", "a"}
		}), []Problem{ProblemPresetDuplicate}},
		{"well-formed restriction", OnlyFrom(LocalEndpoint(ep(1)), ActionMessageSend, node(1), node(2)), nil},
		{"restriction on all endpoints", OnlyFrom(AllLocalEndpoints(), ActionSee, node(1)), nil},
		{"restriction with an empty set", OnlyFrom(LocalEndpoint(ep(1)), ActionMessageSend), []Problem{ProblemNodeSetEmpty}},
		{"restriction with the zero node", OnlyFrom(LocalEndpoint(ep(1)), ActionMessageSend, ident.NodeID{}), []Problem{ProblemNodeSetZero}},
		{"restriction with a duplicate node", OnlyFrom(LocalEndpoint(ep(1)), ActionMessageSend, node(1), node(1)), []Problem{ProblemNodeSetDuplicate}},
		{"restriction with an action the object cannot take", OnlyFrom(ThisNode(), ActionMessageSend, node(1)), []Problem{ProblemActionObject}},
		{"restriction with a subject", mod(func(r *Rule) { *r = OnlyFrom(LocalEndpoint(ep(1)), ActionMessageSend, node(1)); r.Subject = Members() }), []Problem{ProblemRestrictionSubject}},
		{"restriction with a window", mod(func(r *Rule) {
			*r = OnlyFrom(LocalEndpoint(ep(1)), ActionMessageSend, node(1))
			r.Cond.NotAfter = t0
		}), []Problem{ProblemRestrictionConditions}},
		{"restriction with a rate cap", mod(func(r *Rule) {
			*r = OnlyFrom(LocalEndpoint(ep(1)), ActionMessageSend, node(1))
			r.Cond.Rate = RateCap{Count: 1, Window: time.Second}
		}), []Problem{ProblemRestrictionConditions}},
		{"allow with a node set", mod(func(r *Rule) { r.Nodes = []ident.NodeID{node(1)} }), []Problem{ProblemNodeSetEffect}},
		{"several problems are all reported", mod(func(r *Rule) {
			r.Effect = 0
			r.Action = ActionRelayConnect
			r.Cond.MaxBodyBytes = -3
		}), []Problem{ProblemEffect, ProblemActionObject, ProblemBodyCapRange, ProblemBodyCapAction}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.rule.Problems(); !slices.Equal(got, tc.want) {
				t.Fatalf("problems = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNewPolicyRejectsMalformedRulesWithReasons(t *testing.T) {
	good := Allow(Members(), AllLocalEndpoints(), ActionMessageSend, Conditions{})
	badEffect := good
	badEffect.Effect = 0
	badBody := Allow(Members(), AllLocalEndpoints(), ActionSee, Conditions{MaxBodyBytes: 1})

	_, err := NewPolicy([]Rule{good, badEffect, good, badBody})
	if err == nil {
		t.Fatal("malformed rules were accepted")
	}
	var found []RuleError
	for _, e := range err.(interface{ Unwrap() []error }).Unwrap() {
		var re *RuleError
		if !errors.As(e, &re) {
			t.Fatalf("error %v is not a *RuleError", e)
		}
		found = append(found, *re)
	}
	want := []RuleError{{1, ProblemEffect}, {3, ProblemBodyCapAction}}
	if !slices.Equal(found, want) {
		t.Fatalf("errors = %+v, want %+v", found, want)
	}
}

func TestDefaultPolicyIsWellFormed(t *testing.T) {
	for i, r := range DefaultPolicy().Rules() {
		if p := r.Problems(); p != nil {
			t.Errorf("default rule %d: %v", i, p)
		}
	}
}

func TestPolicyDoesNotAliasNodeSet(t *testing.T) {
	nodes := []ident.NodeID{node(1)}
	p := mustPolicy(t, OnlyFrom(LocalEndpoint(ep(10)), ActionMessageSend, nodes...), Allow(Members(), AllLocalEndpoints(), ActionMessageSend, Conditions{}))
	nodes[0] = node(2)
	if got := Evaluate(p, send()); got.Reason != ReasonGranted {
		t.Fatalf("policy changed with its input: %v", got.Reason)
	}
	p.Rules()[0].Nodes[0] = node(2)
	if got := Evaluate(p, send()); got.Reason != ReasonGranted {
		t.Fatalf("policy changed through Rules(): %v", got.Reason)
	}
}

// rateCmp is only the order in which several granting rules are chosen between.
func TestRateCmp(t *testing.T) {
	min := func(n uint32) RateCap { return RateCap{Count: n, Window: time.Minute} }
	tests := []struct {
		name string
		a, b RateCap
		want int
	}{
		{"no cap equals no cap", RateCap{}, RateCap{}, 0},
		{"no cap permits more than any cap", RateCap{}, min(1000000), 1},
		{"any cap permits less than no cap", min(1), RateCap{}, -1},
		{"higher count in the same window permits more", min(10), min(5), 1},
		{"same cap is equal", min(5), min(5), 0},
		{"lower sustained rate over a longer window permits less", RateCap{Count: 100, Window: time.Hour}, min(10), -1},
		{"higher sustained rate over a shorter window permits more", RateCap{Count: 5, Window: time.Second}, RateCap{Count: 100, Window: time.Hour}, 1},
		{"equal sustained rate: the larger burst permits more", RateCap{Count: 60, Window: time.Hour}, min(1), 1},
		{"no overflow on huge values", RateCap{Count: math.MaxUint32, Window: math.MaxInt64}, RateCap{Count: math.MaxUint32 - 1, Window: math.MaxInt64}, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := rateCmp(tc.a, tc.b); got != tc.want {
				t.Fatalf("rateCmp = %d, want %d", got, tc.want)
			}
			if got := rateCmp(tc.b, tc.a); got != -tc.want {
				t.Fatalf("rateCmp reversed = %d, want %d", got, -tc.want)
			}
		})
	}
}

func TestRateWithin(t *testing.T) {
	per := func(n uint32, w time.Duration) RateCap { return RateCap{Count: n, Window: w} }
	tests := []struct {
		name string
		a, b RateCap
		want bool
	}{
		{"equal caps", per(5, time.Minute), per(5, time.Minute), true},
		{"a lower count in the same window is within", per(4, time.Minute), per(5, time.Minute), true},
		{"a higher count in the same window is not", per(6, time.Minute), per(5, time.Minute), false},
		{"10 per minute is within 600 per hour", per(10, time.Minute), per(600, time.Hour), true},
		{"600 per hour is not within 10 per minute: its burst is larger", per(600, time.Hour), per(10, time.Minute), false},
		{"100 per hour is not within 2 per minute: its burst is larger", per(100, time.Hour), per(2, time.Minute), false},
		{"10 per minute is not within 10 per hour", per(10, time.Minute), per(10, time.Hour), false},
		{"10 per hour is within 10 per minute", per(10, time.Hour), per(10, time.Minute), true},
		{"2 per 2s and 3 per 3s are incomparable: first way", per(2, 2*time.Second), per(3, 3*time.Second), false},
		{"2 per 2s and 3 per 3s are incomparable: other way", per(3, 3*time.Second), per(2, 2*time.Second), false},
		{"anything is within no cap", per(1000, time.Nanosecond), RateCap{}, true},
		{"no cap is within no cap", RateCap{}, RateCap{}, true},
		{"no cap is not within a cap", RateCap{}, per(math.MaxUint32, time.Nanosecond), false},
		{"no overflow: the largest burst in the shortest window against a very long window",
			per(math.MaxUint32, time.Nanosecond), per(math.MaxUint32, math.MaxInt64), false},
		{"no overflow: a long window within a short one only by count",
			per(1, math.MaxInt64), per(1, time.Nanosecond), true},
		{"no overflow: a short window against a long one needs a covering count",
			per(1, time.Nanosecond), per(math.MaxUint32, math.MaxInt64), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := rateWithin(tc.a, tc.b); got != tc.want {
				t.Fatalf("rateWithin = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPolicyExemptTraffic(t *testing.T) {
	tests := []struct {
		kind   TrafficKind
		exempt bool
	}{
		{TrafficMessage, false},
		{TrafficControlSignal, false},
		{TrafficStatusReport, true},
		{TrafficControlReply, true},
		{TrafficSystemFeedback, true},
		{TrafficTrustMaintenance, true},
		{0, false},
		{99, false},
	}
	for _, tc := range tests {
		if got := tc.kind.PolicyExempt(); got != tc.exempt {
			t.Errorf("kind %d: exempt = %v, want %v", tc.kind, got, tc.exempt)
		}
	}
}
