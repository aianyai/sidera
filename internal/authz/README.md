# authz

The node-local access policy: whether this node accepts a request from another
node. Read [../README.md](../README.md) first for the conventions every contract
package follows, and [docs/references/policy](../../docs/references/policy/README.md)
for the reasons behind the rules.

## Entry points

| Name                                 | Purpose                                                                                   |
| ------------------------------------ | ----------------------------------------------------------------------------------------- |
| `Evaluate(Policy, Request)`          | Decides one request: `Verdict{Outcome, Reason, Obligations}`.                             |
| `NewPolicy([]Rule)`                  | Validates a rule list and returns a `Policy`; malformed rules come back as `*RuleError`s. |
| `OnlyFrom(object, action, nodes...)` | Builds a restriction: the object accepts the action only from these nodes.                |
| `Rule.Problems()`                    | Lists every way one rule is malformed, as typed `Problem` values.                         |
| `DefaultPolicy()`                    | The policy of a new node: members may do everything.                                      |
| `Classify(old, new Policy)`          | `Unchanged`, `Tighten` or `Loosen`: how a policy change moves what the node permits.      |
| `TrafficKind.PolicyExempt()`         | Whether endpoint policy leaves a kind of traffic alone.                                   |

The same `Evaluate` serves both checks: at intake, and again when `recv` hands
out the body. The caller passes the current policy, trust and time each time, and
says which check it is making in `Request.Stage` (`StageIntake` or `StageRecv`);
the zero value is malformed, so a caller cannot forget.

## Model

A rule is one of three kinds: an allow or a deny (subject x object x action ->
allow or deny, with optional conditions), or a restriction (object x action x a
set of nodes).

- Subjects: every cluster member (direct or inherited), every directly trusted
  node, any endpoint of one node, one endpoint of one node.
- Objects: one local endpoint, all local endpoints (runtime endpoints
  included), one runtime endpoint, the node itself. An endpoint object and a
  runtime object both match by endpoint identifier, whatever the kind of the
  target: identifiers are unique on a node, and a deny that named the wrong kind
  must never silently match nothing. They differ only in the actions a rule may
  pair them with: an endpoint object takes the actions of an endpoint or a
  runtime endpoint, a runtime object those of a runtime endpoint.
- Actions: `see`, `message.send`, `session.create`, `session.close`,
  `session.interrupt`, `endpoint.status`, `relay.reserve`, `relay.connect`. Each
  action applies to certain object kinds only (`message.send` to endpoints,
  `session.create` to runtime endpoints, `relay.*` to the node); a rule that
  pairs an action with an object it cannot apply to is malformed.
- Conditions: validity window `[NotBefore, NotAfter)`, rate cap, body size cap
  (`message.send` only), and a preset set (`session.create` only). Deny rules
  accept only a window and a preset set. Restrictions accept none.
- A request is allowed when some allow rule covers it with all conditions
  holding, no deny rule covers it and no restriction excludes the sender. Deny
  wins whatever the order. With no matching allow rule the request is refused: the
  policy is closed-world, and the empty policy allows nothing.
- A restriction `OnlyFrom(object, action, nodes...)` refuses senders whose node is
  not in its set with `ReasonNotAuthorized`. It grants nothing. Several
  restrictions on one object and action all apply, so the sender must be in every
  set. Unlike a deny, a verified reply passes it (see Reply permission): an
  allow-list is how an endpoint is narrowed to a few nodes while the answers to its
  own messages keep arriving, which "deny everyone else" could not express because
  a deny stops replies too. A restriction needs a non-empty set of distinct,
  non-zero node identifiers, an action its object can take, and no subject or
  conditions.
- When several allow rules grant the same request, the one with the highest rate
  cap by the selection order counts (see Rate caps), because rules are a union of
  grants.

## Evaluation order

1. Connection trust, a `trust.Outcome` given by the caller. `trust.Denied` gives
   `Reject`; it is the zero value, so an unset field fails closed. A value
   outside the four outcomes is `Reject` (malformed). `AllowedDirect`,
   `AllowedInherited` and `Suspended` go on.
2. Request shape, including the stage. A malformed request is `Reject`.
3. Explicit deny rules give `Reject(ExplicitDeny)`.
4. Restrictions and allow rules. If no allow rule grants the request, or a
   restriction excludes the sender, a verified reply may stand in for them
   (message.send only); otherwise `Reject` with the most specific reason: excluded
   by a restriction (not authorized), preset not allowed, body too large, or not
   authorized.
5. The target endpoint's own declarations (paused at intake, refuses
   multi-recipient, accepts only listed senders, hidden from directory). They can
   only turn an allow into a reject.
6. If the result is an allow and trust is suspended, the verdict is `Suspend`
   with no obligations.

Suspend is never terminal. At intake the sender keeps retrying. At `recv` the
message stays queued, neither handed out nor prompted for. A suspended sender is
treated as an inherited member whatever `Direct` says. Suspend replaces only an
allow: a request the policy refuses is refused whether trust is allowed or
suspended, for the same reason, so a tightened policy or a lost grant ends a
delivery even while a guarantee has lapsed instead of leaving it queued until trust
returns. Only denied trust, an explicit deny, a missing grant, a restriction or a
violated condition ends a delivery as rejected.

## Rate caps

