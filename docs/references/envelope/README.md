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
| 2 | `id` | 1 to 128 bytes of visible ASCII (`ValidMessageID`); the same for every retry, path and recipient |
| 3 | `to` | list of `{1: node, 2: endpoint}`; the recipients on the target node, or all of them when `disclose_recipients` is set |
| 4 | multi-recipient flag | the message as sent has more than one recipient, whatever `to` shows |
| 5 | `disclose_recipients` | the sender lets each recipient see the whole list |
| 6 | `kind` | 1 message, 2 `control.session.create`, 3 `control.session.close`, 4 `control.session.interrupt`, 5 `control.endpoint.status` |
| 7 | `reply_to` | optional id; never on a control signal |
| 8 | `thread` | optional id-shaped token; the channel does not interpret it |
| 9 | `evidence` | optional list of at most two embedded signed records: a control reply, then a status report; never on a control signal |
| 10 | `seq` | list of `{1: endpoint, 2: seq}` ascending by endpoint, one per recipient on the target node; `seq` from 1 |
| 11 | `sent_at` | Unix seconds |
| 12 | `accept_before` | Unix seconds |
| 13 | `report_by` | Unix seconds; must be `accept_before` plus the report offset of the delivery rules |
| 14, 15 | `content_type`, `body` | both present or both absent |
| 16 | `params` | `{1: preset}`, only for `control.session.create` |

Rules: a message has a body; a `session.create` may have one (the initial
message); the other control signals have neither body nor params; a control
signal has exactly one recipient, is not multi-recipient, and carries neither a
`reply_to` nor evidence (it answers no message, and its own result returns as
its control reply); several recipients need the multi-recipient flag; a
disclosed list of one entry is the whole message, so it is not flagged
multi-recipient (an undisclosed list trimmed to one entry may be); endpoint
identifiers are distinct across the list; no recipient is the system endpoint
(`ident.SystemEndpoint`), refused with `ErrSystemRecipient`, which wraps
`ErrRecipients`; evidence needs a `reply_to`. `from` is
not in the envelope. Encoding refuses what decoding refuses. The cap on
`accept_before` is judged by the delivery intake, not by the envelope. The
longest body and the most recipients are parameters (`Limits`), so a caller may
tighten them; `DefaultLimits()` returns the protocol values, 64 KiB and 50, and
a zero value is refused, never read as no limit.
A body over the longest is `ErrBodyTooLarge`, which wraps `ErrBody` (the content
type and body are inconsistent or not allowed), so a caller that reports the two
differently to the sender can tell them apart; an input longer than the size the
limits allow is refused before parsing, with the record package's oversize error.

The system endpoint is the sender of a node's own channel feedback and is never
addressed, so the one place that decodes recipients refuses it. A consequence for
local senders: an Agent cannot send to the system endpoint, and cannot reply to a
feedback message either, because a reply is addressed to the endpoint that sent
the message; both are refused when the source takes the message in. Refusing the
system endpoint as the *sender* of a request is not an envelope rule: the sender
is not in the envelope.

The receiving node adds what the bytes cannot say (`ValidateFor`, which the
receiving node must call on every decoded envelope): it has a recipient in `to`;
without disclosure all of `to` is on the node; the `seq` entries are exactly its
recipients'. The sender produces the per-node form with `TrimmedFor`.

The **request digest** (`Envelope.Digest`) is what the target stores with the
deduplication record of each delivery of the request. It covers the kind, the
preset, the content type, the body, the two deadlines, `reply_to`, `thread`, the
multi-recipient flag and `disclose_recipients`, and, when `disclose_recipients`
is set, the whole `to` list (each address as node then endpoint bytes, in the
sender's order). It covers nothing a retry may change (the recipient list as
trimmed for a path when it is not disclosed, sequence numbers, the send time, the
evidence). A retry with the same id and another digest is a conflict; this also
means a `message.send` under the id of a control signal's initial message
conflicts with the initial message's delivery.

### Status report

A record of type `sidera/status-report/1`, signed by the target node's network
key.

