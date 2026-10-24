# Envelope, evidence and reply permission

The message envelope that carries one message or control signal from a sender's
node to a target node; the two signed records a target node issues about a
delivery (the status report and the control reply); the single rule by which a
sender's node takes a control reply in; and the decision whether a reply is
allowed back in. This document describes the committed code of
`internal/envelope` and `internal/reply`. The record format they build on is in
[records](../records/README.md), the delivery states in [delivery](../delivery/README.md),
and the access policy that reads the reply fact in [policy](../policy/README.md).

## What

### Envelope

A deterministic CBOR map with integer keys from 1. It is not a signed record: the
sender is known from the connection, and signing would add nothing to who the
sender is. Key 1 is the type string `sidera/envelope/1`; an incompatible change
is a new type string.

| Key | Field | Notes |
| --- | ----- | ----- |
| 1 | type | `sidera/envelope/1` |
| 2 | `id` | 1 to 128 bytes of visible ASCII; the same for every retry, path and recipient |
| 3 | `to` | list of `{1: node, 2: endpoint}`; the recipients on the target node, or all of them when `disclose_recipients` is set |
| 4 | multi-recipient flag | the message as sent has more than one recipient, whatever `to` shows |
| 5 | `disclose_recipients` | the sender lets each recipient see the whole list |
| 6 | `kind` | 1 message, 2 `control.session.create`, 3 `control.session.close`, 4 `control.session.interrupt`, 5 `control.endpoint.status` |
| 7 | `reply_to` | optional id |
| 8 | `thread` | optional id-shaped token; the channel does not interpret it |
| 9 | `evidence` | optional list of at most two embedded signed records: a control reply, then a status report |
| 10 | `seq` | list of `{1: endpoint, 2: seq}` ascending by endpoint, one per recipient on the target node; `seq` from 1 |
| 11 | `sent_at` | Unix seconds |
| 12 | `accept_before` | Unix seconds |
| 13 | `report_by` | Unix seconds; must be `accept_before` plus the report offset of the delivery rules |
| 14, 15 | `content_type`, `body` | both present or both absent |
| 16 | `params` | `{1: preset}`, only for `control.session.create` |

Rules: a message has a body; a `session.create` may have one (the initial
message); the other control signals have neither body nor params; a control
signal has exactly one recipient and is not multi-recipient; several recipients
need the multi-recipient flag; endpoint identifiers are distinct across the
list; evidence needs a `reply_to`. `from` is not in the envelope. The cap on
`accept_before` is judged by the delivery intake, not by the envelope. The
longest body and the most recipients are parameters (`Limits`) without defaults.

The receiving node adds what the bytes cannot say (`ValidateFor`): it has a
recipient in `to`; without disclosure all of `to` is on the node; the `seq`
entries are exactly its recipients'. The sender produces the per-node form with
`TrimmedFor`.

The **request digest** (`Envelope.Digest`) is what the target stores with the
deduplication record of each delivery of the request. It covers the kind, the
preset, the content type, the body and the two deadlines, and nothing a retry may
change (the recipient list as trimmed for a path, sequence numbers, the send
time, the evidence, the thread). A retry with the same id and another digest is a
conflict; this also means a `message.send` under the id of a control signal's
initial message conflicts with the initial message's delivery.

### Status report

A record of type `sidera/status-report/1`, signed by the target node's network
key.

| Key | Field | Notes |
| --- | ----- | ----- |
| 1 | message | the id |
| 2 | recipient | the endpoint the message was delivered to; for the initial message of a created session, the session endpoint |
| 3 | state | 1 accepted, 2 delivered, 3 rejected, 4 delivery_unknown, 5 closed_unconfirmed |
| 4 | reject reason | exactly when rejected: 1 unauthorized, 2 endpoint_missing, 3 endpoint_closed, 4 endpoint_declined, 5 too_large, 6 expired, 7 backlog_expired, 8 second_check_failed, 9 malformed, 10 deadline_out_of_range |
| 5 | block cause | present exactly when the delivery is blocked (only with accepted): 1 adapter_offline, 2 runtime_auth, 3 native_unavailable |
| 6 | reported_at | when the target issued it |
| 7 | delivered_at | exactly when delivered; not after `reported_at` |

The blocked flag of `delivery.StatusReport` is the presence of key 5. A report
proves arrival when its state is accepted, delivered or delivery_unknown.

