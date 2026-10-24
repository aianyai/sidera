# Delivery

How one message delivery behaves from the moment the sender's node records it
to the moment it is delivered, refused, or given up on, and what the sender is
told along the way. A delivery is one message to one recipient endpoint; a
message with several recipients has several deliveries that share a message id.
Control signals use the same states, the same deduplication and the same
feedback, including the wording of each notice (the delivery_unknown wording
covers both a message and a control signal); on the target they differ only in how a delivery ends (see Control
signals on the target). This document describes the committed code of `internal/delivery`.

## What

### States

| State | Meaning | Final |
| ----- | ------- | ----- |
| `queued` | The sender's node has recorded the message. Sender view only. | no |
| `accepted` | The target authorized, deduplicated and stored it. On the target this covers waiting in the queue and being leased. | no |
| `delivered` | The whole body reached the agent and was acknowledged; for a control signal, its reply was committed. | yes |
| `rejected` | The target refused it, with a reason (unauthorized, endpoint missing or closed, endpoint declined, too large, expired, backlog expired, second authorization check failed, malformed, deadline out of range). On the target only for a body that was never handed out. | yes |
| `expired_unconfirmed` | The deadline passed and an active query found no receipt. The target may have the message. | no, correctable |
| `delivery_unknown` | The body was handed out and whether the agent saw it cannot be told; for a control signal, it was accepted for execution and its reply was never committed. The target produces it only for a delivery that was handed over: a body handed out at least once, or any accepted control signal. | no, correctable |
| `closed_unconfirmed` | `report_by` passed while the delivery was open. | yes, silent |

`notified` is a target-internal sub-state of `accepted`: a prompt was handed to
the session. It changes nothing outside the target and never produces feedback.

Legal moves: from `queued` to any other state; from `expired_unconfirmed` to
`accepted`, `delivered`, `rejected`, `delivery_unknown`, `closed_unconfirmed`;
from `accepted` to `delivered`, `rejected`, `delivery_unknown`,
`closed_unconfirmed`; from `delivery_unknown` to `delivered` and
`closed_unconfirmed`. Nothing leaves a final state. On the target, `delivered`
is reached only through the acknowledgement of every chunk (a control signal:
through the committed reply) and `delivery_unknown` only for a delivery that was
handed over; a fact that asks for either otherwise is an error. A fact that describes an
earlier stage than the record shows is ignored; a fact that contradicts a final
state is an error.

### Feedback

| Kind | When | Notes |
| ---- | ---- | ----- |
| unreachable | Contact with the target failed for 2 minutes while the sender still retries. | Cause: network or trust chain suspended. Queued deliveries only. |
| delivery blocked | The caller reports the message cannot reach the session. | Not a state; the message stays queued. Cause: adapter offline, runtime authentication, native service. |
| blocked recovered | The cause ended, or the message was delivered while still marked blocked. | Only if the block was reported. |
| expired_unconfirmed | Deadline plus tolerance passed and a later active query found no receipt. | Wording: the target may have received the message. |
| expired corrected | An expired delivery turned out accepted or delivered. | |
| rejected | The target refused. | Carries the reason. |
| delivery unknown | Lease redelivery exhausted, leases unsettled at the settle deadline, or a delivery whose body was handed out closed by force (backlog deadline, denied second check, closed endpoint); for a control signal, no committed reply by the settle deadline or a forced closure. | Wording: the target handed it over (to the agent, or to the adapter that executes a control signal), whether it was seen or executed is unknown, and the sender must not assume it was not delivered. |
| unknown corrected | A late acknowledgement completed an unknown delivery. | |

Rules: normal progress and `closed_unconfirmed` give nothing; each kind is given
at most once per delivery; moving from expired to rejected or unknown gives the
new news instead of a correction. Merging: one notice per message for its
anomalies, per (sender endpoint, target node) for unreachable hints and, as a
notice of their own, for expired_unconfirmed notices, per (sender endpoint,
target endpoint) for blocked reports with the recovery as its own notice. The merge window and the per-endpoint rate cap are undecided
parameters without defaults; notices over the cap are held, not dropped.

### Deadlines and retention