| Key | Field | Notes |
| --- | ----- | ----- |
| 1 | message | the id |
| 2 | recipient | the endpoint the message was delivered to; for the initial message of a created session, the session endpoint |
| 3 | state | 1 accepted, 2 delivered, 3 rejected, 4 delivery_unknown, 5 closed_unconfirmed |
| 4 | reject reason | exactly when rejected: 1 unauthorized, 2 endpoint_missing, 3 endpoint_closed, 4 endpoint_declined, 5 too_large, 6 expired, 7 backlog_expired, 8 second_check_failed, 9 malformed, 10 deadline_out_of_range, 11 backlog_full |
| 5 | block cause | present exactly when the delivery is blocked (only with accepted): 1 adapter_offline, 2 runtime_auth, 3 native_unavailable |
| 6 | reported_at | when the target recorded the state the report states |
| 7 | delivered_at | exactly when delivered; not after `reported_at` |
| 8 | sender | the node that sent the message, as the target saw it on the connection |

The blocked flag of `delivery.StatusReport` is the presence of key 5. A report
proves arrival when its state is accepted, delivered or delivery_unknown: a
delivery_unknown report is always a delivery handed over (see below).

**A target never reports a loss.** A target whose record of a delivery was lost to
storage has nothing to report, and the sender learns of it from the target's
answer to a pull that it holds no record (an unsigned answer: see the sender's
record, below), so a status report has no cause of an unknown result and the
state 4 means a delivery handed over and nothing else. Key 9, which once carried
`lost_at_target`, is not part of the format: a payload that has it is refused like
any other unknown key, whatever the state and the value, and no report is encoded
with it. It needs no reserved number: no report with it was ever issued outside
this repository's own builds, and the type string stays `sidera/status-report/1`
for that reason; once nodes are deployed, a new field is a new type string.

The reject reason codes are written down in this table, not taken from the
order of the Go constants of `delivery`, so reordering those never changes what
a signed report means; a new reason is appended with the next number and a code
already issued is never reused. Code 11, `backlog_full`, is the rejection of an
ordinary message that arrived at an endpoint whose queue was at its limit (see
the [delivery](../delivery/README.md) reference). A node that predates a code
refuses the report as malformed, so a sender and a target must run versions
that know the same codes. Tests: `TestStatusReportWireTablesAreIndependentOfGoOrder`
(fails when a reason has no pinned code) and the `rejected, backlog full` case of
`TestGoldenStatusReports`.

`reported_at` is the moment the target recorded the state, not the moment it
signed. A target that sends the report again for an unchanged state (as evidence
on a reply, or in answer to another pull) repeats the same `reported_at` under a
fresh signature, so the earliest report that proves arrival, which starts the
reply window, cannot be moved by resending.

The two times come from the target's delivery record: `reported_at` is
`delivery.Record.StateAt`, the moment the record entered the state, and
`delivered_at` is `Record.DeliveredAt()`, which is `StateAt` of a delivered
record (delivered is final, so the two are the same instant). The record writes
them in its transitions, so every state a report can state has its time.

### Control reply

A record of type `sidera/control-reply/1`, signed by the target node's network
key: message id (1), the control's recipient endpoint (2), issue time (3), the
sending node (4), and exactly one result under key 5 create, 6 close, 7 interrupt
or 8 status, so the key says which signal the reply is for.

| Result | Fields |
| ------ | ------ |
| create (5) | `{1: outcome, 2: session endpoint, 3: reason}`: outcomes 1 created, 2 unconfirmed, 3 failed; the endpoint (16 bytes) for created and unconfirmed; reasons 1 to 6 and 9 for failed, 7 and 8 for unconfirmed |
| close (6) | `{1: outcome, 2: reason}` |
| interrupt (7) | `{1: outcome, 2: reason}` |
| status (8) | `{1: activity, 2: queued, 3: conditions, 4: presets, 5: as_of}` |

The integer codes and the rules between a result's fields are those of the
adapter protocol (`adapterproto`); there is no second set of codes. Both records
carry the network key signature only: the signer is not in the payload, because
the verifier already knows from its own record which node the message went to,
and verifies against that node's accepted network key. A verified report or
reply (`SignedReport`, `SignedControlReply`) keeps that node.

The one node a payload does name is the **sending node**. A message is
identified at the target by (sender node, message id, recipient endpoint), the
key of its deduplication record, and two senders may use the same id. A report
or reply that did not name its sender could be applied by the wrong node to its
own message of the same id; the verifiers (`VerifyStatusReport`,
`VerifyControlReply`) take the local node and refuse a record that names
another.

