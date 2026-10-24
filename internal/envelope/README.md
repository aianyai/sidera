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
| `Envelope`, `Limits`, `Encode(limits)`, `Decode(data, limits)` | The envelope and its deterministic CBOR form with strict decoding. `Limits` (longest body, most recipients) has no defaults; a zero value is refused. |
| `Envelope.ValidateFor(node)`, `RecipientsOn(node)`, `TrimmedFor(node)` | What only the receiving node can check; the sender's trimming of recipients and sequence numbers for one target node. |
| `Envelope.Digest()` | The request digest the target stores for deduplication. |
| `Kind`, `Kind.Action()`, `Kind.Signal()`, `KindOfSignal` | The five kinds, the one mapping to `authz.Action`, and the mapping to the adapter protocol's control signals. |
| `CheckEvidence(records)` | The shape of an evidence list: at most one control reply, then at most one status report. |
| `StatusReport`, `StatusReportDescriptor(node, accepted, now)`, `VerifyStatusReport` | The signed status report. `SignedReport` is the verified value and remembers the node it was verified for. |
| `ControlReply`, `ControlResult`, `CreateResult`, `ControlReplyDescriptor(node, accepted, now)`, `VerifyControlReply` | The signed control reply for the four control signals, in the values of `adapterproto`. `SignedControlReply` is the verified value. |
| `Outbound`, `Tracked`, `Arrival` | The sender's record of a message it sent: one delivery per recipient, the derived delivery of an initial message, and what signed reports proved about arrival. |
| `Outbound.ApplyReport(now, report)` | The one rule by which a sender applies a verified status report, pulled or attached to a reply. |
| `HandleControlReply(now, outbound, reply, report)` | The one rule by which a sender applies a verified control reply, pulled or attached to a reply. |

Errors wrap the sentinels in `errors.go`; match them with `errors.Is`.

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
  tell even when `To` was trimmed to one entry. A control signal has exactly one
  recipient and is not multi-recipient. Endpoint identifiers are distinct across
  the whole list because sequence numbers are keyed by endpoint. `Seq` has one
  entry per recipient on the target node; the structural check only requires
  that every entry is a recipient, and `ValidateFor` checks that the entries are
  exactly this node's recipients.
- **Deadlines.** `report_by` must equal `delivery.ReportBy(accept_before)`. The
  cap on `accept_before` is judged by `delivery.Intake`, not here.
- **Body.** Content type and body are both present or both absent. A message has
  a body. A `session.create` may have one: it is the initial message. The other
  control signals have none. The longest body is `Limits.MaxBody`; the
  endpoint-level size caps of the policy are separate.
- **Params.** Only `session.create` has params, `{1: preset}`, and it requires
  them; the preset follows `adapterproto.ValidatePresetName`.
- **Evidence.** At most two records: a control reply, then a status report. An
  envelope with evidence has a `reply_to`. Signatures are not checked in the
  envelope; `reply` checks them against the sender's own records.
- **One mapping from kind to action.** `Kind.Action()`; no caller maps kinds
  itself.
- **Status report and control reply** are signed by the target node's network
  key, accepted by `record.NetworkKeyOf`. The node is not in the payload: the
  verifier names it from its own record of the message, and the verified value
  (`SignedReport`, `SignedControlReply`) keeps it, so a report cannot be applied
  to a delivery on another node. Wire values of states, reasons and causes are
  tables of their own, independent of the Go constants of `delivery`. The values
  of control replies are those of `adapterproto`, and their field rules are
  `adapterproto`'s own `Validate`.
- **Arrival.** A report proves arrival when its state is accepted, delivered or
  delivery_unknown. `Arrival` keeps the earliest delivery time of a delivered
  report and the earliest issue time of a report that proves arrival; a later
  report never moves either forward.
- **Applying a report.** `Outbound.ApplyReport` checks that the report is about
  the message, is from the node the delivery's endpoint is on and is not dated
  after now plus `timing.ClockTolerance`; applies it with
  `delivery.Record.ApplyStatusReport`; and adds its arrival facts. A report for
  an endpoint the sender has no delivery to changes nothing
  (`ReportNotTracked`). On an error nothing changes.
- **Handling a control reply.** `HandleControlReply` is all or nothing. It ends
  the control signal's delivery with `delivery.EventDelivered`; for a
  `session.create` with a body whose outcome is created or unconfirmed it
  creates the delivery of the initial message to the session endpoint (accepted,
  with the signal's own deadlines) and binds the endpoint; it then applies the
  accompanying report. A reply that names a different endpoint than the bound
  one, or none, is `ErrDerivedConflict`. Handling the same reply twice changes
  nothing the second time, and the pull path and the evidence path reach the same
  state in either order.

## What the caller must guarantee

- `now` is UTC and comes from the caller. Every returned value is committed in
  the transaction of the triggering input, with the effects the outcomes call
  for (feedback, events).
- The node passed to `VerifyStatusReport` and `VerifyControlReply` is the node
  the message or signal was sent to, taken from the sender's own record, never
  from the record being verified. `accepted` is the network key accepted for that
  node with its revocation status filled in.
- `Outbound` mirrors storage: one `Tracked` per recipient, source-side delivery
  records, the derived delivery only after a control reply bound it.
  `Outbound.Validate` checks the invariants.
- `Limits` is chosen by the caller; there is no default body limit.
- The request digest is stored by the target with the deduplication record of
  each delivery of the request, and compared with `delivery.CheckDedup`.

## Dependencies

The standard library, `delivery`, `authz`, `ident`, `frame`, `record`, `timing`,
`sdk/go/adapterproto` and `github.com/fxamacker/cbor/v2` (tests only for the
last). Nothing else; in particular no libp2p.
