# adapterproto

The messages of the adapter protocol: the version range and its negotiation,
every method with its direction, the parameter and result types, the enums, the
application error codes, the capabilities and the control-signal values. It has no framing
and no transport; the daemon and every adapter use it to read and write what
travels over the line.

Read [protocol/README.md](../../../protocol/README.md) for the schema this
package is held to, and [the adapter-protocol reference](../../../docs/references/adapter-protocol/README.md)
for what each message means and why the protocol is shaped this way.

## Entry points

| Name | What it is for |
| ---- | -------------- |
| `Negotiate(requested, supported)` | The version for a connection: the highest in both ranges, or a `*NoCommonVersionError` carrying both ranges |
| `Methods()`, `Lookup(name)` | Each method's name, direction, kind, and constructors for its parameters and result |
| `MethodInfo.DecodeParams`, `DecodeResult` | Strict decode and validate for a method |
| `Decode(data, &msg)`, `Encode(msg)` | Strict decoding of any message type and validation before encoding |
| `msg.Validate()` | The rules of one message, on every type |
| `CancelRequestParams`, `MethodCancelRequest` | The params and name of `$/cancel_request`, which belong to the `peer` package together with the request id rule; listed here so the method table is complete |
| `ValidateDisplayName(s)`, `ValidateOptionalDisplayName(s)`, `DisplayNamePattern`, `MaxDisplayName` | The one rule of a display name: 1 to 128 characters, none a control, line or paragraph separator or bidirectional formatting control; the optional form lets an empty name be |
| `SentKeyWindow`, `ReceivedRedeliveryWindow` | The two windows an adapter that keeps its own records reasons about: counted from the moment it starts a send with no `accept_before_s`, how long the idempotency key is certainly still held (never resend with the same key after it); counted from the moment it first receives a message, how long the node may still hand it out again (its table of processed message ids may be pruned after it) |
| `DefaultAcceptBefore`, `MaxAcceptBefore`, `ClockTolerance`, `ReportByOffset`, `MaxQueueCap`, `LeaseRenewalCap`, `MaxRedeliveries`, `MaxLeases`, `MaxRedeliveryPeriod` | The delivery timing the windows follow from; the node's own packages take their values from here, so there is one definition |
| `EnumTables()`, `Patterns()`, `ApplicationCodes()` | The vocabularies the schema is compared with |
| `ErrorObject` | The `error` member of a response: `peer.Error` with the protocol's rules for its code, message and data |
| `CodeNodeBusy`, `BusyData`, `BusyReasons()` | The code of a request the node refused before any effect and that may be sent again later, its required data `{reason}` (any reason word is accepted) and the reasons this version of the node sends |
| `SendRefusedData`, `SendRefusedReason`, `SendRefusedReasons()` | The required data `{reason}` of a refused send (`CodeSendRefused`; any reason word is accepted) and the reasons this version of the node sends: `message_unknown` (a `reply_to` that names no message the endpoint holds, including one already swept, so an adapter answering late falls back to a plain send), `too_large`, `idempotency_conflict`, `identity_retired` and `invalid_argument` |
| `DirectoryParams`, `DirectoryResult`, `PeersParams`, `PeersResult`, `DeclareParams`, `DeclareResult`, `MaxDirectoryLimit` | The three requests a direct endpoint makes to read the directory and the nodes and to set its declaration, only when the daemon declared `direct_endpoint_access` (`DaemonCapabilities`). A directory read that got no page is a result (`DirectoryOutcome`, `DirectoryReason`), not an error; a page holds at most `MaxDirectoryLimit` (100) entries so that it fits a line |
| `ParamsReason`, `ParamsReasons()` | The words in the `reason` of the data of a `-32602` of `endpoint.directory` or `endpoint.declare` that an adapter may act on (`cursor_invalid`, `empty_patch`, `too_many_senders`, `duplicate_sender`) |
| `EndpointKind`, `EndpointAction`, `PeerTrust` | Enums of a directory entry and a peer. `EndpointAction` is not `ControlSignal`: an entry advertises `message.send`, which is not a signal a message may carry as control |
| `TrueOnly` | A flag that is sent as `true` or omitted; its decoder refuses an explicit `false` |
| `WakeInfo`, `MaxWakeEntries` | What an adapter needs to deliver a prompt to an attached session: at most 16 entries keyed like environment variables, opaque to the daemon, never written down or logged; carried by `SessionAttachParams` and by an attached `SessionAttach` entry |
| `ControlReply` and the `Control*` enums | The replies to the control signals, with their numeric codes |
| `MaxAckItems` | The existing limit of 1024 entries in an acknowledgement, shared with other readers of the same protocol values |

## Rules

- **Strict.** `Decode` rejects unknown fields, a field name that differs in
  case, `null`, a missing required field, a repeated key, and an explicit empty
  string, zero, list or object for an optional field (omit it instead). `false`
  is accepted for a boolean, except for a `TrueOnly` member (`first_turn_ended`,
  `attached`), which is written as `true` or left out. A pointer field accepts any value of its type,
  zero included.
- **Open capability declarations.** The one exception to the unknown-field rule:
  `Capabilities` (`shutdown_intent` among its members), `RuntimeCapabilities`,
  `DirectCapabilities` (`feedback` among its members: the adapter decodes the `feedback`
  member of a `PageMessage`, which the daemon otherwise leaves out of its pages) and `DaemonCapabilities` (`direct_endpoint_access`) ignore the members they have no field for, so a newer
  peer can declare a capability that this version does not know. Their known
  members are decoded as strictly as everything else, and a repeated key is
  refused even inside an ignored member. Ignoring a capability means not using
  the feature, which is the safe side.
- **A required list or map may be empty but is never nil.** A nil one would
  encode as `null`; `Validate` refuses it.
- **Optional means omitted.** Optional fields have `omitempty`; their zero value
  is "not provided".
- **Enums are strings.** The empty string is never valid.
  The control enums are small integers from 1 with names; `0` is never valid.
- **Times** an adapter reports are Unix milliseconds, for display; the daemon
  judges by its own clock.
- **Lengths** count characters, as JSON Schema does.
- **Numbers** are plain integers. A document that writes `1.0` or `1e2` is
  rejected by Go and, as an integer, accepted by the schema; documents in that
  form are outside the guarantee that the two agree.

## What the caller must guarantee

- Frames are valid UTF-8 and exactly one JSON value; `Decode` checks that again
  for the part it is given.
- A request is answered with a result or an error; what the daemon acts on is
  in the result, and the error only means the request was not understood or not
  allowed. Which failures leave the outcome open is the effect rule of package
  `peer` (`peer.NoEffect`, `peer.ResultUnknown`), not restated here.
- A message built by hand is validated (`Encode` does it) before it is sent.