| Name | Value |
| ---- | ----- |
| `accept_before` | default 10 min, at most 1 h ahead (plus clock tolerance) |
| unreachable hint | 2 min of failed contact |
| endpoint queue time | default 30 min, protocol cap 2 h |
| lease | 5 min; total life at most 15 min; at most 3 redeliveries (4 leases) |
| safety margin | 5 min |
| clock tolerance | 2 min, `timing.ClockTolerance` (see [timing](../timing/README.md)) |
| `report_by` | `accept_before` + 2 h + 4 x 15 min + 2 min |

The redelivery period is the lease count times the lease life cap and depends on
nothing else. Dedup records are kept until `report_by`; status reports for the
longer of until `report_by` and a full `report_by` window from their creation;
the sender's idempotency keys as long as the dedup records.

Target guarantee: by `report_by - margin` every delivery is final or reported as
`delivery_unknown`. A queued message becomes `rejected` (backlog expired) at the
earlier of the endpoint's queue time from acceptance and `report_by - margin`,
provided its body was never handed out; one that was handed out and requeued
becomes `delivery_unknown` at that instant. A lease still open at
`report_by - margin` becomes `delivery_unknown`. The same holds for any forced
closure once a body was handed out: a denied second check or a closed endpoint
gives `delivery_unknown`, never `rejected`. Sender
side: expired is judged only after the deadline plus tolerance and a query made
after that without a receipt; open deliveries close after `report_by`.

Intake of a new request: a deadline already past is rejected as expired; one
beyond now plus cap plus tolerance is rejected as out of range; denied
authorization is rejected with the reason the denial carries (a denial without
one is invalid input); suspended authorization is a non-final result and the
sender keeps retrying until the deadline. `IntakeAuthorization` is the single
place where the verdict of the [policy](../policy/README.md) becomes that
authorization:

| Policy verdict | Authorization |
| -------------- | ------------- |
| allow (granted, reply permission) | allow |
| suspend (trust suspended) | suspend |
| reject: trust denied, explicit deny, not authorized, preset not allowed | deny, `unauthorized` |
| reject: body too large | deny, `too_large` |
| reject: endpoint paused, refuses several recipients, sender not accepted | deny, `endpoint_declined` |
| reject: malformed request | deny, `malformed` |
| reject: endpoint hidden | deny, `endpoint_missing` |
| reject: any reason not listed | deny, `unauthorized` |
| an outcome that is none of the three | the zero authorization, refused by `Intake` and `Handout` as invalid input |

The mapping is used only at intake. At `recv`, `Handout` takes only the kind
(`IntakeAuthorization(v).Kind`), and a refusal there is always reported as
`second_check_failed`, so no specific rule is revealed.

A hidden endpoint is reported as not existing, so the sender cannot tell it from
an endpoint that is absent, and all the rules that refuse without a declaration
by the endpoint are reported alike as `unauthorized`, so the sender does not
learn which rule refused it.

### Leases, acknowledgements, chunks

`recv` leases a message for 5 minutes. A lease is renewed only while the
endpoint has an active turn and never lives beyond 15 minutes in total. An
expired lease returns the message to the queue flagged as a possible duplicate;
after the fourth lease expires unacknowledged the delivery is `delivery_unknown`.
Each page `recv` prints carries a `LeaseID`, 16 random bytes shown as 32
lowercase hex characters, generated by the caller once per `recv` from an
injected random source and passed to every hand-out on that page. A repeated
`recv` by the same endpoint first gets back the messages it holds a live lease
on, flagged as possible duplicates, under the same lease and a new page
identifier. A chunk is flagged as a possible duplicate when it was handed out
before, not merely because another chunk of the message was; asking before the
hand-out (`PossibleDuplicate(part)`) and the hand-out itself give the same
answer. An acknowledgement needs the owning endpoint's token and names a page
identifier under which some chunk of that message was handed out; one that
names none (never used, or unset) is refused and changes nothing. It stays valid
after the lease expired, even after a redelivery issued a newer lease, and
corrects `delivery_unknown`. A message handed out as n chunks is
`delivered` only when all n are acknowledged; only a chunk that was handed out
can be acknowledged, and the first chunk not yet acknowledged is the next one
to hand out. At `recv`, suspended authorization skips the message (no lease, no
prompt, no feedback); denied authorization closes it: `rejected` if the body
was never handed out, `delivery_unknown` if it was.

