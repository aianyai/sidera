# Node-to-node protocol

How one node asks another to take an envelope, to report on a delivery, or to list its endpoints: three protocols, one request and one answer each, each on its own stream. This topic covers the wire format, the byte limits and the meaning of the outcomes. The identifiers, messages and limits are defined once, in `internal/nodeproto`; the pre-authentication rules that decide who may open these streams are in [preauth](../preauth/README.md), and the two ways a call can get no answer are in [transport](../transport/README.md).

## What

### Protocols

| Protocol | Request | Answer |
| --- | --- | --- |
| `/sidera/send/1.0.0` | one envelope, from an endpoint of the sending node | the receipt: signed status reports and, for a control signal that has one, the control reply |
| `/sidera/pull/1.0.0` | one message identifier and the recipients on the target | the current signed reports, the recipients the target holds no record of, and the control reply if there is one |
| `/sidera/directory/1.0.0` | a continuation and a page size | a page of the open endpoints the target shows the asker |

None of them is in the access table of [preauth](../preauth/README.md), so each falls under "every other protocol": only members may open one. The node the request comes from is the node the secure handshake authenticated; no message names it, only the endpoint of that node the request is made for.

### One message and one stream

A request is one deterministic CBOR message and so is its answer. The client writes the request and closes its write side; the server reads to the end of the stream, answers and closes. The end of the stream is the end of the message, so nothing is length-prefixed and a reader stops at the limit of the protocol, plus one byte to tell a message that is exactly at the limit from one that is over it, and refuses more.

A message is a map with integer keys and is decoded with the strict rules of [records](../records/README.md): the size limit first, then no duplicate key, unknown field, indefinite length or tag, and the bytes must be the one canonical encoding. A field that must be present and is absent makes the bytes differ from the re-encoding and is refused; a field that may be absent is refused when it is written out empty. The sender writes only what the receiver accepts: `Encode` runs its own output through the decoder.

Records travel as the byte strings of their wire format. The message decoder reads their shape (`record.Decode` with the record limit) and nothing else; the signatures, the type of each record and whether it belongs to the call are the receiving engine's to check, as everywhere else.

A multi-frame stream, for a later protocol that needs one, prefixes each frame with its length as an unsigned varint. The prefix must be the shortest encoding of its value, a frame is at least one byte, and a length above the reader's bound is refused before any payload byte is read or allocated.

### Messages

Identifiers marked *text* are in their bech32m text form, which must be the form the identifier writes (the upper-case spelling is refused); the others are byte strings.

**Send**

```
request = { 1: sender endpoint (text), 2: envelope (bytes) }
answer  = { 1: outcome, 2: [report, ...], 3: control reply (optional), 4: reason (refused only) }
```

The envelope is 1 to `envelope.MaxEncoded()` bytes and is not read here. At most 50 reports.

**Pull**

```
request = { 1: message id (text), 2: [recipient endpoint, ...] }
answer  = { 1: outcome, 2: [report, ...], 3: [no-record endpoint, ...],
            4: control reply (optional), 5: reason (refused only) }
```

The message identifier follows `envelope.ValidMessageID`. The request names 1 to 50 distinct recipients. In an answer the reports and the no-record endpoints together are at most 50, and the no-record endpoints are distinct. A recipient of the request that is in neither list has nothing to report yet; the absence of a record is never inferred from a missing report.

**Directory**

```
request = { 1: sender endpoint (text), 2: cursor (optional), 3: limit (optional, 1 to 256) }
answer  = { 1: outcome, 2: [entry, ...], 3: next cursor (optional), 4: reason (refused only) }
entry   = { 1: endpoint, 2: kind (name), 3: display name, 4: description, 5: [action name, ...] }
```

A cursor is at most 256 bytes of visible ASCII and means something to the target only. An entry names its kind (`runtime`, `session`, `direct`) and the actions it accepts (`message.send`, `see`, ...) by name; the display name is at most 128 characters and the description at most 1024, both valid UTF-8. An entry carries no node: the asker fills in the node it called and never takes one from the answer. At most 256 entries, the most endpoints a node has.

### Outcomes

Every answer opens with an outcome:

| Outcome | Value | Meaning | Rest of the answer |
| --- | --- | --- | --- |
| ok | 1 | the target handled the request | the result |
| refused | 2 | the target refused the request for a reason it names | the reason, and nothing else |
| failed | 3 | the target could not handle the request and says nothing more; no receipt exists | nothing |

The reason is the name of the target engine's refusal reason (`message_conflict`, `node_queue_full`, ...), at most 64 bytes of lower-case letters, digits and underscores, so that a peer cannot put text of its own choosing into a log line. It is present exactly when the outcome is refused.

### Limits

Each protocol has a limit on the request a server reads and on the answer a client reads; a reader refuses more before it decodes anything. `nodeproto.LimitOf` returns them.

| Protocol | Request | Answer |
| --- | --- | --- |
| send | `envelope.MaxEncoded()` + 512 | 51 records of at most 64 KiB (a report per recipient and a control reply), + 512 |
| pull | 2 KiB | the same as send |
| directory | 512 | 256 entries of at most 5 KiB, + the cursor limit, + 512 |

## Why

- **One CBOR message per stream, not HTTP over streams.** The only calls are three request-and-answer pairs. HTTP's routing, status codes and headers would all go unused, its request body limit and its timeouts would have to be added, the library's HTTP binding is marked experimental and registers its handlers on the raw host, which bypasses the connection gate, and a stream-per-request needs no connection reuse the multiplexer does not already give. A single message with a half-close is the way the certificate exchange already works, shares its decoder, and gives each protocol its own identifier, which is exactly what the access table and the per-protocol limits of the resource manager are keyed by.
- **The records are decoded for shape here and verified by the engine.** The signature of a report depends on the keys the engine holds, and a second place that judged a record would be a second rule. The decoder only makes sure that what reaches the engine is a record, so a malformed answer is refused at the stream and not deep in a state machine.
- **The envelope limit is read from the envelope.** The send request is sized from `envelope.MaxEncoded()` plus a margin, so a limit raised in one place cannot leave the other refusing what the first allows.
- **Limits are derived from what the rules allow.** 50 recipients, 64 KiB per record and 256 entries are the bounds the envelope, record and publication rules already set; a limit written down separately would one day be below them. The record limit is generous for a status report, which is a few hundred bytes; it is the limit every record decoder shares, and a smaller one for this protocol alone would be a second rule.
- **The sender is a text and the recipients are bytes.** The sender endpoint is the one field a person reading a capture looks for, and a strict re-encoding does not catch a second spelling of a text identifier, so the decoder compares the text with the canonical form. Recipient lists repeat sixteen-byte values and stay compact as bytes.
- **The asker names the node of a directory entry, not the answer.** A peer could claim another node's endpoints; the entry has no field for it.

## Where

- `internal/nodeproto/protocol.go`: the identifiers, `LimitOf`, `Outcome`.
- `internal/nodeproto/send.go`, `pull.go`, `directory.go`: the messages with their `Encode` and `Decode…` functions; `codec.go`: the shared strict reading of records, endpoints and the encode-through-decode rule.
- `internal/nodeproto/frame.go`: `ReadMessage`, `AppendFrame`, `ReadFrame`.
- Tests: `golden_test.go` (bytes written by an independent encoder), `refuse_test.go` (one row per rule), `frame_test.go` (framing and the largest legal message of each kind against its limit), `fuzz_test.go` (one target per decoder).
