# envelope

The message envelope that travels from a sender's node to a target node, the
two signed records that report on a delivery (the status report and the control
reply), and the two rules by which a sender's node takes those records in. Read
[../README.md](../README.md) first for the conventions every contract package
follows, and
[../../docs/references/envelope/README.md](../../docs/references/envelope/README.md)
for the formats and the reasons behind them. Signed records are built on
[`record`](../record/README.md); rules about deliveries come from
[`delivery`](../delivery/README.md); the reply-permission decision that uses
this package is [`reply`](../reply/README.md).

## Entry points

| Name | Purpose |
| ---- | ------- |
| `Envelope`, `Limits`, `Encode(limits)`, `Decode(data, limits)` | The envelope and its deterministic CBOR form with strict decoding. `Limits` (longest body, most recipients) is a parameter a caller may tighten; a zero value is refused. |
| `DefaultLimits()` | The protocol values: a body of at most 64 KiB (`MaxBody`) and at most 50 recipients (`MaxRecipients`, read from `adapterproto.MaxRecipients`). |
| `Envelope.ValidateFor(node)`, `RecipientsOn(node)`, `TrimmedFor(node)` | What only the receiving node can check; the sender's trimming of recipients and sequence numbers for one target node. |
| `Envelope.Digest()` | The request digest the target stores for deduplication. |
| `Kind`, `Kind.Action()`, `Kind.Signal()`, `KindOfSignal` | The five kinds, the mapping to `authz.Action` (a control signal's action comes from `adapterreg.ActionOf`), and the mapping to the adapter protocol's control signals. |
| `ValidMessageID(id)` | The one rule for what a message id is (1 to `MaxIDLen` bytes of visible ASCII; an error wraps `ErrID`). The decoders apply it to every id on the wire; whoever stores an id outside an envelope, such as a record key, asks here instead of copying the rule. |
| `CheckEvidence(records)` | The shape of an evidence list: at most one control reply, then at most one status report. |
| `StatusReport` (its `Status` is the state, reject reason and block cause that `delivery` owns), `StatusReportDescriptor(node, accepted, now)`, `VerifyStatusReport(record, target, local, accepted, now)` | The signed status report. `SignedReport` is the verified value and remembers the node it was verified for. The report names the sending node, which must be `local`. |
| `ControlReply`, `ControlResult`, `CreateResult`, `ControlReplyDescriptor(node, accepted, now)`, `VerifyControlReply(record, target, local, accepted, now)` | The signed control reply for the four control signals, in the values of `adapterproto`. `SignedControlReply` is the verified value. The reply names the sending node, which must be `local`. `ControlResult.AdapterForm()` is the result as an agent reads it (the status of a message that carried the signal). |
| `Outbound`, `Tracked`, `Arrival` | The sender's record of a message it sent: one delivery per recipient, the derived delivery of an initial message, the digest of the accepted control reply, and what signed reports proved about arrival. |
| `Outbound.ApplyNoRecord(now, node, recipient)` | Applies a target's unsigned answer that it holds no record of the delivery to the tracked delivery (original or derived) on that node, through `delivery.EventNoRecordAtTarget`. Returns a `ReportEffect`: applied (the delivery may ignore it) or not tracked. |
| `Outbound.ApplyObservation(now, node, recipient, event)` | Applies a fact the sender observes itself (a query that found no receipt, failing contact, `report_by` passing) to the tracked delivery on that node, through `delivery.Record.Apply`. A fact the target reports is refused with `ErrObservation`. |
| `Outbound.Deliveries()` | Every delivery the sender tracks of the message, the original ones and then the derived one, so that a caller that asks a target about them all cannot leave the derived one out. |
| `Outbound.ApplyReport(now, report)` | The one rule by which a sender applies a verified status report, pulled or attached to a reply. Returns a `ReportEffect`: applied, not tracked, too late, or awaiting the control reply. |
| `HandleControlReply(now, outbound, reply, report)` | The one rule by which a sender applies a verified control reply, pulled or attached to a reply. |

Errors wrap the sentinels in `errors.go`; match them with `errors.Is`.
`ErrPeerContradiction` means the peer broke the contract (a second distinct
control reply, a reply to a rejected signal, a reply that names the signal's own
recipient as the session, a state that cannot follow): the caller stops pulling from
that peer for the message and logs it. `ErrBodyTooLarge` wraps `ErrBody`, so a
caller tells a body over `Limits.MaxBody` from a malformed one; an input over the
size derived from the limits is refused before parsing, with `record.ErrOversize`.

## Rules

- **Envelope format.** A deterministic CBOR map with integer keys from 1 and
  the type string `sidera/envelope/1` as key 1, decoded strictly (size limit
  first, then the hardened options, then a comparison with the re-encoding). It
  is not a signed record. The sender is not in it: the receiver knows it from
  the connection.
- **One place refuses.** `fromWire` is the only place that refuses an invalid
  envelope; `Encode` runs its own output through `Decode`, so what this package
  writes is what every receiver accepts.
- **Recipients.** `To` lists addresses: only the recipients on the target node
  unless the sender discloses recipients, then all of them. `MultiRecipient` is
  independent of both, so a target that refuses multi-recipient messages can
  tell even when `To` was trimmed to one entry; a disclosed list of one entry is
  the whole message and is not flagged. No recipient is the system endpoint
  (`ident.SystemEndpoint`): the decoder refuses it with `ErrSystemRecipient`,
  which wraps `ErrRecipients`, so a local Agent can neither send to it nor reply
  to a feedback message. A control signal has exactly one recipient, is not
  multi-recipient, and has neither `reply_to` nor evidence. Endpoint identifiers
  are distinct across the whole list because sequence numbers are keyed by
  endpoint. `Seq` has one entry per recipient on the target node; the structural
  check only requires that every entry is a recipient, and `ValidateFor` checks
  that the entries are exactly this node's recipients.
- **Deadlines.** `report_by` must equal `delivery.ReportBy(accept_before)`. The
  cap on `accept_before` is judged by `delivery.Intake`, not here.
- **Body.** Content type and body are both present or both absent. A message has
  a body. A `session.create` may have one: it is the initial message. The other
  control signals have none. The longest body is `Limits.MaxBody`; the
  endpoint-level size caps of the policy are separate.
- **Params.** Only `session.create` has params, `{1: preset}`, and it requires
  them; the preset follows `adapterproto.ValidatePresetName`.
- **A status report carries no cause of an unknown result.** A state
  delivery_unknown is a delivery handed over and nothing else: a target never
  reports a loss, and the sender learns that a target holds no record from the
  target's answer to a pull, applied with `Outbound.ApplyNoRecord`. The status
  report has eight keys; key 9, which once carried `lost_at_target`, is an unknown
  key and a payload with it is refused. A delivery_unknown report proves arrival.
- **Evidence.** At most two records: a control reply, then a status report. An
  envelope with evidence has a `reply_to`. Signatures are not checked in the
  envelope; `reply` checks them against the sender's own records.
- **Request digest.** Covers the kind, preset, content type, body, both
  deadlines, `reply_to`, `thread`, the multi-recipient flag and
  `disclose_recipients`, and, when recipients are disclosed, the whole `To` list
  in the sender's order (it is what the recipients see, and every path carries it
  untrimmed); not the recipient list when it is not disclosed (each path carries
  a list trimmed for its node), sequence numbers, send time or evidence, which
  differ between attempts of one request.