### Control signals on the target

A control signal is executed by the runtime's adapter, not handed to an agent by
`recv`, and its reply is committed together with the execution result. Its
target record is built by `NewTargetControlRecord(acceptBefore, reportBy)` and
differs from a message record in four ways:

- It is never queued for `recv`: `Handout` and `Ack` refuse it (`ErrWrongSide`),
  `Renew` finds no lease, a prompt (`EventNotified`) and a block
  (`EventBlocked`) are refused, and it has no backlog deadline, so it is never
  `rejected` as backlog expired. Blocked is defined as prompts persistently
  failing to reach a session; a control signal has neither, and an adapter that
  is offline reaches the sender through the control reply instead.
- `EventReplyCommitted` moves it from `accepted` (or from `delivery_unknown`,
  as a correction) to `delivered`. Any other record, source or target message
  record, refuses the event with `ErrWrongSide`.
- Accepting it counts as handing it over, because the adapter may execute it at
  once. A control record not final at `report_by - margin` therefore becomes
  `delivery_unknown` in `Tick`, and a forced closure (`EventRejected`) also
  gives `delivery_unknown`, never `rejected`. After `report_by` it closes like
  any other record.
- `StatusReport` needs no new rule: it reports `accepted` (never blocked), then
  `delivered` once the reply is committed, or `delivery_unknown` after
  settlement.

The sender applies the signed reply with the existing `EventDelivered` (or
`ApplyStatusReport` of a `delivered` report); nothing on the source side
changes.

### Status reports

A target reports its state, the rejection reason, and whether the accepted
message is blocked. The sender applies the report in one place: the state
first, then, if the record is accepted, the block or its end. So a block
reported before the receipt was pulled is still reported, and an expired
delivery that turns out accepted and blocked gives both the correction and the
blocked notice.

### Deduplication

A request is identified by (sender node, message id, recipient endpoint) and a
body digest. The same key and digest returns the original record; the same key
with another digest is a conflict. A control signal's reply is stored once in
the dedup record and returned on every repeat.

## Why

- **Silence has to mean delivered.** The sender is told only about anomalies, so
  every path that can end without the sender knowing must be bounded: the target
  settles before `report_by - margin`, the sender keeps pulling until
  `report_by`, and only then closes silently. Without the margin a result created
  at the last instant could not be pulled in time.
- **Expiry says "may have received".** A lost receipt is indistinguishable from
  a lost message. Wording that implied non-delivery would make agents resend, and
  a resend is a new message, so the recipient could get the content twice.
- **The tolerance and the query before expiry.** A target whose clock runs
  behind may still accept the message; judging expiry only after deadline plus
  tolerance, and only after an active query, keeps a slow clock from producing a
  false expiry.
- **`report_by` depends only on caps.** The sender writes it before knowing the
  target's settings, and every target must reach the same value. The redelivery
  period therefore uses the lease life cap and the lease count, not what a
  particular lease did. The lease cap is a total life from issue, so four leases
  cannot exceed the period counted in `report_by`.
- **Backlog expiry is clamped to the settle deadline.** An endpoint owner may set
  a queue time the sender's `report_by` cannot cover; rejecting at the settle
  deadline keeps the guarantee, and the sender still hears before it stops asking.
- **One delivery_unknown wording for messages and control signals.** The
  sender's record cannot tell them apart, and the rule that the sender must not
  assume non-delivery has to stay in one place. The wording names both
  hand-overs, to the agent and to the adapter, instead of the agent alone, which
  would be wrong for a control signal.
- **Corrections are separate kinds.** A delivery can be wrong twice (expired,
  then unknown); a single "correction" kind would silence the second.
- **Replacement, not correction, when news changes.** Telling a sender "the
  earlier notice was wrong" and then "rejected" is two notices for one fact.
- **Notices over the rate cap are held, not dropped.** Dropping an anomaly would
  make silence mean "possibly failed", defeating the rule above.
- **Authorization suspended is not a refusal.** A node of the trust chain being
  offline says nothing about the message; refusing would punish the sender for an
  outage. The message waits in the queue where the queue time bounds the wait.
