package authz

import (
	"slices"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/trust"
)

func TestEvaluate(t *testing.T) {
	member := Members()
	allEps := AllLocalEndpoints()
	tightened := mustPolicy(t) // allows nothing

	tests := []struct {
		name   string
		policy Policy
		req    Request
		want   Reason
	}{
		// Connection trust: denied and unset end the request at once; suspended
		// lets the rules speak first and suspends only what they would allow.
		{"suspended trust suspends a request the policy allows", DefaultPolicy(),
			with(send(), func(r *Request) { r.Trust = suspended }), ReasonTrustSuspended},
		{"suspended trust: a covering deny rule is still an explicit deny",
			mustPolicy(t, Deny(member, allEps, ActionMessageSend, Conditions{})),
			with(send(), func(r *Request) { r.Trust = suspended }), ReasonExplicitDeny},
		{"suspended trust: a malformed request is still malformed", DefaultPolicy(),
			with(send(), func(r *Request) {
				r.Trust = suspended
				r.Action = 0
			}), ReasonMalformedRequest},
		{"suspended trust: a request no rule grants is not authorized", tightened,
			with(send(), func(r *Request) { r.Trust = suspended }), ReasonNotAuthorized},
		{"suspended trust: a paused endpoint is still reported at intake", DefaultPolicy(),
			with(send(), func(r *Request) {
				r.Trust = suspended
				r.TargetDeclaration = Declaration{Paused: true}
			}), ReasonEndpointPaused},
		{"suspended trust: a verified reply is suspended, not admitted", tightened,
			with(send(), func(r *Request) { r.Trust = suspended; r.Reply = true }), ReasonTrustSuspended},
		{"suspended trust: the sender counts as inherited, so a direct-only allow does not grant",
			mustPolicy(t, Allow(DirectTrusted(), allEps, ActionMessageSend, Conditions{})),
			with(send(), func(r *Request) { r.Trust = trust.Suspended }), ReasonNotAuthorized},
		{"denied trust rejects even when the policy allows", DefaultPolicy(),
			with(send(), func(r *Request) { r.Trust = trust.Denied }), ReasonTrustDenied},
		{"denied trust is not lifted by a reply permission", tightened,
			with(send(), func(r *Request) { r.Trust = trust.Denied; r.Reply = true }), ReasonTrustDenied},
		{"unset trust is the zero outcome, which is denied: an unset field fails closed", DefaultPolicy(),
			with(send(), func(r *Request) { r.Trust = 0 }), ReasonTrustDenied},
		{"an out-of-range trust outcome is rejected instead of trusted", DefaultPolicy(),
			with(send(), func(r *Request) { r.Trust = trust.Outcome(99) }), ReasonMalformedRequest},

		// The default policy.
		{"default policy: member sends a message to any endpoint", DefaultPolicy(), send(), ReasonGranted},
		{"default policy: inherited member sends a message", DefaultPolicy(),
			with(send(), func(r *Request) { r.Trust = inherited }), ReasonGranted},
		{"default policy: member creates a session with any preset", DefaultPolicy(), create("anything"), ReasonGranted},
		{"default policy: member reserves a relay slot", DefaultPolicy(), relay(), ReasonGranted},
		{"default policy: member sees the node", DefaultPolicy(),
			with(relay(), func(r *Request) { r.Action = ActionSee }), ReasonGranted},
		{"default policy: member sees an endpoint", DefaultPolicy(),
			with(send(), func(r *Request) { r.Action = ActionSee }), ReasonGranted},
		{"empty policy allows nothing", tightened, send(), ReasonNotAuthorized},

		// Subjects.
		{"subject direct: direct sender matches",
			mustPolicy(t, Allow(DirectTrusted(), allEps, ActionMessageSend, Conditions{})), send(), ReasonGranted},
		{"subject direct: inherited sender does not match",
			mustPolicy(t, Allow(DirectTrusted(), allEps, ActionMessageSend, Conditions{})),
			with(send(), func(r *Request) { r.Trust = inherited }), ReasonNotAuthorized},
		{"subject node: any endpoint of the node matches",
			mustPolicy(t, Allow(AnyEndpointOf(node(1)), allEps, ActionMessageSend, Conditions{})),
			with(send(), func(r *Request) { r.Sender.Endpoint = ep(7) }), ReasonGranted},
		{"subject node: a sender without an endpoint matches",
			mustPolicy(t, Allow(AnyEndpointOf(node(1)), ThisNode(), ActionRelayReserve, Conditions{})), relay(), ReasonGranted},
		{"subject node: another node does not match",
			mustPolicy(t, Allow(AnyEndpointOf(node(2)), allEps, ActionMessageSend, Conditions{})), send(), ReasonNotAuthorized},
		{"subject endpoint: that endpoint matches",
			mustPolicy(t, Allow(EndpointOf(addr(1, 1)), allEps, ActionMessageSend, Conditions{})), send(), ReasonGranted},
		{"subject endpoint: another endpoint of the same node does not match",
			mustPolicy(t, Allow(EndpointOf(addr(1, 2)), allEps, ActionMessageSend, Conditions{})), send(), ReasonNotAuthorized},
		{"subject endpoint: a sender without an endpoint does not match",
			mustPolicy(t, Allow(EndpointOf(addr(1, 1)), ThisNode(), ActionRelayReserve, Conditions{})), relay(), ReasonNotAuthorized},

		// Objects.
		{"object endpoint: that endpoint matches",
			mustPolicy(t, Allow(member, LocalEndpoint(ep(10)), ActionMessageSend, Conditions{})), send(), ReasonGranted},
		{"object endpoint: another endpoint does not match",
			mustPolicy(t, Allow(member, LocalEndpoint(ep(11)), ActionMessageSend, Conditions{})), send(), ReasonNotAuthorized},
		{"object runtime: that runtime endpoint matches",
			mustPolicy(t, Allow(member, RuntimeEndpoint(ep(20)), ActionSessionCreate, Conditions{})), create("p"), ReasonGranted},
		{"object runtime: another runtime endpoint does not match",
			mustPolicy(t, Allow(member, RuntimeEndpoint(ep(21)), ActionSessionCreate, Conditions{})), create("p"), ReasonNotAuthorized},
		{"object all endpoints covers a runtime endpoint",
			mustPolicy(t, Allow(member, allEps, ActionSessionCreate, Conditions{})), create("p"), ReasonGranted},
		{"object endpoint covers a runtime endpoint with that identifier",
			mustPolicy(t, Allow(member, LocalEndpoint(ep(20)), ActionEndpointStatus, Conditions{})),
			with(create("p"), func(r *Request) { r.Action = ActionEndpointStatus }), ReasonGranted},
		{"object endpoint: a deny covers a runtime endpoint with that identifier",
			mustPolicy(t, Allow(member, allEps, ActionEndpointStatus, Conditions{}), Deny(member, LocalEndpoint(ep(20)), ActionEndpointStatus, Conditions{})),
			with(create("p"), func(r *Request) { r.Action = ActionEndpointStatus }), ReasonExplicitDeny},
		{"object runtime covers an endpoint with that identifier",
			mustPolicy(t, Allow(member, RuntimeEndpoint(ep(10)), ActionEndpointStatus, Conditions{})),
			with(send(), func(r *Request) { r.Action = ActionEndpointStatus }), ReasonGranted},
		{"object node: relay reservation matches",
			mustPolicy(t, Allow(member, ThisNode(), ActionRelayReserve, Conditions{})), relay(), ReasonGranted},

		// Actions.
		{"action: an allow for one action does not grant another",
			mustPolicy(t, Allow(member, allEps, ActionMessageSend, Conditions{})),
			with(send(), func(r *Request) { r.Action = ActionSessionClose }), ReasonNotAuthorized},
		{"action: relay.connect is separate from relay.reserve",
			mustPolicy(t, Allow(member, ThisNode(), ActionRelayReserve, Conditions{})),
			with(relay(), func(r *Request) { r.Action = ActionRelayConnect }), ReasonNotAuthorized},

		// Deny wins.
		{"deny wins over an allow with the same scope",
			mustPolicy(t, Allow(member, allEps, ActionMessageSend, Conditions{}), Deny(member, allEps, ActionMessageSend, Conditions{})),
			send(), ReasonExplicitDeny},
		{"deny wins regardless of rule order",
			mustPolicy(t, Deny(member, allEps, ActionMessageSend, Conditions{}), Allow(member, allEps, ActionMessageSend, Conditions{})),
			send(), ReasonExplicitDeny},
		{"deny on a narrow subject beats a broad allow",
			mustPolicy(t, Allow(member, allEps, ActionMessageSend, Conditions{}), Deny(AnyEndpointOf(node(1)), allEps, ActionMessageSend, Conditions{})),
			send(), ReasonExplicitDeny},
		{"deny on a narrow subject leaves other senders alone",
			mustPolicy(t, Allow(member, allEps, ActionMessageSend, Conditions{}), Deny(AnyEndpointOf(node(2)), allEps, ActionMessageSend, Conditions{})),
			send(), ReasonGranted},
		{"deny on one endpoint leaves the others alone",
			mustPolicy(t, Allow(member, allEps, ActionMessageSend, Conditions{}), Deny(member, LocalEndpoint(ep(11)), ActionMessageSend, Conditions{})),
			send(), ReasonGranted},

		// Validity window: [NotBefore, NotAfter).
		{"window: before NotBefore the rule is not in force",
			mustPolicy(t, Allow(member, allEps, ActionMessageSend, Conditions{NotBefore: t0.Add(time.Second)})), send(), ReasonNotAuthorized},
		{"window: NotBefore itself is in force",
			mustPolicy(t, Allow(member, allEps, ActionMessageSend, Conditions{NotBefore: t0})), send(), ReasonGranted},
		{"window: just before NotAfter is in force",
			mustPolicy(t, Allow(member, allEps, ActionMessageSend, Conditions{NotAfter: t0.Add(time.Nanosecond)})), send(), ReasonGranted},
		{"window: NotAfter itself is out of force",
			mustPolicy(t, Allow(member, allEps, ActionMessageSend, Conditions{NotAfter: t0})), send(), ReasonNotAuthorized},
		{"window: an expired allow no longer grants (policy tightens by time)",
			mustPolicy(t, Allow(member, allEps, ActionMessageSend, Conditions{NotBefore: t0.Add(-time.Hour), NotAfter: t0.Add(-time.Minute)})),
			send(), ReasonNotAuthorized},
		{"window: a deny that has not started does not apply",
			mustPolicy(t, Allow(member, allEps, ActionMessageSend, Conditions{}), Deny(member, allEps, ActionMessageSend, Conditions{NotBefore: t0.Add(time.Hour)})),
			send(), ReasonGranted},
		{"window: a deny in force applies",
			mustPolicy(t, Allow(member, allEps, ActionMessageSend, Conditions{}), Deny(member, allEps, ActionMessageSend, Conditions{NotBefore: t0.Add(-time.Hour), NotAfter: t0.Add(time.Hour)})),
			send(), ReasonExplicitDeny},

		// Body size cap.
		{"body cap: a body at the cap is allowed",
			mustPolicy(t, Allow(member, allEps, ActionMessageSend, Conditions{MaxBodyBytes: 100})),
			with(send(), func(r *Request) { r.BodySize = 100 }), ReasonGranted},
		{"body cap: a body over the cap is rejected as too large",
			mustPolicy(t, Allow(member, allEps, ActionMessageSend, Conditions{MaxBodyBytes: 100})),
			with(send(), func(r *Request) { r.BodySize = 101 }), ReasonBodyTooLarge},
		{"body cap: without a cap any size is allowed",
			mustPolicy(t, Allow(member, allEps, ActionMessageSend, Conditions{})),
			with(send(), func(r *Request) { r.BodySize = 1 << 40 }), ReasonGranted},
		{"body cap: rules are a union, so a larger cap in another rule grants",
			mustPolicy(t,
				Allow(member, allEps, ActionMessageSend, Conditions{MaxBodyBytes: 100}),
				Allow(AnyEndpointOf(node(1)), allEps, ActionMessageSend, Conditions{MaxBodyBytes: 1000})),
			with(send(), func(r *Request) { r.BodySize = 500 }), ReasonGranted},
		{"body cap: over every cap is too large, not unauthorized",
			mustPolicy(t,
				Allow(member, allEps, ActionMessageSend, Conditions{MaxBodyBytes: 100}),
				Allow(AnyEndpointOf(node(1)), allEps, ActionMessageSend, Conditions{MaxBodyBytes: 1000})),
			with(send(), func(r *Request) { r.BodySize = 5000 }), ReasonBodyTooLarge},
		{"body cap: a sender no rule covers is unauthorized, not too large",
			mustPolicy(t, Allow(AnyEndpointOf(node(2)), allEps, ActionMessageSend, Conditions{MaxBodyBytes: 100})),
			with(send(), func(r *Request) { r.BodySize = 5000 }), ReasonNotAuthorized},
		{"body cap: an expired capped rule does not make the body too large",
			mustPolicy(t, Allow(member, allEps, ActionMessageSend, Conditions{MaxBodyBytes: 100, NotAfter: t0})),
			with(send(), func(r *Request) { r.BodySize = 5000 }), ReasonNotAuthorized},

		// Presets.
		{"preset: a listed preset is allowed",
			mustPolicy(t, Allow(member, allEps, ActionSessionCreate, Conditions{Presets: []string{"safe", "sandboxed"}})),
			create("safe"), ReasonGranted},
		{"preset: an unlisted preset is rejected",
			mustPolicy(t, Allow(member, allEps, ActionSessionCreate, Conditions{Presets: []string{"safe"}})),
			create("unsafe"), ReasonPresetNotAllowed},
		{"preset: another rule can grant the preset",
			mustPolicy(t,
				Allow(member, allEps, ActionSessionCreate, Conditions{Presets: []string{"safe"}}),
				Allow(AnyEndpointOf(node(1)), allEps, ActionSessionCreate, Conditions{Presets: []string{"unsafe"}})),
			create("unsafe"), ReasonGranted},
		{"preset: a deny on one preset blocks only that preset",
			mustPolicy(t, Allow(member, allEps, ActionSessionCreate, Conditions{}), Deny(member, allEps, ActionSessionCreate, Conditions{Presets: []string{"unsafe"}})),
			create("unsafe"), ReasonExplicitDeny},
		{"preset: a deny on one preset leaves the others usable",
			mustPolicy(t, Allow(member, allEps, ActionSessionCreate, Conditions{}), Deny(member, allEps, ActionSessionCreate, Conditions{Presets: []string{"unsafe"}})),
			create("safe"), ReasonGranted},
		{"preset: a local-only preset is expressed as a deny for members",
			mustPolicy(t, Allow(member, allEps, ActionSessionCreate, Conditions{}), Deny(member, RuntimeEndpoint(ep(20)), ActionSessionCreate, Conditions{Presets: []string{"local-only"}})),
			create("local-only"), ReasonExplicitDeny},

		// Restrictions: only the listed nodes, and a verified reply still passes.
		{"restriction: a node in the set passes",
			mustPolicy(t, Allow(member, allEps, ActionMessageSend, Conditions{}), OnlyFrom(LocalEndpoint(ep(10)), ActionMessageSend, node(1), node(2))),
			send(), ReasonGranted},
		{"restriction: a direct node outside the set is not authorized",
			mustPolicy(t, Allow(member, allEps, ActionMessageSend, Conditions{}), OnlyFrom(LocalEndpoint(ep(10)), ActionMessageSend, node(2))),
			send(), ReasonNotAuthorized},
		{"restriction: an inherited node outside the set is not authorized",
			mustPolicy(t, Allow(member, allEps, ActionMessageSend, Conditions{}), OnlyFrom(LocalEndpoint(ep(10)), ActionMessageSend, node(2))),
			with(send(), func(r *Request) { r.Trust = inherited }), ReasonNotAuthorized},
		{"restriction: another endpoint is not restricted",
			mustPolicy(t, Allow(member, allEps, ActionMessageSend, Conditions{}), OnlyFrom(LocalEndpoint(ep(11)), ActionMessageSend, node(2))),
			send(), ReasonGranted},
		{"restriction: another action is not restricted",
			mustPolicy(t, Allow(member, allEps, ActionSessionClose, Conditions{}), OnlyFrom(LocalEndpoint(ep(10)), ActionMessageSend, node(2))),
			with(send(), func(r *Request) { r.Action = ActionSessionClose }), ReasonGranted},
		{"restriction: the sender must be in every restriction on the same object and action",
			mustPolicy(t, Allow(member, allEps, ActionMessageSend, Conditions{}),
				OnlyFrom(LocalEndpoint(ep(10)), ActionMessageSend, node(1), node(2)),
				OnlyFrom(allEps, ActionMessageSend, node(2), node(3))),
			send(), ReasonNotAuthorized},
		{"restriction: a node in every restriction passes",
			mustPolicy(t, Allow(member, allEps, ActionMessageSend, Conditions{}),
				OnlyFrom(LocalEndpoint(ep(10)), ActionMessageSend, node(1), node(2)),
				OnlyFrom(allEps, ActionMessageSend, node(1), node(3))),
			send(), ReasonGranted},
		{"restriction: a verified reply from a node outside the set passes",
			mustPolicy(t, Allow(member, allEps, ActionMessageSend, Conditions{}), OnlyFrom(LocalEndpoint(ep(10)), ActionMessageSend, node(2))),
			with(send(), func(r *Request) { r.Reply = true }), ReasonReplyPermission},
		{"restriction: an explicit deny still beats a reply from a node in the set",
			mustPolicy(t, OnlyFrom(LocalEndpoint(ep(10)), ActionMessageSend, node(1)), Deny(member, allEps, ActionMessageSend, Conditions{})),
			with(send(), func(r *Request) { r.Reply = true }), ReasonExplicitDeny},
		{"restriction: grants nothing by itself",
			mustPolicy(t, OnlyFrom(LocalEndpoint(ep(10)), ActionMessageSend, node(1))),
			send(), ReasonNotAuthorized},
		{"restriction: outside the set is not authorized even when the body is too large",
			mustPolicy(t, Allow(member, allEps, ActionMessageSend, Conditions{MaxBodyBytes: 100}), OnlyFrom(allEps, ActionMessageSend, node(2))),
			with(send(), func(r *Request) { r.BodySize = 5000 }), ReasonNotAuthorized},

		// Malformed requests.
		{"malformed: zero sender node", DefaultPolicy(),
			with(send(), func(r *Request) { r.Sender.Node = ident.NodeID{} }), ReasonMalformedRequest},
		{"malformed: unknown action", DefaultPolicy(),
			with(send(), func(r *Request) { r.Action = 99 }), ReasonMalformedRequest},
		{"malformed: action cannot target that kind", DefaultPolicy(),
			with(send(), func(r *Request) { r.Target = Target{Kind: TargetNode} }), ReasonMalformedRequest},
		{"malformed: endpoint target without an endpoint", DefaultPolicy(),
			with(send(), func(r *Request) { r.Target.Endpoint = ident.EndpointID{} }), ReasonMalformedRequest},
		{"malformed: node target with an endpoint", DefaultPolicy(),
			with(relay(), func(r *Request) { r.Target.Endpoint = ep(1) }), ReasonMalformedRequest},
		{"malformed: stage unset", DefaultPolicy(),
			with(send(), func(r *Request) { r.Stage = 0 }), ReasonMalformedRequest},
		{"malformed: negative body size", DefaultPolicy(),
			with(send(), func(r *Request) { r.BodySize = -1 }), ReasonMalformedRequest},
		{"malformed: session.create without a preset", DefaultPolicy(), create(""), ReasonMalformedRequest},

		// Endpoint self-declarations narrow, and only message.send / see read them.
		{"declaration: a paused endpoint rejects messages", DefaultPolicy(),
			with(send(), func(r *Request) { r.TargetDeclaration = Declaration{Paused: true} }), ReasonEndpointPaused},
		{"declaration: a paused endpoint does not stop recv from handing out a queued message", DefaultPolicy(),
			with(send(), func(r *Request) {
				r.Stage = StageRecv
				r.TargetDeclaration = Declaration{Paused: true}
			}), ReasonGranted},
		{"declaration: at recv the other declarations still apply: sender list", DefaultPolicy(),
			with(send(), func(r *Request) {
				r.Stage = StageRecv
				r.TargetDeclaration = Declaration{Paused: true, RestrictSenders: true, AcceptFrom: []ident.Address{addr(1, 2)}}
			}), ReasonSenderNotAccepted},
		{"declaration: at recv the other declarations still apply: multi-recipient refusal", DefaultPolicy(),
			with(send(), func(r *Request) {
				r.Stage = StageRecv
				r.MultiRecipient = true
				r.TargetDeclaration = Declaration{Paused: true, RefuseMultiRecipient: true}
			}), ReasonEndpointRefusesMultiRecipient},
		{"declaration: at recv a hidden endpoint is still hidden from the directory", DefaultPolicy(),
			with(send(), func(r *Request) {
				r.Stage = StageRecv
				r.Action = ActionSee
				r.TargetDeclaration = Declaration{HiddenFromDirectory: true}
			}), ReasonEndpointHidden},
		{"declaration: a paused endpoint can still be asked for its status", DefaultPolicy(),
			with(send(), func(r *Request) {
				r.Action = ActionEndpointStatus
				r.TargetDeclaration = Declaration{Paused: true}
			}), ReasonGranted},
		{"declaration: refusing multi-recipient rejects a multi-recipient message", DefaultPolicy(),
			with(send(), func(r *Request) {
				r.MultiRecipient = true
				r.TargetDeclaration = Declaration{RefuseMultiRecipient: true}
			}), ReasonEndpointRefusesMultiRecipient},
		{"declaration: refusing multi-recipient accepts a single-recipient message", DefaultPolicy(),
			with(send(), func(r *Request) { r.TargetDeclaration = Declaration{RefuseMultiRecipient: true} }), ReasonGranted},
		{"declaration: a listed sender is accepted", DefaultPolicy(),
			with(send(), func(r *Request) {
				r.TargetDeclaration = Declaration{RestrictSenders: true, AcceptFrom: []ident.Address{addr(1, 1)}}
			}), ReasonGranted},
		{"declaration: an unlisted sender is rejected", DefaultPolicy(),
			with(send(), func(r *Request) {
				r.TargetDeclaration = Declaration{RestrictSenders: true, AcceptFrom: []ident.Address{addr(1, 2)}}
			}), ReasonSenderNotAccepted},
		{"declaration: restricting senders to nobody rejects everyone", DefaultPolicy(),
			with(send(), func(r *Request) { r.TargetDeclaration = Declaration{RestrictSenders: true} }), ReasonSenderNotAccepted},
		{"declaration: a list without the restriction flag is ignored", DefaultPolicy(),
			with(send(), func(r *Request) { r.TargetDeclaration = Declaration{AcceptFrom: []ident.Address{addr(1, 2)}} }), ReasonGranted},
		{"declaration: hidden endpoint is not visible in the directory", DefaultPolicy(),
			with(send(), func(r *Request) {
				r.Action = ActionSee
				r.TargetDeclaration = Declaration{HiddenFromDirectory: true}
			}), ReasonEndpointHidden},
		{"declaration: hidden endpoint still receives messages", DefaultPolicy(),
			with(send(), func(r *Request) { r.TargetDeclaration = Declaration{HiddenFromDirectory: true} }), ReasonGranted},
		{"declaration: hiding does not hide the node", DefaultPolicy(),
			with(relay(), func(r *Request) {
				r.Action = ActionSee
				r.TargetDeclaration = Declaration{HiddenFromDirectory: true}
			}), ReasonGranted},
		{"declaration: cannot widen a policy that denies", tightened,
			with(send(), func(r *Request) { r.TargetDeclaration = Declaration{} }), ReasonNotAuthorized},
		{"declaration: the policy verdict is reported first to a sender the policy refuses",
			mustPolicy(t, Allow(AnyEndpointOf(node(2)), allEps, ActionMessageSend, Conditions{})),
			with(send(), func(r *Request) { r.TargetDeclaration = Declaration{Paused: true} }), ReasonNotAuthorized},

		// Reply permission.
		{"reply: passes a tightened inbound policy", tightened,
			with(send(), func(r *Request) { r.Reply = true }), ReasonReplyPermission},
		{"reply: the same message without the reply fact is rejected (forged reply_to)", tightened, send(), ReasonNotAuthorized},
		{"reply: explicit deny rules beat the reply permission",
			mustPolicy(t, Deny(AnyEndpointOf(node(1)), allEps, ActionMessageSend, Conditions{})),
			with(send(), func(r *Request) { r.Reply = true }), ReasonExplicitDeny},
		{"reply: a paused endpoint beats the reply permission at intake", tightened,
			with(send(), func(r *Request) { r.Reply = true; r.TargetDeclaration = Declaration{Paused: true} }), ReasonEndpointPaused},
		{"reply: a paused endpoint still takes the reply at recv", tightened,
			with(send(), func(r *Request) {
				r.Reply = true
				r.Stage = StageRecv
				r.TargetDeclaration = Declaration{Paused: true}
			}), ReasonReplyPermission},
		{"reply: lifts the body size cap of a covering allow rule",
			mustPolicy(t, Allow(member, allEps, ActionMessageSend, Conditions{MaxBodyBytes: 100})),
			with(send(), func(r *Request) { r.Reply = true; r.BodySize = 5000 }), ReasonReplyPermission},
		{"reply: lifts an accept-only-from list", tightened,
			with(send(), func(r *Request) {
				r.Reply = true
				r.TargetDeclaration = Declaration{RestrictSenders: true, AcceptFrom: []ident.Address{addr(9, 9)}}
			}), ReasonReplyPermission},
		{"reply: does not lift a multi-recipient refusal", tightened,
			with(send(), func(r *Request) {
				r.Reply = true
				r.MultiRecipient = true
				r.TargetDeclaration = Declaration{RefuseMultiRecipient: true}
			}), ReasonEndpointRefusesMultiRecipient},
		{"reply: is read for message.send only", tightened,
			with(send(), func(r *Request) {
				r.Reply = true
				r.Action = ActionSessionClose
			}), ReasonNotAuthorized},
		{"reply: a covering allow rule is reported as the grant",
			mustPolicy(t, Allow(member, allEps, ActionMessageSend, Conditions{})),
			with(send(), func(r *Request) { r.Reply = true }), ReasonGranted},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Evaluate(tc.policy, tc.req)
			if got.Reason != tc.want {
				t.Fatalf("reason = %v, want %v", got.Reason, tc.want)
			}
			if got.Outcome != tc.want.Outcome() {
				t.Fatalf("outcome = %v, want %v", got.Outcome, tc.want.Outcome())
			}
		})
	}
}