The package does not count. An allow whose granting rule carries a rate cap
returns one `RateObligation{Key, Limit, Window}`: the caller's limiter admits at
most `Limit` requests under `Key` in any span of `Window`. Body size caps are
decided here, directly from `Request.BodySize`.

Obligations are honoured at the intake check only. At the `recv` check the caller
ignores them, because the message was counted at intake. A verified reply carries
no obligation at all: it is an answer bounded by the reply window and the node's
resource limits, so a sender trusted with a capped allow is not treated worse than
one with no allow.

The key is the sender, the target and the action. The sender part is the sending
node, plus its endpoint only when the cap comes from a rule that names that
endpoint. Endpoint identifiers are chosen by the sending node and this node cannot
verify them, so a cap counted per claimed endpoint would let a node escape it by
inventing identifiers. When equal caps come from a node-level and an endpoint-level
rule the key stays per node, whatever the order of the rules.

Several allow rules can grant one request. `rateCmp` picks the cap that counts: a
higher sustained rate (`Count/Window`) ranks above, at equal sustained rate the
larger `Count`, and no cap above every cap. That is a total order, so the choice
is deterministic; it is a selection order only and does not say which cap is wider.
Whether one cap is within another is `rateWithin`: with `k = ceil(b.Window /
a.Window)`, cap a is within cap b when `a.Count*k <= b.Count`. Any span of
`b.Window` is covered by `k` spans of `a.Window`, and bursts one `a.Window` apart
reach that bound, so the test is exact. Two caps that neither contains are
incomparable.

## Reply permission

`Request.Reply` is the caller's verified fact that the message is a reply inside
an outstanding reply permission. The caller has checked the evidence against its
own send records; this package does not. The fact is read for `message.send`
only. It replaces the missing allow rule and lifts both a restriction and an
accept-only-from list. Only a deny stops it, besides the endpoint's paused
declaration at intake, a multi-recipient refusal and connection trust. A reply
admitted without an allow rule has no size cap, because the caps belong to rules
that did not grant it, and a reply never carries a rate obligation.

## Endpoint self-declarations

`Request.TargetDeclaration` is looked up by the caller. Paused, multi-recipient
refusal and the sender list apply to `message.send`. Pausing stops new intake: it
is applied at `StageIntake` only, because at `StageRecv` the endpoint is pulling
the messages it already holds. The other declarations apply at both stages.
Hidden-from-directory applies to `see` on an endpoint and nothing else: a hidden
endpoint can still be messaged. A declaration never turns a reject into an allow.

## Exempt traffic

Channel status reports, control replies, system-endpoint feedback and trust
maintenance are not subject to endpoint policy, only to resource limits.
`TrafficKind.PolicyExempt` classifies a kind. The caller still checks each kind's
own preconditions (a status report must match an outbound record and come from
the original target node, and so on) and does not call `Evaluate` for exempt
traffic.

## Classifying a change

`Classify(old, new)` is `Loosen` when some request is allowed by `new` and not by
`old`; when both allow it but the rate cap in `new` is not within the cap in `old`
(incomparable counts as not within, because the gate must never guess in favour of
the change); or when `new` admits a verified reply that `old` stopped. A mixed
change is `Loosen`. It is `Tighten` when nothing is loosened and something is
narrowed, and `Unchanged` when every request gets the same answer, however the
rules are written or ordered.

It is exact for this closed syntax. Rules only test a request against fixed
values (node and endpoint identifiers, window bounds, size caps, preset
names), so `Classify` builds one representative request for each combination of
equivalence classes of those values and compares the two policies on each with
the evaluator's own rule logic. Every identifier a rule mentions is probed as both
an endpoint and a runtime, and every node in a restriction's set counts as a
sender. The cost grows with the product of the numbers of distinct senders,
targets and condition values in the two rule lists, which is small for a
node-local policy. For `message.send` the comparison is made twice per probe:
once for the ordinary decision, and once for the verified-reply decision, which
only a deny stops. `Classify` and `Evaluate` read the rule list through the same
`decide`, `permits` and `admitsReply`, so they are not two implementations of one
rule.

Restoring an endpoint from "only specific nodes" to "whole cluster", removing a
deny rule or a restriction, growing a restriction's node set and widening the
preset set are `Loosen`; the reverse changes are `Tighten`. A deny over senders no
allow covers still tightens, because it stops their replies. The caller maps the
class to the privilege needed to apply it (only the owner or a person with full
control may loosen).

## What the caller must guarantee

- Connection trust is the trust package's verdict (`trust.Evaluate`) and is
  current. `Request.Trust` is a `trust.Outcome`: `AllowedDirect` is read as a
  direct sender, `AllowedInherited` and `Suspended` as an inherited one.
- `Stage` says which check is being made.
- `Reply` is true only after the reply evidence was verified.
- `Now` is the caller's clock reading in UTC; the same request at the same time
  gets the same verdict on every node.
- `TargetDeclaration` is the target endpoint's current declaration.
- Rate obligations are enforced by a limiter elsewhere, at the intake check only,
  and ignored at the `recv` check.
- The endpoint, runtime and preset named in a request exist; this package judges
  permission only.

## Not here

The on-disk rule format, the CLI that edits rules, who may apply which change,
and the counting of rate caps. Storage and the privilege check use the types and
`Classify` defined here.