- **One mapping from kind to action.** `Kind.Action()`, which takes a control
  signal's action from `adapterreg.ActionOf`; no caller maps kinds itself.
- **Status report and control reply** are signed by the target node's network
  key, accepted by `record.NetworkKeyOf`. The node is not in the payload: the
  verifier names it from its own record of the message, and the verified value
  (`SignedReport`, `SignedControlReply`) keeps it, so a report cannot be applied
  to a delivery on another node. Both name the sending node, and the verifiers
  refuse one that names a node other than the local one. Wire values of states, reasons and causes are
  tables of their own, independent of the Go constants of `delivery`. The values
  of control replies are those of `adapterproto`, and their field rules are
  `adapterproto`'s own `Validate`.
- **Arrival.** A report proves arrival when its state is accepted, delivered or
  delivery_unknown. `Arrival` keeps the earliest delivery time of a delivered
  report and the earliest `reported_at` of a report that proves arrival; a later
  report never moves either forward. `reported_at` is the moment the target
  recorded the state, and a report sent again for an unchanged state repeats it
  under a fresh signature.
- **Applying a report.** `Outbound.ApplyReport` checks that the report is about
  the message, is from the node the delivery's endpoint is on and is not dated
  after now plus `timing.ClockTolerance`; applies it with
  `delivery.Record.ApplyStatusReport`; and adds its arrival facts. A report for
  an endpoint the sender has no delivery to changes nothing
  (`ReportNotTracked`). A report dated after the delivery's `report_by` plus
  `timing.ClockTolerance` changes nothing and is not an error
  (`ReportTooLate`): an honest target that slept past `report_by` settles the
  delivery when it wakes and dates its report with that moment, and such a
  report must not move the arrival facts or the reply window. A control
  signal's own delivery ends only through its control reply: while
  `ControlReplyDigest` is zero, a `delivered` report for the signal's recipient
  changes nothing and the effect is `ReportAwaitsControlReply`, so the caller
  keeps pulling the signal and gets the reply, which carries the result and the
  session endpoint (every other state of that report is applied as usual). On an error nothing changes.
