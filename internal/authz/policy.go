package authz

import (
	"errors"
	"fmt"
	"slices"

	"github.com/aianyai/sidera/internal/ident"
)

// Policy is a validated rule list. The only way to build one is NewPolicy or
// DefaultPolicy, so an evaluator never meets a malformed rule. The zero Policy
// has no rules and therefore allows nothing.
type Policy struct{ rules []Rule }

// DefaultPolicy is the policy of a new node: every cluster member may do every
// action on every local endpoint and on the node itself, and may use every
// registered preset.
func DefaultPolicy() Policy {
	var rules []Rule
	for _, a := range Actions {
		for _, o := range []Object{AllLocalEndpoints(), ThisNode()} {
			if appliesToObject(a, o) {
				rules = append(rules, Allow(Members(), o, a, Conditions{}))
			}
		}
	}
	p, err := NewPolicy(rules)
	if err != nil {
		panic("authz: default policy is malformed: " + err.Error())
	}
	return p
}

// NewPolicy validates the rules and returns the policy they make up. If any
// rule is malformed it returns an error joining one *RuleError per problem.
// The result does not alias the input.
func NewPolicy(rules []Rule) (Policy, error) {
	var errs []error
	for i, r := range rules {
		for _, prob := range r.Problems() {
			errs = append(errs, &RuleError{Index: i, Problem: prob})
		}
	}
	if len(errs) > 0 {
		return Policy{}, errors.Join(errs...)
	}
	return Policy{rules: cloneRules(rules)}, nil
}

// Rules returns a copy of the policy's rules.
func (p Policy) Rules() []Rule { return cloneRules(p.rules) }

func cloneRules(rules []Rule) []Rule {
	out := slices.Clone(rules)
	for i := range out {
		out[i].Cond.Presets = slices.Clone(out[i].Cond.Presets)
		out[i].Nodes = slices.Clone(out[i].Nodes)
	}
	return out
}

// Problem names one way a rule can be malformed.
type Problem uint8

// The reasons a rule is rejected.
const (
	// ProblemEffect: the effect is not allow, deny or restrict.
	ProblemEffect Problem = iota + 1
	// ProblemSubject: the subject kind is unknown.
	ProblemSubject
	// ProblemSubjectIDs: the subject lacks an identifier its kind needs, or
	// carries one its kind does not use.
	ProblemSubjectIDs
	// ProblemObject: the object kind is unknown.
	ProblemObject
	// ProblemObjectIDs: the object lacks an endpoint identifier its kind needs,
	// or carries one its kind does not use.
	ProblemObjectIDs
	// ProblemAction: the action is unknown.
	ProblemAction
	// ProblemActionObject: the action cannot be aimed at anything the object
	// covers (for example relay.reserve on endpoints).
	ProblemActionObject
	// ProblemWindowEmpty: NotAfter is not later than NotBefore, so the rule
	// would never be in force.
	ProblemWindowEmpty
	// ProblemRateShape: the rate cap has a count without a positive window, or
	// a window without a count.
	ProblemRateShape
	// ProblemRateOnDeny: a deny rule carries a rate cap.
	ProblemRateOnDeny
	// ProblemBodyCapRange: the body size cap is negative or the maximum value.
	ProblemBodyCapRange
	// ProblemBodyCapOnDeny: a deny rule carries a body size cap.
	ProblemBodyCapOnDeny
	// ProblemBodyCapAction: a body size cap on an action that carries no body.
	ProblemBodyCapAction
	// ProblemPresetsAction: a preset set on an action other than session.create.
	ProblemPresetsAction
	// ProblemPresetName: a preset name is empty.
	ProblemPresetName
	// ProblemPresetDuplicate: a preset name is listed twice.
	ProblemPresetDuplicate
	// ProblemRestrictionSubject: a restriction carries a subject; it has none.
	ProblemRestrictionSubject
	// ProblemRestrictionConditions: a restriction carries conditions; it takes
	// none.
	ProblemRestrictionConditions
	// ProblemNodeSetEmpty: a restriction names no node, which would refuse
	// everyone and is better written as a deny.
	ProblemNodeSetEmpty
	// ProblemNodeSetZero: a restriction lists the zero node identifier.
	ProblemNodeSetZero
	// ProblemNodeSetDuplicate: a restriction lists a node twice.
	ProblemNodeSetDuplicate
	// ProblemNodeSetEffect: an allow or deny rule carries a node set.
	ProblemNodeSetEffect
)

var problemNames = [...]string{
	ProblemEffect:          "effect is not allow, deny or restrict",
	ProblemSubject:         "unknown subject kind",
	ProblemSubjectIDs:      "subject identifiers do not match its kind",
	ProblemObject:          "unknown object kind",
	ProblemObjectIDs:       "object identifier does not match its kind",
	ProblemAction:          "unknown action",
	ProblemActionObject:    "action cannot be aimed at the object",
	ProblemWindowEmpty:     "validity window is empty",
	ProblemRateShape:       "rate cap needs both a count and a positive window",
	ProblemRateOnDeny:      "deny rule cannot carry a rate cap",
	ProblemBodyCapRange:    "body size cap is out of range",
	ProblemBodyCapOnDeny:   "deny rule cannot carry a body size cap",
	ProblemBodyCapAction:   "body size cap only applies to message.send",
	ProblemPresetsAction:   "preset set only applies to session.create",
	ProblemPresetName:      "preset name is empty",
	ProblemPresetDuplicate: "preset name is listed twice",

	ProblemRestrictionSubject:    "restriction cannot carry a subject",
	ProblemRestrictionConditions: "restriction cannot carry conditions",
	ProblemNodeSetEmpty:          "restriction names no node",
	ProblemNodeSetZero:           "restriction lists the zero node identifier",
	ProblemNodeSetDuplicate:      "restriction lists a node twice",
	ProblemNodeSetEffect:         "only a restriction carries a node set",
}

