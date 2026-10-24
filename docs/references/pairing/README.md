# Pairing

How two nodes establish mutual trust: the commit-reveal-open protocol that
yields a six-digit code for the two owners to compare, the order in which the
two nodes sign, the result exchange for an initiator that is not online, the
limits that bound a stranger's guesses, and the pre-confirmation of a node whose
identifier the owner entered ahead of time. This document describes the code as
committed in `internal/pairing`. The signed credential is described with the
[records](../records/README.md); the administrative operations that confirm and
reject are described with [administration](../administration/README.md); the
connection layer that carries the frames is outside this topic.

## What

**Two segments.** The *online segment* runs on one stream, in the connection the
certificates were exchanged on, under a time limit (30 seconds from the opening of
the stream, in `Params`). It ends with the target holding a request for its
owner and the initiator holding a record of its own. The *result segment* is a
series of request-and-answer exchanges, on any connection the initiator can make,
by which the initiator learns what became of the request and receives the
complete credential. An initiator behind NAT needs no inbound connection.

**Messages.** Every message is a deterministic CBOR map `{1: type, 2: payload}`,
the outer shape of a signed record, where the type is a string
(`sidera/pairing-commit/1` and so on) and the payload is a map of the fields
below; each is decoded strictly (unknown fields at either level, non-canonical
encodings and frames over 8 KiB are refused). The first frame of a stream is
routed by its outer type alone, so a frame is never tried as one message and then
as another. Only credentials are signed records; the sender of a message is
authenticated by the connection. The records a node persists have the same outer
shape.

| Message | From | Fields |
| ------- | ---- | ------ |
| M1 commitment | initiator | `1` commitment, `2` created_at, `3` not_after (optional) |
| M2 reveal | target | `1` nB |
| M3 open | initiator | `1` nA |
| M4 accepted | target | `1` pairing id, `2` expires_at |
| refusal | target, instead of M2 or M4 | `1` reason, `2` retry_after (optional) |
| F1 fetch | initiator | `1` pairing id, `2` partially signed credential or `3` withdraw reason |
| F2 result | target | `1` pairing id, `2` status, `3` credential (complete only), `4` retry_after, `5` rejection reason (rejected only) |

An explicit zero `retry_after` is refused. Reasons and statuses are small
integers listed in `message.go`; a refusal for a node that is retired, locally
revoked, already trusted or the node itself is one reason (`not_accepted`), so a
stranger cannot tell which.

**The construction.** Every concatenation is [`frame.Frame`](../records/README.md)
with a domain of its own: each field behind its own length, so no two inputs
frame to the same bytes. `terms...` stands for the framed fields listed first.

```
terms      = protocol id, then for the initiator and the target in this order:
             node id, identity key, network key, certificate serial,
             then created_at, not_after   (each field framed)
C          = SHA-256(Frame("sidera/pairing/commit/1", terms..., nA))
body       = terms..., C, nB, nA
pairing_id = SHA-256(Frame("sidera/pairing/transcript/1", body...))
PRK        = HKDF-Extract(SHA-256, salt = empty, IKM = nA || nB)
OKM        = HKDF-Expand(PRK, Frame("sidera/pairing/sas/1", body...), 32)
cred nonce = HKDF-Expand(PRK, Frame("sidera/pairing/credential-nonce/1", body...), 16)
code       = the first of the eight big-endian 4-byte integers of OKM that is
             below 4 294 000 000, modulo 10^6; shown as "482 019"
```

The keys are in the public-key encoding of the record format (38 bytes for
Ed25519), integers are 8 bytes big-endian, and an absent `not_after` is the
empty field. The protocol identifier is the one the stream negotiated and is an
input of the package. Each side takes its own end from its own connection (the
network key of that connection and the certificate shown on it, not the current
configuration) and the other end from the authenticated peer, never from a
message; the target recomputes the commitment from its own view, so any
disagreement between the two views fails it. An identity key equal to the
network key is refused.

**The credential is never sent.** Both sides compute it from the transcript:
the two node ids in their fixed order, `created_at`, `not_after` and the derived
nonce. The initiator signs first and puts its partial record in F1; the target
checks that the payload equals its own computation, that exactly one signature
is there and that it is the initiator's identity key's, then completes. The
initiator accepts the complete record only if it equals its own computation and
is signed by exactly the two identity keys.