### Control reply

A record of type `sidera/control-reply/1`, signed by the target node's network
key: message id (1), the control's recipient endpoint (2), issue time (3), and
exactly one result under key 5 create, 6 close, 7 interrupt or 8 status, so the
key says which signal the reply is for.

| Result | Fields |
| ------ | ------ |
| create (5) | `{1: outcome, 2: session endpoint, 3: reason}`: outcomes 1 created, 2 unconfirmed, 3 failed; the endpoint (16 bytes) for created and unconfirmed; reasons 1 to 6 for failed, 7 and 8 for unconfirmed |
| close (6) | `{1: outcome, 2: reason}` |
| interrupt (7) | `{1: outcome, 2: reason}` |
| status (8) | `{1: activity, 2: queued, 3: conditions, 4: presets, 5: as_of}` |

The integer codes and the rules between a result's fields are those of the
adapter protocol (`adapterproto`); there is no second set of codes. Both records
carry the network key signature only: the node is not in the payload, because the
verifier already knows from its own record which node the message went to, and
verifies against that node's accepted network key. A verified report or reply
(`SignedReport`, `SignedControlReply`) keeps that node.

### The sender's record and applying a report

`Outbound` is the sender node's record of one message: the local sending
endpoint, the kind, whether the body was non-empty, the delivery deadline, one
`Tracked` per recipient (address, source-side delivery record, arrival facts)
and, for a `session.create` with a body whose control reply named a session, one
more for the initial message. `Outbound.ApplyReport` is the one rule for taking
in a verified report, whether pulled or attached to a reply: it checks message,
signer node and date, applies it with `delivery.Record.ApplyStatusReport`, and
learns the arrival facts (earliest delivery time of a delivered report, earliest
issue time of a report that proves arrival).

### Control reply handling

`HandleControlReply` is the one entry that takes a verified control reply in. In
one step, all or nothing:

1. the control signal's own delivery ends as delivered (`EventDelivered`),
   whatever the outcome: the target committed a reply;
2. for a `session.create` with a body and an outcome of created or unconfirmed,
   the delivery of the initial message to the session endpoint is created
   (accepted, with the signal's own `accept_before` and `report_by`, because
   the body and the signal were accepted together), and the endpoint is bound;
3. the accompanying status report, if any, is applied to the delivery it reports
   on.

A failed creation, a creation without an initial message, and every other
control signal bind nothing. A report for an endpoint the sender holds no
delivery for is not applied. A reply that names another endpoint than the bound
one is refused. The pull path and the evidence path call it, so they reach the
same state in either order. If the control signal had been judged
`expired_unconfirmed`, the reply corrects it (one correction notice) and the
session is still bound.

### Reply permission

`reply.Admit` decides whether a message that says it answers one of this node's
own is allowed back in, and its verdict is `authz.Request.Reply`.

- The replier is an original recipient of the message, or the session endpoint
  bound by a verified control reply of a `session.create` with a non-empty body.
- The reply is addressed to the endpoint that sent the message, and `reply_to`
  matches the node's own record of it.
- Evidence is read only if the replier's node is a recipient's node. It must be
  signed by that node's network key, name the message, and a report must be about
  the replier's own delivery. It is applied (control reply first) before the
  replier is looked up. Evidence that fails refuses the reply.
- The window is 24 hours from the delivery time the target reported; otherwise
  from the earliest signed report that proves arrival; otherwise there is no
  permission. A control reply never starts the window. The window ends at the
  instant start plus 24 hours.
- The owner can turn it off; the zero request is off.

## Why

- **Unsigned envelope.** The sender is known from the connection and the sending
  node's voucher; a signature adds nothing to authentication, and signing for
  relays that store and forward is not decided. If it is, it arrives as a new
  record type without touching the envelope.
- **`to` as addresses, and a separate multi-recipient flag.** The envelope is
  trimmed to the target node's recipients, so the length of `to` cannot tell a
  target that refuses multi-recipient messages whether the message had several.
  Disclosure needs the full list in the same field; trimming is a sender
  decision (`TrimmedFor`), and the receiver checks it (`ValidateFor`).
- **Control signal: one recipient, no body but for create.** A control signal is
  executed by one adapter and produces one reply; several recipients would bundle
  several results under one id. Body and content type on the other signals would
  have no reader, and an envelope field nobody reads is a place to hide data.
