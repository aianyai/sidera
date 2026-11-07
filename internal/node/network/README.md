# network

The node's connection to the other nodes: the libp2p host, the gate in front of
every stream, the table of connections and its accounting, the certificate
exchange that makes a connection a member's, dialing, and the client and server
of the send, pull, directory and profile protocols, and the streams and result fetches of
the pairing protocol. Read
[../../../docs/references/network/README.md](../../../docs/references/network/README.md)
for the reasons and for what the tests prove, and
[../../preauth/README.md](../../preauth/README.md) for the rules this package
applies.

This is a runtime package of layer 5 of [`internal/node/README.md`](../README.md).
It imports the contract packages `ident`, `nodeproto`, `pairing`, `preauth` and `record`,
`transport` and `clock`, `config` (for the one definition of an address and the error that
names it), and the networking library. It imports neither `engine`
nor `daemon`: it defines `Node`, which the engine implements, and `Router`, which
the engine calls through its own interface. It reads time only through the
injected clock (calls and dials are bounded by contexts, which run on the real
clock), touches no file and draws randomness only from the reader it is given (for the wait before a dial that keeps a connection).

## Entry points

| Name | Purpose |
| ---- | ------- |
| `New(Config)`, `Network.Close` | Build the host (it listens on nothing) and start the connection checks and the watch on the host's address events; close the listeners and every connection and wait for every goroutine the package started. |
| `Config`, `DefaultParams` | The network key, the clock, the accounting parameters (zero means `DefaultParams`), the source of randomness, the log, and the timeouts (4 s to connect, 10 s per call, the pairing protocol's fetch timeout, 10 s unreachable memo, 1 s tick). |
| `Network.SetNode(Node)`, `Node` | The engine's side: `transport.Handler`, `NotePeerCertificate` (a member's certificate before its connection becomes a member's), `RefusalReason` (the name of a refusal, for the answer), `Profile` (the answer to a profile request) and `MemberConnected` (a connection became a member's; called from a goroutine of the network, once per connection). |
| `Network.Listen(addrs)`, `ListenAddrs`, `PeerID` | Open the listeners (the address is in the error when one cannot open); where they are open; the identifier a contact line names after `/p2p/`. |
| `Network.SetMembership(Membership)` | The snapshot of the node's own certificate, the facts of a decision and the held certificates; decides live connections again and closes those that no longer keep, and those of a non-member that is now a member (once idle, forgetting the node's unreachable memo). |
| `Network.SetAddressBook(map)` | The multiaddrs of each node, with `/p2p/<peer id>` unless a held certificate gives the peer. |
| `Network.CheckAddresses(node, addrs)` | The rule for addresses the owner enters: judged by `config.ValidatePeerAddress`, then one must name the peer while no certificate is held, and every `/p2p/` must be the held certificate's peer once one is. The error is a `*config.FieldError` with the path of the address. |
| `Network.ContactAddrs()` | The addresses to give another owner: the open listeners, unspecified ones expanded into the interfaces' addresses, no loopback or link-local (IPv4 or IPv6), each ending in `/p2p/<peer id>`. |
| `Network.StartPairing(ctx, node, addrs, sid)` | The initiator's end of a pairing stream: finds or dials a connection to exactly that node (member or non-member; `addrs` are hints for this pairing, never kept), opens the stream, and returns the `PairingPeer` and the `PairingStream` the engine writes to. The node's frames reach `Node.PairingFrame(sid, ...)`, an end that is not the engine's own `PairingClosed(sid)`. Call it from a worker, never from the engine's loop. |
| `Network.PairingFetch(ctx, node, addrs, frame)` | One frame out, one frame back (no half-close before it), then the stream closed, within the fetch timeout from the stream's opening; returns the answer and the `PairingPeer` of the connection. |
| `PairingPeer`, `PairingStream` | The facts of a pairing connection (both parties, protocol, the accepted remote certificate with its record and serial state), and the engine's queue-backed end of a stream: `Send` never blocks, `Close` half-closes the stream once the queue is written; the stream, its handler and the connection's busy count stay until the peer ends its side. |
| `Node.PairingOpened`, `PairingFrame`, `PairingClosed` | The target's side of a pairing stream, and the frames and ends of both sides. |
| `Network.FetchProfile(ctx, node)` | Asks a node, on the member connection that is up to it, for the signed profile it issued for itself; the record comes back unverified. A node that does not serve the protocol is `served` false and no error. It dials nothing. |
| `Network.askDialBack(ctx, node)` | Asks a member, on the member connection that is up to it, whether it can dial this node back: reached (with the address), not reached, no address, or cannot ask (the member does not serve the protocol). It dials nothing; its caller is the tick (`askMembers`, `reach.go`), which records the answers. |
| `Network.Send`, `Pull`, `QueryDirectory`, `Connected` | The remote client, with the signatures of the loopback (each logs the first failure of a run of failed calls to a node and the first success after it, `callhealth.go`); whether a member connection to a node is up. |
| `NewRouter(self, loopback, remote)`, `Router` | The transport the engine calls: this node's calls to the loopback, every other node's to the remote client. |

