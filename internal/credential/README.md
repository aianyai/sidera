# credential

The signed records that carry trust between nodes: the mutual-trust credential,
the revocation of one, the online voucher and the offline administrative
directive. Each is a record type of [`record`](../record/README.md); this
package supplies the payloads, the descriptors and the facts that
[`trust`](../trust/README.md) and [`admin`](../admin/README.md) read, and it
decides nothing that either of them decides. Read [../README.md](../README.md)
first for the conventions every contract package follows, and
[../../docs/references/records/README.md](../../docs/references/records/README.md)
(section "Trust records") for the formats and the reasons behind them.

## Entry points

| Name                                                          | Purpose                                                                                                         |
| ------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------- |
| `Credential`, `New`, `Descriptor`, `Type`                     | The credential payload and its record type. `New(x, y, createdAt, notAfter, nonce)` puts the two nodes in the fixed order. Signed by both ends' identity keys. |
| `Nonce`, `NewNonce(r)`, `NonceLen`                            | The 16-byte nonce, supplied by the caller or read from an injected `io.Reader`.                                  |
| `Credential.Payload()`, `Credential.Digest()`                 | The exact payload bytes and the record digest, known to both sides before anyone signs.                         |
| `Facts(v) trust.Credential`                                   | What `trust` reads of a verified credential.                                                                     |
| `Revocation`, `NewRevocation`, `RevocationDescriptor`         | A credential revocation: it embeds the revoked credential's payload; either end signs. `Revocation.Digest` is the entry for `trust.View.RevokedCredentials`. |
| `Voucher`, `VoucherDescriptor(accepted, now)`                 | An online voucher, signed by the issuer's network key (`record.NetworkKeyOf`).                                   |
| `CheckVoucher(v, cred) (trust.Voucher, VoucherReason)`        | The one rule `trust` leaves to its caller: the issuer is an end of the credential the voucher names. Returns what `trust.Vouchers.Accept` takes. |
| `OfflineDirective`, `NewOfflineDirective`, `DirectiveDescriptor` | An offline directive that carries the administrator's device certificate and is signed by that device key.      |
| `OfflineDirective.AdminDirective(CertRevocation)`, `.TrustDirective()` | The `admin.Directive` and the `trust.EdgeRevocationDirective` of a verified directive.                  |

## Rules

- **Fixed order of the two ends.** `Credential.A` is the node ID that is smaller
  as 32 bytes compared from the first byte, `B` the larger. The decoder requires
  strictly ascending order, so equal ends and the wrong order are refused and a
  credential has exactly one encoding. Two nodes that know the same facts
  therefore compute the same payload whoever started the pairing.
- **Pairing sequence.** Both sides build the credential with `New` (the order of
  its two node arguments does not matter), the initiator signs with `record.Seal`
  and sends the partial record, the target checks it with `record.VerifyPartial`
  and compares the payload with the one it computed itself, then completes it
  with `record.CoSign`. The record is complete only when both ends' identity keys
  have signed; a third key, a network key or a second signature from an end is
  refused. The digest is the same at every stage.
- **No time is judged here** except the shape of a payload: `created_at` is
  required, `not_after` is optional (left out, never written as 0) and must be
  after `created_at`. An expired credential is a valid record and reaches `trust`
  with its `NotAfter`. A voucher's `issued_at` and a directive's `signed_at` are
  passed on as facts; the device certificate's window is not judged either.
- **A revocation embeds the credential payload.** The verifier decodes the
  payload strictly, recomputes the digest from it (`Revocation.Digest`, never
  taken from the sender) and accepts exactly one signature, by the identity key
  of one of the two ends the payload shows. A record that names only a digest
  cannot exist in this format. The revocation type fixes the credential type it
  embeds.
- **A voucher is signed by the issuer's network key**, bound through
  `record.NetworkKeyOf` with the specific reason on refusal. `CheckVoucher`
  additionally requires the issuer to be an end of the credential and the
  credential to be the one the voucher names. A zero `Verified` or a zero
  credential is `VoucherInvalid`.
- **A directive is signed by the attached certificate's device key.** The
  certificate is itself checked when the directive is decoded: a well-formed
  device-certificate record signed by the root key of the person it names. Its
  kind is fixed by the record type (revoke one trust edge). The claimed ends are
  ascending and different. Whether they match the credential is `trust`'s
  decision (`trust.ApplyDirective`), not made here.

## What the caller must guarantee

- `CheckVoucher` is given the credential the voucher names, found by the digest,
  already verified. The caller keeps the voucher only if the answer is
  `VoucherOK`, and then passes it to `trust.Vouchers.Accept` at the time of
  receipt.
- `AdminDirective` leaves the two revocation flags to the caller: they come from
  the revocation records (`CertRevocation`), which this package never reads. The
  zero value says nothing is revoked, so a caller with a revocation source fills
  both fields before the directive reaches `admin.AcceptDirective`.
- A verifier of a voucher builds `VoucherDescriptor` from the network-key
  certificate it accepted for the issuer and its revocation flag
  (`record.AcceptedNetworkKey`).
- Times in the payloads have the precision of the wire: whole seconds.

## Dependencies

The standard library, `ident`, `record`, `trust` and `admin`. No libp2p.
