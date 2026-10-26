# Delivery

How one message delivery behaves from the moment the sender's node records it
to the moment it is delivered, refused, or given up on, and what the sender is
told along the way. A delivery is one message to one recipient endpoint; a
message with several recipients has several deliveries that share a message id.
Control signals use the same states, the same deduplication and the same
feedback, including the wording of each notice (the delivery_unknown wording
covers both a message and a control signal, and the loss wordings the same); on the target they differ only in how a delivery ends (see Control
signals on the target). This document describes the committed code of `internal/delivery`.

## What

### States

| State | Meaning | Final |
| ----- | ------- | ----- |
| `queued` | The sender's node has recorded the message. Sender view only. | no |
| `accepted` | The target authorized, deduplicated and stored it. On the target this covers waiting in the queue and being leased. | no |
| `delivered` | The whole body reached the agent and was acknowledged; for a control signal, its reply was committed. | yes |
| `rejected` | The target refused it, with a reason (unauthorized, endpoint missing or closed, endpoint declined, too large, expired, backlog expired, second authorization check failed, malformed, deadline out of range, backlog full). On the target only for a body that was never handed out. | yes |
| `expired_unconfirmed` | The deadline passed and an active query found no receipt. The target may have the message. | no, correctable |
| `delivery_unknown` | Whether the delivery reached the agent cannot be told, for the cause in `Record.Unknown`. `handed_over`: the body was handed out and whether the agent saw it cannot be told; for a control signal, it was accepted for execution and its reply was never committed. `lost_at_sender`: the sender's node lost its record of the delivery to storage corruption, so nothing is known. `lost_at_target`: the target answered a pull of an accepted delivery by saying it holds no record of it. On the target, `delivery_unknown` is reached only by a delivery that was handed over (a body handed out at least once, or any accepted control signal): a target never holds a loss. On the source it has one of three causes, each with one way in: `handed_over`, from a target's report; `lost_at_target`, by `EventNoRecordAtTarget` on an accepted delivery before its `report_by`; `lost_at_sender`, only by `NewLostRecord`. | no, correctable |
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
handed over; a fact that asks for either otherwise is an error. `EventDeliveryUnknown`
carries no cause: a target reports a hand-over and nothing else, and a status
report has no cause either. A fact that describes an earlier stage than the record
shows is ignored; a fact that contradicts a final state is an error.

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
| lost at sender | The sender's node lost its record of the delivery to storage corruption. Given once, when the replacement record is made. | Wording: this node lost what it had stored about the message, so whether it was delivered, or for a control signal executed, is unknown; do not assume it was not delivered. Never says the target handed anything over. |
| lost at target | The target answered a pull of an accepted delivery by saying it holds no record of it (`EventNoRecordAtTarget`). Given once. | Wording: the target node no longer holds any record of the message it had accepted, so whether it was delivered, or for a control signal executed, is unknown; do not assume it was not delivered. It names no cause: the sender knows only that the record is gone. |
| lost unknown message | A record of the sender's node was unreadable, its first line included, so nobody can tell which message it was. Not a transition of any delivery: an item built by `NewLostUnknownMessageItem`, naming no message and no recipient but the stored record that holds it (`Item.Source`): the queue keeps one item per source record and one notice per sender endpoint carries them all, so every record it names is cleared and none is told twice. | Wording: this node lost what it had stored about one of the messages this endpoint sent, to storage corruption, and cannot tell which one; whether it was delivered is unknown; do not assume it was not delivered. |

