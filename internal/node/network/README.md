# network

The node's connection to the other nodes: the libp2p host, the gate in front of
every stream, the table of connections and its accounting, the certificate
exchange that makes a connection a member's, dialing, and the client and server
of the send, pull and directory protocols. Read
[../../../docs/references/network/README.md](../../../docs/references/network/README.md)
for the reasons and for what the tests prove, and
[../../preauth/README.md](../../preauth/README.md) for the rules this package
applies.

This is a runtime package of layer 5 of [`internal/node/README.md`](../README.md).
It imports the contract packages `ident`, `nodeproto`, `preauth` and `record`,
`transport` and `clock`, and the networking library. It imports neither `engine`
nor `daemon`: it defines `Node`, which the engine implements, and `Router`, which
the engine calls through its own interface. It reads time only through the
injected clock (calls and dials are bounded by contexts, which run on the real
clock), touches no file and takes no randomness of its own.

## Entry points

| Name | Purpose |
| ---- | ------- |
| `New(Config)`, `Network.Close` | Build the host (it listens on nothing) and start the connection checks; close the listeners and every connection and wait for every goroutine the package started. |
| `Config`, `DefaultParams` | The network key, the clock, the accounting parameters (zero means `DefaultParams`), the log, and the timeouts (4 s to connect, 10 s per call, 10 s unreachable memo, 1 s tick). |
| `Network.SetNode(Node)`, `Node` | The engine's side: `transport.Handler`, `NotePeerCertificate` (a member's certificate before its connection becomes a member's) and `RefusalReason` (the name of a refusal, for the answer). |
| `Network.Listen(addrs)`, `ListenAddrs`, `PeerID` | Open the listeners (the address is in the error when one cannot open); where they are open; the identifier a contact line names after `/p2p/`. |
| `Network.SetMembership(Membership)` | The snapshot of the node's own certificate, the facts of a decision and the held certificates; decides live connections again and closes those that no longer keep. |
| `Network.SetAddressBook(map)` | The multiaddrs of each node, with `/p2p/<peer id>` unless a held certificate gives the peer. |
| `Network.Send`, `Pull`, `QueryDirectory`, `Connected` | The remote client, with the signatures of the loopback; whether a member connection to a node is up. |
| `NewRouter(self, loopback, remote)`, `Router` | The transport the engine calls: this node's calls to the loopback, every other node's to the remote client. |

## Rules

- **One gate.** `Network.handle` is the only place a stream handler is registered on
  the host, and `openStream` the only way a member protocol is opened: both ask
  `preauth.Allowed` with the state of the one connection. The library's own
  protocols (identify, ping) are registered by the host itself.
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
- **Timers are the clock's.** The accounting's deadlines and the unreachable memo
  use the injected clock; a test advances it. A timer only wakes the loop; the
  accounting's `Overdue` is the truth.

## What the caller must guarantee

- Call `SetMembership` before the first connection is expected to complete an
  exchange: without a snapshot an exchange is refused. The snapshot is not changed
  after it is handed over.
- Call `SetNode` before `Listen`: a stream that arrives while no node is set is reset.
- `Facts.Revocations` and `Facts.SerialSeen` are loaded (an empty map says "nothing
  seen"). `Facts.Now` is ignored.
- Calls to `Send`, `Pull` and `QueryDirectory` come from the engine's outbound
  worker and never from its loop, as for any transport.

## Where the tests are

`network_test.go` and `helpers_test.go` run nodes in one process on the loopback,
each with its own fake engine and a static snapshot, and the fake clock where time
matters. `docs/references/network/README.md` lists what each test shows.