- **Late acknowledgements count.** The agent saw the chunk; rejecting the
  acknowledgement because the lease timed out would redeliver a message the agent
  already acted on.
- **Once a body was handed out, a forced closure is `delivery_unknown`.** The
  agent may have seen the body, and `rejected` would tell the sender it was not
  delivered, so it would resend and the recipient could get the content twice.
  Whether the body was handed out, not whether a prompt was shown, decides: a
  prompt carries no body. One rule closes a delivery by force, so the backlog
  deadline, a denied second check and a closed endpoint cannot disagree.
- **Delivered only through acknowledgement.** A target record that could be
  marked delivered by an event would let a caller skip the chunk bookkeeping and
  report a body the agent never fully received. For the same reason
  `delivery_unknown` needs a hand-out and an acknowledgement needs a chunk that
  was handed out.
- **The rejection reason is carried by the denial, from one mapping.** The policy
  knows why it denied; collapsing every denial to one reason would hide from the
  sender whether to retry, fix its request or give up. The mapping from policy
  reason to rejection reason lives in `IntakeAuthorization` alone, so no caller
  of intake can tell a sender a reason of its own. At `recv` a denial is always
  `second_check_failed`, so the sender learns that the check failed and not
  which rule refused it.
- **One rule for applying a report.** If every caller turned the report into
  events itself, one of them would apply the receipt and forget the block, and a
  block reported while the receipt was still unknown would never be told.
- **Chunk acknowledgements persist across leases.** Redelivery then asks only
  for what is missing.
- **Tokens.** Only the endpoint a message is for may settle its lease, so another
  session sharing a project directory cannot mark messages delivered.
- **An acknowledgement names a page the message was handed out on, not only the
  latest.** Matching the latest alone would refuse the acknowledgement of an
  agent that read a page whose lease expired and was replaced by a redelivery;
  the agent did see the chunk, so the message would be handed out a third time.
  The record keeps every page identifier under which some chunk was handed out
  and accepts any of them.
- **One identifier per page, generated by the caller.** One `recv` hands out a
  page, possibly of several messages, under one lease, and the page prints one
  identifier. An identifier made per message would leave all but one of them
  unknown to the agent, whose acknowledgement of the others would be refused
  and the messages handed out again. The caller therefore generates it once per
  `recv`, and the record, which sees one message, only records it.
- **Possibly duplicate is per chunk.** A chunk handed out for the first time
  has not been seen, even when an earlier chunk of the same message was handed
  out and its lease has since run out. A flag on the whole message would tell
  the agent that unseen text may be a repeat.
- **Lease IDs are random, not counters.** An ID that could be guessed would let an
  acknowledgement name a page it was never given; 16 random bytes cannot be
  guessed, and the randomness is injected so tests and replays stay
  deterministic.
- **Dedup keys include the recipient.** One message to several endpoints on one
  node is several deliveries, each with its own state.
- **Retention equals `report_by`.** A retry after `report_by` cannot reach the
  target within its deadline and would be refused as expired, so remembering the
  request longer has no effect on the answer.
- **One state machine for both sides.** Sender and target share the states and
  the transition table so they cannot disagree about what a move means; the
  record carries its side because only the sender produces feedback and only the
  target hands out bodies.
- **A control signal is delivered by its reply, not by an acknowledgement.** The
  adapter executes it and the reply is committed with the execution result in
  one transaction, so "reply committed" is the one fact that says the signal
  reached its end. Without it the target-side record could only stay `accepted`,
  and at its backlog deadline would be turned into `rejected(backlog_expired)`,
  telling the sender that a signal the adapter may have executed was refused.
  Giving it a constructor of its own, instead of a flag on queued messages, keeps
  the special case in one place: the record carries the `Control` flag, the
  entry points that only make sense for a queued message (`Handout`, `Ack`,
  `EventNotified`, `EventBlocked`) refuse a flagged record, and the lease and
  backlog rules in `Tick` are guarded by one predicate (`hasQueue`, a target
  record that is not a control record), so none of them needs a control case of
  its own.