The target stores the payload of a control reply with the signal's deduplication
record and signs it again, with its current network key, each time it sends it as
evidence or in answer to a pull. The digest excludes signatures, so a reply
signed again (after a key rotation, say) has the digest it had.

### The sender's record and applying a report

`Outbound` is the sender node's record of one message: the local sending
endpoint, the kind, whether the body was non-empty, the delivery deadline, one
`Tracked` per recipient (address, source-side delivery record, arrival facts),
the digest of the control reply it accepted and, for a `session.create` with a
body whose control reply named a session, one more `Tracked` for the initial
message. `Outbound.ApplyReport` is the one rule for taking
in a verified report, whether pulled or attached to a reply: it checks message,
signer node and date, applies it with `delivery.Record.ApplyStatusReport`, and
learns the arrival facts (earliest delivery time of a delivered report, earliest
`reported_at` of a report that proves arrival). A report whose state cannot
follow the delivery's is `ErrPeerContradiction`. One dated after the delivery's
`report_by` plus the clock tolerance is not applied and teaches the sender
nothing: `ApplyReport` returns `ReportTooLate` and no error, because a target
that slept past `report_by` settles late and its honest report carries that late
`reported_at`. In the reply evidence such a report contributes nothing and is not
an evidence error.

`Outbound.ApplyNoRecord(now, node, recipient)` takes in the other answer a pull
can bring: the target holds no record of the delivery. It finds the tracked
delivery of the endpoint (original or derived), checks that it is on the node
that answered (`ErrMismatch` otherwise), and applies
`delivery.EventNoRecordAtTarget`, whose rule lives in `delivery`. An endpoint the
sender has no delivery to is `ReportNotTracked`. The answer is not signed: it
comes only over the connection the network key authenticates and is no evidence
for a third party, the reason a status report is signed.

A control signal's own delivery ends only through its control reply. While the
sender has accepted none (`ControlReplyDigest` is zero), a `delivered` report for
the signal's recipient is not applied and `ApplyReport` returns
`ReportAwaitsControlReply`, so the caller keeps pulling the signal. Every other
state of that report is applied as usual, and once a reply was accepted a
`delivered` report is an ordinary one.

### Control reply handling

`HandleControlReply` is the one entry that takes a verified control reply in. In
one step, all or nothing:

1. a reply with another digest than the one already accepted for the signal is
   `ErrPeerContradiction`; the same digest again is the same reply and changes
   nothing;
2. the control signal's own delivery ends as delivered (`EventDelivered`),
   whatever the outcome: the target committed a reply (a reply that names the
   signal's own recipient as the session endpoint is `ErrPeerContradiction`,
   judged before anything is built);
3. for a `session.create` with a body and an outcome of created or unconfirmed,
   the delivery of the initial message to the session endpoint is created
   (accepted, with the signal's own `accept_before` and `report_by`, because
   the body and the signal were accepted together), and the endpoint is bound;
4. the accompanying status report, if any, is applied to the delivery it reports
   on.

A failed creation, a creation without an initial message, and every other
control signal bind nothing. A report for an endpoint the sender holds no
delivery for is not applied. The pull path and the evidence path call it, so
they reach the same state in either order. If the control signal had been judged
`expired_unconfirmed`, the reply corrects it (one correction notice) and the
session is still bound. If it had closed as `closed_unconfirmed`, it stays so,
the session is still bound, and the derived delivery, which shares the signal's
deadlines, closes silently at its next tick: a reply after the report window
makes the session known to the sender, so a reply from it can bind, and tells
the sender nothing it can act on.

`ErrPeerContradiction` is returned for a second distinct control reply, for a
reply to a signal the target is known to have rejected, for a reply that names the
signal's own recipient as the session it created, and for a report that cannot
follow the delivery's state. It means the peer broke the contract: the
sender stops pulling from it for this message and logs it. The error wraps the
specific cause.

### What each side keeps

Reply permission reads facts that outlive the delivery. The source node keeps the
outbound record, with its arrival facts and the digest of the accepted control
reply, until `reply.RetainFactsUntil(report_by)`: `report_by` plus
`timing.ClockTolerance` plus the reply window (24 hours). The target keeps the
control reply payload and the delivery facts it reports on for the same period,
which is longer than delivery keeps its dedup records and status reports, so the
stored control reply is not purged with the deduplication record. The target
stores payloads only; the signature is made when a report or reply is sent.