Rules: normal progress and `closed_unconfirmed` give nothing; each kind is given
at most once per delivery; moving from expired to rejected or unknown gives the
new news instead of a correction. A recipient endpoint is never
told of a loss: feedback goes only to the sender. Merging: one notice per message for its
anomalies, per (sender endpoint, target node) for unreachable hints and, as a
notice of their own, for expired_unconfirmed notices, per (sender endpoint,
target endpoint) for blocked reports with the recovery as its own notice, and per
sender endpoint alone for the notices of lost unknown messages (any number is
one notice). The
merge window (1 minute) and the per-endpoint rate cap (10 notices per 10
minutes) are the values of `DefaultFeedbackParams`; notices over the cap are
held, not dropped.

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
the sender's idempotency keys as long as the dedup records. Reply permission
reads some of these facts for longer; see
[what each side keeps](../envelope/README.md#what-each-side-keeps).

A target keeps its record of a delivery until `RecordRetainUntil(report_by)`,
`report_by` plus the clock tolerance. A source pulls until `report_by` on its own
clock and reads a missing record as a loss at the target (below), so a target
whose clock is ahead of the sender's by the whole tolerance must still hold the
record at the sender's last pull.

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
sender keeps retrying until the deadline; an ordinary message for an endpoint
whose queue is at its limit is rejected as backlog full (below). `IntakeAuthorization` is the single
place where the verdict of the [policy](../policy/README.md) becomes that
authorization:

| Policy verdict | Authorization |
| -------------- | ------------- |
| allow (granted, reply permission) | allow |
| suspend (trust suspended) | suspend |
| reject: trust denied, explicit deny, not authorized, preset not allowed, preset closed to local sources | deny, `unauthorized` |
| reject: body too large | deny, `too_large` |
| reject: endpoint paused, refuses several recipients, sender not accepted | deny, `endpoint_declined` |
| reject: malformed request | deny, `malformed` |
| reject: endpoint hidden | deny, `endpoint_missing` |
| reject: any reason not listed | deny, `unauthorized` |
| an outcome that is none of the three | the zero authorization, refused by `Intake` and `Handout` as invalid input |

The mapping is used only at intake. At `recv`, `Handout` takes only the kind
(`IntakeAuthorization(v).Kind`), and a refusal there is always reported as
`second_check_failed`, so no specific rule is revealed.

Exempt traffic carries no verdict. The [policy](../policy/README.md) does not
judge status reports, control replies, system feedback or trust maintenance, and
its callers do not call `Evaluate` for them, so there is nothing to put in an
`Authorization`. `authz.TrafficKind.PolicyExempt` is the one place that
classifies; `Intake` reads it and, for exempt traffic, requires the zero
`Authorization` and refuses any other (even a deny or an allow) as invalid
input: a verdict for it would mean the policy was applied to what it does not
govern, and a denial would silence system feedback. The record of such traffic
carries the decision as `Record.Exempt`, set only by `NewTargetNoticeRecord`
(system feedback is the one exempt kind `Intake` judges; a test pins that), so
the decision is taken once, where the traffic is taken in, and is never
recomputed from a traffic kind a caller must remember. `Handout` and
`OccupiesBacklogSlot` read the record: `Handout` makes no second check for an
exempt record and requires the zero `AuthorizationKind` for it, so no caller
fabricates an allow, and `OccupiesBacklogSlot` takes no traffic argument. Storage
keeps the marker, and with it the rule that such a record holds no verdict (see
[storage](../storage/README.md)). A rejected alternative was to let `Handout`
accept any verdict and ignore it for notices: the same call would then do two
things, and a verdict that was not read would look like one that was.

### Backlog limit

`DefaultEndpointBacklogLimit` is 200 per endpoint, and an endpoint's owner may
set another. A message that arrives when the endpoint holds as many queued
messages as the limit is `rejected` with `backlog_full`, in `Intake`.

| Case | Result |
| ---- | ------ |
| ordinary message, count below the limit | accepted |
| ordinary message, count at or above the limit | rejected, `backlog_full` |
| ordinary message, authorization denied | rejected with the denial's reason, whatever the count |
| ordinary message, authorization suspended | suspended, whatever the count |
| deadline already past, out of range or missing | rejected as `expired`, `deadline_out_of_range`, `malformed`, whatever the count |
| control signal | accepted: never queued, so no slot is taken and none is needed |
| system feedback (zero `Authorization`) | accepted: exempt traffic, the count is not read |
| system feedback with any verdict | invalid input |
| limit unset (zero) or negative, negative count, a traffic kind `Intake` does not judge (status report, control reply, trust maintenance, unset or unknown) | invalid input |

Which records are counted is `Record.OccupiesBacklogSlot`: a target record that
is `accepted` (queued, leased or prompted for) and neither a control record nor
exempt. A record that was handed over (`delivery_unknown`), delivered or rejected
is not queued any more. System feedback is not counted: its record is exempt.

A hidden endpoint is reported as not existing, so the sender cannot tell it from
an endpoint that is absent, and all the rules that refuse without a declaration
by the endpoint are reported alike as `unauthorized`, so the sender does not
learn which rule refused it.

### When the next Tick and the next Flush are due

`Record.NextTick` returns the earliest instant at which `Tick` could change the
record, or the zero time when none is pending, which is exactly the case for a
final record. It is the earliest of the deadlines `Tick` consults that apply to
the record in its present state:

| Deadline | Instant | Applies to |
| -------- | ------- | ---------- |
| control settle | `report_by - margin` | an accepted control record |
| lease settle | `report_by - margin` | an accepted target message under an open lease |
| lease expiry | `Lease.ExpiresAt` | an accepted target message under an open lease |
| backlog | `BacklogDeadline` | an accepted target message without a lease |
| close | the nanosecond after `report_by` | every record that is not final |

`Tick` closes strictly after `report_by`, so the close instant is the first
nanosecond past it. The instant can be in the past of the caller's clock when a
deadline was missed. It follows the record's state: ask again after `Tick`, after
any event, and after a hand-out, a renewal or an acknowledgement.

`FeedbackQueue.NextFlush(params)` returns the earliest instant at which `Flush`
would send a notice, or the zero time when nothing is pending. For each sender
endpoint with items waiting it is the later of the end of the merge window of the
sender's oldest item and the instant at which fewer than `RateCap` of the
sender's sent notices are still inside the rate window (when the `RateCap`-th most
recent of them leaves it); the answer is the earliest over the senders. It
refuses unset parameters as `Flush` does. Dropping sent notices that have left
the rate window, which `Flush` does along the way, changes no later result and
is not counted as a change.

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
target record is built by `NewTargetControlRecord(acceptedAt, acceptBefore, reportBy)` and
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