func TestEvaluateRateObligations(t *testing.T) {
	member := Members()
	allEps := AllLocalEndpoints()
	perMin := func(n uint32) RateCap { return RateCap{Count: n, Window: time.Minute} }
	key := RateKey{Sender: Sender{Node: node(1)}, Target: send().Target, Action: ActionMessageSend}

	tests := []struct {
		name   string
		policy Policy
		req    Request
		want   []RateObligation
	}{
		{"an allow without a cap carries no obligation",
			mustPolicy(t, Allow(member, allEps, ActionMessageSend, Conditions{})), send(), nil},
		{"an allow with a cap carries one obligation keyed by sender node, target and action",
			mustPolicy(t, Allow(member, allEps, ActionMessageSend, Conditions{Rate: perMin(10)})), send(),
			[]RateObligation{{Key: key, Limit: 10, Window: time.Minute}}},
		{"an uncapped rule beats a capped one",
			mustPolicy(t,
				Allow(member, allEps, ActionMessageSend, Conditions{Rate: perMin(10)}),
				Allow(AnyEndpointOf(node(1)), allEps, ActionMessageSend, Conditions{})),
			send(), nil},
		{"of two caps the wider one is the obligation",
			mustPolicy(t,
				Allow(member, allEps, ActionMessageSend, Conditions{Rate: perMin(10)}),
				Allow(AnyEndpointOf(node(1)), allEps, ActionMessageSend, Conditions{Rate: perMin(100)})),
			send(), []RateObligation{{Key: key, Limit: 100, Window: time.Minute}}},
		{"caps in different windows are chosen by sustained rate",
			mustPolicy(t,
				Allow(member, allEps, ActionMessageSend, Conditions{Rate: RateCap{Count: 100, Window: time.Hour}}),
				Allow(AnyEndpointOf(node(1)), allEps, ActionMessageSend, Conditions{Rate: perMin(10)})),
			send(), []RateObligation{{Key: key, Limit: 10, Window: time.Minute}}},
		{"a capped rule that is out of force does not count",
			mustPolicy(t,
				Allow(member, allEps, ActionMessageSend, Conditions{Rate: perMin(10)}),
				Allow(AnyEndpointOf(node(1)), allEps, ActionMessageSend, Conditions{Rate: perMin(100), NotAfter: t0})),
			send(), []RateObligation{{Key: key, Limit: 10, Window: time.Minute}}},
		{"a reply granted without a rule carries no obligation", mustPolicy(t),
			with(send(), func(r *Request) { r.Reply = true }), nil},
		{"reply: carries no rate obligation even under a capped allow",
			mustPolicy(t, Allow(member, allEps, ActionMessageSend, Conditions{Rate: perMin(10)})),
			with(send(), func(r *Request) { r.Reply = true }), nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Evaluate(tc.policy, tc.req)
			if got.Outcome != OutcomeAllow {
				t.Fatalf("outcome = %v (%v), want allow", got.Outcome, got.Reason)
			}
			if !slices.Equal(got.Obligations, tc.want) {
				t.Fatalf("obligations = %+v, want %+v", got.Obligations, tc.want)
			}
		})
	}
}

