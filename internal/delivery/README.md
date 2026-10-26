# delivery

The rules for one message delivery between a sender endpoint and one recipient
endpoint: states and legal transitions, deadlines and retention, the lease and
acknowledgement protocol of the pull interface, deduplication, and the feedback
the sender must get. Read [../README.md](../README.md) first for the
conventions every contract package follows, and
[docs/references/delivery](../../docs/references/delivery/README.md) for the
reasons behind the rules.

## Entry points

| Entry point | Purpose |
| ----------- | ------- |
| `NewSourceRecord(queuedAt, acceptBefore)`, `NewTargetRecord(acceptedAt, ...)` | Create the sender's or the recipient node's `Record` of a delivery, entered into its first state at the given time (`Record.StateAt`). |
| `NewTargetNoticeRecord` | Create the recipient's `Record` of a notice of the system endpoint (system feedback, exempt traffic): marked `Record.Exempt`, so no authorization verdict is made, stored or read for it. |
| `NewTargetControlRecord(acceptedAt, acceptBefore, reportBy)` | Create the target's `Record` of a control signal: never queued for `recv`, no backlog deadline, delivered by `EventReplyCommitted`. |
| `NewRejectedTargetControlRecord(rejectedAt, acceptBefore, reportBy, reason)`, `RejectReason.AtControlIntake()` | Create the target's `Record` of a control signal it refused when it arrived: born `rejected` with an intake reason, final, never handed over. The only way a control record is `rejected`. |
| `Record.Valid()` | Says whether a record is one the constructors and transitions can produce, and names the first invariant it breaks (`*InvalidRecordError`, `ErrInvalidRecord`, `Violation`). For a record restored from storage. |
| `NewLostRecord(discoveredAt)`, `LostReportBy`, `Record.NeedsBody()` | The replacement for the sender's own delivery record lost to storage corruption (a target keeps none); the instant a replacement is kept until; whether a record still needs the bytes of its body. |
| `Record.AwaitsHandout()` | Whether a `recv` would hand something of a target message out: never handed out, put back by a lease that ran out, or handed out in parts of which some have not been. What the prompt rules count as a waiting message. |
| `Record.StateAt`, `Record.DeliveredAt()` | When this node's record entered its current state, and, for a delivered record, when it was delivered. The two times a target puts in a signed status report (`reported_at`, `delivered_at`). |
| `Record.Apply(now, Event)` | Applies one fact or occurrence. Returns the new record and an `Outcome` (applied or ignored with a reason, the transition, and the feedback to give). |
| `Record.Tick(now)` | Everything due by time alone: lease expiry, backlog expiry, the settle deadline, closing after `report_by`. A source delivery that closes while `queued` or `accepted` gives the notice of that; the caller makes its last pull of the target before it ticks (see "Closing at `report_by`"). |
| `Record.NextTick()` | The earliest instant at which `Tick` could change the record; the zero time for a final record. |
| `Record.Handout`, `Renew`, `Ack`, `NextPart`, `PossibleDuplicate`, `SortForRecv` (generic over what holds a record) | The pull lease: hand a chunk of a message out on a page, renew the lease, acknowledge chunks, name the chunk to hand out next, say whether a chunk may be a repeat. |
| `LeaseID`, `NewLeaseID(io.Reader)`, `ParseLeaseID` | A page's identifier: 16 random bytes from an injected reader, all-zero refused, 32 lowercase hex characters as text (`LeaseIDTextLen`). |
| `EventNoRecordAtTarget`, `RecordRetainUntil(reportBy)` | The source fact "the target holds no record of this delivery" (an accepted delivery before `ReportBy` becomes `delivery_unknown`, `lost_at_target`; every other state ignores it), and the instant a target may forget a delivery's record. |
| `KindLostUnknownMessage`, `NewLostUnknownMessageItem(sender, source, at)`, `MergeLostUnknownBySender`, `LastRecordKind` | The feedback of a message nobody can name: its item (no message, no recipient, the stored record that holds it as `Item.Source`), its merge by sender endpoint, and the last kind a record can have reported (the new kind is after it: no delivery produces it; `KindAcceptedUnconfirmed`, the notice of a close while accepted, is the last a record produces). |
| `Record.StatusReport`, `Record.ApplyStatusReport(now, StatusReport)` | What a target reports about a delivery, and the one rule by which the sender applies it. A reported `delivery_unknown` is always a hand-over. |
| `Intake(now, acceptBefore, traffic, Authorization, Backlog)` | Target-side validation of the delivery deadline, the authorization verdict (kind and, for a denial, the reason) and the endpoint's backlog limit, for a request deduplication found new. Exempt traffic carries no verdict: its `Authorization` is the zero value. |
| `Backlog`, `Record.OccupiesBacklogSlot()`, `DefaultEndpointBacklogLimit`, `ValidateEndpointBacklogLimit` | The endpoint's backlog limit: the count and limit `Intake` takes, the one rule for which records are counted, the default and its validation. |
| `IntakeAuthorization(authz.Verdict)` | The one mapping from the policy's verdict to an `Authorization`: allow, suspend, or a denial with the `RejectReason` the sender hears. |
| `CheckDedup`, `NewDedupRecord`, `DedupRecord.WithReply` | Deduplication, with the stored reply of a control signal. |
| `TransitionFeedback(from, to, unknown, reported)`, `Kind` | Which feedback a change of state calls for; the fixed wording of each kind. |
| `FeedbackQueue` (`Add`, `Flush`, `NextFlush`), `FeedbackParams`, `DefaultFeedbackParams()`, `MergeKeyOf` | Merging and rate-capping pending feedback into notices; `NextFlush` is the earliest instant at which `Flush` would send one. `DefaultFeedbackParams` is the decided window and cap. |
| `ReportBy`, `SettleBy`, `BacklogDeadline`, `DedupRetainUntil`, `StatusReportRetainUntil`, `IdempotencyKeyRetainUntil`, `ValidateEndpointMaxQueue` | Deadline and retention arithmetic. |

