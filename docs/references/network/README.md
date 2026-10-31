# Node-to-node protocol

How one node asks another to take an envelope, to report on a delivery, or to list its endpoints: three protocols, one request and one answer each, each on its own stream. This topic covers the wire format, the byte limits and the meaning of the outcomes, and the runtime that carries them: the libp2p host, the gate in front of every stream, the connection table, the certificate exchange on a connection, dialing, and how a call's failures map to the transport's errors. The identifiers, messages and limits are defined once, in `internal/nodeproto`; the pre-authentication rules that decide who may open these streams are in [preauth](../preauth/README.md), and the two ways a call can get no answer are in [transport](../transport/README.md).

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

## The runtime

The package `internal/node/network` (layer 5, beside the engine; neither imports the other) holds everything below. It applies the decisions of `internal/preauth`, speaks the messages of `internal/nodeproto` and decides nothing about trust: the engine hands it the facts of a decision as a snapshot and receives what arrived through an interface the network package defines.

### The host

The libp2p host is assembled from its sub-packages, not made with `libp2p.New`: swarm, upgrader, TCP with Noise, QUIC (with the library's own TLS handshake), yamux, and the basic host with identify and ping. The node's network key is the host's identity, so a connection is authenticated by the very key the node's certificate names, and a peer's identifier maps back to the network key it embeds. The NAT manager, the relay service, hole punching and autonat are off: nothing opens a router port or reaches a node through a relay. The resource manager runs with the library's default limits for members and a smaller tier for a peer until its exchange has made it a member; the per-peer limit object is stable and moves between the tiers.

### The gate

Every stream handler is registered through one function, which asks `preauth.Allowed` with the state of the stream's connection and resets the stream, after negotiation, when the answer is no; the reset carries the library's `StreamGated` code. Outbound, a member protocol is opened only on a connection this side judged a member's: the stream is opened on that connection, not through the host, which would pick any connection to the peer. A caller that gets the gated code knows the peer never read the call.

### The connection table and the accounting

The table holds one entry per live connection: its state, the peer's key, the peer's exchange message, the node whose certificate was accepted, and the calls in flight. `preauth.Accounting` is driven from it: the connection gater's secured callback, the connected and disconnected callbacks, and the outcome of the exchange. A timer of the injected clock wakes a loop that closes the connections `Overdue` lists and sweeps; a connection that the accounting does not track is closed.

### The certificate exchange

The side whose connection is outbound opens the exchange stream on that connection, writes its certificate message and closes its write side; the other side reads at most `MaxMessageSize` plus one byte, decides with `preauth.Decide` and the current snapshot, and answers with its own. A decided member's certificate is handed to the engine before the connection becomes a member's; when the engine refuses it the connection is closed. A certificate that names this node's own identifier means a second instance (or a copy of the data directory) is calling: the connection is closed and the log says so. The serials seen of nodes that are not stored are kept in memory only, at most 1,024 of them.

### The membership snapshot

`SetMembership` takes the node's own certificate, the facts of a decision, and the certificates the node holds. It gives the accounting the members of the held certificates, decides every live connection again and closes those that no longer keep (refused, or demoted from member to non-member), closes a non-member connection to a node that is now a member as soon as it is idle, and forgets that the node was unreachable, and, when the node's own certificate has a new serial, closes every connection as soon as it is idle: an exchange on an authenticated connection is ignored, so a renewed certificate reaches a peer only on a new connection, and a call that was in flight on a closed one ends as a timeout and is sent again under the deduplication rules.

### Dialing

A node is dialed by a peer identifier and an address. The identifier comes from an accepted certificate (it is a function of the network key the certificate names) or, for a first contact, from the `/p2p/` part of an address book entry; the library does not dial a peer whose identifier it does not know. Address book addresses are written to the peerstore to stay. A connect is a dial plus the exchange within 4 seconds; a node that could not be connected is answered unreachable at once for the next 10 seconds, so that the engine's one outbound worker does not spend the budget on every resend to a node that is down. The memo is the only backoff: the library keeps a dial backoff of its own per address (5 seconds plus the square of the failures, up to 5 minutes, cleared only by a success), and a dial bypasses it, since it would otherwise keep a node that came back unreachable for minutes after the memo ran out.

The owner enters addresses by hand (`network.json`), and `CheckAddresses` judges them before they are kept, by the rule that follows from the paragraph above: while no certificate is held for the node, at least one address must end in `/p2p/<peer id>`, since there is no other source of the identifier; once one is held, every `/p2p/` must be the peer identifier of the certificate's network key, since an address that names another peer could never reach the node, and an address with none is fine. Each address is first judged by `config.ValidatePeerAddress`, the one definition of what an address may be. The error is a `config.FieldError` that carries the path of the address in the request (`addresses[1]`, or `addresses` for the list). The rule is here because this package derives peer identifiers from certificates and holds the certificates the engine pushed in the snapshot.

`ContactAddrs` are the addresses a person can give another owner to reach this node: the addresses the listeners are open on, an unspecified one (`0.0.0.0`, `::`) expanded into the addresses of the machine's interfaces of its family, loopback and link-local addresses (IPv4 `169.254.0.0/16` and IPv6) left out, each ending in `/p2p/<peer id>`.

### Calls and their errors

A call opens one stream on a member connection, writes the request, closes its write side and reads the answer, within 10 seconds. Of the member connections to a node the most recently established is used. A call that ran out of time, or whose connection gave an error that a live connection does not give (a stream the peer reset, a protocol the peer does not serve and an answer over the limit are the ones it does give), closes that connection, and when no other member connection to the node remains the node is remembered as unreachable like a failed dial.

| Case | Error |
| --- | --- |
| no address, no connection within the budget, the exchange failed, the node the peer proved to be is not the one asked for, or this side judges the peer a non-member | `transport.ErrUnreachable` |
| the stream was refused by the peer's gate, or its protocol negotiation failed | `transport.ErrUnreachable` |
| the request began to go out and no complete answer came in time, the stream broke, or the answer does not decode | `transport.ErrTimeout` |
| the answer's outcome is refused | `transport.Refused{Reason}` |
| the answer's outcome is failed | a plain error |
| the caller's own context ended | its error, as it is |

On the serving side an error of the engine is answered refused, with the reason name the engine gives, when the engine says it is a refusal, and failed otherwise. The request is read to the limit of its protocol, decoded strictly, and handed to the engine as a request from the node the connection authenticated; a request that does not decode resets the stream.

### The router

`Router` is what the engine calls: the local node's own calls go to the loopback, which makes the local origin, and every other node's to the remote client. The remote client has no fault knobs, so `Waiting` is the loopback's.

## Why

- **One CBOR message per stream, not HTTP over streams.** The only calls are three request-and-answer pairs. HTTP's routing, status codes and headers would all go unused, its request body limit and its timeouts would have to be added, the library's HTTP binding is marked experimental and registers its handlers on the raw host, which bypasses the connection gate, and a stream-per-request needs no connection reuse the multiplexer does not already give. A single message with a half-close is the way the certificate exchange already works, shares its decoder, and gives each protocol its own identifier, which is exactly what the access table and the per-protocol limits of the resource manager are keyed by.
- **The records are decoded for shape here and verified by the engine.** The signature of a report depends on the keys the engine holds, and a second place that judged a record would be a second rule. The decoder only makes sure that what reaches the engine is a record, so a malformed answer is refused at the stream and not deep in a state machine.
- **The envelope limit is read from the envelope.** The send request is sized from `envelope.MaxEncoded()` plus a margin, so a limit raised in one place cannot leave the other refusing what the first allows.
- **Limits are derived from what the rules allow.** 50 recipients, 64 KiB per record and 256 entries are the bounds the envelope, record and publication rules already set; a limit written down separately would one day be below them. The record limit is generous for a status report, which is a few hundred bytes; it is the limit every record decoder shares, and a smaller one for this protocol alone would be a second rule.
- **The sender is a text and the recipients are bytes.** The sender endpoint is the one field a person reading a capture looks for, and a strict re-encoding does not catch a second spelling of a text identifier, so the decoder compares the text with the canonical form. Recipient lists repeat sixteen-byte values and stay compact as bytes.
- **The asker names the node of a directory entry, not the answer.** A peer could claim another node's endpoints; the entry has no field for it.

- **Sub-package assembly, not `libp2p.New`.** The root package links WebRTC, WebSocket and WebTransport whatever options it is given, about 30 more modules and 5.5 MiB more than the TCP-and-QUIC assembly. The assembly still links autonat, the relay service, hole punching and the NAT manager, which stay off until something needs them.
- **The reset code, not a guess, tells a gated call.** The gate can only reset a stream after negotiation, so a client that reads a reset cannot otherwise tell "the peer never looked at the call" from "the peer broke while handling it". The first is unreachable and the second a timeout; the engine sends the same request again after either, so a wrong guess would cost a retry, but the distinction is cheap and keeps the two errors as the transport reference defines them. A write or half-close error is not judged by itself: the read that follows reports the reset with its code, which the library's write path does not always do.
- **A connection a call timed out on is closed.** When a peer goes away without closing its connections (it sleeps or loses power) nothing on this side learns it for a long while, and the connection would be picked for every call to the node, each waiting out the call timeout while the engine's one outbound worker waits with it. The first call pays for it; the memo then answers the next ones at once, and the redial after the memo finds the peer or not. A call that timed out on a peer that is alive but slow loses its connection too, which costs a reconnect.
- **A promotion closes the connection it was judged on.** The state of a connection is decided by its exchange and an exchange is not repeated, so a non-member connection to a node that became a member would stay a non-member's until it ended, while a dial returns it and every call fails as to a non-member. Pairing always runs on a non-member connection and then promotes, so the next call must exchange again on a new one; the connection is closed when idle so that a call or a pairing still on it is not cut off.
- **Idle connections are closed after a certificate renewal, not renegotiated.** Adding a member protocol that pushes the new certificate would change the pre-authentication contract. A renewal comes about every 20 days and a reconnect on a local network costs next to nothing.
- **Strangers' serials are not stored.** Storing every certificate serial any caller presents would let anyone grow the disk; the in-memory table is bounded, and a stranger can only pair anyway.
- **The limiter table drops idle peers.** The resource manager offers no list of the peers it holds, so the table drops the entry of a peer that has no connection and has not been asked about for two minutes, which is after the manager has collected its scope; a scope that is still alive keeps the object it was given.

## Where

- `internal/nodeproto/protocol.go`: the identifiers, `LimitOf`, `Outcome`.
- `internal/nodeproto/send.go`, `pull.go`, `directory.go`: the messages with their `Encode` and `Decode…` functions; `codec.go`: the shared strict reading of records, endpoints and the encode-through-decode rule.
- `internal/nodeproto/frame.go`: `ReadMessage`, `AppendFrame`, `ReadFrame`.
- Tests: `golden_test.go` (bytes written by an independent encoder), `refuse_test.go` (one row per rule), `frame_test.go` (framing and the largest legal message of each kind against its limit), `fuzz_test.go` (one target per decoder).
- `internal/node/network/`: `network.go` (the `Network`, `Config`, `Listen`, `Close`), `host.go` (the assembly), `limits.go` (the resource manager's per-peer limit), `conns.go` (the connection table, the gater, the accounting and the timer loop), `gate.go` (registering and opening streams), `exchange.go`, `membership.go` (`SetMembership`), `dial.go` (the address book and connecting), `addresses.go` (the rule for the addresses the owner enters, and the contact addresses), `remote.go` (the client and the servers), `router.go`, `node.go` (the interface the engine implements).
- Tests: `network_test.go` runs two or three nodes on the loopback with a fake engine and static snapshots: the three requests over TCP and over QUIC, the state each side gives the other for every combination of who trusts whom, a member protocol reset until the exchange completes, a non-member that can only pair, an unauthenticated connection closed at the deadline on the fake clock, a certificate of the node's own identity, one row per error of a call, a new snapshot that closes a revoked member's connection, a promotion that closes the non-member connection and clears the memo, a call that times out and drops its connection, the newest of two member connections preferred, a node that came back reached as soon as the memo runs out, a renewed certificate on a new connection, dialing by held certificate, the router, and no goroutine left after `Close`; `TestCheckAddresses` has one row per case of the rule for entered addresses and `TestContactAddrsListNoLoopbackAndEndInThePeer` the contact addresses.
