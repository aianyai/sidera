# Policy

How a node decides whether to accept a request from another node: the access
policy, its three outcomes, the reply permission, endpoint self-declarations,
the traffic that is exempt, and the classification of a policy change as
tighter or looser.

## What

**Rules.** A policy is a list of rules. A rule is subject x object x action ->
allow or deny, with optional conditions, or a restriction: object x action x a
set of nodes.

| Dimension  | Values                                                                                                            |
| ---------- | ----------------------------------------------------------------------------------------------------------------- |
| Subject    | every cluster member (direct or inherited); every directly trusted node; any endpoint of one node; one endpoint   |
| Object     | one local endpoint; all local endpoints (runtime endpoints included); one runtime endpoint; the node itself. Endpoint and runtime objects both match by endpoint identifier. |
| Action     | `see`, `message.send`, `session.create`, `session.close`, `session.interrupt`, `endpoint.status`, `relay.reserve`, `relay.connect` |
| Conditions | validity window `[NotBefore, NotAfter)`; rate cap; body size cap; preset set                                     |

- Each action applies to certain object kinds only, and a rule pairing an action
  with an object it cannot apply to is rejected as malformed. Body size caps
  belong to `message.send`, preset sets to `session.create`. Deny rules carry
  only a window and a preset set.
- A request is allowed when an allow rule covers it with every condition
  holding, no deny rule covers it and no restriction excludes the sender. Deny
  wins whatever the order. No matching allow means refusal; the empty policy
  allows nothing.
- A restriction (`OnlyFrom(object, action, nodes...)`) refuses senders whose node
  is not in its set, as not authorized. It grants nothing, carries no subject and
  no conditions, and needs a non-empty set of distinct non-zero node identifiers.
  Several restrictions on one object and action all apply. A verified reply passes
  a restriction but not a deny.
- Endpoint and runtime objects match by endpoint identifier, whatever the kind of
  the target. The only difference is which actions a rule may pair them with.
- The default policy lets every cluster member do every action on every local
  endpoint and on the node itself, with every registered preset.
- A `Policy` can only be built from validated rules, so an evaluator never meets
  a malformed one.

**Three outcomes.** Allow, Suspend, Reject, each with a typed reason.

- The caller supplies the sender's connection trust: allowed, suspended or
  denied. Denied always gives Reject, and an unset state is rejected. For allowed
  and suspended alike the request shape, deny rules, restrictions, allow rules and
  the endpoint's declarations are then applied. A request that comes out as an
  allow is Suspend when trust is suspended; one that comes out as a refusal is
  refused with that reason. A suspended sender counts as an inherited member.
- Suspend is not final. At intake the source keeps retrying until the message
  expires; when `recv` would hand out the body the message stays queued, neither
  handed out nor prompted for.
- Rejected reasons: trust denied, malformed request, explicit deny, not
  authorized, body too large, preset not allowed, endpoint paused, endpoint
  refuses multi-recipient messages, sender not accepted by the endpoint, endpoint
  hidden.
- One function serves both checks, at intake and again when `recv` hands out the
  body, so a tightened policy, a lapsed window or a revoked trust between the two
  turns into a rejection at the second. The request says which check it is
  (`Stage`); an unset stage is malformed.

**Rate caps are obligations.** The package does not count. An allow returns a
rate obligation (key, limit, window) for a stateful limiter, honoured at the
intake check only: at the `recv` check the caller ignores it, because the message
was counted at intake. A verified reply carries none. The key is the sending node,
the target and the action; it includes the sender's endpoint only when the cap
comes from a rule naming that endpoint. When several allow rules grant a request,
the cap that ranks highest by a total selection order (higher sustained rate,
then larger burst, no cap above all) is the one returned, and no obligation if any
granting rule is uncapped. Whether one cap is wider than another is a different
question, answered by containment: cap a is within cap b when
`a.Count * ceil(b.Window / a.Window) <= b.Count`. Body size caps are decided here
from the request's size.

**Reply permission.** The caller verifies the reply evidence and passes the fact.
For `message.send` it stands in for a missing allow rule and lifts both a
restriction and an accept-only-from list. It never overrides an explicit deny, a
paused endpoint at intake, a multi-recipient refusal or connection trust. It
carries no rate obligation and no size cap.

**Endpoint self-declarations.** An endpoint can pause itself, refuse
multi-recipient messages, accept only listed senders, and hide from the
directory. They only narrow. Pausing applies at intake only: at `recv` the
endpoint is reading what it already holds. Hidden affects the `see` action on that
endpoint only; the endpoint stays reachable.

**Exempt traffic.** Status reports, control replies, system-endpoint feedback and
trust maintenance are not subject to endpoint policy. A classification function
tells the caller which kinds.

**Change classification.** `Classify(old, new)` returns Unchanged, Tighten or
Loosen. Loosen if any request, including its time, body size and preset, is
allowed by the new policy and not the old, or allowed by both under a rate cap
that is not within the old one (incomparable counts as not within), or if a
verified reply is admitted by the new policy and was stopped by the old; a mixed
change is Loosen. `Classify` compares both the ordinary decision and the
verified-reply decision, which only a deny stops. Restoring an endpoint from
"only specific nodes" to the whole cluster, removing a deny rule or a
restriction, growing a restriction's node set and widening a preset set are
Loosen; the reverse are Tighten. A deny over senders no allow covers is a
Tighten, because it stops their replies.

## Why

- **A hand-written rule list with a closed condition syntax.** Whether a change
  tightens or loosens must be decidable, because only the owner or a person with
  full control may loosen. Cedar can decide it, but only through a solver that
  exists for Rust, not Go. Casbin's free-form expressions cannot be compared.
  OPA adds about 20 MB for a question this small. Conditions limited to
  intervals, numeric bounds and finite sets are comparable by construction.
