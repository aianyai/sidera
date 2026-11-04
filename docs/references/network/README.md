# Node-to-node protocol

How one node asks another to take an envelope, to report on a delivery, to list its endpoints, or to say whether it can dial the asker back: four protocols, one request and one answer each, each on its own stream and each a frame; and how two nodes that need not be members of each other carry a pairing: a multi-frame stream and a one-frame result fetch on the pairing protocol. This topic covers the wire format, the byte limits and the meaning of the outcomes, and the runtime that carries them: the libp2p host, the gate in front of every stream, the connection table, the certificate exchange on a connection, dialing, and how a call's failures map to the transport's errors. The identifiers, messages and limits are defined once, in `internal/nodeproto`; the pre-authentication rules that decide who may open these streams are in [preauth](../preauth/README.md), and the two ways a call can get no answer are in [transport](../transport/README.md).

## What

### Protocols

| Protocol | Request | Answer |
| --- | --- | --- |
| `/sidera/send/2.0.0` | one envelope, from an endpoint of the sending node | the receipt: signed status reports and, for a control signal that has one, the control reply |
| `/sidera/pull/2.0.0` | one message identifier and the recipients on the target | the current signed reports, the recipients the target holds no record of, and the control reply if there is one |
| `/sidera/directory/2.0.0` | a continuation and a page size | a page of the open endpoints the target shows the asker |
| `/sidera/dial-back/1.0.0` | nothing (the empty map) | whether the target could dial the asker back, and on which address |

The dial-back protocol is new and was framed from its first version, so it has no other form. A node also serves, and a client falls back to, the same three under `/sidera/{send,pull,directory}/1.0.0`, the identifiers of the nodes before 2.0.0, which differ only in how a stream is delimited (below). The messages, limits and access are those of the 2.0.0 protocol of the same name.

None of the seven is in the access table of [preauth](../preauth/README.md), so each falls under "every other protocol": only members may open one. The node the request comes from is the node the secure handshake authenticated; no message names it, only the endpoint of that node the request is made for.

### One message and one stream

A request is one deterministic CBOR message and so is its answer.

**On a 2.0.0 stream** (and on a dial-back stream) each is one frame: an unsigned varint length, as for the pairing protocol, then the message. The prefix must be the shortest encoding of its value, a frame is at least one byte, and a length above the protocol's limit is refused before any payload byte is read. The client writes its request frame and does not half-close, reads the answer frame, and only then closes the stream. The server reads the request frame, answers, half-closes its own side and keeps the stream until the client's side ends (the close, a reset, or the connection going), and only then returns, which is when the connection's busy count falls. The close is the client's statement that it has the answer, which no layer of the library offers on its own; a client that sends anything after its request frame breaks the protocol and the stream is reset. The wait is bounded by the call's budget, counted from the start of the stream: a client that never closes has its stream reset then.

**On a 1.0.0 stream** the end of the stream is the end of the message, so nothing is length-prefixed: the client writes the request and half-closes, the server reads to the end of the stream, answers and closes, and a reader stops at the limit of the protocol, plus one byte to tell a message that is exactly at the limit from one that is over it, and refuses more. The server has already seen the client's end when it answers, so it cannot tell when the client has the answer and releases the stream at once.

A client proposes the 2.0.0 identifier and then the 1.0.0 one in one protocol negotiation (the dial-back protocol has only the one to propose) (`multistream-select` takes the first the peer serves), so a node that serves both is called in the framed form with no extra round trip, and a node that serves only 1.0.0 costs one.

A message is a map with integer keys and is decoded with the strict rules of [records](../records/README.md): the size limit first, then no duplicate key, unknown field, indefinite length or tag, and the bytes must be the one canonical encoding. A field that must be present and is absent makes the bytes differ from the re-encoding and is refused; a field that may be absent is refused when it is written out empty. The sender writes only what the receiver accepts: `Encode` runs its own output through the decoder.

Records travel as the byte strings of their wire format. The message decoder reads their shape (`record.Decode` with the record limit) and nothing else; the signatures, the type of each record and whether it belongs to the call are the receiving engine's to check, as everywhere else.

A framed stream (a 2.0.0 member call's, and the pairing protocol's) prefixes each frame with its length as an unsigned varint. The prefix must be the shortest encoding of its value, a frame is at least one byte, and a length above the reader's bound is refused before any payload byte is read or allocated.

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