The sender applies the signed reply with the existing `EventDelivered`; nothing
on the source side changes. A `delivered` status report of the control signal
does not end the sender's record: the reply carries the result and the session
endpoint, so the sender keeps pulling until it has the reply (see
[envelope](../envelope/README.md)).

### Validity of a restored record

Every record that the constructors (`NewSourceRecord`, `NewTargetRecord`,
`NewTargetNoticeRecord`, `NewTargetControlRecord`) and the transitions (`Apply`, `Tick`, `Handout`,
`Renew`, `Ack`) produce is accepted by `Record.Valid`, under the caller
guarantees of the package README, and every invariant listed there is refused;
the accepted set is not claimed to equal the reachable set, and only for the
feedback bookkeeping of a source record is it equal, which
`TestRecordValidSourceFeedbackBookkeepingIsExact` pins by enumerating the
transitions. An invalid record yields an `*InvalidRecordError`; its `Violation`
is a typed reason naming the
first invariant broken, in a fixed order of checks (kind and state, unknown cause,
replacement shape, deadlines, state time, block flags, produced feedback, lease, outcome
against lease). The invariants
are listed in the package README; each is a property the transitions keep:

- what `change` clears on leaving `accepted` (block flag, outstanding lease, sub-state);
- what only one side or one kind of record has (feedback and expiry on the
  sender, leases, backlog deadline and sub-state on a queued target message);
- the shapes `Handout` creates and `Renew` and `Tick` leave (chunk flags,
  identifiers, times, lease count);
- the end states of a target (delivered by the last acknowledgement, rejected
  only before a hand-out, delivery_unknown only after one);
- the unknown cause and the replacement of a lost record (see the next section):
  the cause is set exactly in `delivery_unknown`, a target holds `handed_over`
  alone, and a source record that carries the `Lost` marker has the shape
  `NewLostRecord` gives it, in every state it can reach.

The lease expiry has an upper bound, `LeaseRenewalCap` after the issue time,
and no lower one: `Renew` extends from the instant it is called, and the wall
clock may step back by up to `timing.ClockTolerance` in normal operation, so a
renewal can end earlier than `LeaseDuration` after the issue time.

`Valid` binds stored records to the protocol constants it reads. Lowering
`LeaseRenewalCap` or `MaxRedeliveries`, or changing `ReportByOffset`, would make
records already stored invalid, so such a change is a storage format change that
needs a migration of the stored records.

### Status reports