// String describes the problem.
func (p Problem) String() string {
	if int(p) >= len(problemNames) || problemNames[p] == "" {
		return "unknown problem"
	}
	return problemNames[p]
}

// RuleError reports one problem with one rule of a rule list.
type RuleError struct {
	// Index is the position of the rule in the list given to NewPolicy.
	Index   int
	Problem Problem
}

// Error implements error.
func (e *RuleError) Error() string {
	return fmt.Sprintf("rule %d: %s", e.Index, e.Problem)
}

// Problems lists every way the rule is malformed, in a stable order. A nil
// result means the rule is well formed.
func (r Rule) Problems() []Problem {
	var out []Problem
	add := func(p Problem) { out = append(out, p) }

	restriction := r.Effect == EffectRestrict
	if r.Effect != EffectAllow && r.Effect != EffectDeny && !restriction {
		add(ProblemEffect)
	}

	if restriction {
		if r.Subject != (Subject{}) {
			add(ProblemRestrictionSubject)
		}
	} else {
		r.Subject.problems(add)
	}

	o := r.Object
	switch o.Kind {
	case ObjectEndpoint, ObjectRuntime:
		if o.Endpoint.IsZero() {
			add(ProblemObjectIDs)
		}
	case ObjectAllEndpoints, ObjectNode:
		if !o.Endpoint.IsZero() {
			add(ProblemObjectIDs)
		}
	default:
		add(ProblemObject)
	}

	if !r.Action.valid() {
		add(ProblemAction)
	} else if o.Kind >= ObjectEndpoint && o.Kind <= ObjectNode && !appliesToObject(r.Action, o) {
		add(ProblemActionObject)
	}

	if restriction {
		r.nodeSetProblems(add)
		if !r.Cond.isZero() {
			add(ProblemRestrictionConditions)
		}
		return out
	}
	if len(r.Nodes) > 0 {
		add(ProblemNodeSetEffect)
	}
	r.conditionProblems(add)
	return out
}

func (s Subject) problems(add func(Problem)) {
	switch s.Kind {
	case SubjectMember, SubjectDirect:
		if !s.Node.IsZero() || !s.Endpoint.IsZero() {
			add(ProblemSubjectIDs)
		}
	case SubjectNode:
		if s.Node.IsZero() || !s.Endpoint.IsZero() {
			add(ProblemSubjectIDs)
		}
	case SubjectEndpoint:
		if s.Node.IsZero() || s.Endpoint.IsZero() {
			add(ProblemSubjectIDs)
		}
	default:
		add(ProblemSubject)
	}
}

func (r Rule) nodeSetProblems(add func(Problem)) {
	if len(r.Nodes) == 0 {
		add(ProblemNodeSetEmpty)
	}
	seen := make(map[ident.NodeID]bool, len(r.Nodes))
	for _, n := range r.Nodes {
		switch {
		case n.IsZero():
			add(ProblemNodeSetZero)
		case seen[n]:
			add(ProblemNodeSetDuplicate)
		}
		seen[n] = true
	}
}

// conditionProblems checks the conditions of an allow or deny rule.
func (r Rule) conditionProblems(add func(Problem)) {
	c := r.Cond
	if !c.NotBefore.IsZero() && !c.NotAfter.IsZero() && !c.NotAfter.After(c.NotBefore) {
		add(ProblemWindowEmpty)
	}
	if (c.Rate.Count == 0) != (c.Rate.Window == 0) || c.Rate.Window < 0 {
		add(ProblemRateShape)
	}
	if !c.Rate.unlimited() && r.Effect == EffectDeny {
		add(ProblemRateOnDeny)
	}
	if c.MaxBodyBytes < 0 || c.MaxBodyBytes == maxInt64 {
		add(ProblemBodyCapRange)
	}
	if c.MaxBodyBytes != 0 {
		if r.Effect == EffectDeny {
			add(ProblemBodyCapOnDeny)
		}
		if r.Action != ActionMessageSend {
			add(ProblemBodyCapAction)
		}
	}
	if len(c.Presets) > 0 {
		if r.Action != ActionSessionCreate {
			add(ProblemPresetsAction)
		}
		seen := make(map[string]bool, len(c.Presets))
		for _, name := range c.Presets {
			switch {
			case name == "":
				add(ProblemPresetName)
			case seen[name]:
				add(ProblemPresetDuplicate)
			}
			seen[name] = true
		}
	}
}

const maxInt64 = 1<<63 - 1

// appliesToObject reports whether the action can be aimed at anything the rule
// object covers.
func appliesToObject(a Action, o Object) bool {
	switch o.Kind {
	case ObjectEndpoint:
		// An endpoint is matched by identifier, which a runtime endpoint also has.
		return a.appliesTo(TargetEndpoint) || a.appliesTo(TargetRuntime)
	case ObjectRuntime:
		return a.appliesTo(TargetRuntime)
	case ObjectAllEndpoints:
		return a.appliesTo(TargetEndpoint) || a.appliesTo(TargetRuntime)
	case ObjectNode:
		return a.appliesTo(TargetNode)
	}
	return false
}
