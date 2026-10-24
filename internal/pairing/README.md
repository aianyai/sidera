# pairing

How two nodes establish mutual trust, and the short verification code their
owners compare while they do. Read [../README.md](../README.md) first for the
conventions every contract package follows, and
[../../docs/references/pairing/README.md](../../docs/references/pairing/README.md)
for the protocol, the reasons behind it and its honest limits. The signed
credential is [`credential`](../credential/README.md); the envelope is
[`record`](../record/README.md); the one framing rule is
[`frame`](../frame/README.md).

The package is pure. It reads no clock, opens no socket, starts no goroutine and
touches no disk: the connection layer gives it the facts of a connection and the
frames of a stream, performs the actions it returns in the order it returns
them, and stores the records it hands out.

## Entry points

| Name | Purpose |
| ---- | ------- |
| `NewParty(cert, revocation, highestSerial, connectionKey, now)` | One end of a pairing as a connection shows it: node, identity key, the network key the connection is authenticated with, and the certificate serial. Judged by `record.NetworkKeyBelongs`, which also refuses a certificate below the highest serial seen. |
| `Terms`, `Terms.Commit(nA)`, `Open(terms, c, nB, nA)`, `Transcript` | The commitment, and the transcript it opens to: `ID()`, `Code()`, `Credential()` (the credential both sides sign, never sent). |
| `Code`, `Code.String()`, `ParseCode` | The six digits, shown as two groups of three. |
| `CommitMsg` (M1), `RevealMsg` (M2), `OpenMsg` (M3), `AcceptedMsg` (M4), `RefusalMsg`, `FetchMsg` (F1), `ResultMsg` (F2), `DecodeOpening` | The typed canonical CBOR messages, each with `Encode` and a strict `Decode...`. |
| `Params`, `DefaultParams()` | Every tunable number. The zero value of any field is invalid. |
| `Limits` | The reveal budget and the verification-failure pause, as persisted sliding-window counters. |
| `Expectation`, `Author` | A node identifier entered ahead of time, by someone allowed to confirm trust. |
| `State`, `Snapshot`, `Restore`, `State.Recover` | Everything one node keeps: counters, expectations, target records, initiator records, and the volatile streams and schedules. A value: each call returns the next one. |
| `World`, `Conn` | What a call reads of the rest of the node and of the connection. |
| `State.TargetOpen`, `TargetReceive`, `TargetClosed` | The target's online segment and the answer to a fetch. |
| `State.Initiate`, `InitiatorReceive`, `InitiatorClosed` | The initiator's online segment. |
| `State.Tick`, `OnFetchResult`, `OnFetchFailed` | Time, and the result exchanges of the initiator. |
| `State.Confirm`, `Reject`, `Expect`, `Forget` | The management requests, in the order `admin` defines. |
| `State.Withdrawn(digest)` | Whether a synchronised credential is one this node signed and revoked. |
| `TargetRecord`, `InitiatorRecord`, `Display`, `Encode`/`Decode...` | The persisted records. The identifier, the code and the credential are recomputed whenever a record is decoded. |
| `Action` and its types | What the caller does: `Send`, `Close`, `FetchRequest`, `Persist...`, `Delete...`, `StoreCredential`, `StoreRevocation`, `Notify`. |

## Rules

- **Commit, reveal, open.** The initiator sends a commitment to every negotiated
  parameter and to its nonce; the target reveals its nonce only after it has
  checked the request; the initiator opens its nonce last; the target checks the
  commitment against its own view of the connection. The code, the pairing
  identifier and the credential nonce all come from the opened transcript.
- **Every concatenation is `frame.Frame`** with its own domain, so no input of
  one hash can be read as an input of another. The domains are listed in the
  records reference.
- **Each side takes its own end from its own connection.** The local network key
  and certificate serial are those of this connection, the remote ones those
  the connection authenticated. A message never supplies a key.
- **Nobody signs bytes the other chose.** Both sides compute the credential from
  the transcript. The initiator signs first and only after its own owner
  confirmed; the target checks that signature against its own computation,
  checks its own confirmation, and completes. Only complete credentials are
  stored.
- **A confirmation names the request and carries the code.** The node checks the
  code itself. A wrong code changes nothing and counts for nothing; only an
  owner's explicit report that the codes differ counts as a failure. A request
  that shares its code with another one awaiting an owner is not confirmed by
  code.
- **The reveal budget counts when the target sends its nonce**, to initiators it
  does not expect, and is persisted before the nonce leaves. The pause starts at
  the third reported mismatch in the window. Both are node-wide.
- **The pending list has a cap and no eviction.** A new request from the same
  initiator replaces an older one only while the older one holds no signature of
  the initiator.
- **An expectation is single use, expires, and stands only while its author
  still may confirm trust.** It exempts a node from the budget, the cap and the
  pause, and confirms for its side. Naming a target when initiating is not an
  expectation.
- **A signature that left the initiator is revoked** when the pairing ends in
  anything but the complete credential, at once and with the credential the
  initiator computed itself; `State.Withdrawn` refuses the credential if it
  arrives by synchronisation.
- **Fetching is indistinguishable.** Anyone who is not the initiator of a
  request, or who presents an older certificate than one already seen, gets
  exactly the answer for a request that does not exist.
- **The initiator never reruns the online segment by itself.** It reports a
  segment that ended after it opened its nonce as an attempt used.

## What the caller must guarantee

- Perform the returned actions in order, and finish each persist before the next
  action. A frame is sent only after the persist that made it safe; a
  credential is stored before the record that says it was.
- Persist the records it is handed exactly as `Encode` gives them, one file per
  record named by `ID()` (limits and expectations each in one file), replacing
  atomically with a checksum. On loading, decode with the `Decode...` functions,
  compare `ID()` with the file name, call `Restore` and then `Recover`.
- Call `Tick` on a timer and after every command and event; it issues the result
  exchanges that are due, including the one right after a confirmation.
- Build `Party` values from certificates it has verified, with the revocation
  lookup stated, for the connection the stream runs on. The certificate given to
  `NewParty` must be the one the serial rule (`record.DecideSerial`) accepted in
  the certificate exchange of that connection, for the remote end as for the
  local one: a certificate that was merely seen, or an older one, would bind a
  key the node no longer stands behind into the transcript. The negotiated
  protocol identifier comes from the stream.
- Give `crypto/rand` as the source of randomness. A target that repeats a nonce
  lets an attacker learn it in one session and use it in the next, so the
  randomness must not repeat after a virtual machine is snapshotted, cloned or
  restored.
- Keep the connection lifetime of nodes that are not members at least as long as
  the certificate exchange plus `Params.OnlineTimeout`.
- Store a credential from `StoreCredential` as a credential of the trust view,
  and a revocation from `StoreRevocation` with the other revocations.

## Dependencies

The standard library, `admin`, `credential`, `frame`, `ident`, `record`,
`revocation`, `timing`, `trust`. No libp2p: the negotiated protocol identifier is
an input, and no constant for it is defined here.
