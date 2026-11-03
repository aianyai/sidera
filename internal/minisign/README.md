# minisign

Verification of [minisign](https://jedisct1.github.io/minisign/) signatures:
the parsers for a public key and a signature file, and the check of a message
against them. Read [../README.md](../README.md) first for the conventions every
contract package follows. It signs nothing, reads no files and has no state;
the caller supplies the bytes. The one third-party dependency approved for it is
`golang.org/x/crypto/blake2b`, because the Go standard library has no BLAKE2b
and the format is defined over it.

## Entry points

| Name | What it is for |
| ---- | -------------- |
| `ParsePublicKey(file)` | A `.pub` file, or the bare key line, to a `PublicKey` (`ID`, `Key`) |
| `ParseSignature(file)` | A `.minisig` file to a `Signature` (`KeyID`, `Sig`, `TrustedComment`, `Global`), unverified |
| `Verify(keys, message, sig)` | Checks both signatures with the key the signature names and returns the trusted comment |
| `FormatKeyID(id)` | The key id as minisign prints it, for messages |
| `ErrMalformed`, `ErrLegacyFormat`, `ErrUnknownKey`, `ErrBadSignature` | The four reasons for a refusal; callers match with `errors.Is` |

## Format

Checked against the output of `minisign 0.12`.

- Public key file: `untrusted comment: <text>`, then the key line
  `base64("Ed" || key id[8] || Ed25519 public key[32])`, 42 bytes decoded. The
  key line alone is accepted too.
- Signature file, four lines:
  1. `untrusted comment: <text>`;
  2. `base64(alg[2] || key id[8] || signature[64])`, 74 bytes decoded;
  3. `trusted comment: <text>`;
  4. `base64(global signature[64])`.
- `alg` `ED` is the prehashed format: the signature is Ed25519 over the
  BLAKE2b-512 hash of the message. The global signature is Ed25519 over the 64
  signature bytes followed by the text of the trusted comment, without its
  prefix or line end. Both must verify; the second is what makes the trusted
  comment trustworthy.
- The specification's page spells the third line's prefix `trusted_comment:`;
  the tool writes `trusted comment: ` with a space and that is the only form
  accepted.
- The key id prints as its eight bytes in reverse order, upper-case hex
  (`internal/release/minisign.pub` is `BA9335CE3A9A8F7B`).

## Rules

- **Only the prehashed format.** The legacy `Ed` signature signs the message
  itself, not its hash; the specification says new implementations must use the
  hashed format and must not accept the legacy one by default, and the release
  pipeline never makes one. `ParseSignature` returns `ErrLegacyFormat` for it
  and `Verify` never sees one.
- **Strict reading, nothing guessed.** The prefixes must match exactly, base64
  is standard and canonical (padded, no stray characters), every length must be
  exact, and a signature file has exactly four lines. A carriage return anywhere
  refuses the file, so no two readers can disagree about where a line ends.
  The last line may omit its newline; nothing may follow it, not even a blank
  line. A public key file has one or two lines.
- **The comments are not trusted by being parsed.** The untrusted comment is
  checked for its prefix and otherwise ignored. The trusted comment is returned
  only by a `Verify` that succeeded, and an error returns the empty string.
- **The key is picked by id, then proven.** `Verify` takes a list so a key
  rotation is two entries. It uses the first key whose id matches the one in
  the signature; no match is `ErrUnknownKey` even if another key would verify,
  because the id is part of what the signer stated. A key that is not 32 bytes
  is `ErrMalformed` rather than a panic in `crypto/ed25519`.
- **Errors say which.** `ErrMalformed` is about the bytes handed in,
  `ErrLegacyFormat` about the algorithm, `ErrUnknownKey` about the id,
  `ErrBadSignature` about a signature that does not verify (the wrapped text
  says which of the two).

## Tests

`testdata/` holds public keys, a message and signatures made by the real tool
with throwaway keys whose secrets were not kept; `testdata/README.md` has the
commands. `minisign_test.go` tampers with them one field at a time (message,
signature, trusted comment, global signature, key id, algorithm, prefixes,
base64, lengths, line counts, line ends) and with the keys the same way, and
names the error each must give.
