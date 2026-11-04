# nodeproto

The contract of the node-to-node request protocols: the protocol identifiers,
the request and answer of each, their byte limits and the framing rules. Read
[../README.md](../README.md) first for the conventions every contract package
follows, and
[the topic document](../../docs/references/network/README.md) for the formats
and the reasons behind them. This package is pure data: the network layer opens
the streams, applies the limits and the framing, and hands the decoded messages
to the engine.

## Entry points

| Name | Purpose |
| ---- | ------- |
| `ProtocolSend`, `ProtocolPull`, `ProtocolDirectory`, `LegacyProtocolSend`, `LegacyProtocolPull`, `LegacyProtocolDirectory`, `Protocols()` | The identifiers (2.0.0, framed; and the 1.0.0 ones of the nodes before it, kept until no such node is in use), defined here and nowhere else. They are not in the pre-authentication access table, so only members may use them. |
| `Offer(protocol)`, `IsLegacy(protocol)` | The identifiers a client proposes for a call, current first and legacy second; whether an identifier is a legacy one, whose streams are not framed. |
| `LimitOf(protocol)`, `Limit` | The byte limit of the request a server reads and of the answer a client reads, the same under both identifiers of a protocol; on a framed stream, the limit of the frame's payload. |
| `Outcome`, `OutcomeOK`, `OutcomeRefused`, `OutcomeFailed` | How the target handled a request; the first field of every answer. |
| `SendRequest`, `SendAnswer`, `DecodeSendRequest`, `DecodeSendAnswer` | Send: an envelope as bytes; the receipt. |
| `PullRequest`, `PullAnswer`, `DecodePullRequest`, `DecodePullAnswer` | Pull: a message and its recipients; reports, no-record endpoints and the control reply. |
| `DirectoryRequest`, `DirectoryAnswer`, `DirectoryEntry`, `DecodeDirectoryRequest`, `DecodeDirectoryAnswer` | Directory: a cursor and a page size; a page of entries. |
| `ReadMessage(r, max)` | Reads the one message of a legacy (1.0.0) stream to its end, at most `max` bytes. |
| `AppendFrame`, `ReadFrame` | One frame: an unsigned varint length, then the payload. A 2.0.0 member stream is one frame each way; the pairing protocol's streams are many. |

Every message has an `Encode` method and a `Decode…` function. Errors wrap
`ErrInvalid` for a rule of the protocol, `ErrFrame` for the framing, and the
errors of [`record`](../record/README.md) (`ErrOversize`, `ErrMalformed`,
`ErrNotCanonical`) for bytes that do not decode.

## Rules

- **Strict, canonical, one place.** A message is a map with integer keys,
  decoded with `record.UnmarshalStrict`, so the bytes of a message that decodes
  are its one canonical encoding. `Encode` runs its output through the decoder,
  so the rules of a message live in its decoder alone.
- **Shape, not verdict.** Records are read with `record.Decode`; nothing here
  verifies a signature or reads an envelope. Those belong to the engine and
  `envelope`.
- **Fields follow the outcome.** A reason is present exactly when the outcome is
  refused; a refused or failed answer carries no records, entries or cursor.
- **Limits come from the rules.** The send request is sized from
  `envelope.MaxEncoded()`; counts of recipients, records and entries are the
  bounds of `envelope`, `record` and `publication`.
- **No node in a message.** The sender node is the one the connection
  authenticated, and a directory entry names no node: the asker fills in the
  one it called.