// The rate key names the sender by what this node can verify. A sending node
// chooses its endpoint identifiers freely, so a cap that is not about one named
// endpoint is counted per node.
func TestEvaluateRateKeys(t *testing.T) {
	member := Members()
	allEps := AllLocalEndpoints()
	capped := Conditions{Rate: RateCap{Count: 10, Window: time.Minute}}
	keyOf := func(t *testing.T, p Policy, r Request) RateKey {
		t.Helper()
		got := Evaluate(p, r)
		if len(got.Obligations) != 1 {
			t.Fatalf("obligations = %+v (%v), want exactly one", got.Obligations, got.Reason)
		}
		return got.Obligations[0].Key
	}
	fromEndpoint := func(n, e byte) Request {
		return with(send(), func(r *Request) { r.Sender = Sender{Node: node(n), Endpoint: ep(e)} })
	}

	t.Run("a node-level cap is keyed by node: two endpoints of one node share a key", func(t *testing.T) {
		for name, p := range map[string]Policy{
			"member":    mustPolicy(t, Allow(member, allEps, ActionMessageSend, capped)),
			"direct":    mustPolicy(t, Allow(DirectTrusted(), allEps, ActionMessageSend, capped)),
			"node rule": mustPolicy(t, Allow(AnyEndpointOf(node(1)), allEps, ActionMessageSend, capped)),
		} {
			a, b := keyOf(t, p, fromEndpoint(1, 1)), keyOf(t, p, fromEndpoint(1, 2))
			if a != b || !a.Sender.Endpoint.IsZero() {
				t.Errorf("%s: keys %+v and %+v, want one key with no endpoint", name, a, b)
			}
		}
	})
	t.Run("an endpoint-subject cap is keyed by that endpoint", func(t *testing.T) {
		p := mustPolicy(t, Allow(EndpointOf(addr(1, 1)), allEps, ActionMessageSend, capped))
		if got, want := keyOf(t, p, fromEndpoint(1, 1)).Sender, (Sender{Node: node(1), Endpoint: ep(1)}); got != want {
			t.Fatalf("key sender = %+v, want %+v", got, want)
		}
	})
	t.Run("two senders under one capped rule get separate keys", func(t *testing.T) {
		p := mustPolicy(t, Allow(member, allEps, ActionMessageSend, capped))
		if a, b := keyOf(t, p, fromEndpoint(1, 1)), keyOf(t, p, fromEndpoint(2, 1)); a == b {
			t.Fatalf("senders on different nodes share the key %+v", a)
		}
	})
	t.Run("equal caps from a node rule and an endpoint rule are keyed by node, whatever the rule order", func(t *testing.T) {
		node1 := Allow(AnyEndpointOf(node(1)), allEps, ActionMessageSend, capped)
		endpoint1 := Allow(EndpointOf(addr(1, 1)), allEps, ActionMessageSend, capped)
		for _, p := range []Policy{mustPolicy(t, node1, endpoint1), mustPolicy(t, endpoint1, node1)} {
			if k := keyOf(t, p, fromEndpoint(1, 1)); !k.Sender.Endpoint.IsZero() {
				t.Errorf("key %+v names an endpoint although a node rule grants the same cap", k)
			}
		}
	})
	t.Run("a wider endpoint-subject cap is keyed by endpoint even beside a narrower node cap", func(t *testing.T) {
		wide := Conditions{Rate: RateCap{Count: 100, Window: time.Minute}}
		p := mustPolicy(t, Allow(member, allEps, ActionMessageSend, capped), Allow(EndpointOf(addr(1, 1)), allEps, ActionMessageSend, wide))
		got := Evaluate(p, fromEndpoint(1, 1)).Obligations
		if len(got) != 1 || got[0].Limit != 100 || got[0].Key.Sender.Endpoint != ep(1) {
			t.Fatalf("obligations = %+v, want the 100/min cap keyed by endpoint 1", got)
		}
	})
}

