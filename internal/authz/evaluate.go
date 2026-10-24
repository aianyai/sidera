package authz

import (
	"time"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/trust"
)

// query is the part of a request that the rule list reads.
type query struct {
	now    time.Time
	direct bool
	sender Sender
	action Action
	target Target
	size   int64
	preset string
}

// decision is what the rule list says about a query.
type decision struct {
	// denied: an active deny rule covers the query.
	denied bool
	// restricted: a restriction covers the query's object and action and the
	// sender's node is not in its set.
	restricted bool
	// granted: an active allow rule covers the query and all its conditions
	// hold. rate is then the highest-ranking cap among the granting rules by
	// rateCmp, the zero cap when any of them is uncapped. rateByEndpoint says
	// the counter of that cap is per endpoint: it is true only when every
	// granting rule with that cap names the sender's endpoint, because any other
	// rule covers the sender's node whatever endpoint identifier it claims.
	granted        bool
	rate           RateCap
	rateByEndpoint bool
	// sizeBlocked and presetBlocked: an active allow rule covers the query but
	// fails only the body size cap, or only the preset set.
	sizeBlocked   bool
	presetBlocked bool
}

// permits reports whether the rule list lets the query through as an ordinary
// request.
func (d decision) permits() bool { return d.granted && !d.denied && !d.restricted }

// admitsReply reports whether the rule list lets a verified reply through. A
// verified reply stands in for a missing allow, so only a deny stops it; a
// restriction does not, because the answers to an endpoint's own messages must
// still come back after its inbound policy is narrowed.
func (d decision) admitsReply() bool { return !d.denied }

// refusal says why the rule list does not permit the query, most specific
// first. It is only meaningful when permits is false.
func (d decision) refusal() Reason {
	switch {
	case d.denied:
		return ReasonExplicitDeny
	case d.restricted:
		return ReasonNotAuthorized
	case d.presetBlocked:
		return ReasonPresetNotAllowed
	case d.sizeBlocked:
		return ReasonBodyTooLarge
	}
	return ReasonNotAuthorized
}

// decide applies the rule list to a query: deny wins; a restriction refuses
// senders outside its set; otherwise any allow rule whose conditions hold
// grants it. Rules are a union of grants, so when several grant the same query
// the highest-ranked rate cap, by rateCmp, is the one that counts.
func (p Policy) decide(q query) decision {
	var d decision
	for _, r := range p.rules {
		if !r.matches(q) || !r.Cond.activeAt(q.now) {
			continue
		}
		if r.Effect == EffectRestrict {
			d.restricted = d.restricted || r.restricts(q)
			continue
		}
		presetOK := r.Cond.allowsPreset(q.preset)
		if r.Effect == EffectDeny {
			if presetOK {
				d.denied = true
			}
			continue
		}
		sizeOK := r.Cond.MaxBodyBytes == 0 || q.size <= r.Cond.MaxBodyBytes
		switch {
		case presetOK && sizeOK:
			byEndpoint := r.Subject.Kind == SubjectEndpoint
			switch c := rateCmp(r.Cond.Rate, d.rate); {
			case !d.granted || c > 0:
				d.rate, d.rateByEndpoint = r.Cond.Rate, byEndpoint
			case c == 0:
				d.rateByEndpoint = d.rateByEndpoint && byEndpoint
			}
			d.granted = true
		case !presetOK:
			d.presetBlocked = true
		default:
			d.sizeBlocked = true
		}
	}
	return d
}