**Confirmation.** A request is named by its pairing id. The owner confirms by
entering the code shown on the other node's screen; the node checks it against
the request the identifier names. A wrong code changes nothing and is not a
failure. Only an owner's explicit report that the codes differ counts as one,
on the side whose owner reported it; a mismatch claimed by the other side counts
for nothing here. A request whose code equals the code of another request that
still awaits its owner is not confirmed by code. Confirming trust and initiating
a pairing are authorized as `OpConfirmTrust` (local owner or full control),
rejecting as `OpRejectTrustRequest`; both run in the order `admin` defines. A
client that fetches a record through a management connection derives the
identifier, the code and the credential from the raw fields itself and never
shows digits a node reports.

**Expectations.** An owner with the right to confirm trust may enter a node
identifier ahead of time. When the connection authenticates exactly that node,
that side is exempt from the reveal budget, the pause and the cap, and confirms
for itself, without a human, after asking again whether the author still may
confirm trust. An expectation lasts 60 minutes, is used once, is dropped when its
author loses the right, and is deleted by anyone who may reject a request. At
most 32 are held. Each side decides only for itself: if only one side expects,
the other still compares, against the code the expecting side keeps showing
while the request is pending. Naming a target when initiating, an address, a
node found on the local network and anything a peer says about itself are not
expectations.

**Limits.** All counters are node-wide, not per peer, and persisted.

- *Reveal budget.* The target counts a reveal when it sends M2 to an initiator
  it does not expect: at most 5 in a sliding hour. Over budget it refuses with
  `paused` and a retry hint (at least one second).
- *Pause.* The third reported mismatch within an hour pauses pairing with nodes
  that are not expected: no new request is accepted or started, and requests
  already held are not confirmed. It lifts by itself when the failure that holds
  the count at the limit slides out.
- *A clock that steps back.* An entry is recorded at the later of now and the
  last entry, so the persisted lists stay in ascending order (a list out of order
  is refused on loading, and the node would lose its counters) and the windows
  only become more conservative. The retry hint is capped at 24 hours, the most
  a message can carry; a clock that stepped back by days therefore still yields a
  refusal the initiator can read.
- *Cap.* At most 16 unexpected requests wait at once, counting streams that
  reserved a slot. A full list answers `busy`; nothing is evicted. A request of
  the same initiator replaces an older one of its own only while that one holds
  no signature of the initiator (an orphan left by a crash); otherwise the new
  one is refused as busy.