- **Handling a control reply.** `HandleControlReply` is all or nothing. It ends
  the control signal's delivery with `delivery.EventDelivered`; a reply that
  names the signal's own recipient as the session endpoint is
  `ErrPeerContradiction`, judged before anything is built; for a
  `session.create` with a body whose outcome is created or unconfirmed it
  creates the delivery of the initial message to the session endpoint (accepted,
  with the signal's own deadlines) and binds the endpoint; it then applies the
  accompanying report. The record keeps the digest of the reply it accepted: the
  same digest again changes nothing, any other is `ErrPeerContradiction` (so a
  creation after a recorded failure builds no delivery). A reply after the
  signal closed as `closed_unconfirmed` still binds the session, and the derived
  delivery closes at its next tick, as every delivery that is accepted and was
  never told an outcome does: with the one `accepted_unconfirmed` notice. The pull path and the evidence path
  reach the same state in either order.

## What the caller must guarantee

- `now` is UTC and comes from the caller. Every returned value is committed in
  the transaction of the triggering input, with the effects the outcomes call
  for (feedback, events).
- The target node passed to `VerifyStatusReport` and `VerifyControlReply` is the
  node the message or signal was sent to, taken from the sender's own record,
  never from the record being verified; `local` is the verifier's own node.
  `accepted` is the network key accepted for the target node with its revocation
  status and the highest certificate serial seen for that node filled in; a
  certificate below that serial binds nothing.
- The receiving node calls `Envelope.ValidateFor` with its own node on every
  decoded envelope before acting on it: `Decode` cannot know which node reads it.
- Storage keeps, at least until `reply.RetainFactsUntil(report_by)`, the
  source's `Outbound` with its arrival facts and the digest of the accepted
  control reply, and the target's control reply payload and delivery facts,
  longer than delivery keeps its dedup records: the stored control reply is not
  purged with the deduplication record. The target
  stores payloads only and signs a status report or control reply with its
  current network key each time it sends one as evidence or in answer to a pull;
  the digest excludes signatures, so signing again does not change it.
- A caller that gets `ReportAwaitsControlReply` keeps pulling the control signal
  (its reply) as it does for any delivery that is not final.
- `Outbound` mirrors storage: one `Tracked` per recipient, source-side delivery
  records, the derived delivery only after a control reply bound it, and the
  digest of that reply with it. `Outbound.Validate` checks the invariants.
- `Limits` is passed by the caller. `DefaultLimits()` is the protocol values,
  which sender and receiver must share; a caller may tighten them, and a limit
  above them makes a node accept what others refuse. A zero field is refused.
- The request digest is stored by the target with the deduplication record of
  each delivery of the request, and compared with `delivery.CheckDedup`.

## Dependencies

The standard library, `delivery`, `authz`, `adapterreg`, `ident`, `frame`,
`record`, `timing`, `sdk/go/adapterproto` and `github.com/fxamacker/cbor/v2` (tests only for the
last). Nothing else; in particular no libp2p.
