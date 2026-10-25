package config

import (
	"errors"
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/ident"
)

// policyWire is policy.json: the rules of authz, written as names and ids.
type policyWire struct {
	Format        int            `json:"format"`
	Version       uint64         `json:"version"`
	Rules         []ruleWire     `json:"rules"`
	LastOperation *operationWire `json:"last_operation,omitempty"`
}

type ruleWire struct {
	Effect     string       `json:"effect"`
	Subject    *subjectWire `json:"subject,omitempty"`
	Object     objectWire   `json:"object"`
	Action     string       `json:"action"`
	Conditions *condWire    `json:"conditions,omitempty"`
	Nodes      []string     `json:"nodes,omitempty"`
}

type subjectWire struct {
	Kind    string `json:"kind"`
	Node    string `json:"node,omitempty"`
	Address string `json:"address,omitempty"`
}

type objectWire struct {
	Kind     string `json:"kind"`
	Endpoint string `json:"endpoint,omitempty"`
}

type condWire struct {
	NotBefore    string    `json:"not_before,omitempty"`
	NotAfter     string    `json:"not_after,omitempty"`
	Rate         *rateWire `json:"rate,omitempty"`
	MaxBodyBytes int64     `json:"max_body_bytes,omitempty"`
	Presets      []string  `json:"presets,omitempty"`
}

type rateWire struct {
	Count   uint32 `json:"count"`
	WindowS int64  `json:"window_s"`
}

// The names of the file for the kinds authz knows. The names of the actions are
// authz's own (Action.String); the rest are written here, once, in both
// directions.
var (
	effectNames = []struct {
		e    authz.Effect
		name string
	}{{authz.EffectAllow, "allow"}, {authz.EffectDeny, "deny"}, {authz.EffectRestrict, "restrict"}}
	subjectNames = []struct {
		k    authz.SubjectKind
		name string
	}{{authz.SubjectMember, "member"}, {authz.SubjectDirect, "direct"}, {authz.SubjectNode, "node"}, {authz.SubjectEndpoint, "endpoint"}}
	objectNames = []struct {
		k    authz.ObjectKind
		name string
	}{{authz.ObjectEndpoint, "endpoint"}, {authz.ObjectAllEndpoints, "all_endpoints"}, {authz.ObjectRuntime, "runtime"}, {authz.ObjectNode, "node"}}
)

func effectOf(name string) (authz.Effect, bool) {
	for _, e := range effectNames {
		if e.name == name {
			return e.e, true
		}
	}
	return 0, false
}

func effectName(e authz.Effect) (string, bool) {
	for _, x := range effectNames {
		if x.e == e {
			return x.name, true
		}
	}
	return "", false
}

func subjectKindOf(name string) (authz.SubjectKind, bool) {
	for _, s := range subjectNames {
		if s.name == name {
			return s.k, true
		}
	}
	return 0, false
}

func objectKindOf(name string) (authz.ObjectKind, bool) {
	for _, o := range objectNames {
		if o.name == name {
			return o.k, true
		}
	}
	return 0, false
}

func actionOf(name string) (authz.Action, bool) {
	for _, a := range authz.Actions {
		if a.String() == name {
			return a, true
		}
	}
	return 0, false
}

func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%q is not an RFC 3339 time", s)
	}
	return t.UTC(), nil
}

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func (w ruleWire) rule() (authz.Rule, error) {
	var r authz.Rule
	var ok bool
	if r.Effect, ok = effectOf(w.Effect); !ok {
		return r, invalidf("effect", "%q is not allow, deny or restrict", w.Effect)
	}
	if w.Subject != nil {
		kind, ok := subjectKindOf(w.Subject.Kind)
		if !ok {
			return r, invalidf("subject.kind", "%q is not member, direct, node or endpoint", w.Subject.Kind)
		}
		r.Subject.Kind = kind
		if w.Subject.Node != "" {
			n, err := ident.ParseNodeID(w.Subject.Node)
			if err != nil {
				return r, invalid("subject.node", err)
			}
			r.Subject.Node = n
		}
		if w.Subject.Address != "" {
			if w.Subject.Node != "" {
				return r, invalidf("subject.address", "an address names its node: give node or address, not both")
			}
			a, err := ident.ParseAddress(w.Subject.Address)
			if err != nil {
				return r, invalid("subject.address", err)
			}
			r.Subject.Node, r.Subject.Endpoint = a.Node, a.Endpoint
		}
	}
	kind, ok := objectKindOf(w.Object.Kind)
	if !ok {
		return r, invalidf("object.kind", "%q is not endpoint, all_endpoints, runtime or node", w.Object.Kind)
	}
	r.Object.Kind = kind
	if w.Object.Endpoint != "" {
		e, err := ident.ParseEndpointID(w.Object.Endpoint)
		if err != nil {
			return r, invalid("object.endpoint", err)
		}
		r.Object.Endpoint = e
	}
	if r.Action, ok = actionOf(w.Action); !ok {
		return r, invalidf("action", "%q is not an action of the policy", w.Action)
	}
	if c := w.Conditions; c != nil {
		if c.NotBefore != "" {
			t, err := parseTime(c.NotBefore)
			if err != nil {
				return r, invalid("conditions.not_before", err)
			}
			r.Cond.NotBefore = t
		}
		if c.NotAfter != "" {
			t, err := parseTime(c.NotAfter)
			if err != nil {
				return r, invalid("conditions.not_after", err)
			}
			r.Cond.NotAfter = t
		}
		if c.Rate != nil {
			window := secondsToDuration(c.Rate.WindowS)
			if c.Rate.WindowS > 0 && window/time.Second != time.Duration(c.Rate.WindowS) {
				return r, invalidf("conditions.rate.window_s", "%d seconds are more than a duration holds", c.Rate.WindowS)
			}
			r.Cond.Rate = authz.RateCap{Count: c.Rate.Count, Window: window}
		}
		r.Cond.MaxBodyBytes = c.MaxBodyBytes
		r.Cond.Presets = append([]string(nil), c.Presets...)
	}
	for i, s := range w.Nodes {
		n, err := ident.ParseNodeID(s)
		if err != nil {
			return r, invalid(fmt.Sprintf("nodes[%d]", i), err)
		}
		r.Nodes = append(r.Nodes, n)
	}
	return r, nil
}

