# diskfacts

Named read-only disk facts shared by Sidera lifecycle test bindings. Read the
[testkit README](../README.md) and the
[testkit reference](../../../../../docs/references/testkit/README.md) for the
execution layers and their responsibilities.

## Entry points

| Entry | Contract |
| ----- | -------- |
| `InitPreservation.Remember`, `Check` | Remember the first bytes actually present for the identity key, network key, self-revocation, clock record and four owner configuration files. Require remembered files to stay present and byte-identical across init retries. The zero value is ready to use. |
| `ReadSigning`, `SigningSnapshot.Unchanged` | Read and compare the identity key, network key, self-revocation, certificate and serial bytes. All five files must be readable. Obtain a snapshot with `ReadSigning`; its bytes stay private. |
| `InspectTerminal` | Read a local-owner operation's raw result from the binding's actual stopped-disk `store.Scan`, and verify all kept revocations. Return an owned copy of the raw result, or nil when absent. Only a valid self-revocation of the supplied node establishes retirement. |

## Boundaries

- Readers are `fsx.Reader`; no helper obtains a writer, loads keys, repairs state
  or starts a node. The caller passes a non-nil scan of the same stopped disk to
  `InspectTerminal`.
- Init deliberately excludes the certificate and serial: interrupted init may
  reissue them. A protected file that has never appeared has no baseline; the
  next `Remember` after it appears records its first actual bytes.
- Both snapshot types hold private copies and format only as fixed type names,
  keeping key and self-revocation bytes out of diagnostics.
- Every revocation is decoded through `config.DecodeRevocation`, which verifies
  its signature and digest filename. A valid self-revocation does not stop the
  scan or hide a later read or validation error.
- Results and retirement are facts only. Responses, replays, process lifecycle,
  restart audits and domain judgements belong to the execution binding and its
  recipes. A persisted result never supplies a guessed response or replay.
- Production imports are limited to the standard library, `fsx`, `keys`,
  `config`, `store`, `admin`, `ident` and `revocation`. The package imports no
  engine, daemon, fixture or acceptance recipe.

`diskfacts_test.go` exercises the preservation and signing file sets, late file
appearance and allowed reissue, raw result ownership and absence, node-bound
retirement and invalid or unreadable revocations after a valid self-revocation.
