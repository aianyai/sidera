# Identity

Identifiers for nodes, people and endpoints. The topic will also cover identity
keys, certificates and signed records as they land. This document describes the
identifiers only, as committed.

## What

| Identifier   | Length   | Meaning                                                  |
| ------------ | -------- | -------------------------------------------------------- |
| `NodeID`     | 32 bytes | A node. Becomes a digest of its long-term public key.    |
| `PersonID`   | 32 bytes | A person. Becomes a digest of their root public key.     |
| `EndpointID` | 16 bytes | An endpoint on a node. Random, issued by that node.      |
| `Address`    | -        | `{Node, Endpoint}`: one endpoint on one node.            |

- Lengths are fixed. Constructors reject input of any other length.
- The all-zero value means "unset" and is never a valid identifier. Constructors
  and parsers reject it, so a non-zero value obtained from the package is valid.
  An `Address` is zero if either part is.
- An `EndpointID` is random and never changes once its node has issued it.
  `NewEndpointID` reads it from a caller-supplied `io.Reader`.
- The text form is canonical lowercase hex of the exact length; uppercase,
  prefixes and separators are rejected. An `Address` is written
  `<node>/<endpoint>`.
- The text form is provisional and will be fixed together with the record
  encoding.
- Until the record formats that carry the keys exist, a `NodeID` or `PersonID`
  is an opaque 32-byte value; the digest derivations are not part of the code
  yet.

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

## Where

- `internal/ident/ident.go`: the types, constructors, parsers and
  `NewEndpointID`.
- `internal/ident/ident_test.go`: table-driven tests of the rules above.
- `internal/ident/README.md`: entry points and invariants of the package.