**Ending without the credential.** The target ends a request by rejection,
withdrawal or expiry. The initiator never reruns the online segment; it shows
a segment that ended after it opened its nonce as an attempt used. Once its
signature may have left the node (the record is persisted as released before the
first F1 that carries it), any end that is not the complete credential (rejected,
expired, cancelled, unknown, given up, or withdrawn by its owner) makes it issue
the revocation of the credential it computed, at once and without waiting for
the target. A credential that arrives by synchronisation with a withdrawn
digest is refused. The initiator asks until the target stops keeping results
(24 hours after the request's expiry), then gives up.

**The fetch answers.** Only the initiator, by node (a node identifier fixes its identity key; not by
connection or network key), with a certificate serial not older than the highest
seen, gets a real answer. Everyone else gets `unknown`, the answer for a request
that does not exist or was forgotten.

**Restart.** Records are persisted as bytes, one per request; the identifier,
the code and the credential are recomputed on every load. Streams and fetch
schedules are not persisted. `Recover` completes a request that was confirmed and
signed but not marked complete, and issues again the revocation of a signature
that may have left; both are idempotent, and the completed credential has the
same bytes every time (Ed25519 signatures are deterministic and the digest does
not cover them). A request holds a confirmation and the initiator's signature
only if both arrived while it still waited, so `Recover` completes it even when
the restart comes after the expiry: both decisions predate it, and the initiator
that is waiting for the credential gets the one it signed for. A request that
lacks either one ends as expired as usual.

## Why

**Commit, reveal, open (ZRTP order).** The initiator must commit before it sees
the target's nonce and open after, so the code depends on a random value each
side chose before it saw the other's. A man in the middle gets one guess at the
code per reveal: whichever order the two sessions are interleaved, one side's
code is decided by a value revealed after the other side committed. All keys
and every negotiated parameter go into both the commitment and the derivation,
so offline search over keys gains nothing.

**The reveal budget counts reveals, not requests.** Aborting is free before the
target shows a code, so an attacker can abort and start again until the codes
agree. Only a count of reveals bounds the guesses, and only a node-wide count is
reliable, because an attacker can use any number of identities. The success
bound, stated plainly: **under sustained attack the chance that a man in the
middle gets the same code on both screens is at most the reveal budget times
10^-6 per window** (5 per hour is 5 in a million), on top of the human errors a
code cannot prevent, such as confirming without reading it.

**The availability trade-off.** Because the count is node-wide, any stranger who
can connect to a node can, at low cost, exhaust the budget and make pairing with
nodes that are not expected temporarily unavailable. That is accepted. The escape
is an expectation: a node whose identifier was entered ahead of time is exempt,
and cannot be impersonated, because the identifier is the digest of the identity
key. The same holds for the cap, which a stranger can fill with requests that
each cost it a reveal.

**No libp2p session binding.** The transcript binds the network keys the
connection authenticated, both identity keys and the certificate serials, and
the stream's negotiated protocol; the online segment runs on one stream of one
connection. go-libp2p v0.51.0 exports no channel-binding value for Noise, for
TLS over TCP or for relayed connections, and only an experimental interface for
QUIC, so a mandatory binding would fail for the relayed pairs that are common,
and an optional one would be two security levels and two code paths. What this
loses: an attacker that holds the network private keys of both nodes can carry
the messages between two sessions unchanged and be undetectable by the code.
Even then the credential can only be the one between the real two nodes, because
it needs both identity-key signatures; an attacker with the network key of one
side only can replace a request with a new one and withdraw the other side's with
F1, a visible denial of service.

**Initiator signs first, target completes.** Completing is always decided by the
target (sovereignty over who becomes trusted), which exposes most to strangers
and so never hands out half of its signature; it only publishes the complete
credential, which the trust graph publishes anyway. The initiator, which may be
offline, does not rely on the target to withdraw a signature: it revokes the
credential it computed itself.

**Messages are not signed records.** A signature by the network key would add
nothing to a connection already authenticated to it, and a signature by the
identity key would be a signature the other side chose the bytes for.

**Fetching answers `unknown` to strangers** so the existence of a request is not
disclosed, and does not depend on the connection so that an initiator that moved,
went through a relay or changed its network key is still served.

**The random numbers must not repeat.** If a target repeated nB, an attacker
could learn it in one session and choose its own nonce for the code offline in
the next. `crypto/rand` is the source; whether it can repeat after a virtual
machine is snapshotted, cloned or restored is not verified, and if it could, the
protocol would be defeated. Nonces are never persisted before they are
revealed.

## Where

| What | Where |
| ---- | ----- |
| Construction, vectors | `internal/pairing/transcript.go`, `code.go`, `hkdf.go`; `transcript_test.go` (`TestTranscriptVectors`, `TestCredentialRecordVectors`, `TestRejectionVectorEndToEnd`, `TestSampleCodeUnitVectors`, `TestNegativeVectors`, `TestEveryInputByteChangesThePairingID`), `hkdf_test.go` (RFC 5869 A.1 to A.3) |
| Messages | `message.go`; `message_test.go` (golden bytes, refusals, `TestDecodeOpening`, fuzz of every decoder) |
| Limits, expectations | `limits.go`, `expectation.go`, `params.go`; `limits_test.go` (`TestLimitsAgainstAModel`, `TestLimitsSurviveTheClockSteppingBack`) |
| Target and initiator | `target.go`, `initiator.go`, `lifecycle.go`, `command.go`, `state.go` |
| Persisted records | `records.go`, `codec.go`; `codec_test.go` (golden bytes, wrong shapes) |
| Scenarios | `scenario_test.go` (`TestRestartAfterExpiryCompletesARequestDecidedBeforeIt`) |
| Properties under faults | `property_test.go` (`TestPropertyRandomSchedules`, `TestCrashAtEveryPersistedStep`) |

The expected values of the vectors were computed by an independent Python 3
implementation (standard library `hashlib` and `hmac`, a hand-written HKDF and
CBOR encoder, and the `cryptography` package for Ed25519 with the keys of RFC
8032 section 7.1) and copied into the test as hex.
