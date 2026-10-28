# Identity

Identifiers for nodes, people and endpoints, the public-key type they are
derived from, their text form, and the keys a node holds on disk. The signed
records that carry keys and certificates are described in
[records](../records/README.md).

## What

| Identifier   | Length   | Meaning                                                              |
| ------------ | -------- | -------------------------------------------------------------------- |
| `NodeID`     | 32 bytes | A node. A digest of its long-term identity public key.               |
| `PersonID`   | 32 bytes | A person. A digest of their root public key.                          |
| `EndpointID` | 16 bytes | An endpoint on a node. Random, issued by that node.                  |
| `Address`    | -        | `{Node, Endpoint}`: one endpoint on one node.                         |

- Lengths are fixed. Constructors reject input of any other length.
- The all-zero value means "unset" and is never a valid identifier. Constructors
  and parsers reject it, so a non-zero value obtained from the package is valid.
  An `Address` is zero if either part is.
- An `EndpointID` is random and never changes once its node has issued it.
  `NewEndpointID` reads it from a caller-supplied `io.Reader`.
- One `EndpointID` value is reserved and never issued: the system endpoint (next
  section).

### The system endpoint

`SystemEndpoint()` returns the `EndpointID` of sixteen `0xff` bytes; its text is
`ep1lllllllllllllllllllllllllusa628q`. `EndpointID.IsSystem()` reports it. It is
the sender a node gives to its own channel feedback (a notice that a message was
refused, expired or went unanswered), so a reader can tell feedback from a
message another endpoint wrote by looking at the endpoint part alone.

- `NewEndpointID` fails with `ErrReserved` if the randomness is exactly this
  value, as it fails with `ErrZero` for all zeros. It does not draw again: a
  source that produces such a block is broken, and a loop would hide that (and
  never end for a constant source). The chance of an honest draw is 2^-128.
- Parsers and `EndpointIDFromBytes` accept it, because a node has to write it
  into records and pages.
- Refusing it as a recipient on the wire is the envelope's rule (see
  [envelope](../envelope/README.md)). Refusing it as the sender of a request is
  the policy's request shape check (see [policy](../policy/README.md)), shared by
  the check of requests from other nodes and of requests from this node's own
  endpoints. This package only reserves the value and does not judge anything.

### Public keys

A `PublicKey` is an algorithm tag and the key bytes. The tag is a small integer
table: 1 is Ed25519, 2 is reserved for P-256 (hardware-backed keys) and is
refused today, 0 is "none". `NewPublicKey` accepts a key only if

1. the tag is 1 and the key is 32 bytes;
2. the encoding is the canonical one: the y coordinate is below 2^255 - 19, the
   point is on the curve, and an x of zero does not carry the sign bit;
3. the point is not one of the eight points of order 1, 2, 4 or 8.

`PublicKey.Verify` additionally depends on the standard library's Ed25519
verification, which refuses a signature whose scalar is not reduced. That makes a
valid signature non-malleable for a third party: nobody who merely holds it can
produce a second valid encoding. It does not make the signature unique for a
message: the signer can make many different valid signatures of one message, so
a signed statement is identified by its digest (see records), never by its
signature.

Mixed-order keys are not rejected. A key whose point is a prime-order point plus
a non-zero small-order point is a valid curve point with a canonical encoding,
and screening it out takes a scalar multiplication per key. The consequence is
that one secret scalar can stand behind up to eight public keys and so eight
identifiers. That grants nothing: anyone can create as many unrelated
identities as they like by generating keys, a signature made for one of the eight
keys does not verify under another (the challenge hash covers the public key
bytes), and no third party can forge a signature or re-encode one. Only the
eight points of small order themselves are refused, because a key of small order
verifies signatures for many messages.

### Derivation

```
NodeID   = SHA-256(Frame("sidera/id/node/1",   tag, key bytes))
PersonID = SHA-256(Frame("sidera/id/person/1", tag, key bytes))
```

`Frame` is the one framing rule of the project: the domain and every field,
each behind its uvarint length (see [records](../records/README.md)). `tag` is
the one-byte algorithm tag. The key bytes are the canonical key bytes.

### Text form

bech32m (BIP 350). The human-readable part names the kind: `node`, `person`,
`ep`. The data part is the identifier bytes regrouped into 5-bit values, then
the six-character checksum.

| Kind       | Text length    | Prefix   |
| ---------- | -------------- | -------- |
| `NodeID`   | 63 characters  | `node1…` |
| `PersonID` | 65 characters  | `person1…` |
| `EndpointID` | 35 characters | `ep1…`  |