func ruleWireOf(r authz.Rule) (ruleWire, error) {
	var w ruleWire
	var ok bool
	if w.Effect, ok = effectName(r.Effect); !ok {
		return w, fmt.Errorf("effect %d has no name", r.Effect)
	}
	if r.Effect != authz.EffectRestrict {
		s := &subjectWire{}
		for _, x := range subjectNames {
			if x.k == r.Subject.Kind {
				s.Kind = x.name
			}
		}
		if s.Kind == "" {
			return w, fmt.Errorf("subject kind %d has no name", r.Subject.Kind)
		}
		switch r.Subject.Kind {
		case authz.SubjectNode:
			s.Node = r.Subject.Node.String()
		case authz.SubjectEndpoint:
			s.Address = ident.Address{Node: r.Subject.Node, Endpoint: r.Subject.Endpoint}.String()
		}
		w.Subject = s
	}
	for _, x := range objectNames {
		if x.k == r.Object.Kind {
			w.Object.Kind = x.name
		}
	}
	if w.Object.Kind == "" {
		return w, fmt.Errorf("object kind %d has no name", r.Object.Kind)
	}
	if !r.Object.Endpoint.IsZero() {
		w.Object.Endpoint = r.Object.Endpoint.String()
	}
	w.Action = r.Action.String()
	if r.Cond.Rate.Window%time.Second != 0 {
		return w, fmt.Errorf("a rate window of %v is not a whole number of seconds", r.Cond.Rate.Window)
	}
	c := condWire{MaxBodyBytes: r.Cond.MaxBodyBytes, Presets: r.Cond.Presets}
	if !r.Cond.NotBefore.IsZero() {
		c.NotBefore = formatTime(r.Cond.NotBefore)
	}
	if !r.Cond.NotAfter.IsZero() {
		c.NotAfter = formatTime(r.Cond.NotAfter)
	}
	if r.Cond.Rate != (authz.RateCap{}) {
		c.Rate = &rateWire{Count: r.Cond.Rate.Count, WindowS: int64(r.Cond.Rate.Window / time.Second)}
	}
	if c.NotBefore != "" || c.NotAfter != "" || c.Rate != nil || c.MaxBodyBytes != 0 || len(c.Presets) != 0 {
		w.Conditions = &c
	}
	for _, n := range r.Nodes {
		w.Nodes = append(w.Nodes, n.String())
	}
	return w, nil
}

func decodePolicy(data []byte) (PolicyFile, error) {
	var w policyWire
	if err := decodeStrict(data, &w); err != nil {
		return PolicyFile{}, err
	}
	if err := checkHeader(w.Format, w.Version, true); err != nil {
		return PolicyFile{}, err
	}
	rules := make([]authz.Rule, 0, len(w.Rules))
	for i, rw := range w.Rules {
		r, err := rw.rule()
		if err != nil {
			return PolicyFile{}, under(fmt.Sprintf("rules[%d]", i), err)
		}
		rules = append(rules, r)
	}
	pol, err := authz.NewPolicy(rules)
	if err != nil {
		var re *authz.RuleError
		if errors.As(err, &re) {
			return PolicyFile{}, invalid(fmt.Sprintf("rules[%d]", re.Index), re)
		}
		return PolicyFile{}, invalid("rules", err)
	}
	p := PolicyFile{Version: versionOf(w.Version), Policy: pol}
	if w.LastOperation != nil {
		op, err := w.LastOperation.operation()
		if err != nil {
			return PolicyFile{}, err
		}
		p.LastOperation = op
	}
	return p, nil
}

// DecodePolicy decodes policy.json: the rules, each as authz reads it. The error
// is a *FileError that names the file.
func DecodePolicy(data []byte) (PolicyFile, error) {
	p, err := decodePolicy(data)
	if err != nil {
		return PolicyFile{}, fileErrorFor(PolicyPath, KindPolicy, err)
	}
	return p, nil
}

// Encode returns the bytes of policy.json.
func (p PolicyFile) Encode() ([]byte, error) {
	if err := checkEncodeVersion(uint64(p.Version)); err != nil {
		return nil, err
	}
	rules := p.Policy.Rules()
	w := policyWire{Format: FormatVersion, Version: uint64(p.Version), Rules: make([]ruleWire, 0, len(rules))}
	for _, r := range rules {
		rw, err := ruleWireOf(r)
		if err != nil {
			return nil, err
		}
		w.Rules = append(w.Rules, rw)
	}
	op, err := operationWireOf(p.LastOperation)
	if err != nil {
		return nil, err
	}
	w.LastOperation = op
	return encodeJSON(w)
}