`Apply`, `Tick`, `Handout` and `Ack` change a record; every state change goes
through one transition function. The constructors start one: no event makes a
record out of nothing.

## States

`queued` (sender only), `accepted`, `delivered`, `rejected` (with a reason),
`expired_unconfirmed`, `delivery_unknown`, `closed_unconfirmed`, plus the
target-internal sub-state `notified` of `accepted`.

- Final: `delivered`, `rejected`, `closed_unconfirmed`.
- Open but correctable by later facts: `expired_unconfirmed` (to `accepted`,
  `delivered`, `rejected`, `delivery_unknown`) and `delivery_unknown` (to
  `delivered` only).
- On the target, `delivered` is reached only by acknowledging every chunk
  (`Ack`) or, for a control record, by `EventReplyCommitted`.
- `Record.Unknown` says why a delivery is `delivery_unknown` and is set exactly
  then. On the target, `delivery_unknown` is reached only by a delivery that was
  handed over (a body handed out at least once, or any control record): a target
  never holds a loss. On the source, `delivery_unknown` has one of three causes,
  each with one way in: `handed_over`, from a target's report
  (`EventDeliveryUnknown`, which carries no cause: it is that and nothing else);
  `lost_at_target`, by `EventNoRecordAtTarget` on an accepted delivery before its
  `ReportBy`; `lost_at_sender`, only by `NewLostRecord`, which makes the
  replacement of the sender's own record lost to storage corruption. A `handed_over`
  target record needs a hand-out. No event puts a record into a storage loss of the
  sender's own: `NewLostRecord` alone makes one.
- `change` refuses, with `ErrInvalidInput`, a transition that enters
  `delivery_unknown` without a cause or any other state with one: the cause and
  the state are set together (`causeFitsState`, which `Valid` uses as well). That
  is a programming error, never a fact.