An `Address` is written `<node>/<endpoint>`; neither part contains `/`.

The parser accepts exactly one text per identifier:

- lowercase only (BIP 350 allows an all-uppercase form; it is refused, so one
  value does not have two texts);
- the expected prefix (a `person` text where a node is expected fails with
  `ErrPrefix`, but only after the checks that come before it: total length, the
  character range, lowercase, the separator and a checksum-length tail, the
  alphabet and the bech32m checksum. Only a well-formed bech32m string of
  another kind gets as far as the prefix check; a damaged one fails earlier with
  the earlier error. After the prefix come the padding bits, the canonical
  re-encoding and finally the data length);
- the expected data length, zero padding bits, and the bech32m checksum constant
  (a string with the older bech32 constant fails the checksum);
- after decoding, the identifier is encoded again and compared with the input.

### The node's keys on disk

The node's own keys live in `keys/` of the data directory (package
`internal/node/keys`). Every file is owner-only (0600 on Unix, an owner-only DACL
on Windows), in a directory that is owner-only too. A file of `keys/` that is a
symbolic link is refused as too broad whatever it points to, because the check
sees the link and the read follows it
(`TestAKeyFileThatIsASymlinkIsRefused`,
`TestAKeyFileThatIsASymlinkWithOwnerOnlyBitsIsRefused`).

| File | Holds | Written by |
| ---- | ----- | ---------- |
| `identity.key` | The identity private key: an Ed25519 seed. Signs the network-key certificates and the self-revocation, nothing else. | `init`, once |
| `network.key` | The network private key: an Ed25519 seed. Signs everything the node signs on the network. | `init`, once |
| `network.cert` | The current network-key certificate, as the record wire bytes. | `init`, each renewal |
| `network-serial` | The highest certificate serial this node has issued, and the digest of the certificate issued at it. | `init`, each renewal, first |
| `self-revocation.rec` | The node's statement that its identity key is void, as the record wire bytes. | `init`, once |
| `clock.rec` | The wall-clock high water mark and, while a rollback pause is in force, since when. | every minute; each renewal, when it raises the mark; the owner's clear |
| `init.rec` | The marker that says `init` finished. | `init`, last |

The serial and the clock live here, and not under `state/`, because deleting
`state/` must not make a serial reusable or erase the mark and the pause.