- **Deny wins, closed world.** With deny winning, adding a deny can only
  restrict, so "add a deny is a tighten" holds without looking at other rules.
  An unmatched request being refused means the empty policy is the safest one
  and an omitted rule never grants.
- **Union of grants, one cap chosen.** If caps from every covering rule were
  combined, adding a broad capped rule would silently throttle senders that a
  narrower rule serves, and the most specific rule would never be able to give a
  sender more. Treating allow rules as a union keeps a rule's meaning local. The
  obligation is one cap, so a deterministic choice is needed: a total order by
  sustained rate, then burst, with no cap on top.
- **The gate uses containment, selection uses a total order.** The two jobs need
  different things. Choosing among granting rules needs a total order, so the
  result never depends on rule order. Deciding whether a change widens exposure
  needs the truth: a cap with a low sustained rate but a huge window can allow an
  unbounded burst, for example of session creations, each spending the target
  owner's subscription, so ranking it as tighter would let a low-privilege person
  loosen policy. Containment is exact and needs one multiplication (done in 128
  bits, so nothing overflows). Caps that neither contains are incomparable, and
  incomparable is a Loosen.
- **Rate keys use only what the target can verify.** Endpoint identifiers are
  chosen by the sending node. A cap counted per claimed endpoint would let one
  node get a fresh counter per invented identifier, so a cap is counted per sending
  node unless a rule names that endpoint. When equal caps come from both kinds of
  rule, the per-node key wins whatever the rule order, which is also the stricter
  one.
- **Obligations at intake only, none for replies.** A message is counted once, when
  it arrives; counting it again when `recv` hands it out would charge the sender
  twice. A reply is an answer bounded by the reply window and the node's resource
  limits, so a sender trusted with a capped allow is not treated worse than one
  with no allow.
- **Objects match by identifier.** Endpoint identifiers are unique on a node.
  Matching the target kind as well would let a deny that names an endpoint where a
  runtime was meant (or the reverse) silently match nothing, which fails open.
- **An allow-list restriction, not a deny.** Narrowing an endpoint to a few nodes
  cannot be written as a deny, because subjects have no negation and a deny would
  also stop the replies to the endpoint's own messages. A restriction refuses
  other nodes but lets a verified reply through, so a conversation the node started
  keeps working after its inbound policy is narrowed. It grants nothing and takes
  no conditions, which keeps it comparable by the same enumeration.
- **Classify enumerates equivalence classes rather than solving.** The closed
  syntax means requests fall into a finite set of classes (the identifiers,
  window bounds, caps and preset names that appear in the rules, plus "none of
  those"). Checking one representative per class is exact, needs no solver, and
  reuses the evaluator's own decision functions, for the ordinary decision and for
  the verified-reply decision alike. Sampling random requests was rejected because
  it can miss a narrow class, and a miss here would let a low-privilege person
  loosen policy.
- **Denied trust first; suspended only replaces an allow.** A lapsed guarantee
  heals by itself, so a sender must not receive a terminal answer for something that
  may be fine a minute later: an allow is suspended. A refusal, though, would be the
  same once trust returns, and a deny rule or a lost grant must not leave a delivery
  queued behind a lapsed guarantee, so it is given at once. A suspended sender counts
  as inherited, since a lapsed direct tie is not a direct tie.
- **Pause applies at intake only.** Pausing stops new messages. The endpoint
  pulling a message that is already queued wants it, and rejecting it at `recv`
  would end a delivery that was accepted.
- **Reply lifts policy, not trust, deny or pause.** A tightened inbound policy
  would otherwise break every conversation the node started. A reply can only
  come back to an endpoint that wrote first, so it needs no standing grant; but a
  deny rule or a paused endpoint is the owner or the agent saying no on purpose,
  and a sender not trusted at all must stay out regardless.
- **Declarations are checked after the policy.** A sender the policy refuses
  hears "not authorized" and learns nothing about the endpoint's private
  preferences.
- **Pure inputs.** Trust, reply evidence, the declaration and the clock arrive as
  parameters, so two nodes with the same inputs reach the same verdict and tests
  can replay anything.

## Where

| What                                                 | Where                                                            |
| ---------------------------------------------------- | ---------------------------------------------------------------- |
| Rule types, conditions, rate selection order and containment | `internal/authz/rule.go`                                  |
| Policy, validation, default policy                   | `internal/authz/policy.go`                                       |
| Request, verdict, reasons, trust input               | `internal/authz/request.go`                                      |
| Evaluation                                           | `internal/authz/evaluate.go`                                     |
| Change classification                                | `internal/authz/classify.go`                                     |
| Exempt traffic                                       | `internal/authz/traffic.go`                                      |
| Evaluation table, rate obligations and keys, the two checks | `evaluate_test.go`: `TestEvaluate`, `TestEvaluateRateObligations`, `TestEvaluateRateKeys`, `TestEvaluateTwoChecks`, `TestDefaultPolicyGrantsEveryActionOnEveryObject` |
| Rule validation, rate containment and selection, exempt traffic | `policy_test.go`: `TestRuleProblems`, `TestNewPolicyRejectsMalformedRulesWithReasons`, `TestRateWithin`, `TestRateCmp`, `TestPolicyExemptTraffic` |
| Classification table and exhaustive oracle           | `classify_test.go`: `TestClassify`, `TestClassifyAgainstExhaustiveOracle`, `TestClassifyArgumentSwap` |
| Trust, declaration and reply properties              | `property_test.go`                                               |
