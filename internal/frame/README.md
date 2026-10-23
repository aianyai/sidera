# frame

The one rule for building bytes that are hashed or signed. Read
[../README.md](../README.md) first for the conventions every contract package
follows, and
[../../docs/references/records/README.md](../../docs/references/records/README.md)
for the reasoning and the list of domains in use.

## Entry points

| Name                 | Purpose                                                                                           |
| -------------------- | ------------------------------------------------------------------------------------------------- |
| `Frame(domain, fields...)` | `uvarint(len(domain)) \|\| domain \|\| (uvarint(len(field)) \|\| field)*`: the input of a hash or a signature. |
| `Domain(d)`          | Returns `d` after the domain rules are checked; panics otherwise. Declares a domain.               |
| `CheckDomain(d)`     | The same check as an error, for callers that have an untrusted string.                             |

## Rules

- **Every part is length-prefixed.** The domain and each field carry their own
  `uvarint` length. Two different inputs therefore never produce the same bytes,
  and the framed bytes of one domain are never a prefix of another's. Nothing
  else may join inputs for a hash or a signature: identifier derivation,
  record signatures and digests, and any later construction call `Frame`.
- **A domain starts with `sidera/` and is at most 63 bytes.** The length is the
  first byte of the framed input, so it is a value below 64 and cannot equal
  the first byte, always a letter, of the text-prefixed payloads libp2p signs
  with the same key.
- **Domains are declared with `frame.Domain("...")`** as package-level
  variables, with a string literal. A package picks the domain for each purpose
  it hashes or signs and puts a version at the end (`.../1`); a changed
  construction is a new domain.
- **All domains are distinct.** There is no registry. The tests of this package
  parse every Go file of the module, find each call of `frame.Domain`, and fail
  on a malformed, repeated or non-literal domain. A package that declares a
  domain does not import `frame` under another name, or the scan would not see
  it.

## What the caller must guarantee

- `Frame` does not check its domain. Pass a value returned by `Domain`.
- `Frame` makes the framed input unambiguous; it does not say what the fields
  mean. A construction decides its own field order and keeps it fixed.