**Formats.** The three records that are not wire bytes of a signed record, and
the two key files, are singleton records in the record framing of the data
directory (`recfile`; see [storage](../storage/README.md#the-records-of-state)
for the first line, its checks and the reasons). Each has one fixed file name and
its key is its kind. The body is JSON in the one canonical form the writer
produces: fields in the order below, no spaces, times as UTC in whole seconds
(`2026-10-24T05:56:00Z`). A file is read only in that form.

| Kind (file) | Body |
| ----------- | ---- |
| `identity-key`, `network-key` | `{"algorithm":"ed25519","seed":"<43 characters of base64url>"}` |
| `serial` (`network-serial`) | `{"node":"node1…","serial":1,"digest":"<64 hex>"}` |
| `clock` (`clock.rec`) | `{"high_water":"…Z"}`, or `{"high_water":"…Z","rollback_paused_since":"…Z"}` while paused |
| `init` (`init.rec`) | `{"completed_at":"…Z"}` |

**Loading.** The node refuses to start, naming the file, when the directory or a
file can be reached by anyone but the owner, when a file is missing or damaged
(`init.rec` included: only the marker says that `init` finished; a file larger than
the largest record file is damaged, and is not read), or when the
files contradict each other: the certificate must be for the node of
the identity key and name the network key; the serial file must be this node's
and not behind the certificate, and at the certificate's serial it must name the
certificate's digest; the self-revocation must verify under the identity key. It
never changes a permission and never repairs a file.

**The certificate and its serial.** A certificate is valid for 30 days from its
issue (it starts the clock tolerance earlier, see [records](../records/README.md))
and is renewed when 10 days or less remain, when it has expired, when it starts
after the current time, or when an earlier renewal was interrupted. A renewal
writes the next serial to `network-serial` first, durably, then raises the mark
of `clock.rec` to the time it signs at (when that is above the mark), and then
writes the certificate. The serial is the commit point: it is never issued twice. A crash between the two writes leaves a
gap in the serials, which peers accept because they require only that serials
rise, and a certificate whose serial is below the serial file's, which is an
*interrupted renewal*: the node starts, reports it, and renews at once, whatever
the remaining validity, at the serial after the reserved one. The renewal
refuses to sign during a rollback pause and when the clock reads behind the
persisted mark by more than the clock tolerance.

A certificate that starts after the current time is what a forward jump of the
wall clock leaves behind once the owner has cleared the pause of the clock that
was set right again: it was signed on the fast clock and is not valid for the real
one, so nothing can be signed under it. It is due at once, whatever the remaining
validity, so that the renewal that follows the clear replaces it, and signing
works again within that same pass of the daemon's periodic tasks.

**The clock record.** The periodic write never lowers the mark, never lifts or
moves the pause, never writes without a mark, and refuses a time the decoder
would refuse (before 1970-01-01T00:00:01Z, after year 9999), in either of its two
times, except that a pause that started before 1970-01-01T00:00:01Z is stored with
its start at that second. Only the owner's clear sets
the mark to the current time, which may be lower, and removes the pause, in one
replacement of the file. A missing `clock.rec` is a first start; a damaged one
refuses the start.

**`init`.** It looks before it writes and never overwrites. A key file that
exists and parses is reused; one that does not parse is refused, naming it. The
self-revocation is reused only if it verifies under the identity key that is
current; one that does not is refused, never replaced. The clock record is kept
whatever it says, so that deleting `init.rec` is no way around a rollback pause.
The certificate is the one file that may be replaced, because it can be issued
again: when it does not match the keys and the serial file, a new one is signed
at a serial above every serial known, and the serial file is written first, at
the later of the current time and the persisted high water mark (see the clock
rules below); a
serial file missing next to a valid certificate is written from the certificate.
`init` that finished is refused. Deleting `keys/` and running `init` again makes
a new identity, certificate and self-revocation.

**The self-revocation.** `Keys.SelfRevocation` returns the statement `Load`
verified as the statement of this node's identity key: the record wire bytes, the
record digest (see [records](../records/README.md)) and the node. A running node
retires itself and exports with it, so that no caller has to compare the node of a
statement with its own. `ReadSelfRevocation` reads and verifies the file for a
caller that has not loaded the keys, such as an export while the node is stopped;
it cannot say that the statement is this node's, only that it verifies under the
key of the node it names. `FormatRevocationExport` is the one formatter of the
export, and panics on the zero statement (which only a caller that built one by
hand could pass): an export of nothing would read as a backup that succeeded
(`TestFormatRevocationExportPanicsOnAZeroSelfRevocation`):

```
-----BEGIN SIDERA SELF-REVOCATION-----
<record wire bytes in standard base64, 64 characters per line>
-----END SIDERA SELF-REVOCATION-----
```

with a trailing newline, and the digest returned apart from the armor, because the
record digest is what identifies the statement. `ParseRevocationExport` is its
inverse: it decodes with `encoding/pem`, verifies the record as a node
self-revocation signed by the identity key of the node it names, then formats
the verified statement again. The entire input must match that canonical text
byte for byte, with the final LF optional. Text before or after the block,
multiple blocks, headers, other line endings and non-canonical base64 are refused
with `keys.ErrCorrupt` and a zero statement. A successful result holds the wire
bytes, digest and node of the same verified statement. Where an export is written
and with which permissions are the caller's. The package writes nothing into `keys/`
for a retirement; `CheckNotRetired` refuses to run a node whose identity is among
the loaded revocations, because a retired identity cannot become valid again.

**What the daemon does with them.** The daemon starts with the keys loaded and no
signature made (step 3); judges the clock (step 4); and renews the certificate when it
is due (step 5), before anything is signed, and then every hour in the loop, where
everything that signs runs, so the renewal replaces the certificate the engine signs
the node's reports under without two goroutines touching it. A node that may not sign
renews nothing and says so, by the one judgement of signing the engine hands the
check: a paused node waits, and the owner's clear makes the check due at once, so the
renewal follows it in the same pass, on the clock the owner accepted (a certificate
signed on a clock that was ahead is not yet valid for the real one and is due at
once; `TestAClockThatJumpedForwardAndWasPutRightLeavesACertificateTheClearRenews`); an
identity retired by the owner signs nothing from the commit on, so the check made in
the time before the daemon is down finds nothing to do
(`TestACertificateThatFallsDueAfterTheRetirementIsNotSigned`). A node whose identity
is retired is refused at step 3, before the clock is judged.

The owner's commands on the identity are four operations of the management
interface, all held to the local owner:

| Command | What it does |
| ------- | ------------ |
| `identity export-revocation` | Prints the armored statement and its digest. The statement is the one the keys verified at load, and the export is not stored under `state/`. |
| `identity confirm-backup <digest>` | Records, in `config/revocation-backup.json`, that the owner keeps a copy outside this device. The digest must be that of the statement the node holds; the file names it, so a confirmation made for an earlier identity says nothing about a node made again over the same data directory. |
| `identity retire --node <id>` | Keeps the statement as a verified revocation in `config/revocations/` and stops the daemon. From the commit nothing is signed in the node's name, even before the daemon is down, and every later start refuses to run. The node is named so that a retirement meant for another data directory is refused. |
| `clock clear-rollback` | Lifts the pause of a wall clock that was set back: the mark becomes the clock as it reads and the flag is removed, in one replacement of `clock.rec`. |

A retirement is for good, so the deliveries the node has not settled when it is
retired are abandoned, without feedback to their senders: the node never starts
again, and nothing is left to send a report, close a delivery or answer a pull.
That is what retiring an identity means, not a loss the channel owes feedback for.
From the commit until the daemon is down everything that would take in work or sign
refuses with `identity_retired`, so no new acknowledgement is given for work that
will be abandoned: a send or a reply is refused by the capacity check, an envelope
that arrives is refused before it is judged, and a pull is not answered. The one
answer that is not refused is a repeat of a request the node had already
acknowledged, which is answered with the first answer, a statement that was true
when it was made; the state of the message stays readable. Nothing acknowledged
after the commit is lost, because nothing is acknowledged after it
(`TestAfterTheIdentityIsRetiredASendIsRefusedAndNothingIsRecorded`).

Until the backup is confirmed `status` reminds, first, on every call. The reminder is
derived from the confirmation on disk and the statement the node holds, and has no
record of its own.

**Windows.** The owner-only check is the DACL rule of the file system layer: the
owner is the user, Administrators or SYSTEM, and only those are granted.
`TestWindowsDACLWiderThanTheOwnerIsRefusedNamingTheFile` widens the DACL of each
file and runs only on Windows; it passes in the Windows CI jobs. The order in
which two renames reach the disk after a power loss is not documented for
Windows, and the file system layer does not flush a directory there, so the
certificate's rename can survive while the serial file's is lost (not exercised
by CI, that is, no CI job can observe it: no job loses power). The node then
refuses to start naming `network-serial`, and the owner mends it as described
under the refusal of a serial file behind the certificate below.

## Why

- **Fixed-length value types.** Each identifier is a struct around an unexported
  array. It is comparable, so it works as a map key, and it cannot be built from
  a slice of the wrong length. A plain `[]byte` or string would be neither, and
  every caller would have to re-check the length.
- **Injected randomness.** Endpoint IDs need a random source, but the contract
  packages are pure: no hidden I/O, replayable in tests. Callers pass
  `crypto/rand.Reader`; tests pass a deterministic reader.
- **Zero rejected everywhere.** The zero value is what an uninitialised field
  holds. If it were a valid identifier, an unset field could pass as a real
  node, person or endpoint without any error. Refusing it at every entry point
  makes "unset" distinguishable from every real identifier.
- **The system endpoint is reserved in the shared identifier contract.** An
  endpoint identifier is chosen freely by the node that owns it, and the
  receiving node cannot check it; a message arrives with the sending node
  (known from the connection) and an endpoint part the remote node picked. If
  each node chose its own system endpoint, a remote node could still send from
  `<its node>/<any endpoint part>`, including the value this node uses, and an
  Agent would have to compare whole addresses, two opaque texts of about 60
  characters each, character by character, to tell feedback from impersonation;
  a model does that unreliably. Reserving one value for every node lets every
  node refuse it on the wire, so the endpoint part alone says "this node's
  channel feedback", and the rule exists before any node issues an identifier:
  changing it later would mean migrating the records and pages that already
  hold the value. All-ones was chosen because its text, `ep1` followed by a run
  of `l`, is recognisable at a glance and no random identifier looks like it; it
  is also not the all-zero "unset" value, which stays refused everywhere. The
  value is refused in both directions: as a recipient by the envelope, and as a
  sender by the policy shape check, because a request claiming to come from it is
  forged or a bug: the node's own feedback is exempt traffic and is never judged
  as a request.
  Rejected: a value chosen per node (reasons above); a random value stored in
  node configuration (it leaks through any reply and is reusable by the remote
  party); an explicit "feedback" flag on the page (a change to the page format,
  seen by direct endpoints too, and it would not stop a remote node from using
  the same endpoint part).
- **bech32m text.** Node identifiers are copied and compared by people (a trust
  setup that skips the verification code depends on both sides knowing the
  other's node identifier in advance), so the text needs a kind marker and typo
  detection. bech32 puts the prefix inside the checksum, so a person text cannot
  be pasted into a node field, and its six-character checksum is guaranteed to
  catch up to four character errors. The text is one lowercase word without
  separators, so a double click selects it and it is safe in a file name or URL.
  Rejected: plain hex (no kind, no typo detection), Syncthing-style grouped
  base32 with check characters (needs a custom checksum, groups break
  selection, no kind), base58 (case-sensitive, no checksum), multibase and CID
  (a dependency and variable-length machinery we do not use), `nodekey:`-style
  prefixed hex (no typo detection). bech32m rather than bech32: the original
  checksum does not notice insertions or deletions of `q` before a final `p`;
  bech32m changes one constant to remove that. The package carries its own
  roughly 100-line implementation because the available libraries are not
  importable or bring in large unrelated dependency trees.
- **Padding bits and re-encoding.** 32 bytes are 256 bits but 52 five-bit
  characters carry 260, so the last character has four spare bits; a decoder that
  ignores them accepts 16 different texts for one identifier. A second text for
  an identifier is a second spelling that logs, allow lists and comparisons can
  disagree about. The padding bits must be zero and the result is re-encoded and
  compared, which also covers anything the individual checks missed.
- **The tag and a domain in the derivation.** Without a tag, a second algorithm
  with keys of the same length could collide with Ed25519 keys; the tag is the
  same small integer the record format carries. The domain differs between
  nodes and people so that a single key used in both roles gives two unrelated
  identifiers, and it is versioned so that changing the derivation means a new
  domain and cannot silently re-label existing identifiers. The input goes
  through the same `Frame` as record signatures and digests, with the domain and
  each field behind its length: one joining rule for everything that is hashed
  or signed, so no derivation has to argue on its own that its input is
  unambiguous, and a domain added later cannot be a prefix of these.
- **Canonical keys only.** The standard library's point decoding accepts
  non-canonical encodings of the same point, so hashing "whatever 32 bytes
  arrived" would let an attacker create a second identifier for a key they do
  not hold. Small-order points validate signatures for many messages; no
  legitimate identity is one. Both are refused when the key is built, so every
  `PublicKey` is safe to derive from. The check is done with `math/big` because
  the standard library does not expose its decoder, the inputs are public and
  speed does not matter.
- **The key type lives in `ident`.** Derivation takes a key, and the record
  package depends on `ident`. If the key type lived next to the record format,
  `ident` would have to import `record`, or derivation would have to accept raw
  bytes and repeat the canonical checks. The validation is part of what an
  identifier means, so it belongs with the derivation.

### Why the node's keys are kept this way

- **A 0600 file, refused when wider.** Operating-system key stores are not
  available unattended on every platform, and a wrapper that reaches one through a
  command line still hands the key to every process of the same user. OpenSSH,
  WireGuard and Syncthing keep a key in an owner-only file and sshd refuses a
  wider one. The node does the same and never narrows the permissions itself:
  only the owner knows who has read the file already.
- **The record framing for the key files too.** One checksummed, length-checked
  first line, shared with `state/`, and one canonical body mean one decoder for
  every small file of the directory and one test of "what decodes encodes to the
  same bytes". The framing's damages name the rule that failed and never the
  first line, which a damaged newline can fill with the body of a key file. PEM or
  OpenSSH key formats would add a second parser for a file only this program
  reads. The key is the 32-byte seed, not the 64-byte Go form, so that nothing
  stored can disagree with the key derived from it.
- **Canonical form only.** A body that decodes but differs from what the writer
  writes (spaces, another field order) is read as damage. It removes the question
  of which spelling a file may have, and makes the decoders fuzzable for
  round-tripping.
- **Errors never quote a file.** Terminals and logs are not places for a seed,
  damaged or not; the decoders report which check failed. Keys print as a fixed text that names only
  their node, as a pointer or as a copy, and holders of keys are held behind a
  pointer so that they print as an address, because `fmt` shows the unexported
  fields of a struct whatever methods its members have. The self-revocation is
  handled the same way for the same reason: its wire bytes are a declaration that
  anyone holding them can publish to retire the identity irreversibly, so it
  prints as a fixed text naming only its node and digest
  (`TestSelfRevocationPrintsOnlyItsNodeAndDigest`).
- **The serial is written before the certificate and is the commit point.** If the
  certificate came first, a crash between the writes would leave a published
  certificate whose serial the serial file did not know, and the next renewal could
  issue the same serial for another certificate: peers that saw the first would
  call the second a conflict, a sign of a copied identity key. With the serial
  first, the worst case is a gap, which costs nothing. Where a directory
  flush reaches the disk (Unix), the serial's replacement is durable before the
  certificate is written, so no power loss shows the certificate without the
  serial; on Windows the order of the two renames at a power loss is not known (see
  the Windows paragraph above), and the refusal below is the safety net. Rejected: one file holding both (a serial and a certificate
  are replaced together or not at all, and a lost write would take the serial with
  it), and a counter in `state/` (deleting `state/` would reset it).
- **Only the marker says `init` finished.** The transaction layer cannot require
  a write that may be a reused file, so every write of `init` is optional, and a
  crash after the self-revocation leaves keys that all verify next to a missing
  clock record and configuration: a `Load` that accepted it would start the node
  as a first start with no owner decisions. The marker is the one required write
  and the last, so `Load` refuses without it and the recovery predicate finds no
  work in such a directory: the owner runs `init` again, which reuses what is
  valid.
- **An interrupted renewal is a start, not a refusal.** The state is a normal
  product of a crash and harmless to repair, so refusing would turn a crash into an
  outage that needs the owner. Repairing it in `Load` would make a read write; the
  startup renewal is the repair.
- **A serial file behind the certificate is refused.** No step of a renewal makes
  it, so it means a file was edited, restored from a backup or lost in a power loss
  that reordered renames. Continuing could issue a serial twice; the refusal is
  loud, cheap to notice, and fixed by the owner: the error says to delete
  `keys/init.rec` and run `init` again, which signs a new certificate above both
  serials and writes the serial file first (`TestInitReSignsACertificateThatDoesNotMatch`,
  its row for a serial behind a valid certificate). The one rule is applied by
  `Load` and by the recovery predicate alike, so the owner reads the same reason
  from either.
- **No signing while the clock is in doubt.** A certificate signed with a clock
  that was set back starts before certificates the node already issued. The check
  is repeated in the renewal, next to the signature it protects, because the
  persisted mark and the pause flag are this package's. The one signing allowed
  during a pause is the re-signing of a re-run `init`, and it does not refuse:
  only a running daemon clears a pause and the daemon cannot start before `init`
  has finished, so a refusal would leave the owner no way out but deleting
  `clock.rec`, which loses the pause. `init` signs at the later of the current
  time and the persisted mark, so the certificate does not start before what the
  node already issued, and it leaves `clock.rec` as it is. The next renewal after
  the pause is cleared replaces that certificate
  (`TestInitSignsNoEarlierThanTheHighWaterMark`).
- **A write refuses what its own decoder would refuse.** A clock reset to zero by
  a dead battery is exactly when the rollback pause must be written, and a pause
  that started at the epoch would be written and then read as a damaged file; the
  owner's way out would be to delete the file, which loses the pause and the mark
  together. One rule of what a time may be serves the decoder and every write
  (`SaveClock`, the owner's clear, the marker and first clock record of `init`), and
  a write that cannot be read back fails before it writes. The one value that is
  clamped instead of refused is the start of a rollback pause before
  1970-01-01T00:00:01Z: refusing it would leave no pause persisted exactly when the
  clock is wrong, and the start is only the alert's "since", so the earliest
  second stands for it. The mark is never clamped
  (`TestSaveClockClampsAPauseThatStartedBeforeTheEarliestSecond`).
- **A renewal raises the mark with the certificate.** The mark exists to catch a
  clock that is ahead of what the node has signed under. A certificate signed above
  the persisted mark defeats it: a daemon stopped within a minute of the renewal
  never persisted the mark, and after the clock was set back it started without a
  pause although the certificate was not yet valid. The write of the mark is part
  of the renewal's transaction, between the serial and the certificate, so no state
  on disk has a certificate above the mark
  (`TestRenewRaisesTheMarkOfTheClockToTheTimeItSignsAt`, and the same check in
  every crash state of `TestRenewalReplaysToAConsistentStateFromEveryCrashState`).
  It comes after the serial because the serial is the commit point and stays the
  first write; a crash between the two leaves a mark that is only higher than the
  clock has read, which costs nothing.
- **A certificate that is not yet valid is due.** The expiry rule alone looks only
  at the end of the validity, so a certificate signed on a clock that was ahead and
  then cleared looked healthy for as long as the fast clock's validity lasted while
  nothing could be signed under it.
- **The clock record never goes back through the periodic write.** If it could,
  the in-memory mark's rule (raise only) would be enforced by every caller instead
  of by the file; and lifting the pause by an ordinary write would skip the owner's
  confirmation. The clear is a separate function that does exactly that and
  nothing else.
- **`init` never overwrites, and refuses a statement of another identity.** A
  private key lost to an overwrite is lost for good, and an `init` that rewrote
  `config/` or `clock.rec` would undo the owner's decisions or a rollback pause.
  Reusing a valid file and refusing an invalid one costs nothing, because an
  interrupted `init` wrote only synced defaults. A self-revocation left behind by
  an identity the owner deleted would, if reused, give the new node a backup that
  retires nobody; if replaced, it would destroy a statement the owner may have
  exported. It is refused. `age` and `ssh-keygen` make the same choice for key
  files (create exclusively, never replace). The certificate is the exception
  because nothing is lost: it is re-signed from the keys.
- **Keys are read from the reader and nothing else.** `ed25519.GenerateKey(nil)`
  reads the system source without importing it, and since Go 1.26 the generators
  of `crypto/ecdh` and others ignore the reader they are given, so neither can be
  made reproducible, and the import scan of the module cannot see the first. The
  key is 32 bytes read with `io.ReadFull` and built with `NewKeyFromSeed`; a test
  fixes a node's identity with a seeded reader, and a reader that fails fails
  `init`.
- **30 days, renewed at 10.** A leaked network key stops being accepted within a
  month even if nobody revokes it; a node that has been off for a week still
  holds a valid certificate when it returns.
- **The loaded keys hand out the self-revocation.** A reader of the file cannot
  know whose node it runs for, so a caller that read it would compare the node of
  the statement with its own at the call site, and a retirement cannot be undone:
  writing another node's statement would leave this node running while the owner
  believes it retired. `Load` has the identity key and has already verified the
  statement as this node's, so it keeps that statement and hands it out. The fields
  are unexported so that no one can build a statement whose digest is not the
  digest of its bytes.
- **The armor is base64 between two marker lines.** The statement is copied by
  hand, printed or kept in a password manager; the text survives all three, and
  the markers say what it is. Standard base64 with padding is what every tool
  decodes. The digest is the record digest rather than a hash of the armor so that
  the same statement has one name wherever it is held.
- **The export parser checks by re-encoding.** `pem.Decode` searches for a block
  and returns any trailing input; it also accepts spellings the formatter does
  not write. Decoding alone could therefore accept a damaged or concatenated
  backup, a failure that stays hidden until the owner needs to retire the
  identity. Verifying the statement and comparing the complete text with the
  existing formatter's output keeps the armor rules in one place and refuses
  a false success without another parser or dependency.

## Where

- `internal/ident/ident.go`: the identifier types, constructors, parsers and
  `NewEndpointID`, `SystemEndpoint` and `IsSystem`.
- `internal/ident/bech32m.go`: the bech32m codec and the canonical-text check.
- `internal/ident/key.go`: `PublicKey`, `Algorithm`, the canonical-point and
  small-order checks.
- `internal/ident/derive.go`: `NodeIDFromKey` and `PersonIDFromKey`; the
  derivation domains are listed in the domain table of the records reference.
- Tests: `ident_test.go` (identifier rules, text vectors computed with an
  independent implementation, `TestParseKindRules`; the system endpoint:
  `TestSystemEndpointValue`, `TestSystemEndpointTextGolden`,
  `TestSystemEndpointRoundTrip`, `TestIsSystem`,
  `TestNewEndpointIDNeverIssuesReserved`), `bech32m_test.go` (specification
  vectors), `key_test.go` (key rules, an oracle that decodes points with a
  separate method, and a derivation of the torsion list from the curve
  equation, and `TestDecoderDenominatorIsNeverZero`, which backs the decoder's
  omission of a zero check), `derive_test.go` (derived identifiers for the RFC
  8032 test keys), `fuzz_test.go` (every parser and the key constructor).
- `internal/ident/README.md`: entry points and invariants of the package.
- `internal/node/keys/`: the package; its `README.md` lists the entry points.
  `codec.go` holds the kinds and bodies (the framing is `internal/node/recfile`), `key.go` key generation and
  printing, `read.go` and `load.go` the checks, `renew.go` the renewal and the
  recovery predicate, `clock.go` the clock record, `init.go` the key side of
  `init`, `export.go` the armor, `paths.go` the paths, roles, parameters and
  declarations.
- Tests (`internal/node/keys`): `TestPermissionsTooBroadAreRefusedNamingTheFile`,
  `TestUnixPermissionBitsTooBroadAreRefusedNamingTheFile` and
  `TestWindowsDACLWiderThanTheOwnerIsRefusedNamingTheFile` (permissions);
  `TestSerialNeverGoesBackAcrossRestartsAndCrashes`,
  `TestRenewalAfterAnInterruptionIssuesTheSerialAfterTheReservedOne`,
  `TestInterruptedRenewalIsRecognisedAndRepaired`, `TestRenewalDue` (the serial and
  renewal, the reason of a certificate that is not yet valid),
  `TestRenewRaisesTheMarkOfTheClockToTheTimeItSignsAt`; `TestLoadRefusals`, `TestLoadOfTheGoldenFiles`; `TestInitRerunNeverOverwritesAKey`,
  `TestInitReusesAValidSelfRevocation`,
  `TestInitRefusesASelfRevocationThatDoesNotVerifyUnderTheCurrentKey`,
  `TestInitReSignsACertificateThatDoesNotMatch` (init); `TestExportFormatGolden` (the
  export, against a vector made by an independent implementation, with the digest
  computed by hand); `TestParseRevocationExportReturnsTheVerifiedStatement` and
  `TestParseRevocationExportRefusesMalformedBackups` (the strict inverse, including
  signature damage and a valid signature by another identity);
  `TestSaveClockNeverGoesBack`,
  `TestClearingTheRollbackResetsTheMarkAndTheFlagInOneReplacement` (the clock);
  the crash-state tests `TestRenewalReplaysToAConsistentStateFromEveryCrashState`,
  `TestInitReplaysToAConsistentStateFromEveryCrashState`,
  `TestClockHighWaterIsOldOrNewAfterEveryCrashState`,
  `TestClockClearIsPausedOrClearedAfterEveryCrashState`;
  `TestRecoveryPredicatesAreEmptyOnCleanScenarios`,
  `TestRecoveryWorkOfAnUnfinishedInitIsEmpty`;
  `TestLoadedKeysHandOutTheVerifiedSelfRevocationOfThisNode`,
  `TestLoadAndRecoveryWorkRefuseADisagreeingSerialFileWithTheSameWords`,
  `TestPlanInitRefusesATimeTheDecodersRefuse`,
  `TestClearingTheRollbackRefusesATimeTheDecoderRefuses`,
  `TestAFileOverTheLargestRecordIsRefusedWithoutBeingRead`;
  `TestKeyGenerationWithAnInjectedReaderIsReproducible`,
  `TestKeysSourceNeverReachesAGlobalRandomSource`,
  `TestPrivateKeysNeverPrintThemselves`; fuzz targets `FuzzDecodeKeyFile`,
  `FuzzDecodeClock`, `FuzzDecodeSerial`, `FuzzDecodeInit` in `codec_internal_test.go`.
  `FuzzParseRevocationExport` checks canonical text,
  verified wire bytes, digest and node, and round trips from valid and malformed
  inputs.
  The Windows test is the only one that cannot run on a developer's macOS machine;
  none is skipped.
- Tests (`internal/node/daemon`) for what the daemon does with the keys:
  `TestRefusesToRunUnderRetiredIdentity`, `TestNetworkKeyCertificateRenewedBeforeExpiry`,
  `TestACertificateThatFellDueWhileTheNodeWasDownIsRenewedAtTheStep`,
  `TestACertificateThatIsDueWhileTheClockIsInDoubtIsLeftAloneAtTheStart`,
  `TestARollbackPauseIsKeptOnDiskAcrossARestartUntilTheOwnerClearsIt`,
  `TestStatusRemindsUntilRevocationBackupConfirmed`,
  `TestAFailureRenewingTheCertificateAtTheStartEndsTheStartAsAnyFailureOfTheDiskDoes`,
  `TestAFailureRenewingTheCertificateInTheLoopIsRetriedOrEndsTheDaemon`,
  `TestAClockThatJumpedForwardAndWasPutRightLeavesACertificateTheClearRenews`,
  `TestACrashAnywhereInTheRenewalLeavesKeysTheNextStartAcceptsAndRepairs`; the code is
  `internal/node/daemon` (`start.go`, `periodic.go`) and the operations are in the
  [engine](../engine/README.md) reference.
