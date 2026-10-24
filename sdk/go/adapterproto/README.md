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
| `EnumTables()`, `Patterns()`, `ApplicationCodes()` | The vocabularies the schema is compared with |
| `ErrorObject` | The `error` member of a response: `peer.Error` with the protocol's rules for its code, message and data |
| `ControlReply` and the `Control*` enums | The replies to the control signals, with their numeric codes |

## Rules

- **Strict.** `Decode` rejects unknown fields, a field name that differs in
  case, `null`, a missing required field, a repeated key, and an explicit empty
  string, zero, list or object for an optional field (omit it instead). `false`
  is accepted for a boolean. A pointer field accepts any value of its type,
  zero included.
- **Open capability declarations.** The one exception to the unknown-field rule:
  `Capabilities`, `RuntimeCapabilities`, `DirectCapabilities` and
  `DaemonCapabilities` ignore the members they have no field for, so a newer
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