- **An accepted control signal is already handed over.** `delivery_unknown`
  exists so that the target never claims "not delivered" for something the other
  party may have seen. For a message that point is the first hand-out; for a
  control signal the execution may begin at acceptance and the record cannot
  tell whether it did, so the point is acceptance. Settling an unreplied
  control record as `delivery_unknown` keeps the guarantee that the sender hears
  of every delivery before it stops asking; leaving it to close silently would
  tell the sender nothing about a signal that may have been executed. A reply
  committed late still corrects it.
- **No scheduler.** A timer inside the package would need a clock; contract
  packages read none, so callers run `Tick` when an instant may have passed.

## Where

- `internal/delivery/state.go`: states, sub-state, reject reasons, causes,
  the transition table. Tests: `TestCanTransition`, `TestStateClasses`.
- `internal/delivery/feedback.go`: kinds, wording, `TransitionFeedback`. Tests:
  `TestTransitionFeedback`, `TestSummaries`.
- `internal/delivery/record.go`: `Record`, `NewTargetControlRecord`, `Apply`,
  `Tick`, `closeForcibly`, `StatusReport`, `ApplyStatusReport`. Tests:
  `TestSourceTransitions`, `TestRejectionReasonIsCarried`,
  `TestUnreachableHint`, `TestDeliveryBlocked`,
  `TestTargetRecordProducesNoFeedback`, `TestTargetDeliveryUnknownNeedsHandout`,
  `TestSideRules` (including that a source record has no queue),
  `TestNotifiedSubState`.
- Control records (`record.go`, tests in `internal/delivery/control_test.go`):
  `TestNewTargetControlRecord`, `TestControlRecordIsNeverHandedOut` (also
  prompts and blocks refused), `TestReplyCommitted`,
  `TestReplyCommittedAppliesToControlRecordsOnly`,
  `TestControlRecordSettlement`, `TestControlStatusReport`,
  `TestScenarioControlSignalLifecycle`,
  `TestPropertyControlSignalsAreNeverQueuedAndAlwaysEnd` (state and the
  feedback the sender is told, against an independent model).
- `internal/delivery/timing.go`: constants, `ReportBy`, retention, backlog
  deadline. Tests: `TestReportByDerivation`, `TestDecidedValues`,
  `TestRetention`, `TestValidateEndpointMaxQueue`, `TestBacklogDeadline`.
- `internal/delivery/lease.go`: `Handout`, `Renew`, `Ack`, `NextPart`,
  `SortForRecv`. Tests:
  `TestHandout`, `TestHandoutLeaseTimes`, `TestHandoutInputs`, `TestRenew`,
  `TestLeaseExpiry`, `TestAck`, `TestChunkedAck`, `TestSortForRecv`,
  `TestLeaseIDs`, `TestAckLease`, `TestPossibleDuplicatePerPart`.
- `internal/delivery/leaseid.go`: `LeaseID`, `NewLeaseID`, `ParseLeaseID`.
  Tests: `TestNewLeaseID`, `TestLeaseIDText`, `TestParseLeaseID`.
- `internal/delivery/authorization.go`, `internal/delivery/intake.go`:
  `Authorization`, `IntakeAuthorization`, `Intake`. Tests: `TestIntake`,
  `TestIntakeAuthorization`, `TestIntakeAuthorizationRejectReasons`,
  `TestIntakeAuthorizationMapsEveryReason` (fails when a policy reason has no
  explicit mapping).
- `internal/delivery/dedup.go`: deduplication. Tests: `TestCheckDedup`,
  `TestDedupReply`.
- `internal/delivery/merge.go`: merge keys, `FeedbackQueue`. Tests:
  `TestMergeKey`, `TestFeedbackParamsValidate`, `TestFeedbackQueue`.
- Acceptance scenarios: `internal/delivery/scenario_test.go`
  (`TestScenario*`, among them `TestScenarioForcedClosure` and
  `TestScenarioStatusReportApplication`). The property test over random event
  sequences is `TestPropertyEveryDeliveryClosesAndNoFeedbackRepeats` in
  `property_test.go`: it checks, from the states the source went through, that
  every anomaly was reported, and it fails when a path of the rules (a renewal
  cap, a delivery_unknown, a correction) is reached fewer than 20 times.