### Reply permission

`reply.Admit` decides whether a message that says it answers one of this node's
own is allowed back in, and its verdict is `authz.Request.Reply`. Without
permission a message is not refused: it is judged by the node's ordinary policy,
which may still admit it.

- For a plain message the replier is an original recipient. For a control signal
  the original recipient never has permission, because the signal's result
  already returns as its control reply; only the session endpoint bound by a
  verified control reply of a `session.create` with a non-empty body does.
- The reply is addressed to the endpoint that sent the message, and `reply_to`
  matches the node's own record of it.
- Evidence is read only if the replier's node is a recipient's node. It must be
  signed by that node's network key, name this node as the sending node and the
  message, and a report must be about the replier's own delivery. It is applied
  (control reply first) before the replier is looked up. Evidence that fails any
  of this is not applied, none of it, and is reported in `EvidenceError` for
  logging and alerting; the reply is judged on the facts the record already holds,
  as if it had carried none.
- The window is 24 hours from the delivery time the target reported; otherwise
  from the earliest signed report that proves arrival; otherwise there is no
  permission. A control reply never starts the window. The window ends at the
  instant start plus 24 hours.
- The owner can turn it off; the zero request is off.
- The verdict is made once, at intake, and stored with the target's record of the
  message, as the rest of the request that admitted it is. At the second check
  (`recv`) the target passes the stored verdict unchanged as
  `authz.Request.Reply` with `StageRecv`: the window governs entry, not reading
  (pausing is applied at intake only for the same reason), so a reply admitted
  inside the window is still handed out after it ended, and an explicit deny
  rule still refuses it. Deciding again at `recv` would turn a reply that was
  let in into a rejected delivery.

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
- **The system endpoint is refused as a recipient, in the decoder.** Every node
  must refuse it, whoever the sender is and whichever path the envelope took,
  because the system endpoint only sends: no node gives it an inbox, so a
  message addressed to it can never be read. Refusing it where every envelope is
  decoded makes that mistake fail at the source with its own reason, on every
  node and every path, instead of being taken in and ending as an undeliverable
  message (see [identity](../identity/README.md) for why the value is reserved
  in the shared identifier contract). The decoder is the one
  place every envelope passes, and `Encode` runs its output through it, so a
  sender cannot build one that its target would refuse. The reason has its own
  error under `ErrRecipients`, so a caller can tell a refused address from a
  malformed list. The cost is that a local Agent cannot answer a feedback
  message; feedback is a notice, and the Agent acts by sending a new message
  to the endpoint the feedback concerns.
- **A message has a non-empty body.** The channel carries messages for agents,
  and an empty message carries nothing, so the envelope refuses one rather than
  store, sign and deliver it. The adapter protocol states the same rule for
  `endpoint.send` (schema and `adapterproto` validation), so an adapter learns
  of it from its own SDK and not from a refusal by the node.
- **Control signal: one recipient, no body but for create.** A control signal is
  executed by one adapter and produces one reply; several recipients would bundle
  several results under one id. Body and content type on the other signals would
  have no reader, and an envelope field nobody reads is a place to hide data.
- **Request digest instead of a digest of the whole envelope.** A retry travels
  by other paths with other trimming and a new send time; the fields that make
  it the same request are the ones that decide what the target does. That
  includes `reply_to` (it decides reply permission), `thread`, the
  multi-recipient flag (a target may refuse multi-recipient messages) and
  `disclose_recipients` (what the recipients see): a retry that changed any of
  them under the same id would otherwise be taken for the request the target
  already holds. The evidence, the sequence numbers and the send time differ
  between attempts of one request and stay out, and so does the recipient list
  when it is not disclosed, because each path carries a list trimmed for its node.
  When recipients are disclosed every path carries the whole list untrimmed, and
  it is what the recipients are shown, so a retry under the same id that listed
  other recipients, or the same in another order, is another request and must
  conflict rather than be taken for the one already held.