// Acceptance scenarios that touch the two checks: the same function serves the
// intake and the recv hand-out.
func TestEvaluateTwoChecks(t *testing.T) {
	member := Members()
	allEps := AllLocalEndpoints()
	loose := DefaultPolicy()
	req := send()

	t.Run("policy tightened after a prompt was sent: intake allowed, recv rejected", func(t *testing.T) {
		tight := mustPolicy(t, Allow(member, allEps, ActionMessageSend, Conditions{}), Deny(AnyEndpointOf(node(1)), allEps, ActionMessageSend, Conditions{}))
		if got := Evaluate(loose, req); got.Outcome != OutcomeAllow {
			t.Fatalf("intake = %v, want allow", got.Reason)
		}
		if got := Evaluate(tight, req); got.Outcome != OutcomeReject || got.Reason != ReasonExplicitDeny {
			t.Fatalf("recv = %v/%v, want reject/explicit_deny", got.Outcome, got.Reason)
		}
	})
	t.Run("allow removed after intake: recv rejected", func(t *testing.T) {
		if got := Evaluate(mustPolicy(t), req); got.Reason != ReasonNotAuthorized {
			t.Fatalf("recv = %v, want not_authorized", got.Reason)
		}
	})
	t.Run("window of the allow ends between the checks: recv rejected", func(t *testing.T) {
		p := mustPolicy(t, Allow(member, allEps, ActionMessageSend, Conditions{NotAfter: t0.Add(time.Minute)}))
		if got := Evaluate(p, req); got.Outcome != OutcomeAllow {
			t.Fatalf("intake = %v", got.Reason)
		}
		later := with(req, func(r *Request) { r.Now = t0.Add(time.Minute) })
		if got := Evaluate(p, later); got.Outcome != OutcomeReject {
			t.Fatalf("recv = %v, want reject", got.Outcome)
		}
	})
	t.Run("trust suspended between the checks: recv suspends, not rejects", func(t *testing.T) {
		susp := with(req, func(r *Request) { r.Trust = suspended; r.Stage = StageRecv })
		if got := Evaluate(loose, susp); got.Outcome != OutcomeSuspend {
			t.Fatalf("recv = %v, want suspend", got.Outcome)
		}
	})
	t.Run("policy tightened while trust is suspended: recv rejects", func(t *testing.T) {
		tight := mustPolicy(t, Allow(member, allEps, ActionMessageSend, Conditions{}), Deny(AnyEndpointOf(node(1)), allEps, ActionMessageSend, Conditions{}))
		susp := with(req, func(r *Request) { r.Trust = suspended; r.Stage = StageRecv })
		if got := Evaluate(tight, susp); got.Outcome != OutcomeReject || got.Reason != ReasonExplicitDeny {
			t.Fatalf("recv = %v/%v, want reject/explicit_deny", got.Outcome, got.Reason)
		}
	})
	t.Run("endpoint paused after intake: recv still hands the message out", func(t *testing.T) {
		paused := Declaration{Paused: true}
		if got := Evaluate(loose, with(req, func(r *Request) { r.TargetDeclaration = paused })); got.Reason != ReasonEndpointPaused {
			t.Fatalf("intake = %v, want endpoint_paused", got.Reason)
		}
		if got := Evaluate(loose, with(req, func(r *Request) { r.Stage = StageRecv; r.TargetDeclaration = paused })); got.Outcome != OutcomeAllow {
			t.Fatalf("recv = %v, want allow", got.Reason)
		}
	})
	t.Run("trust revoked between the checks: recv rejects", func(t *testing.T) {
		den := with(req, func(r *Request) { r.Trust = trust.Denied })
		if got := Evaluate(loose, den); got.Outcome != OutcomeReject {
			t.Fatalf("recv = %v, want reject", got.Outcome)
		}
	})
	t.Run("trust restored: the same request is allowed again", func(t *testing.T) {
		if got := Evaluate(loose, req); got.Outcome != OutcomeAllow {
			t.Fatalf("got %v", got.Reason)
		}
	})
}