- `Record.Lost` marks the sender's replacement for a record lost to storage
  corruption. Only `NewLostRecord` sets it and no transition clears it, so a
  replacement stays recognisable after it closes, when `Unknown` is gone. `Unknown` stays set
  exactly in `delivery_unknown`. `checkUnknownCause` and `checkLostShape` are the
  one statement of what a cause and a replacement promise (see "A restored record
  is checked"); `Valid` calls them rather than restating them.
- A repeated or older fact is ignored with a reason, not an error. A fact that
  contradicts the state (a rejection for a delivered message) is
  `ErrIllegalTransition`.
- A target record never produces feedback; the sender's node does, from the
  status reports it pulls.

## Feedback

Only anomalies produce feedback: unreachable (network or suspended trust chain),
delivery blocked and its recovery, expired_unconfirmed, rejected,
delivery_unknown, the losses (the sender's own, the target's, and a message nobody can name), the corrections of
expired_unconfirmed and delivery_unknown, and the close of a delivery that was never told an outcome. Normal progress never does.

- A source delivery that closes at `report_by` while `queued` gives the expiry
  notice (if not given), and while `accepted` gives `accepted_unconfirmed`: the
  target accepted the message, but its result could not be learned before the
  report deadline, so whether it was delivered is unknown and it must not be
  assumed not delivered. Closing from `expired_unconfirmed`, `delivery_unknown` or
  a loss is silent, and so is every close on the target.

- At most once per delivery per kind. Corrections are kinds of their own, so a
  delivery corrected twice is told twice.
- The expiry notice says the target may have received the message.
- `lost_at_target` is given when the target answers a pull by saying it holds no
  record of an accepted delivery (see "A target that holds no record" in the
  reference). It names no cause, because the sender knows only that the record is
  gone. `lost_unknown_message` belongs to no delivery: no transition produces it
  and `Valid` refuses a record that reports it; its item has no message or
  recipient, names the stored record that holds it (`Item.Source`) and merges per
  sender endpoint. `FeedbackQueue.Add` takes an item in exactly one of two shapes
  (a message and a recipient and no source, or this kind: no message, no
  recipient, a source) and refuses any other. It tells an item of this kind from
  another of the same sender by the source alone, so the notice of two damaged
  records carries both and the caller clears both; the same source added again
  is ignored, like any other repeated decision.
- The two loss notices (`lost_at_sender`, `lost_at_target`) say that the node
  lost what it had stored about the message (the target's wording: no longer holds
  any record of it), that whether it was delivered, or for a control signal
  executed, is unknown, and that the sender must not assume it was not delivered.
  They never say the target handed anything over: a node that lost its record
  cannot know. They are kinds of their own and merge by message. A
  `lost_at_target` is corrected like `delivery_unknown` by a later `delivered`,
  while a sender's own loss (`lost_at_sender`) never is: its replacement ignores
  every later fact.
- The delivery_unknown notice says the target handed the delivery over (to the
  agent, or to the adapter that executes a control signal), that whether it was
  seen or executed is unknown, and that the sender must not assume it was not
  delivered. One wording serves messages and control signals, because the
  sender's record cannot tell them apart.
- A rejection or delivery_unknown that follows expired_unconfirmed replaces it
  instead of correcting it. A loss at the target is told only for an accepted
  delivery, so an expiry notice before it was corrected on the way to accepted.
- Merging: anomalies of one message are one notice; unreachable hints and, as
  a notice of their own, expired_unconfirmed notices per (sender endpoint,
  target node); blocked reports per (sender endpoint, target endpoint), with the
  recovery as a separate notice; lost-unknown-message notices per sender endpoint.
  The merge window and the
  per-endpoint rate cap live in `FeedbackParams`; `DefaultFeedbackParams()` is
  the decided values, a 1-minute window and 10 notices per 10 minutes for each
  sender endpoint (60 an hour, the prompts an endpoint may receive per hour).
  Notices over the cap are held back, never dropped. Any value that is not
  positive is refused, so an unset parameter never disables merging or the cap.

## Time

All values are constants in `timing.go`: `accept_before` default 10 min and cap
1 h, unreachable hint 2 min, endpoint queue time default 30 min and cap 2 h,
lease 5 min with a total life of at most 15 min and at most 3 redeliveries,
safety margin 5 min. The clock tolerance (2 min) is not defined here: it is
`timing.ClockTolerance` from [`internal/timing`](../timing/README.md), shared
with the trust and admin packages.

`report_by = accept_before + 2 h + 4 x 15 min + 2 min`: the queue cap, the
redelivery period (4 leases, each at most 15 min, which is the only thing it
depends on), and the tolerance. The target settles every delivery by
`report_by - 5 min`; the sender closes whatever is still open after `report_by`,
after one last pull of the target by the caller, and tells what it has not been
told (see "Closing at `report_by`").

## When a record entered its state

`Record.StateAt` is the moment the record entered `Record.State`: the time the
constructor was given (the acceptance time, the queuing time, or the discovery
time of a loss), then the `now` of the transition that last changed the state.
`change` is the only writer, so every way into a state (`Apply`, `Tick`,
`Handout` denied by the second check, `Ack`, the forcible close) stamps it with
its own `now` and no caller sets it. A fact that leaves the state alone (a
prompt, a block, a hand-out, a lease that runs out and goes back to the queue, a
duplicate or stale fact) leaves it alone, so a report sent again for an
unchanged state repeats the same time. A `Tick` that settles several states in
one call (a target that slept past `report_by`) stamps the last one with the
moment it woke, not with the deadline each step was due: the record shows when
the node recorded the state.

A delivered record is final, so the time it was delivered is the time it entered
its state: `DeliveredAt()` returns `StateAt` for a delivered record and the zero
time otherwise, and no second time is kept that could disagree. A status report
needs exactly these two (`reported_at` and `delivered_at`, the latter present
exactly when delivered). A transition at the zero time is `ErrInvalidInput`, and so is a
constructor that returns an error (`NewTargetRecord`, `NewTargetNoticeRecord`,
`NewLostRecord`); `Valid` refuses a record without `StateAt` (`ViolationStateAt`); it compares
`StateAt` with no other instant, because the wall clock may step back.

## Backlog limit

An endpoint holds at most `DefaultEndpointBacklogLimit` (200, or the limit its
owner set) queued messages. A message that arrives when the count is at the
limit is `rejected` with `backlog_full`, a final state; the rejected record takes no slot and no
body is kept.

- Judged last in `Intake`: after the deadline checks, and only if authorization
  allows. A denied sender hears its denial and learns nothing of the queue; a
  suspension stores nothing, so the limit is not consulted until the retry.
- Governs ordinary messages only. Control signals are executed, never queued,
  and refusing one would keep the owner from closing or interrupting a flooded
  endpoint. Status reports, control replies and system feedback are exempt
  traffic: refusing a notice because the endpoint is behind would silence the
  anomaly it reports. For these `Intake` does not read the `Backlog`.
- `Record.OccupiesBacklogSlot` is the one rule for the count: a target record
  that is `accepted` (queued, leased or prompted for) and is neither a control
  record nor exempt (`Record.Exempt`). System feedback is not counted either,
  so notices the node sends itself never take room from other senders. The count may therefore exceed the
  limit by the notices that are exempt from it.
- The limit is a count, not a deadline: nothing in `Tick` changes with it, so
  it adds no timed rule.

## Closing at `report_by`

`Tick` and `EventReportByPassed` close a record that is not final from the nanosecond
after `report_by`. They know nothing of any pull, so the caller makes its last pull
of the target before it ticks a delivery that is `queued` or `accepted`, while the
target still keeps its record (the target's retention outlasts `report_by`), and
applies the answer with `ApplyStatusReport`. A report applied after `report_by`
while the record is not closed has its true meaning: a rejection is told, a delivery
is silent, a hand-over is told; no rule looks at how late it is. An answer that the
target holds no record is ignored after `report_by` (`IgnoreRetryEnded`): the
target may have forgotten it legitimately. What is still unknown then is closed with
the notices above, at most once per delivery (`Record.Reported`). `Valid` holds a
closed source record to it: it carries the notice of how it closed.

## Closing a delivery by force

A target delivery that cannot go on for a reason other than the agent's own
handling (its backlog deadline passed, the second authorization check denied
it, its endpoint was closed) is closed by one rule. If its body was never handed
out it becomes `rejected` with that reason. If it was handed out at least once
it becomes `delivery_unknown`, because the agent may have seen the body and
`rejected` would tell the sender it was not delivered; a late acknowledgement
then corrects it to `delivered`. Handing out is what counts, not prompting: a
delivery that was only announced with a prompt is still rejected. A control record
counts as handed out from acceptance, so its forced closure is always
`delivery_unknown`: a control record is `rejected` only when it was refused at
intake (`NewRejectedTargetControlRecord`), before it was ever accepted, and with
one of the reasons a control intake gives (`RejectReason.AtControlIntake`: the
endpoint's and the policy's, the deadline's and the envelope's, not the backlog's
or the second check's). A refused control record is final, was never handed over,
and ignores every later fact except a committed reply, which contradicts it. A delivery that is already `delivery_unknown`, whatever the
cause, stays so: a forced closure of it is `IgnoreDuplicate`, never `rejected`.

## Records lost to storage corruption

When storage finds the sender's delivery record corrupt, only the key on its
first line survives: the delivery's identity. `NewLostRecord(discoveredAt)` makes
the replacement. It starts `delivery_unknown` with the cause `lost_at_sender`,
because the delivery may or may not have been handed over and neither `rejected`
nor silence would be true. It carries `Lost`, which it keeps through the states it
can reach: `delivery_unknown`, then `closed_unconfirmed` after `ReportBy`.

- It returns the one feedback the sender must get (`KindLostAtSender`), already
  marked as reported. Every later fact about it (receipt, delivery, rejection, an
  unknown result, the target's answer that it holds no record) is ignored as
  `IgnoreRecordLost`: it has none of the history facts are judged against, and
  the sender was already told the result is unknown. Only time ends it. A control
  signal has no record on the sender's side, so there is nothing else to replace.
- A target that loses its record keeps nothing: no replacement, no replacement
  deduplication record. The sender learns of it when the target answers a pull
  that it holds no record (`EventNoRecordAtTarget`), and a resend of the delivery
  that arrives before its deadline is taken in as new and may be handed out a
  second time (see the reference).
- `ReportBy` is `LostReportBy(discoveredAt)`, an upper bound of the lost record's
  own `report_by`: an accepted deadline lies at most `MaxAcceptBefore` plus the
  clock tolerance after its acceptance, and the acceptance was not after
  `discoveredAt`. It holds by a guarantee of the caller (see below). `AcceptBefore`
  and `BacklogDeadline` stay zero because they are unknown. `Tick` closes the
  record after `ReportBy`, silently, like any other; no other deadline applies, so
  it adds no timed rule.
- `Record.NeedsBody()` is true for the records that still need the bytes of the
  body: a source record that is `queued`, and a target message record that is
  `accepted` (queued or under a lease). When a stored body is found corrupt, a
  source record that needs it is replaced with `NewLostRecord`; a target record has
  no replacement and is removed with an alert; every other record that references the body keeps its state
  unchanged, only dropping the reference.

## Control signals on the target

A control signal is executed by the adapter and answered with a reply that is
committed together with the execution result. Its target record
(`NewTargetControlRecord`, `Record.Control`) is never queued for `recv`:
`Handout` and `Ack` return `ErrWrongSide`, `Renew` finds no lease, a prompt
(`EventNotified`) and a block (`EventBlocked`) are refused with `ErrWrongSide`,
and there is no backlog deadline. Blocked means prompts persistently fail to
reach a session; a control signal has neither, and an adapter that is offline
reaches the sender through the control reply.

- `EventReplyCommitted` is the only fact that delivers it, from `accepted` or,
  as a correction, from `delivery_unknown`. Any other record refuses it with
  `ErrWrongSide`.
- Accepting it hands it over to execution, so one that is not final at
  `report_by - 5 min` becomes `delivery_unknown` in `Tick` (never `rejected`,
  never silent), and a forced closure also gives `delivery_unknown`. It closes
  after `report_by` like any other record.
- `StatusReport` reports `delivered` once the reply is committed. The sender
  applies the reply with the existing `EventDelivered`.

## A restored record is checked

`Record.Valid` is the contract's own answer to "could the transitions have
produced this record". Every record the constructors and the transitions
produce is valid, under the caller guarantees below, and every invariant listed
here is refused: it returns nil for a record that has them all and otherwise an
`*InvalidRecordError` (matching `ErrInvalidRecord` with `errors.Is`) whose
`Violation` names the first invariant broken. The accepted set is not claimed to
equal the reachable set; only for the feedback bookkeeping of a source record is
it equal, which a test pins by enumerating the transitions. It reads only the
record, never a clock.

- Kind and state: a target is never `queued` or `expired_unconfirmed`, a control
  record is never `rejected` and exists only on the target; a rejection reason is
  recorded exactly while the state is `rejected`; `notified` only on an accepted
  message record of the target. `Exempt` only on a target message record (not a
  control record, not a sender's replacement).
- State time (`ViolationStateAt`): `StateAt` is set on every record.
- Deadlines: `ReportBy` is `ReportBy(AcceptBefore)`; a queued message has a
  backlog deadline no later than `SettleBy`, every other record has none. A
  replacement is judged by its marker instead (below).
- Unknown cause (`ViolationUnknownCause`): the cause is set exactly while the
  state is `delivery_unknown` and is one its side can hold: a source holds
  `handed_over`, `lost_at_target` (the target's answer that it holds no record,
  without the marker) or, as a replacement, `lost_at_sender`; a target holds
  `handed_over` alone, which needs a hand-out or a control signal, because a
  target never holds a loss. A replacement is never `handed_over`.
- Replacement (`ViolationLostShape`): only a source record carries the marker,
  never a target's. It is on `delivery_unknown` or `closed_unconfirmed` records. A
  replacement has a `ReportBy` and no `AcceptBefore` or `BacklogDeadline`, no
  lease and no cause of a block, and has reported exactly its own notice. `Valid`
  cannot check the value of `ReportBy`, which is `LostReportBy` of a discovery
  time the record does not keep. The marker is refused on every other shape.
- Produced feedback (`Reported`) is the sender's alone, and the kinds that go
  together with a state follow `TransitionFeedback`: the rejection notice exactly
  for `rejected`, `delivery_unknown` and its correction only on the way to
  `delivered` or `closed_unconfirmed` and exactly the notice of the record's
  cause (`delivery_unknown` for `handed_over`, `lost_at_target` for the loss the
  target's answer told; the sender's own loss belongs to a replacement alone; no
  record reports `lost_unknown_message`), an expiry notice corrected on the way to
  `accepted` or `delivered` unless a rejection or `delivery_unknown` replaced it
  (a loss at the target never replaces one: it is told only for an accepted
  delivery),
  a recovery after every block that ended in `delivered` or in `accepted`, and a
  closed record that carries the notice of how it closed: the expiry, `accepted_unconfirmed`,
  or the notice of the unknown result it held (`ViolationReportedClosed`).
- Blocked: only an accepted record that is not a control record, with a blocking
  cause; the cause outlives the flag because the recovery notice names it.
- Lease: only a message in the target's queue has one. Chunk flags and the
  chunk count agree, an acknowledged chunk was handed out, page identifiers are
  set and distinct, at most `MaxLeases` leases were issued and the last one is
  not over while the message is still accepted, a lease is outstanding only
  while accepted, its times are set and the expiry is no later than
  `LeaseRenewalCap` after the issue time, and it was issued before the backlog
  deadline. There is no lower bound on the expiry: a renewal made after the
  wall clock stepped back (within `timing.ClockTolerance`) ends earlier than
  `LeaseDuration` after the issue time.
- Outcome: a target message is `delivered` exactly when every chunk is
  acknowledged; a target is `delivery_unknown` only for a delivery that was
  handed over, and `rejected` only for one that was not.

Where a check reads a rule the transitions also use (`hasQueue`, `handedOver`,
`SettleBy`, `ReportBy`, the lease bounds), it calls the same function.

## Applying a status report

The sender's node applies a pulled `StatusReport` with
`Record.ApplyStatusReport`: the state fact first, then, when the record is
accepted, the blocked or unblocked fact. A blocked report pulled before any
receipt therefore still produces the receipt and the one blocked notice, and a
report of accepted and blocked pulled from `expired_unconfirmed` produces the
correction and the blocked notice.

## What the caller must guarantee

- `discoveredAt` of `NewLostRecord` is not earlier than the lost record's
  acceptance: the later of the clock reading and the persisted wall-clock
  high-water mark. The bound of `LostReportBy` holds only then.
- A source record's `AcceptBefore` is no later than its acceptance time plus
  `MaxAcceptBefore` plus the clock tolerance: the sending node does not accept a
  deadline the target's `Intake` would refuse. On the target `Intake` refuses a
  later deadline; on the source only this guarantee keeps the bound of
  `LostReportBy`.
- The replacement and its feedback item are stored in one atomic step.
  `NewLostRecord` returns the notice on every call, so it is called once per
  loss; a step that did not commit queued nothing and may run again. A
  replacement that is itself later lost is a new loss and gives a new notice.
- `now` is UTC and comes from the caller; records are persisted atomically with
  the effects the outcome calls for.
- A record read back from storage is passed to `Valid` after decoding, before
  any other method of the record is called; they trust the record, and a lease
  whose chunk flags disagree with its chunk count would make `Ack` and `Handout`
  index out of range. An invalid record is treated as corrupt, never repaired.
- Times in a stored record round-trip to the nanosecond (for example RFC 3339
  with fractional seconds), because `Valid` compares them exactly: a format that
  rounds `AcceptBefore` would make `ReportBy` differ from `ReportBy(AcceptBefore)`.
- `report_by` in a target record equals `ReportBy(accept_before)` and the
  envelope is otherwise valid; this package does not validate the envelope.
- The authorization verdict is the policy's (`authz.Evaluate`). `Intake` takes
  it as an `Authorization` from `IntakeAuthorization`, which alone maps a
  denial to the reason the sender hears; a caller never builds that reason
  itself. `Handout` takes only the kind, `IntakeAuthorization(v).Kind`, and a
  refusal at `recv` is always reported as `second_check_failed`, so no
  specific rule is revealed. The token check of an
  acknowledgement is made by the caller and passed as `Ack.TokenMatches`.
- Exempt traffic (`authz.TrafficKind.PolicyExempt`, which alone classifies it)
  is not judged by the policy, so the caller never calls `authz.Evaluate` for it
  and no verdict exists. `Intake` requires the zero `Authorization` for it and
  refuses any other as invalid input. The record of such traffic is made with
  `NewTargetNoticeRecord`, which marks it `Record.Exempt`, and `Handout` reads
  that marker instead of a verdict: for an exempt record `auth` must be the zero
  `AuthorizationKind`, for every other record a valid one. The one exempt kind
  `Intake` judges is system feedback; a test pins that.
- The caller generates one `LeaseID` per `recv` (`NewLeaseID` with
  `crypto/rand.Reader` in production) and passes it to `Handout` for every
  chunk the page hands out, because one `recv` hands a page out under one lease
  and the page prints one identifier. `Handout` records it in `Lease.IDs` (a
  zero one is `ErrInvalidInput`) and returns it as `HandoutResult.Lease`; a
  re-send under a live lease records it too and leaves the lease times alone.
  The acknowledgement hands the identifier back in `Ack.Lease`; one under which
  no chunk of the message was handed out is refused (`AckRefusedLease`), while
  any that was counts, expired or not. The order of one `recv`, with the
  handoff package, is in [handoff](../handoff/README.md).
- `HandoutResult.PossibleDuplicate` and `Record.PossibleDuplicate(part)` are per
  chunk: true only for a chunk handed out before. The caller fills the
  `possibly_duplicate` flag of a page from `Record.PossibleDuplicate(part)`
  before it packs.
- The caller splits bodies into chunks and passes the count and the chunk it is
  handing out (starting from `NextPart`) to `Handout`; the count is fixed at
  the first hand-out. Only chunks handed out can be acknowledged: a chunk
  number outside the message is a chunk never handed out (`AckRefusedNoHandout`),
  not an input error.
- A control signal's record is built with `NewTargetControlRecord`, never
  `NewTargetRecord`, and `EventReplyCommitted` is applied in the same
  transaction that commits the control reply and the execution result. A caller
  never passes a control record to `Handout`, `Ack`, `EventNotified` or
  `EventBlocked`. A control signal the target refuses at intake is recorded with
  `NewRejectedTargetControlRecord` and the report of that record is the answer: no
  control reply goes with it, and no execution result or session endpoint is
  stored for it.
- Run `Tick` before `Handout`, and whenever a lease, backlog, settle or
  `report_by` instant may have passed. A scheduler arms its timer for
  `Record.NextTick()` (the zero time: no timer) and for
  `FeedbackQueue.NextFlush(params)`, and asks again after every change to the
  record or the queue, because the answers follow their state. The instant may
  already be past; the package reads no clock and owns no timer. The scheduler
  never works out `Tick`'s deadlines from the record's fields itself.
- Every timed rule of `Tick` is a `Record` deadline method listed in
  `tickDeadlines`, which `NextTick` reads. Adding a timed rule means adding its
  deadline method and listing it there, and extending the property-test
  generator and `requiredNextTickPaths` with it. A window boundary of the
  feedback queue is likewise defined once: `mergeDueAt` for the merge window and
  `rateReleaseAt` for the rate window.
- Whether delivery is blocked is decided by the caller (the prompt logic); this
  package only turns the decision into feedback.
- `Intake` gets, as `Backlog`, the number of the recipient endpoint's records
  for which `Record.OccupiesBacklogSlot()` is true and the endpoint's limit, which is validated with
  `ValidateEndpointBacklogLimit`; an unset limit (zero) is invalid input, not
  "no limit". `traffic` is what the request is; `Intake` judges only a
  message, a control signal and system feedback, and every other kind (a status
  report and a control reply are taken by `ApplyReport` and
  `HandleControlReply`, trust maintenance is not delivered to an endpoint) is
  invalid input. The count and the intake
  decision are made in one atomic step with storing the record, or two requests
  can both see room for the last slot.
- Digests passed to dedup are computed by the caller over everything that must
  match on a retry.