- **The sending node in reports and replies.** The deduplication key is (sender
  node, message id, recipient endpoint), so a report that named only the id and
  the endpoint would fit every sender that used that id. Naming the sender makes
  a record usable only by the node it was issued for, and a node that receives
  one meant for another refuses it.
- **A control signal carries no `reply_to` and no evidence.** Only a message
  answers a message, and a control signal's result returns as its control reply.
  A field nobody reads is a place to hide data, and a control signal with
  evidence would need rules for which node signs it.
- **One control reply per signal, kept by digest.** The target commits one reply
  with the execution result and returns that payload on every repeat; the sender
  keeps the digest of the one it accepted. A different digest later means the
  peer changed its answer (a failure after it said created, say), and building
  the delivery of the initial message from the second answer would invent a
  delivery the first answer ruled out. The digest excludes signatures so a
  reply signed again with a newer network key is still the same reply.
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
- **Evidence that fails is reported, not punished.** The evidence is the target
  node's own automation attaching what it holds, so a failure means a
  malfunctioning or hostile peer, or a network key rotated since the evidence was
  signed; the first two call for an alert and the last is routine. The evidence
  is discarded and reported in `EvidenceError`, and the reply is judged on the
  facts already held. Withholding permission from a reply whose arrival and
  binding the sender already knows would turn a key rotation into lost replies,
  and the evidence adds nothing the sender needs when it knows the facts. A
  failed piece of evidence gives no permission it could not have had without it,
  so nothing is gained by forging it.
- **A control signal's delivery ends through its reply, not its report.** The
  control reply carries the result, and for a `session.create` the session
  endpoint from which the initial message's delivery is built. A `delivered`
  report of the signal ends the sender's record at once, the sender then stops
  pulling and never gets the reply: it loses the pair (initial message, session
  endpoint) and the result of `endpoint.status`. The report is therefore not
  applied before the reply was accepted, and the caller keeps pulling. The report
  is not wrong, only not the last word, so it is not a contradiction.
- **A report cannot extend the window past `report_by`.** A target that slept
  past a delivery's `report_by` settles the delivery when it wakes, at that
  later moment, so its honest report is dated after `report_by`. The report is
  not wrong, but taking its date as an arrival fact would let any peer, honest
  or not, move arrival times and the reply window with them outside the
  delivery. It is therefore ignored (`ReportTooLate`), not refused: the peer
  broke no contract.
- **A control signal's original recipient never replies.** A control signal's
  result already returns to the sender as the control reply, signed by the node
  that executed it; permission for the recipient endpoint as well would admit a
  second, unsigned channel back for a signal that has one. The session a
  `session.create` created is a new endpoint with its own conversation, and only
  that one can answer.
- **Window start.** Many replies come within the turn in which the agent read
  the message, before the message is acknowledged, so the delivery time does not
  exist yet; the report that proves arrival is the next best fact and is signed.
  The earliest such report is kept so that re-issued reports cannot extend a
  window. With nothing, there is no permission: failing closed. A report of
  rejection or a closed delivery does not prove that the agent could have seen
  the message.
- **Third parties.** A reply from a node that is not a recipient's has no
  permission and its evidence is not read, and from another endpoint of a
  recipient's node it has none because that endpoint is neither a recipient nor a
  bound session. The evidence
  of the real target replayed by a stranger is true and harmless to apply, and
  gives the stranger nothing.
- **The reply switch is off by default.** Its zero value is off, as every
  security-relevant zero is.

- **A body limit of 64 KiB and 50 recipients, as protocol values.** The two
  ends of a channel must agree on them: a body or a recipient list that one node
  may send and another refuses would fail only after the sender was told the
  message was accepted. They are therefore constants of the protocol, not node
  settings, and `Limits` is where a caller tightens them for itself. 64 KiB holds
  a long code excerpt or log in one message and is about eight parts at the
  default `recv` output budget; 256 KiB would be about 29 parts, and 16 KiB would
  not hold common excerpts, so larger content goes by another route the agent
  chooses. The recipient limit is the one the adapter protocol applies to a
  message (`adapterproto.MaxRecipients`); `MaxRecipients` reads it instead of
  repeating the number, so a message an adapter may send always fits an envelope.
  50 lets one message reach every endpoint of a node at the scale the channel is
  sized for, about 50 endpoints; a node may publish up to
  `publication.MaxEndpointsPerNode` (256), so this is a sizing choice, not a
  promise that one message can address every endpoint. Each recipient takes about
  96 bytes on the wire, under 5 KiB for 50, and the body is stored once however
  many recipients there are.