A cursor is at most 256 bytes of visible ASCII and means something to the target only. An entry names its kind (`runtime`, `session`, `direct`) and the actions it accepts (`message.send`, `see`, ...) by name; the display name is empty or follows the display-name rule of the adapter protocol (at most 128 characters, no control or bidirectional formatting character), and the description is at most 1024 characters, both valid UTF-8. An entry whose display name breaks the rule makes the whole answer invalid. An entry carries no node: the asker fills in the node it called and never takes one from the answer. At most 256 entries, the most endpoints a node has.

**Dial-back**

```
request = { }
answer  = { 1: outcome, 2: result (ok only), 3: address reached (reached only), 4: reason (refused only) }
result  = 1 reached | 2 not reached | 3 no address
```

The request is the one byte `0xa0`. A result goes with the outcome ok and with no other, and an address with the result reached and with no other. The address is a binary multiaddr of at most 256 bytes that parses and is in the one form the library writes, without a `/p2p/` part. The one reason a dial-back is refused with is `busy`. The meaning of the results is under Dial-back below.

### Outcomes

Every answer opens with an outcome:

| Outcome | Value | Meaning | Rest of the answer |
| --- | --- | --- | --- |
| ok | 1 | the target handled the request | the result |
| refused | 2 | the target refused the request for a reason it names | the reason, and nothing else |
| failed | 3 | the target could not handle the request and says nothing more; no receipt exists | nothing |

The reason is the name of the target engine's refusal reason (`message_conflict`, `node_queue_full`, ...), at most 64 bytes of lower-case letters, digits and underscores, so that a peer cannot put text of its own choosing into a log line. It is present exactly when the outcome is refused.

### Limits

Each protocol has a limit on the request a server reads and on the answer a client reads, the same under both identifiers of a protocol that has two; a reader refuses more before it decodes anything, and on a framed stream the limit bounds the payload of the frame. `nodeproto.LimitOf` returns them.

| Protocol | Request | Answer |
| --- | --- | --- |
| send | `envelope.MaxEncoded()` + 512 | 51 records of at most 64 KiB (a report per recipient and a control reply), + 512 |
| pull | 2 KiB | the same as send |
| directory | 512 | 256 entries of at most 5 KiB, + the cursor limit, + 512 |
| dial-back | 16 | 512 |

## The runtime

The package `internal/node/network` (layer 5, beside the engine; neither imports the other) holds everything below. It applies the decisions of `internal/preauth`, speaks the messages of `internal/nodeproto` and decides nothing about trust: the engine hands it the facts of a decision as a snapshot and receives what arrived through an interface the network package defines.

### The host