// Default configuration: members exchange messages and control signals.
func TestDefaultPolicyGrantsEveryActionOnEveryObject(t *testing.T) {
	p := DefaultPolicy()
	for _, a := range Actions {
		for _, k := range []TargetKind{TargetEndpoint, TargetRuntime, TargetNode} {
			if !a.appliesTo(k) {
				continue
			}
			for _, tr := range []trust.Outcome{allowed, inherited} {
				r := Request{Now: t0, Trust: tr, Stage: StageIntake, Sender: Sender{Node: node(3)}, Action: a, Target: Target{Kind: k}, Preset: "any"}
				if k != TargetNode {
					r.Target.Endpoint = ep(5)
				}
				if got := Evaluate(p, r); got.Reason != ReasonGranted {
					t.Errorf("%v on %v (trust=%v): %v", a, k, tr, got.Reason)
				}
			}
		}
	}
}

func TestReasonOutcomePairing(t *testing.T) {
	for r := ReasonGranted; r <= ReasonEndpointHidden; r++ {
		if r.Outcome() == 0 {
			t.Errorf("reason %d (%v) has no outcome", r, r)
		}
		if r.String() == "invalid" {
			t.Errorf("reason %d has no name", r)
		}
	}
	if Reason(0).Outcome() != 0 || Reason(200).Outcome() != 0 {
		t.Error("unknown reasons must have no outcome")
	}
}

func TestPolicyDoesNotAliasInput(t *testing.T) {
	presets := []string{"a"}
	rules := []Rule{Allow(Members(), AllLocalEndpoints(), ActionSessionCreate, Conditions{Presets: presets})}
	p := mustPolicy(t, rules...)
	presets[0] = "b"
	rules[0].Effect = EffectDeny
	if got := Evaluate(p, create("a")); got.Reason != ReasonGranted {
		t.Fatalf("policy changed with its input: %v", got.Reason)
	}
	out := p.Rules()
	out[0].Cond.Presets[0] = "c"
	if got := Evaluate(p, create("a")); got.Reason != ReasonGranted {
		t.Fatalf("policy changed through Rules(): %v", got.Reason)
	}
}