// Evaluate decides one request against the node policy.
//
// Order of checks: denied or unset connection trust and a malformed request are
// refused first. Then the rules decide, whether trust is allowed or suspended:
// explicit deny rules, restrictions, allow rules (a verified reply may stand in
// for them), and the target endpoint's own declarations, which can only narrow
// the result. Only a request that would be allowed is then suspended when trust
// is suspended: a refusal the policy would give anyway is final and is given at
// once, so a deny rule or a lost grant is not left queued behind a lapsed
// guarantee.
func Evaluate(p Policy, r Request) Verdict {
	switch r.Trust {
	case trust.AllowedDirect, trust.AllowedInherited, trust.Suspended:
	case trust.Denied:
		return verdictFor(ReasonTrustDenied)
	default:
		return verdictFor(ReasonMalformedRequest)
	}
	if !r.wellFormed() {
		return verdictFor(ReasonMalformedRequest)
	}

	d := p.decide(r.query())
	reply := r.Reply && r.Action == ActionMessageSend && d.admitsReply()
	if !d.permits() && !reply {
		return verdictFor(d.refusal())
	}
	if reason, declined := r.asked().declinedBy(reply); declined {
		return verdictFor(reason)
	}
	if r.Trust == trust.Suspended {
		return verdictFor(ReasonTrustSuspended)
	}

	if !d.permits() {
		return verdictFor(ReasonReplyPermission)
	}
	v := verdictFor(ReasonGranted)
	// A reply is an answer bounded by the reply window and the node's resource
	// limits, so it carries no policy rate obligation: a sender trusted with a
	// capped allow must not be treated worse than one with no allow at all.
	if !reply && !d.rate.unlimited() {
		key := RateKey{Sender: Sender{Node: r.Sender.Node}, Target: r.Target, Action: r.Action}
		if d.rateByEndpoint {
			key.Sender.Endpoint = r.Sender.Endpoint
		}
		v.Obligations = []RateObligation{{Key: key, Limit: d.rate.Count, Window: d.rate.Window}}
	}
	return v
}

func (r Request) query() query {
	return query{
		now:    r.Now,
		direct: r.Trust == trust.AllowedDirect,
		sender: r.Sender,
		action: r.Action,
		target: r.Target,
		size:   r.BodySize,
		preset: r.Preset,
	}
}

// asked is what the request-shape check and the target's own declarations read.
// Request and LocalRequest both reduce to it, so the two entry points share one
// shape check and one reading of the declarations.
type asked struct {
	stage          Stage
	sender         Sender
	action         Action
	target         Target
	multiRecipient bool
	declaration    Declaration
}

func (r Request) asked() asked {
	return asked{r.Stage, r.Sender, r.Action, r.Target, r.MultiRecipient, r.TargetDeclaration}
}

func (r LocalRequest) asked() asked {
	return asked{r.Stage, r.Sender, r.Action, r.Target, r.MultiRecipient, r.TargetDeclaration}
}

// wellFormed is the request-shape check. The system endpoint is refused as a
// sender: it only ever sends this node's own channel feedback, which is exempt
// traffic and never reaches this package, and a remote node chooses its own
// endpoint part and could otherwise forge that feedback.
func (a asked) wellFormed() bool {
	if a.sender.Node.IsZero() || a.sender.Endpoint.IsSystem() || !a.action.appliesTo(a.target.Kind) {
		return false
	}
	if (a.target.Kind == TargetNode) != a.target.Endpoint.IsZero() {
		return false
	}
	return a.stage == StageIntake || a.stage == StageRecv
}

func (r Request) wellFormed() bool {
	if !r.asked().wellFormed() || r.BodySize < 0 {
		return false
	}
	return r.Action != ActionSessionCreate || r.Preset != ""
}

// declinedBy applies the target endpoint's self-declarations. The pause (at
// intake only) and hidden declarations hold even for a reply; a reply only lifts
// the sender restriction, because that restriction is the inbound policy the
// reply permission exists to pass. At recv the endpoint is reading its own
// queued messages, so its pause is not applied.
func (a asked) declinedBy(reply bool) (Reason, bool) {
	d := a.declaration
	switch a.action {
	case ActionMessageSend:
		switch {
		case d.Paused && a.stage == StageIntake:
			return ReasonEndpointPaused, true
		case d.RefuseMultiRecipient && a.multiRecipient:
			return ReasonEndpointRefusesMultiRecipient, true
		case d.RestrictSenders && !reply && !d.accepts(a.sender):
			return ReasonSenderNotAccepted, true
		}
	case ActionSee:
		if d.HiddenFromDirectory && a.target.Kind != TargetNode {
			return ReasonEndpointHidden, true
		}
	}
	return 0, false
}

func (d Declaration) accepts(s Sender) bool {
	for _, a := range d.AcceptFrom {
		if a == (ident.Address{Node: s.Node, Endpoint: s.Endpoint}) {
			return true
		}
	}
	return false
}