The libp2p host is assembled from its sub-packages, not made with `libp2p.New`: swarm, upgrader, TCP with Noise, QUIC (with the library's own TLS handshake), yamux, and the basic host with identify and ping. The node's network key is the host's identity, so a connection is authenticated by the very key the node's certificate names, and a peer's identifier maps back to the network key it embeds. The NAT manager, the relay service, hole punching and autonat are off: nothing opens a router port or reaches a node through a relay.

The host holds the **dial-back dialer**, which makes the connections that answer a dial-back (below). Every dial-back gets a swarm of its own, built for it and closed with it, together with every UDP socket its QUIC connection manager opened. The swarm listens on nothing. Its TCP transport does not reuse ports, and its QUIC transport has a connection manager of its own that does not reuse sockets either, so every dial leaves from a new port. It is authenticated with the node's own network key, has no resource manager and no gater (the concurrency and the connect budget bound it), ranks no delay between addresses (every address is tried at once and the first that connects wins), and keeps no UDP or IPv6 black-hole state. Its dials bypass the library's dial backoff, as the host's own do. The resource manager runs with the library's default limits for members and a smaller tier for a peer until its exchange has made it a member; the per-peer limit object is stable and moves between the tiers.

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

A call opens one stream on a member connection, writes the request frame, reads the answer frame and closes the stream, within 10 seconds (against a node that serves only the 1.0.0 identifiers, it writes the request, closes its write side and reads the answer). Of the member connections to a node the most recently established is used. A call that ran out of time, or whose connection gave an error that a live connection does not give (a stream the peer reset, a protocol the peer does not serve and an answer over the limit or not a valid frame are the ones it does give), closes that connection, and when no other member connection to the node remains the node is remembered as unreachable like a failed dial.

| Case | Error |
| --- | --- |
| no address, no connection within the budget, the exchange failed, the node the peer proved to be is not the one asked for, or this side judges the peer a non-member | `transport.ErrUnreachable` |
| the stream was refused by the peer's gate, or its protocol negotiation failed | `transport.ErrUnreachable` |
| the request began to go out and no complete answer came in time, the stream broke, or the answer does not decode | `transport.ErrTimeout` |
| the answer's outcome is refused | `transport.Refused{Reason}` |
| the answer's outcome is failed | a plain error |
| the caller's own context ended | its error, as it is |

The health of the calls to a node is logged where they are made, per node and not per call: the first call that fails after calls worked (or after none was made) is logged at info with its kind (`unreachable`, `timeout`, `refused` with the reason name, `failed`) and the error, which carries where the call stopped (no address, the memo of a failed dial, the error of each address dialed, the exchange, the peer's gate); the failures after it are logged at debug; the first call that succeeds after them is logged at info with the number of failures and how long the run lasted. A call that ended because the caller's own context did says nothing about the node and is not counted. The engine retries on its own schedule and says why only at debug level, so this is what an owner reading the log at info level sees of a node that stopped answering.

On the serving side an error of the engine is answered refused, with the reason name the engine gives, when the engine says it is a refusal, and failed otherwise. The request is read to the limit of its protocol, decoded strictly, and handed to the engine as a request from the node the connection authenticated; a request that does not decode resets the stream. All three servers are one function, `serveCall`, registered under both identifiers of each protocol, so what a stream is held for is decided in one place.

**A connection marked to close when idle does not cut off an answer.** A certificate renewal, a promotion of a non-member and a call that found the connection at fault mark a connection to close as soon as it is idle. A 2.0.0 server holds the connection busy until the client has taken the answer, so none of them closes it under an answer still in flight, which would discard it on QUIC and, because closing a yamux session drops what is queued, on TCP. A 1.0.0 server cannot know and releases as soon as it has written the answer, so a call to a node of the older form can still lose its answer to such a close; the engine then gets a timeout and sends again, and deduplication absorbs the second send, so the cost is a delay. One race is left: a close that lands between the server judging the connection idle and the client opening a new stream on it. The call fails and the engine sends again, at the same cost.

### Dial-back

A node cannot tell from the inside whether another node can dial it: that depends on the other's address book for it and on every firewall and NAT between them. So a member asks another, over a connection they already have, "could you dial me?", and the other tries for real.

**Asking.** The question goes on a member connection that is up; it dials none. It is one frame, the empty map, and carries no address. The call is an ordinary one (the same stream, framing, drain and error mapping as the member calls), and the asker's answer is one of four outcomes: reached (with the address that connected), not reached, no address, and *cannot ask*. The last is the case of a member that does not serve the protocol, as a node before it does not: the protocol negotiation answers "not supported", which is neither a fault of the connection (it is not dropped) nor an answer, and nothing about reachability is known from it. A member that refuses (it is busy) or that does not answer in time gives the errors of any call.

**Answering.** The node that is asked takes the asker from the connection the request came over: the peer the secure handshake authenticated, and the node the exchange accepted. It dials that peer identifier, and only on the addresses its address book wrote to the peerstore for it: the same addresses a call to that node would dial. What identify taught the peerstore is not used, since it lapses with the connection it came from and the next dial after a restart would not have it; an address through a relay (`/p2p-circuit`) is skipped, since reaching a node through a relay says nothing about reaching it directly. The dial is made by the dial-back dialer (see the host), within the connect budget, and counts as made when the secure handshake and the stream-muxer negotiation have completed; the connection is then closed at once and no certificate exchange is run on it. The answer is *reached* with the address that connected, *not reached* when the book holds addresses and none connected, or *no address* when it holds none.

**Bounds.** A node dials back for one asker at a time and for at most four askers at once; a request over that is refused with the reason `busy`. The members are trusted, so the bound is against a member that misbehaves taking the dialer, not against an attacker; each dial-back is also bounded by the connect budget and reaches no address but the asker's own book entries. There is no rate limit.

**What the asker sees.** A new inbound connection from a member's key and a port that is not the member's listening port, which the accounting admits as a known member's and which the member closes at once; it is released when it ends, like any connection.

### Pairing streams and result fetches

The pairing protocol (`/sidera/pairing/1.0.0`) is the one protocol that non-members may use, so it is served on a member's connection and on a non-member node's alike, and it is opened on either. The network only carries it: the engine runs the state machine of [pairing](../pairing/README.md) and the network gives it the facts of the connection and the frames of the stream.

A frame is written with `nodeproto.AppendFrame` and read with `nodeproto.ReadFrame`, both bounded by `pairing.MaxMessage`; a frame that does not read that way resets the stream. There are two uses of the protocol on the same stream shape:

- **The online segment.** The initiator's engine asks for `StartPairing(node, addrs, sid)`, which finds a connection to exactly that node, opens the stream on it and returns the facts of the connection and a `PairingStream`; the target's handler hands the stream to its engine with `PairingOpened`. From then on each side's engine receives the other's frames one at a time and in order through `PairingFrame`, and writes through `PairingStream.Send` and `Close`. The stream is reset at the online timeout of the connection accounting (the value non-member connections are kept open for), counted from its opening, on both sides.
- **The result fetch.** `PairingFetch(node, addrs, frame)` opens a stream on a connection that is up to the node or a new one, writes one frame, reads one frame, closes the stream and returns the answer with the facts of the connection, all within the fetch timeout from the opening of the stream. It does not half-close after its frame: the end the target sees is the close that follows the answer, which is how the target knows the answer was taken (see the end of a stream below). The target sees an ordinary stream, so its engine must have issued the answer and the close while handling the frame.

`PairingStream.Send` never blocks: it queues the framed bytes for the stream's own writer goroutine, which writes them in order. A frame that cannot be framed, or one that finds the queue full (a few frames: a target sends a couple per stream), resets the stream and is dropped, and so is a frame sent after the stream ended. `Close` ends the engine's side once the queued frames are written: the stream is half-closed, and the peer reads those frames and then the end. The read side stays open, so the peer's own end is seen; the peer's frames after the engine's `Close` are read and dropped.

The engine is told that a stream ended (`PairingClosed`) exactly once, unless it ended the stream itself through `Close`: a peer's close or reset, an invalid frame, an engine error from `PairingFrame`, the online timeout and the closing of the network all count. When the peer only half-closes, the writer still flushes what the engine queued before the stream is half-closed too.

**A stream is kept until the peer has ended its side.** On either side, a stream's handler (the target's, and the goroutine that holds the initiator's stream) returns only once the read has seen the peer's end (the end of its side, a reset, or the online deadline) and the writer has half-closed; then the stream is fully closed. A handler that returned as soon as its last frame was written would release its connection while the peer had not yet taken that frame, and a connection marked to close when idle (a promotion, a certificate renewal) would be closed at once, which on QUIC discards what is still in flight: the target that completed a pairing on a fetch promoted the initiator and the closing of the connection cut off the answer that completes the pairing. The initiator ends its side by closing the stream after it has read the answer, or the last frame it wants, which is why a fetch does not half-close first. The member calls follow the same rule. A session identifier is the engine's to choose (the initiator passes it in, the target returns it from `PairingOpened`); an initiator's stream may end before `StartPairing` has returned, so the engine takes a close of a session it has not registered yet as a stream that is already over.

**The parties come from the connection.** A pairing binds both parties' keys into its transcript, so each is made by `pairing.NewParty` from the certificate that connection accepted for it, never from what the node holds at the moment of the stream:

| Party | Certificate | Connection key |
| --- | --- | --- |
| remote | the one `preauth.Decide` accepted in the exchange (`Decision.NodeCertificate`), kept on the connection's entry with the record it was verified from and its serial state | the key the secure handshake authenticated |
| local | the node's own certificate as that connection's exchange presented it | the node's network key |

Both go with the current revocation records and the highest serial seen for their node (the engine's facts together with the in-memory table of strangers' serials), so a certificate that was revoked, expired or overtaken by a newer serial since the exchange resets the stream instead of making a party. A node that renewed its certificate closes its idle connections, so a connection that outlives a renewal ends rather than pairing under a certificate its owner has replaced. The protocol identifier of the facts is the one the stream negotiated. `PairingPeer` carries the parties, the certificate and its record (what a credential stores with it) and the serial state to store when higher.

The node is dialed on the addresses given for the pairing, or on the address book's when there are none. The addresses are dial hints for this pairing only: they go to the library's peerstore for its temporary lifetime, never to the address book. A hint ends in `/p2p/<peer id>`, or names only the transport address for a node whose certificate is held. The connection must reach exactly the node asked for, in the member or the non-member state; another node or a refused exchange is an error (`transport.ErrUnreachable`, like any node that could not be connected). A pairing dial does not read or set the unreachable memo, which exists for the engine's outbound worker's resends to cluster nodes.

### The router

`Router` is what the engine calls: the local node's own calls go to the loopback, which makes the local origin, and every other node's to the remote client. The remote client has no fault knobs, so `Waiting` is the loopback's.

## Why

- **One CBOR message per stream, not HTTP over streams.** The only calls are three request-and-answer pairs. HTTP's routing, status codes and headers would all go unused, its request body limit and its timeouts would have to be added, the library's HTTP binding is marked experimental and registers its handlers on the raw host, which bypasses the connection gate, and a stream-per-request needs no connection reuse the multiplexer does not already give. A single message per direction is the way the certificate exchange already works, shares its decoder, and gives each protocol its own identifier, which is exactly what the access table and the per-protocol limits of the resource manager are keyed by.
- **A length-prefixed frame, not a half-close, ends a request.** With a half-close the server has seen the end of the stream before it answers, so it cannot know when the client has the answer, and the library has no call that says so: closing a stream does not wait for the peer to have the data. When the handler returned, a connection marked to close when idle was closed, and the close discarded the answer. With a frame the client needs no half-close to say its request is complete, so its side stays open until it has the answer, and its close is the acknowledgement the server waits for; this is what the library's own interface documentation prescribes for a stream whose peer must have its data. A grace period before closing was rejected: it guesses a time, loses the answer anyway on a slow link, and cannot be tested for enough. A close code that makes the client retry on a new connection within the same call was rejected for a gain only in a rare race, and an error path of its own.
- **New identifiers with a fallback, not one identifier read two ways.** The two forms cannot be told apart by the first byte (an old request is a map whose first byte is 0xa1 to 0xa3, and a length prefix of 128 or more starts with a byte in the same range), so the identifier chooses the form, which also keeps the access table and the resource manager's per-protocol limits keyed as before. A node serves both so that nodes can be upgraded one at a time. The 1.0.0 identifiers are to be removed in a later release, after which a node of the older form answers a call as unreachable, which the engine retries and reports and never takes for a delivery.
- **The records are decoded for shape here and verified by the engine.** The signature of a report depends on the keys the engine holds, and a second place that judged a record would be a second rule. The decoder only makes sure that what reaches the engine is a record, so a malformed answer is refused at the stream and not deep in a state machine.
- **The envelope limit is read from the envelope.** The send request is sized from `envelope.MaxEncoded()` plus a margin, so a limit raised in one place cannot leave the other refusing what the first allows.
- **Limits are derived from what the rules allow.** 50 recipients, 64 KiB per record and 256 entries are the bounds the envelope, record and publication rules already set; a limit written down separately would one day be below them. The record limit is generous for a status report, which is a few hundred bytes; it is the limit every record decoder shares, and a smaller one for this protocol alone would be a second rule.
- **The sender is a text and the recipients are bytes.** The sender endpoint is the one field a person reading a capture looks for, and a strict re-encoding does not catch a second spelling of a text identifier, so the decoder compares the text with the canonical form. Recipient lists repeat sixteen-byte values and stay compact as bytes.
- **The asker names the node of a directory entry, not the answer.** A peer could claim another node's endpoints; the entry has no field for it.

- **Dial-back is written here, not taken from the library's autonat.** The library's autonat servers do not dial private addresses, and their handlers are registered on the raw host, which bypasses the gate; their conclusion is "reachable from the internet", a global judgment that needs two or three unrelated servers to agree. The question here is whether one member, that would have to find this node, can, so the answer belongs to that member alone, the servers are trusted members behind the gate, and a private address is exactly the usual case. The cost is a protocol of our own, small because the question has no parameters and the stream is the member calls' own shape.
- **The dial-back does not use the listening port, or the host's own swarm.** The host's swarm returns the connection it already has to the peer, so it would not dial at all; and a new connection of its own leaves from the listening port (TCP reuses it, QUIC shares the socket), so the packets are the flow the asker itself opened, which a stateful firewall or NAT passes while it blocks a cold inbound connection. The answer would be "reached" in the very case the question exists for, and nothing would show it. A dialer of its own that listens on nothing and does not reuse ports has no flow in common with the asker's connection; a test asserts that the dial-back arrives from a port that is not the member's listening port, and that a dial to an address nothing answers on is not reached.
- **The dial-back request carries no address.** The member dials only what its own address book holds for the asker, so the protocol cannot be used to make a node dial an address of the asker's choosing, which the library's autonat has to defend against with a cost the asker must pay first; and the dial is a true test of the path, because those addresses are the ones the member would use to find the asker for real.
- **The dial-back dialer is authenticated with the node's own network key.** The asker then sees a member it knows and admits the connection without taking a place among the unauthenticated connections. The library's autonat dials from a throwaway key because its server and client do not trust each other; these two do, and the answer is taken on the member's word.
- **A dial-back builds a swarm and closes it, sockets included.** A QUIC connection manager that does not reuse sockets opens a UDP socket for every dial, and the library gives up the transport of a dial that succeeded only when the manager is closed, which does nothing when reuse is off, and closing a transport it did not make the socket for leaves the socket open: each successful dial-back would leave a socket and a couple of goroutines behind for the life of the process, `Close` included. The dialer records every socket its manager opens and closes them with the swarm. Dial-backs are rare and at most four run at once, so building a swarm for each costs nothing that matters, and no state of one dial-back (peerstore, backoff, sockets) reaches the next.
- **The dial-back dialer ranks no delay and keeps no black-hole state.** A delay between addresses would make the answer wait for the slow ones for nothing, and the library's UDP and IPv6 black-hole detection stops dialing such addresses after enough failures, which would answer "not reached" for QUIC or IPv6 without having tried.
- **Sub-package assembly, not `libp2p.New`.** The root package links WebRTC, WebSocket and WebTransport whatever options it is given, about 30 more modules and 5.5 MiB more than the TCP-and-QUIC assembly. The assembly still links autonat, the relay service, hole punching and the NAT manager, which stay off until something needs them.
- **The parties come from the connection, not from the engine's stored certificates.** The stored certificate of a node may be a newer or older one than the connection showed, and the transcript must bind the keys this very connection is authenticated with, or a code made for one key could be shown for another. The certificate is kept on the connection when the exchange decides, which is the one place that holds both what `Decide` accepted and what this side presented.
- **A write queue and a writer goroutine per pairing stream.** The engine writes from its loop, which must not wait for a peer: a slow or dead peer would stall every other session. The queue is small because a target sends a couple of frames, so a full queue means a stalled writer and the stream is reset rather than the loop blocked.
- **The engine is told of an end it did not make, once.** The state machine must learn that a stream it holds is gone, to close its session, but a close the engine made is already known to it, and a second report would make it handle the same end twice.
- **The reset code, not a guess, tells a gated call.** The gate can only reset a stream after negotiation, so a client that reads a reset cannot otherwise tell "the peer never looked at the call" from "the peer broke while handling it". The first is unreachable and the second a timeout; the engine sends the same request again after either, so a wrong guess would cost a retry, but the distinction is cheap and keeps the two errors as the transport reference defines them. A write or half-close error is not judged by itself: the read that follows reports the reset with its code, which the library's write path does not always do.
- **A connection a call timed out on is closed.** When a peer goes away without closing its connections (it sleeps or loses power) nothing on this side learns it for a long while, and the connection would be picked for every call to the node, each waiting out the call timeout while the engine's one outbound worker waits with it. The first call pays for it; the memo then answers the next ones at once, and the redial after the memo finds the peer or not. A call that timed out on a peer that is alive but slow loses its connection too, which costs a reconnect.
- **A promotion closes the connection it was judged on.** The state of a connection is decided by its exchange and an exchange is not repeated, so a non-member connection to a node that became a member would stay a non-member's until it ended, while a dial returns it and every call fails as to a non-member. Pairing always runs on a non-member connection and then promotes, so the next call must exchange again on a new one; the connection is closed when idle so that a call or a pairing still on it is not cut off, and a pairing handler and a member call's handler stay until the peer has ended its side so that their last frame is not what the close cuts off.
- **Idle connections are closed after a certificate renewal, not renegotiated.** Adding a member protocol that pushes the new certificate would change the pre-authentication contract. A renewal comes about every 20 days and a reconnect on a local network costs next to nothing.
- **Strangers' serials are not stored.** Storing every certificate serial any caller presents would let anyone grow the disk; the in-memory table is bounded, and a stranger can only pair anyway.
- **The limiter table drops idle peers.** The resource manager offers no list of the peers it holds, so the table drops the entry of a peer that has no connection and has not been asked about for two minutes, which is after the manager has collected its scope; a scope that is still alive keeps the object it was given.

## Where

- `internal/nodeproto/protocol.go`: the identifiers (current and legacy, `Offer`, `IsLegacy`), `LimitOf`, `Outcome`.
- `internal/nodeproto/send.go`, `pull.go`, `directory.go`, `dialback.go`: the messages with their `Encode` and `Decode…` functions; `codec.go`: the shared strict reading of records, endpoints and the encode-through-decode rule.
- `internal/nodeproto/frame.go`: `ReadMessage` (1.0.0 streams), `AppendFrame`, `ReadFrame`.
- Tests: `golden_test.go` (bytes written by an independent encoder), `refuse_test.go` (one row per rule), `frame_test.go` (framing, and the largest legal message of each kind against its limit, as a frame), `fuzz_test.go` (one target per decoder).
- `internal/node/network/`: `network.go` (the `Network`, `Config`, `Listen`, `Close`), `host.go` (the assembly, and the dial-back dialer), `limits.go` (the resource manager's per-peer limit), `conns.go` (the connection table, the gater, the accounting and the timer loop), `gate.go` (registering streams, and opening them with the identifiers proposed in order), `exchange.go`, `membership.go` (`SetMembership`), `dial.go` (the address book and connecting), `addresses.go` (the rule for the addresses the owner enters, and the contact addresses), `remote.go` (the client, `call` on a connection chosen by `connect` and `callOn` on one already chosen, and `serveCall`, the one server of the member protocols), `dialback.go` (the question, the answer and the bound on dial-backs in flight), `callhealth.go` (the log of a node's calls starting to fail and working again), `router.go`, `node.go` (the interface the engine implements), `pairing.go` (the pairing parties, streams and fetch).
- Tests: `network_test.go` runs two or three nodes on the loopback with a fake engine and static snapshots: the three requests over TCP and over QUIC, the state each side gives the other for every combination of who trusts whom, a member protocol reset until the exchange completes, a non-member that can only pair, an unauthenticated connection closed at the deadline on the fake clock, a certificate of the node's own identity, one row per error of a call, a new snapshot that closes a revoked member's connection, a promotion that closes the non-member connection and clears the memo, a call that times out and drops its connection, the newest of two member connections preferred, a node that came back reached as soon as the memo runs out, a renewed certificate on a new connection, dialing by held certificate, the router, and no goroutine left after `Close` of nodes that have sent a call (it makes no dial-back); `member_test.go` runs two nodes that are members of each other: each of the three calls over TCP and over QUIC, answered in the handling that renews the target's certificate and repeated twenty times on new nodes (the answer must not be lost to the close that follows), a client that proposes 2.0.0 to a node that serves only 1.0.0, a 1.0.0 client to a node that serves both, and a server that keeps a stream until the client closes it and releases it at the call's budget when it never does; `dialback_test.go` runs two members of each other and asks one whether it can dial the other back, over TCP and over QUIC: the answer for a book that holds the listening address (reached, on that address, from a port that is not the member's listening one), one that holds an address nothing answers on (not reached) and one that holds none (no address), a member that does not serve the protocol (cannot ask, the connection kept), and QUIC dial-backs followed by `Close` leaving no goroutine behind; `pairing_test.go` runs two nodes that are not members of each other: a pairing stream over QUIC with frames both ways and each side ending it (and the engine that ended it not being told), a fetch whose answer is followed, in the same handling, by the promotion of the initiator, which must still get the answer, the parties and serials the certificates and connection keys give on both sides, a result fetch dialed on a hint alone, an address that reaches another node, and the accounting's online timeout equal to the pairing protocol's; `TestCheckAddresses` has one row per case of the rule for entered addresses and `TestContactAddrsListNoLoopbackAndEndInThePeer` the contact addresses.