## Where

- `internal/envelope/envelope.go`: `Envelope`, `Limits`, `DefaultLimits` and the
  protocol values it returns (`MaxBody`, `MaxRecipients`), `Encode`,
  `Decode`, `ValidateFor`, `TrimmedFor`, `Digest`, `CheckEvidence`; `kind.go`:
  `Kind` and its mappings (a control signal's action is `adapterreg.ActionOf`
  of its signal, so that mapping has one home).
- `internal/envelope/statusreport.go`, `controlreply.go`, `verified.go`: the two
  records, their descriptors and the verified values.
- `internal/envelope/outbound.go`: `Outbound`, `Tracked`, `Arrival`,
  `ApplyReport`; `handle.go`: `HandleControlReply`.
- `internal/reply/reply.go`: `Admit`, `RetainFactsUntil`.
- Tests of the protocol values: `limits_defaults_test.go`
  (`TestDefaultLimitsValidate`, `TestDefaultLimitsValues`,
  `TestDefaultLimitsBehaviour`).
- Tests: `internal/envelope/golden_vectors_test.go` (bytes written out with CBOR
  diagnostic notation; node identifiers, digests and signatures computed by an
  independent script) with `golden_test.go`; `envelope_test.go`
  (`TestEnvelopeDecodeRules`, `TestEnvelopeStrictBytes`, `TestKindMappings`,
  `TestValidateFor`, `TestTrimmedForRandom`, `TestRequestDigest`,
  `TestRetryAndDeduplication`, `TestEncodeRefusesWhatDecodeRefuses`, and for the
  system endpoint the rows of `TestEnvelopeDecodeRules` and
  `TestSystemEndpointIsNoRecipient`);
  `records_test.go` (`TestStatusReportRules`, `TestStatusReportOfAnUnknownResultIsAHandOver`,
  `TestStatusReportProvesArrival`, `TestControlReplyRules`,
  `TestReportsAndRepliesAreSignedByTheTargetNodesNetworkKey`, which includes the
  sending-node check); `golden_test.go` (`TestGoldenRequestDigest`);
  `keynine_test.go` (`TestStatusReportRefusesKeyNine`); `norecord_test.go`
  (`TestApplyNoRecordFindsTheTrackedDeliveryOnItsNode`);
  `outbound_test.go` (`TestApplyReport`, `TestHandleControlReply`,
  `TestAControlSignalsDeliveryEndsOnlyThroughItsControlReply`,
  `TestAReportDatedAfterTheDeliverysReportByIsIgnored`);
  `envelope_test.go` (`TestATooLargeBodyIsToldApartFromAMalformedOne`,
  `TestValidMessageIDIsTheRuleTheDecoderAppliesToIds`, and the
  disclosed-list rows of `TestRequestDigest`); `fuzz_test.go`.
  `internal/reply/reply_test.go` holds the named acceptance scenarios
  (`TestAfterInboundTighteningAReplyAndASessionCreateReplyStillGetBack`,
  `TestTheSourceBehindNATAcceptsAReplyCarryingEvidence`,
  `TestAReplyFromTheNewSessionArrivingBeforeTheControlReplyIsAccepted`,
  `TestAForgedReplyToFromAThirdPartyIsRefused`), `TestWindow`,
  `TestEvidenceThatDoesNotVerifyIsDiscardedAndTheReplyIsJudgedWithoutIt`,
  `TestStaleEvidenceDoesNotTakePermissionFromAReplyWhoseFactsAreKnown`,
  `TestTheOriginalRecipientOfAControlSignalNeverGetsReplyPermission`,
  `TestTheIntakeVerdictOfAReplyStillStandsAtTheSecondCheck`,
  `TestRetainFactsUntilIsReportByPlusTheClockToleranceAndTheWindow`, and
  `property_test.go` compares random orders of replies, evidence and pulled
  reports with an oracle written separately; `fuzz_test.go` checks that
  permission earned through evidence implies the evidence verifies for the
  target node and this node as the sender.
- `internal/envelope/README.md`, `internal/reply/README.md`: entry points and
  what callers must guarantee.