A target reports its state, the rejection reason and whether the accepted message
is blocked. A reported `delivery_unknown` is always a hand-over: a target never
reports a loss, because a target that lost its record has nothing to report (the
sender learns of it from the target's answer that it holds no record, below). The
sender applies the report in one place: the state first, then, if the record is
accepted, the block or its end. So a block
reported before the receipt was pulled is still reported, and an expired
delivery that turns out accepted and blocked gives both the correction and the
blocked notice.

### When a record entered its state

`Record.StateAt` is the moment the record entered its current state: the time the
constructor was given (`NewSourceRecord(queuedAt, ...)`,
`NewTargetRecord`, `NewTargetNoticeRecord` and `NewTargetControlRecord` take the
acceptance time, `NewLostRecord` the discovery time), then the `now` of the
transition that last changed `State`. `change` is the only writer, so `Apply`,
`Tick`, a second-check denial in `Handout`, `Ack` and the forcible close all stamp
the state they enter with their own clock, and a fact that leaves the state alone
(prompt, block, hand-out, a lease that goes back to the queue, a duplicate or
stale fact) does not touch it. `DeliveredAt()` is `StateAt` for a delivered
record and zero for any other.

The envelope's signed status report carries two times: `reported_at`, the moment
the target recorded the state the report states, and `delivered_at`, present
exactly when the state is delivered. A record provides exactly these. A
delivered record is final, so its delivery time is the moment it entered the
state, and keeping it in a second field could only let the two disagree. A
target that signs a report again for an unchanged state repeats `StateAt` under a
fresh signature; one that settles a delivery late (a `Tick` after `report_by`)
records and reports the moment it woke, which is what the sender expects of a
late report.

A transition at the zero time is `ErrInvalidInput`, and so is a constructor that
returns an error (`NewTargetRecord`, `NewTargetNoticeRecord`, `NewLostRecord`): a record
made at no time would fail only when it is written. `Valid` refuses a record
without it (`ViolationStateAt`) and compares it with no other instant, since the
wall clock may step back. Tests in `internal/delivery/statetime_test.go`:
`TestConstructorsStartTheStateAtTheGivenTime`,
`TestConstructorsThatCanRefuseRefuseTheZeroTime`,
`TestEveryTransitionIntoAStateSetsTheStateAtToItsClock` (every way into a state,
expected instants from the test's own clock), `TestTickStampsEveryStepOfOneCallWithItsClock`,
`TestFactsThatLeaveTheStateAloneLeaveTheStateAtAlone`,
`TestDeliveredAtIsTheStateAtOfADeliveredRecord`,
`TestPropertyStateAtOnlyMovesWithTheState` (the random deliveries),
`TestTransitionsRefuseAZeroClock`.

### Records lost to storage corruption

A source delivery record that storage finds corrupt keeps only the key on its
first line: the delivery's identity. `NewLostRecord(discoveredAt)` makes the
record that replaces it: `delivery_unknown`, cause `lost_at_sender`, with
`KindLostAtSender` given once and already in `Reported`. Every receipt, delivery,
rejection, unknown result and "no record" answer that follows is ignored as
`IgnoreRecordLost`; only time closes it. A control signal has no record on the
sender's side, so there is nothing else to replace.

The replacement carries `Lost`, set only by `NewLostRecord` and cleared by no
transition. It is how a restored record is recognised as a replacement in every
state it can reach: `delivery_unknown` and `closed_unconfirmed` after `ReportBy`.
The cause cannot be that mark, because it is set only in `delivery_unknown` and a
closed replacement has none; judging a closed replacement by the shape of a
normally accepted delivery would find it corrupt on every restart, and it would
be replaced and reported once more, without end.

`ReportBy` is `LostReportBy(discoveredAt)`, `ReportBy(discoveredAt +
MaxAcceptBefore + clock tolerance)`: an upper bound of the lost record's own
`report_by`, because every accepted deadline lies at most that far after its
acceptance and the acceptance was not after the discovery. It holds because the
sending node does not accept a deadline the target's `Intake` would refuse, a
guarantee of the caller. `AcceptBefore` and `BacklogDeadline` are unknown and
zero. The record is closed by `Tick` after `ReportBy` like any other; nothing else
is timed for it, so `tickDeadlines` has no new entry.

What a record's cause and a replacement promise is stated once, in
`checkUnknownCause` (`ViolationUnknownCause`) and `checkLostShape`
(`ViolationLostShape`), which `Valid` calls: the cause is set exactly in
`delivery_unknown` (`causeFitsState`, shared with `change`) and is one of the
causes of the record's side, and a replacement has the shape `NewLostRecord`
gives it (no lease or block cause, no `AcceptBefore` or `BacklogDeadline`, a
`ReportBy`, the one notice already reported) and sits only in the states above. A
target record has neither the marker nor a loss cause, in any state. `Valid`
cannot check the value of `ReportBy`, which is `LostReportBy` of a discovery time
the record does not keep. A transition that would enter `delivery_unknown`
without a cause, or another state with one, is refused by `change` with
`ErrInvalidInput`. Tests: `TestUnknownCauseAndLostShapeInvariants` (one row per
promise, judged by `Valid`), `TestTargetNeverHoldsALossCause` (the target's
side, and that no fact makes one), `TestNewLostRecordMakesOnlyASenderReplacement`,
`TestChangeRefusesACauseThatDoesNotFitTheState`,
`TestLostMarkerIsSetByNewLostRecordAndNeverCleared`.

A target that loses its record keeps nothing, and neither does it keep a
replacement of the deduplication record: a target that lost its record of a
delivery no longer recognises a resend of it: a resend that arrives before the
delivery deadline is taken in as new and may be handed out a second time. This
needs the target to lose
the record after accepting, the source to hold no receipt (it lost the receipt, or
crashed before applying it), and the source to resend before `accept_before`;
a source with a receipt never resends, and one that was told the target holds no
record does not either. The channel's floor (a failure always produces feedback:
no silent loss, no false acknowledgement) is not touched, since the outcome is a
duplicate and never a silent loss, and a guard would mean keeping the very shape
this removes. A control signal is the same: a lost control record and a resend of
it can run the signal once more.

`Record.NeedsBody()` says whether a record still needs the bytes of its body: a
source record that is `queued`, and a target message record that is `accepted`
(queued or under a lease). A source record that needs a corrupt body is replaced
by `NewLostRecord`; a target record that needs it has no replacement and is removed
with an alert, as the store does for any damaged target record; every other record that
references the body keeps its state and drops only the reference. A control record
never needs the body: the body it references is the initial message, which the
delivery derived from it hands out.

### A target that holds no record

The sender pulls a delivery it has a receipt for until `report_by`. If the target
answers a pull by saying it holds no record of the delivery, the record was lost
at the target: the target did give the receipt. The sender's record applies the
answer with `EventNoRecordAtTarget`, a fact of the source only. Its rules, in
the order they are checked:

| The record | Result | Reason |
| ---------- | ------ | ------ |
| a target's | `ErrWrongSide` | only the sender reads the answer of a pull |
| the replacement of the sender's own lost record | `IgnoreRecordLost` | it ignores every later fact |
| final | `IgnoreAlreadyFinal` | the conclusion is reached, and a final source no longer pulls |
| `now` after `ReportBy` | `IgnoreRetryEnded` | the pull window is over, and a target's retention covers only the window |
| `queued`, `expired_unconfirmed` | `IgnoreNotApplicable` | there is no receipt: the target may never have taken the message in (authorization suspended stores nothing), and the expiry rule reports that case |
| `delivery_unknown`, any cause | `IgnoreNotApplicable` | the sender has been told "unknown"; the target forgetting the record later does not change that |
| `accepted` | `delivery_unknown`, cause `lost_at_target`, `KindLostAtTarget` once | the target gave a receipt and now says it holds no record |

The feedback is chosen by `TransitionFeedback` like every other, and each kind is
still given at most once. A delivery derived from a control signal is accepted
from its start, so it is lost at the target the same way. `envelope`'s
`Outbound.ApplyNoRecord` finds the tracked delivery and checks its node before
calling `Apply`; the rule itself lives only in `Record.Apply`. The engine hands it
each recipient a pull answer lists in `NoRecord` and does not send the message again
(see the engine reference). Tests:
`TestNoRecordAtTargetIsLostAtTargetOnlyFromAccepted` (every state, inside and
outside the window, the replacement, the target's records), `TestLossWordingVectors`,
`TestLostUnknownMessageKindBelongsToNoRecord`, `TestLostUnknownMessageItemsMergeBySender`,
`TestRecordRetainUntilCoversThePullWindowOfASlowerSender`.

### Deduplication

A request is identified by (sender node, message id, recipient endpoint) and a
body digest. The same key and digest returns the original record; the same key
with another digest is a conflict. A control signal's reply is stored once
in the dedup record and returned on every repeat. The stored reply outlives the
record's own retention and is kept until `reply.RetainFactsUntil`, because reply
permission still reads it.

## Why

- **A target that no longer knows a delivery is a loss, not silence.** A sender
  that holds a receipt pulls until `report_by` and then closes silently, and
  silence means delivered. If the target lost its record in between, that closing
  would be a false acknowledgement. The target's own word, "no record", is the
  only evidence the sender can have, so it is read as a loss, and only for an
  accepted delivery inside the pull window. Without a receipt the target may never
  have taken the message in, and after the window a target may have forgotten it
  legitimately.
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
- **A lost record is `delivery_unknown`, not a new state and not `rejected`.**
  "The result is unknown, do not assume it was not delivered" is exactly what a
  node that lost its record, or a sender whose target lost it, has to say, so the state stays and gains a cause that
  selects the wording. `rejected` would tell the sender the message was not
  delivered, about a delivery that may have been handed out. Silence would let
  the sender read the loss as delivery. A new state would need a new wire code,
  a new place in the transition table and the ordering, and would say the same
  thing.
- **A marker, not a missing field, says that a record is a replacement.** The
  replacement's shape (no `AcceptBefore`, no lease) is the shape of nothing else,
  but inferring it from zero fields would take a corrupt normal record for a
  replacement, and the cause is gone once it closes. An explicit `Lost`, set in
  one place and kept, is the cheapest thing that stays true.
- **The replacement is a way to start a record, not an event.** It has no earlier
  state to apply an event to, and if an event could produce it, any caller
  holding a healthy record could turn it into a lost one, which is the shortcut
  the target rules exist to prevent. So `Apply` refuses every route into the
  sender's own loss and `NewLostRecord` is the only way.
- **The loss wordings never say "handed".** A node that lost its record cannot
  know whether the target handed anything over, and the loss at the sender says
  nothing of the target at all. Both keep the closing warning of the other
  unknown wording so that the sender asks before resending. The loss at the target
  does not name a cause either: the sender knows only that the record is gone,
  which may be corruption, the owner removing the state, or a defect.
- **A lost source record ignores later facts.** It has no history: what the sender
  already heard (`Reported` holds only the loss) and what the record went through
  are gone, so the usual rules would misjudge a later fact, for example a target
  that later reports `rejected` would hit the illegal move from
  `delivery_unknown`. The sender was told the result is unknown, which stays
  true, so ignoring is the safe answer. The price is that a later `delivered`
  gives no correction; that is rare and does not mislead.
- **A target keeps no loss, and the sender is told by the target's own word.** A
  target that lost its record has no state to report and no shape to keep: a
  replacement on the target would be a second statement of what the sender's
  record already says after the "no record" answer. The price is stated above: a
  resend before the delivery deadline of a delivery the target lost is taken in as
  new. The older answer, a tombstone that replies "replay" to every request under
  the key, only covered a corruption whose first line could still be read, and
  not the commonest damage, an unreadable file, so it protected little for the
  shape it kept.
- **`LostReportBy` is an upper bound, not a guess.** Keeping a replacement for
  too short a time would close it before the sender's last pull; too long only
  keeps a small record a little longer. The bound reuses the cap on accepted
  deadlines and the report window, so no number is repeated.
- **"Still needs the body" is not "not final".** A record needs the body bytes
  when something still has to send or hand them out. A source that is already
  accepted, delivered or rejected needs no resend; a target `delivery_unknown`
  will not lease again, and a late acknowledgement may still correct it, which a
  replacement would throw away. So only the source records that `NeedsBody` are
  replaced. The rule lives in `delivery` because which states need body bytes is
  delivery semantics.
- **Corrections are separate kinds.** A delivery can be wrong twice (expired,
  then unknown); a single "correction" kind would silence the second.
- **Replacement, not correction, when news changes.** Telling a sender "the
  earlier notice was wrong" and then "rejected" is two notices for one fact.
- **Notices over the rate cap are held, not dropped.** Dropping an anomaly would
  make silence mean "possibly failed", defeating the rule above.
- **A merge window of one minute, ten notices per ten minutes.**
  (`DefaultFeedbackParams`.) Every notice is a message to the sender's endpoint
  and wakes its session under the prompt rules, so the notices must be merged
  and bounded. A typical burst, such as 20 messages sent at once to an
  unreachable target, arrives within seconds, so one minute merges it into one
  notice. The window adds to the time before the first news: an unreachable
  notice is due 2 minutes after the first failure and leaves up to 1 minute
  later, so it arrives within 3 minutes, long before the default delivery
  deadline of 10 minutes, while the sender can still send elsewhere or give up.
  A window of 2 minutes would delay that news for little more merging, and one
  of 30 seconds would split bursts that are slower than seconds. Ten notices per
  ten minutes is 60 an hour, the most prompts an endpoint receives per hour, so
  feedback cannot by itself exhaust the sender's prompts. One sender endpoint
  rarely meets more than 10 different kinds of trouble in 10 minutes. A burst of
  30 notices delays only the last by 20 minutes, and within one endpoint queue
  time of 30 minutes at most 30 notices wait in the sender's queue. Notices are
  not counted against the backlog limit, and this is why that is safe: 30 is
  far below the limit (200), so notices cannot crowd out messages. A cap of 5 would hold notices back for
  longer in ordinary use, and a cap of 20 would let feedback alone wake the
  session as often as a message every 30 seconds.
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
- **Over the backlog limit is a final rejection, not backpressure.** An endpoint
  is full because its agent is not reading, and only the endpoint's owner can
  change that. Backpressure would leave the sender retrying until its delivery
  deadline and then telling it only that the delivery expired unconfirmed, which
  the sender must read as "the target may have it". A rejection tells it at once,
  with a reason of its own, and the sender can act. Reusing `endpoint_declined`
  would blur "the endpoint refuses this sender" with "the endpoint is behind".
  The same rejection keeps the node's queue bounded per endpoint without any
  shared state between endpoints.
- **Why 200.** 200 is the node queue limit of 10,000 unsettled deliveries
  divided by the 50 endpoints a node is sized for, so one flooded endpoint takes
  at most a fiftieth of the node; a node may publish up to
  `publication.MaxEndpointsPerNode` endpoints, so it is the node queue limit, not
  the sum of endpoint limits, that bounds the node. Ordinary traffic stays far
  below it, a few messages a minute over the 30 minute default queue time is at
  most about 100.
- **The limit is judged last.** A sender that may not write to the endpoint
  must not learn how full its queue is, and a suspension stores nothing, so the
  limit has nothing to bound until the retry that is accepted.
- **Control signals and notices are not refused, and notices are not counted.**
  A control signal is never queued; refusing it would stop the owner from
  closing or interrupting the flooded endpoint. A system feedback notice that
  was refused would let the anomaly it reports go unheard, and the loop guard
  (no notice about a notice) would make that silent. Notices are not counted
  either, so ordinary messages are never refused because notices took the room.
  What bounds the notices is the feedback rate cap, at most 30 per endpoint in a
  queue time (far below the limit), so the queue of an endpoint can exceed the
  limit only by that.
  Counting them would instead let the node's own traffic refuse other senders'
  messages.
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
- **The package answers "when next".** A scheduler that guessed from the fields
  ("which instant matters in which state") would restate `Tick`'s rules outside
  the package, and miss a deadline the day `Tick` gains one. `NextTick` and
  `NextFlush` read the same deadline definitions and parameter arithmetic that
  `Tick` and `Flush` act on: the `deadline` methods of `Record`, all listed in
  `tickDeadlines` (`schedule.go`), and `mergeDueAt` and `rateReleaseAt` in
  `merge.go`, the one place each window boundary is defined (`countsTowardCap`
  and `rateFreeAt` both read `rateReleaseAt`). A timed rule added to `Tick` is a
  deadline method listed in `tickDeadlines`, and extends the property-test
  generator and `requiredNextTickPaths`. The answer and the action cannot drift apart. Asking too
  early would be harmless, since `Tick` only acts on instants that have come, but
  waiting for a fixed poll interval would delay settlement and feedback by that
  interval. The package still reads no clock: the caller owns the timer.
- **The lease deadlines apply only under a lease, the backlog deadline only
  without one.** That is how `Tick` consults them: a backlog deadline that passes
  while a lease is open does nothing until the lease ends, so reporting it would
  wake the scheduler for no change.

- **A restored record is checked by the contract, not by the store.** The
  transitions trust their input; a record read back from disk that is well
  formed but impossible (a lease on a delivered message, a rejection that
  followed a hand-out) would be acted on silently, and the first sign would be a
  wrong notice to a sender. The storage layer only knows the encoding, so the rule
  for what a delivery can look like lives here, next to the transitions that
  define it, and a change to a transition changes `Valid` in the same package.
  The one place where the check restates a rule instead of calling it is the set
  of feedback kinds that go with a state, which is the effect of
  `TransitionFeedback` seen from the outside; `TestRecordValidSourceFeedbackBookkeepingIsExact`
  enumerates every sender record the transitions reach and every record that
  differs only in those fields, and fails when `Valid` and the transitions
  disagree in either direction.
- **Exactness is tested where it can be enumerated.** For the lease and the
  deadlines the set of valid records is infinite, so there the tests are a table
  with a row per invariant, a property test over every record the random
  deliveries and control signals produce, and a mutation campaign with a
  measured detection rate per field and a floor on how many mutations each
  invariant must catch. A row is deleted from the table only with its invariant.
- **Rejected alternatives.** Computing the valid set by replaying the legal paths
  of `TransitionFeedback` at each call was rejected: a store that decodes
  thousands of records at start would pay for a search per record, and a
  table cached at package level is global state. Checking only the fields the
  engine reads was rejected because a record is trusted in whole once accepted.

## Where

- `internal/delivery/state.go`: states, sub-state, reject reasons, causes,
  the transition table. Tests: `TestCanTransition`, `TestStateClasses`,
  `TestRejectReasonNames`.
- `internal/delivery/feedback.go`: kinds, wording, `TransitionFeedback`. Tests:
  `TestTransitionFeedback`, `TestSummaries`, `TestLossWordingVectors`,
  `TestTransitionFeedbackChoosesKindByUnknownCause`,
  `TestStorageLossKindsMergeByMessage`, `TestLostUnknownMessageKindBelongsToNoRecord`,
  `TestLostUnknownMessageItemsMergeBySender`.
- A target that holds no record (`record.go`, `timing.go`; tests in
  `internal/delivery/norecord_test.go`): `TestNoRecordAtTargetIsLostAtTargetOnlyFromAccepted`,
  `TestRecordRetainUntilCoversThePullWindowOfASlowerSender`.
- Records lost to storage corruption (`record.go`, `timing.go`; tests in
  `internal/delivery/lost_test.go` and `targetloss_test.go`):
  `TestNewLostRecordMakesOnlyASenderReplacement`,
  `TestLostReportByBoundsEveryAcceptedDeadline`,
  `TestSourceLostRecordReportsOnceWithSenderWording`,
  `TestApplyRefusesEveryOtherWayIntoStorageLoss`,
  `TestSourceLostRecordIgnoresLaterFacts`,
  `TestStatusReportOfAnUnknownResultIsAHandOver`,
  `TestNeedsBodyByStateAndSide`, `TestTargetNeverHoldsALossCause`,
  `TestUnknownCauseAndLostShapeInvariants`, `TestCheckUnknownCauseJudgesTheSideOfALossCause`,
  `TestChangeRefusesACauseThatDoesNotFitTheState`, `TestLostMarkerIsSetByNewLostRecordAndNeverCleared`,
  and `TestPropertyLostRecordLifecycle` (fixed seed, a model written in the test:
  a replacement ignores every fact, reports at most once, and closes; `Valid`
  holds after every step).
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
  explicit mapping). Exempt traffic: `TestIntakeExemptTraffic`,
  `TestNoticeRecordIsTheRecordOfExemptTraffic`, `TestHandoutExemptRecord`; the
  marker's shape is `ViolationExemptShape` in the table of invalid records.
  The backlog limit: `Backlog`, `DefaultEndpointBacklogLimit`,
  `ValidateEndpointBacklogLimit`, `Record.OccupiesBacklogSlot`. Tests:
  `TestIntakeBacklogLimit` (the rule table), `TestIntakeBacklogInputs`,
  `TestEndpointBacklogLimit`, `TestOccupiesBacklogSlot`.
- `internal/delivery/dedup.go`: deduplication. Tests: `TestCheckDedup`,
  `TestDedupReply`.
- `internal/delivery/merge.go`: merge keys, `FeedbackQueue`,
  `FeedbackParams` and its decided values (`DefaultFeedbackParams`). Tests:
  `TestDefaultFeedbackParamsValidate`, `TestDefaultFeedbackParamsValues` (each
  value, named after its reason), `TestDefaultFeedbackParamsReasons` (the figures
  above recomputed from the defaults, including that the notices of one queue
  time stay below `DefaultEndpointBacklogLimit`), `TestDefaultFeedbackParamsAgainstHandoff`
  (the hourly cap stays within the prompt cap of an endpoint), `TestMergeKey`,
  `TestFeedbackParamsValidate`, `TestFeedbackQueue` (among its cases, the exact
  instant at which a sent notice leaves the rate window).
- `internal/delivery/valid.go`: `Record.Valid`, `Violation`,
  `InvalidRecordError`. Tests in `internal/delivery/valid_test.go`:
  `TestRecordValidRefusesImpossibleRecords` (one row per invariant, each starting
  from a record the package produces, and a check that every `Violation` has a
  row), `TestRecordValidAcceptsFreshRecords`, `TestRecordValidReportsTheFirstViolation`,
  `TestRecordValidSourceFeedbackBookkeepingIsExact` (the unknown cause is part of
  the enumerated record),
  `TestPropertyEveryProducedRecordIsValid` (the random deliveries and control
  signals of the other property tests, the replacement records from creation to
  closure and the source records that learn of a loss, through the observer they
  take, with coverage assertions per kind of record) and `TestPropertyMutatedRecordsAreCaught`.
- `internal/delivery/schedule.go`: the deadlines `Tick` consults,
  `Record.NextTick`, `FeedbackQueue.NextFlush`. Tests:
  `TestNextTickNamesEachDeadline` and `TestNextFlushNamesEachDeadline` (instants
  written out by hand), `TestNextFlushRefusesUnsetParameters`, and the property
  tests `TestPropertyNextTickAgreesWithFineStepTick` and
  `TestPropertyNextFlushAgreesWithFineStepFlush`, which run the real `Tick` and
  `Flush` at every minute and a nanosecond either side of it around random
  records and queues, and fail when a deadline kind is the answer fewer than 20
  times.
- Acceptance scenarios: `internal/delivery/scenario_test.go`
  (`TestScenario*`, among them `TestScenarioForcedClosure` and
  `TestScenarioStatusReportApplication`). The property test over random event
  sequences is `TestPropertyEveryDeliveryClosesAndNoFeedbackRepeats` in
  `property_test.go`: it checks, from the states the source went through, that
  every anomaly was reported, and it fails when a path of the rules (a renewal
  cap, a delivery_unknown, a correction) is reached fewer than 20 times.