## Rules

- **One gate.** `Network.handle` is the only place a stream handler is registered on
  the host, and `openStream` the only way a member protocol is opened: both ask
  `preauth.Allowed` with the state of the one connection. The library's own
  protocols (identify, ping) are registered by the host itself.
- **A pairing's parties come from the connection.** Each is built by
  `pairing.NewParty` from the certificate this connection accepted for it (kept on
  `connEntry` when the exchange decided: `peerCert`, `peerRecord`, `peerSeen`,
  `ownCert`) and the key the connection is authenticated with, with the current
  revocations and highest serial; a connection whose facts make no valid party
  resets the stream. `Network.pairingPeer` is the one place that builds them.
- **A pairing stream's engine calls are serial and `Send` never blocks.** The
  stream's reader hands one frame at a time to the engine and reads the next only
  after `PairingFrame` returned; the engine's writes are queued for the stream's
  writer goroutine, and a full queue resets the stream. `PairingClosed` is called
  once for an end the engine did not make, and never after its own `Close`.
- **A request-and-answer stream is kept until the peer has ended its side.** A pairing
  stream's writer only half-closes, and its handler returns (and releases its
  connection) after the read has seen the peer's end, a reset or the online deadline;
  a member call's `serveCall` writes its answer frame, half-closes, and waits for the
  client's end, a reset or the call's budget in the same way. So a connection marked
  to close when idle is not closed under a frame still in flight. Every initiator
  closes its stream after the last frame it wants, and `PairingFetch` and a member
  call do not half-close before they have read the answer. Only a 1.0.0 stream (the
  legacy identifiers, served for nodes before 2.0.0) is released as soon as it is
  answered.
- **A member protocol is registered under every identifier it has, by `handleCall`.** All
  the servers go through `serveCall`, which takes the form from the identifier the
  stream negotiated; the client proposes the identifiers newest first, the legacy one
  last (`nodeproto.Offer`), and takes the form of the one the stream got (`call`
  returns it with the answer). A server is given the connection the request came over,
  for an answer that depends on the connection and not only on the node, and the
  identifier the stream got, for an answer that differs between identifiers: the
  directory leaves the adapters out of an entry under the two older ones, and a client
  reads the answer by the identifier it got.
- **A dial-back is made only by the dial-back dialer, and only on the address book's
  addresses.** The host's own swarm would return the connection the asker already made
  or leave from the listening port, and either makes the answer "reached" whatever a
  firewall does with a cold inbound connection. The request names no address; the
  addresses are `bookWritten` for the asker's peer, without relay addresses. At most
  four askers are dialed back at once and one at a time each.
- **The exchange runs on the connection, not through the host.** The host picks
  the best connection to a peer, which is not necessarily the one being judged.
- **A member's certificate reaches the engine first.** The connection becomes a
  member's only after `NotePeerCertificate` returned, so a member protocol is never
  served for a node whose certificate the engine does not hold. A certificate of a
  node that is not a member, or one that is refused, is never stored: only an
  in-memory table of at most 1,024 serials.