- **Request digest instead of a digest of the whole envelope.** A retry travels
  by other paths with other trimming and a new send time; the fields that make
  it the same request are the ones that decide what the target does.
- **Initial message as a second delivery under the same id.** The body is
  delivered by `recv` to the new session's endpoint, so it is a delivery to that
  endpoint with its own states and report; the sender cannot know the endpoint
  when it sends, so it creates that delivery when the signed control reply names
  it. The alternatives were rejected: carrying the body's fate in the control
  signal's own record puts the queue and lease of one endpoint under another's
  record and makes a backlog expiry of the body look like a rejected creation,
  which invites a duplicate session; a separate id for the initial message needs
  a placeholder key renamed later and an envelope field to link them.
- **One entry for control replies.** The reply can arrive by a pull or as
  evidence on the new session's first reply, in either order and more than once.
  If two code paths applied it, they would drift and a reply from the new session
  would be judged on a state the pulled reply never set up. One function, all or
  nothing, idempotent.
- **Verified values that remember the node.** A report signed by node X about an
  endpoint of node Y must not change Y's delivery; checking that at every call
  site is a rule enforced many times. The verified value carries the node and the
  one rule checks it.
- **Evidence is checked, not skipped.** A reply with evidence that does not
  verify is refused even if the node already knows enough to admit it. The
  sender of evidence is the node's own automation, so a failure means a
  malfunctioning or hostile peer, and silence would hide it.
- **Window start.** Many replies come within the turn in which the agent read
  the message, before the message is acknowledged, so the delivery time does not
  exist yet; the report that proves arrival is the next best fact and is signed.
  The earliest such report is kept so that re-issued reports cannot extend a
  window. With nothing, there is no permission: failing closed. A report of
  rejection or a closed delivery does not prove that the agent could have seen
  the message.
- **Third parties.** A reply from a node that is not a recipient's is refused
  before its evidence is read, and from another endpoint of a recipient's node it
  is refused because it is neither a recipient nor a bound session. The evidence
  of the real target replayed by a stranger is true and harmless to apply, and
  gives the stranger nothing.
- **Parameters without defaults.** The longest body and the most recipients are
  not decided. The reply switch's zero value is off, as every security-relevant
  zero is.

## Where

- `internal/envelope/envelope.go`: `Envelope`, `Limits`, `Encode`, `Decode`,
  `ValidateFor`, `TrimmedFor`, `Digest`, `CheckEvidence`; `kind.go`: `Kind` and
  its mappings.
- `internal/envelope/statusreport.go`, `controlreply.go`, `verified.go`: the two
  records, their descriptors and the verified values.
- `internal/envelope/outbound.go`: `Outbound`, `Tracked`, `Arrival`,
  `ApplyReport`; `handle.go`: `HandleControlReply`.
- `internal/reply/reply.go`: `Admit`.
- Tests: `internal/envelope/golden_vectors_test.go` (bytes written out with CBOR
  diagnostic notation; node identifiers, digests and signatures computed by an
  independent script) with `golden_test.go`; `envelope_test.go`
  (`TestEnvelopeDecodeRules`, `TestEnvelopeStrictBytes`, `TestKindMappings`,
  `TestValidateFor`, `TestTrimmedForRandom`, `TestRequestDigest`,
  `TestRetryAndDeduplication`); `records_test.go` (`TestStatusReportRules`,
  `TestControlReplyRules`, `TestReportsAndRepliesAreSignedByTheTargetNodesNetworkKey`);
  `outbound_test.go` (`TestApplyReport`, `TestHandleControlReply`); `fuzz_test.go`.
  `internal/reply/reply_test.go` holds the named acceptance scenarios
  (`TestAfterInboundTighteningAReplyAndASessionCreateReplyStillGetBack`,
  `TestTheSourceBehindNATAcceptsAReplyCarryingEvidence`,
  `TestAReplyFromTheNewSessionArrivingBeforeTheControlReplyIsAccepted`,
  `TestAForgedReplyToFromAThirdPartyIsRefused`), `TestWindow`,
  `TestEvidenceThatIsNotSignedByTheTargetNodeAboutThisMessageIsRefused`, and
  `property_test.go` compares random orders of replies, evidence and pulled
  reports with an oracle written separately.
- `internal/envelope/README.md`, `internal/reply/README.md`: entry points and
  what callers must guarantee.
