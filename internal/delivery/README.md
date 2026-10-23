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
| `NewSourceRecord`, `NewTargetRecord` | Create the sender's or the recipient node's `Record` of a delivery. |
| `Record.Apply(now, Event)` | Applies one fact or occurrence. Returns the new record and an `Outcome` (applied or ignored with a reason, the transition, and the feedback to give). |
| `Record.Tick(now)` | Everything due by time alone: lease expiry, backlog expiry, the settle deadline, closing after `report_by`. |
| `Record.Handout`, `Renew`, `Ack`, `NextPart`, `SortForRecv` | The pull lease: hand a chunk of a message out, renew its lease, acknowledge chunks, name the chunk to hand out next. |
| `Record.StatusReport`, `Record.ApplyStatusReport(now, StatusReport)` | What a target reports about a delivery, and the one rule by which the sender applies it. |
| `Intake(now, acceptBefore, Authorization)` | Target-side validation of the delivery deadline plus the authorization verdict (kind and, for a denial, the reason), for a request deduplication found new. |
| `CheckDedup`, `NewDedupRecord`, `DedupRecord.WithReply` | Deduplication, with the stored reply of a control signal. |
| `TransitionFeedback`, `Kind` | Which feedback a change of state calls for; the fixed wording of each kind. |
| `FeedbackQueue` (`Add`, `Flush`), `FeedbackParams`, `MergeKeyOf` | Merging and rate-capping pending feedback into notices. |
| `ReportBy`, `SettleBy`, `BacklogDeadline`, `DedupRetainUntil`, `StatusReportRetainUntil`, `IdempotencyKeyRetainUntil`, `ValidateEndpointMaxQueue` | Deadline and retention arithmetic. |

`Apply`, `Tick`, `Handout` and `Ack` change a record; every state change goes
through one transition function.

## States

`queued` (sender only), `accepted`, `delivered`, `rejected` (with a reason),
`expired_unconfirmed`, `delivery_unknown`, `closed_unconfirmed`, plus the
target-internal sub-state `notified` of `accepted`.

- Final: `delivered`, `rejected`, `closed_unconfirmed`.
- Open but correctable by later facts: `expired_unconfirmed` (to `accepted`,
  `delivered`, `rejected`, `delivery_unknown`) and `delivery_unknown` (to
  `delivered` only).
- On the target, `delivered` is reached only by acknowledging every chunk
  (`Ack`), and `delivery_unknown` only for a delivery whose body was handed out
  at least once. `Apply` refuses both shortcuts.
- A repeated or older fact is ignored with a reason, not an error. A fact that
  contradicts the state (a rejection for a delivered message) is
  `ErrIllegalTransition`.
- A target record never produces feedback; the sender's node does, from the
  status reports it pulls.

## Feedback

Only anomalies produce feedback: unreachable (network or suspended trust chain),
delivery blocked and its recovery, expired_unconfirmed, rejected,
delivery_unknown, and the corrections of expired_unconfirmed and
delivery_unknown. Normal progress and `closed_unconfirmed` never do.

- At most once per delivery per kind. Corrections are kinds of their own, so a
  delivery corrected twice is told twice.
- The expiry notice says the target may have received the message.
- A rejection or delivery_unknown that follows expired_unconfirmed replaces it
  instead of correcting it.
- Merging: anomalies of one message are one notice; unreachable hints and, as
  a notice of their own, expired_unconfirmed notices per (sender endpoint,
  target node); blocked reports per (sender endpoint, target endpoint), with the
  recovery as a separate notice. The merge window and the
  per-endpoint rate cap are undecided and live in `FeedbackParams`, which has
  no defaults. Notices over the cap are held back, never dropped.

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
`report_by - 5 min`; the sender closes whatever is still open after `report_by`.

## Closing a delivery by force

A target delivery that cannot go on for a reason other than the agent's own
handling (its backlog deadline passed, the second authorization check denied
it, its endpoint was closed) is closed by one rule. If its body was never handed
out it becomes `rejected` with that reason. If it was handed out at least once
it becomes `delivery_unknown`, because the agent may have seen the body and
`rejected` would tell the sender it was not delivered; a late acknowledgement
then corrects it to `delivered`. Handing out is what counts, not prompting: a
delivery that was only announced with a prompt is still rejected.

## Applying a status report

The sender's node applies a pulled `StatusReport` with
`Record.ApplyStatusReport`: the state fact first, then, when the record is
accepted, the blocked or unblocked fact. A blocked report pulled before any
receipt therefore still produces the receipt and the one blocked notice, and a
report of accepted and blocked pulled from `expired_unconfirmed` produces the
correction and the blocked notice.

## What the caller must guarantee

- `now` is UTC and comes from the caller; records are persisted atomically with
  the effects the outcome calls for.
- `report_by` in a target record equals `ReportBy(accept_before)` and the
  envelope is otherwise valid; this package does not validate the envelope.
- The authorization verdict is computed by the caller; a denial at intake
  carries the `RejectReason` the sender is to hear (the caller maps its policy's
  reasons to it). The token check of an acknowledgement is made by the caller
  and passed as `Ack.TokenMatches`.
- The caller splits bodies into chunks and passes the count and the chunk it is
  handing out (starting from `NextPart`) to `Handout`; the count is fixed at
  the first hand-out. Only chunks handed out can be acknowledged.
- Run `Tick` before `Handout`, and whenever a lease, backlog, settle or
  `report_by` instant may have passed.
- Whether delivery is blocked is decided by the caller (the prompt logic); this
  package only turns the decision into feedback.
- Digests passed to dedup are computed by the caller over everything that must
  match on a retry.