- **One lock, no library call under it.** `Network.mu` guards the table, the
  accounting and the snapshot; connections are closed, streams opened and the
  engine called outside it. The library's callbacks (gater, connected,
  disconnected) only record, and a close they need goes through `closeAsync`.
- **Every goroutine is the package's.** `begin` registers it, `Close` waits for it,
  and a handler's context ends with the network. The exchange, the serving of a
  stream and the tick loop all run this way.
- **Errors keep the transport's meaning.** `ErrUnreachable` only when the target
  cannot have seen the call; a read error after the request went out is
  `ErrTimeout`; a gated reset is read from the reset code on the read, never from a
  write error.
- **A connection the node has decided to close is not used.** An entry with `closeIdle`
  set and nothing in flight (`connEntry.closing`) is closing: the decision is taken under
  `Network.mu` and the close follows outside it, so for that moment the connection is still
  in the table. `acquire` refuses it and `nodeConnLocked` skips it, so a call dials a new
  connection instead of failing on one the node itself ends (which would also memoize the
  member as unreachable). An entry with `closeIdle` set and something in flight stays
  usable until the last release closes it.
- **A connection at fault is closed.** A call that timed out, or failed with an
  error a live connection does not give, closes its connection and, when it was
  the node's last member connection, memoizes the node as unreachable; of several
  member connections the newest is used. The dials of calls are paced by the
  unreachable memo and the dials that keep a connection to a member by their own wait
  (`retryBackoff`), so every dial bypasses the library's backoff.
- **A connection is kept to the members that cannot be relied on to dial this node, and
  to no other.** `reach.go` records, per member and in memory, whether it can dial this
  node (unknown, reachable, unreachable; one answer decides; an hour; reset when the
  node's addresses change), and `keep.go` dials, on the tick, each member with an
  address and a record that is not reachable when no member connection to it is up: at
  most one dial per member and eight in all, 5, 10, 20, 40 then 60 seconds apart after
  failures, through `dialWithin` like a call and without the unreachable memo or the
  engine's worker. A member that does not serve the question counts as unknown.
- **A member connection nobody needs is closed through the table's close-when-idle, in
  one place.** `closeUnneeded` (`idle.go`), on the tick, marks a member connection that
  has been idle for five minutes (`connEntry.idleSince`) to close when idle when this
  node's record says the member can dial it and this node has shown the member (the
  `dialedAt` of the member's record, set when it answered the member's question) that it
  can dial the member. There is no connection manager and no second closer; a count
  limit, if one is needed, is one more reason in the same function.
- **Timers are the clock's.** The accounting's deadlines, the unreachable memo, the
  answers' age and the waits between dials and questions use the injected clock; a
  test advances it. A timer only wakes the loop; the
  accounting's `Overdue` is the truth.

## What the caller must guarantee

- Call `SetMembership` before the first connection is expected to complete an
  exchange: without a snapshot an exchange is refused. The snapshot is not changed
  after it is handed over.
- Call `SetNode` before `Listen`: a stream that arrives while no node is set is reset.
- `Facts.Revocations` and `Facts.SerialSeen` are loaded (an empty map says "nothing
  seen"). `Facts.Now` is ignored.
- Calls to `Send`, `Pull` and `QueryDirectory` come from the engine's outbound
  worker and never from its loop, as for any transport; so do `StartPairing` and
  `PairingFetch`, which dial and wait.
- `PairingFrame` has issued the `Send` and `Close` that answer a frame before it
  returns (a result fetch does not half-close, so the stream goes on until the
  initiator has taken the answer and closed), and `PairingClosed` ignores a session the engine does
  not know, since a stream can end before `StartPairing` has returned.
- Keep a node's connections to non-members at least as long as the exchange plus the
  online timeout: the accounting's non-member lifetime does, and `Params` keeps its
  online timeout equal to the pairing protocol's.

## Where the tests are

`network_test.go`, `member_test.go`, `dialback_test.go`, `keep_test.go`, `idle_test.go`, `pairing_test.go` and `helpers_test.go` run nodes in one process on the loopback,
each with its own fake engine and a static snapshot, and the fake clock where time
matters. `docs/references/network/README.md` lists what each test shows.
