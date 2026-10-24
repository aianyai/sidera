# Identity

Identifiers for nodes, people and endpoints, the public-key type they are
derived from, and their text form. The signed records that carry keys and
certificates are described in [records](../records/README.md).

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
