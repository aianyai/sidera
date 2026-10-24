# keys

The `keys/` directory of the data directory: the node's identity and network
private keys, the network-key certificate and its serial, the self-revocation
statement, the wall-clock high water mark with the rollback pause flag, and the
marker that says `init` finished. Read
[../../../docs/references/identity/README.md](../../../docs/references/identity/README.md)
for the formats, the export format and the reasons.

This is a runtime package (layer 3 of [`internal/node/README.md`](../README.md)):
it imports `txn`, `recfile` (the framing of the record files), the contract
packages, and from `fsx` and `clock` only types and the pure `clock.CheckWall`. It reads no clock and no random source of its own:
the time and the reader of randomness are parameters. Every write goes through a
transaction of `txn`; nothing here calls an `fsx` write.

## Entry points

| Name | Purpose |
| ---- | ------- |
| `Load(w)` | Reads and checks every file, the init marker included; returns `*Keys`. Refuses an init that did not finish. Writes nothing, repairs nothing. |
| `Keys.Node`, `NetworkSigner`, `Certificate`, `CertificateWire`, `SerialSeen` | What the node knows of itself. The identity key never leaves the package: it signs certificates and the self-revocation only. |
| `Keys.RenewalDue`, `RenewalDue`, `Keys.Renew` | The renewal rule (a pure function with a typed verdict and reason) and the renewal itself, which is also the repair of an interrupted one. |
| `Keys.RenewalInterrupted`, `RenewalInterrupted`, `RecoveryWork` | The recovery predicate: the serial file is ahead of the certificate. Pure functions of the disk, for the load, the startup repair and the clean-scenario test. |
| `ReadClock`, `Keys.SaveClock`, `PutClockCleared` | The clock record. `SaveClock` is the periodic write and never goes back; `PutClockCleared` is the write of the owner's clear, for the engine's transaction. |
| `Keys.SelfRevocation`, `ReadSelfRevocation`, `FormatRevocationExport` | The statement `Load` verified as this node's, for a running node; the reader of the file for a caller with no loaded keys; the one formatter of the export. |
| `Keys.CheckNotRetired` | The refusal to run a node whose identity is among the loaded revocations. |
| `PlanInit`, `InitPlan.WriteKeys`, `WriteMarker`, `Initialized` | The key side of `init`, and the marker. |
| `CheckPermissions` | The permission sweep of `keys/`, for a caller that checks before it loads. |
| `Transactions`, `InitWrites`, `InitMarkerWrite`, the `Role…` and `Tx…` constants | Declarations: the transactions this package opens (`cert-renew`, `clock-high-water`), and the writes it contributes to the `init` and `clock-clear` transactions that their composers declare. |
| `CertificateLifetime`, `RenewBefore` | The decided parameters: 30 days and 10 days. |
| `FileError`, `ErrPermissionsTooBroad`, `ErrMissing`, `ErrCorrupt`, `ErrInconsistent`, … | Every failure about a file is a `*FileError` that names it. |

## Rules

- **Files.** `identity.key`, `network.key`, `network-serial`, `clock.rec` and
  `init.rec` are singleton records of `recfile`: one fixed file name each, the key
  of the first line is the kind. Their bodies are canonical JSON, and `network.cert`
  and `self-revocation.rec` are the record wire bytes. A file is read only if it is
  exactly what the writer writes: a body that decodes but is not in the canonical
  form is damaged. Any damage, of the first line or of the body, is `ErrCorrupt`
  naming the file; nothing here replaces a damaged file. A file larger than the
  largest record file is damaged too, and is refused by its size before it is
  read.
- **Permissions are refused, never fixed.** A directory or file of `keys/` that
  anyone but the owner can reach makes `Load`, `PlanInit` and the standalone
  readers fail with `ErrPermissionsTooBroad` naming it. On Unix any group or
  other bit counts; on Windows the DACL check is `fsx.Info.OwnerOnly`. Nothing
  here narrows a permission: only the owner knows who has read the file.
- **Errors never quote a file.** The decoders say which check failed, not what
  they found, because a key file is secret. Keys print as a fixed text that names only
  their node, by pointer or as a copy, and anything that holds one prints as an
  address.
- **Only the marker says init finished.** Every write of `init` is optional for
  the transaction layer, which cannot require a file that was reused, so a disk on
  which `init` stopped can hold every key, the certificate and the
  self-revocation and still lack the clock record and the owner's configuration.
  `Load` refuses it, naming `init.rec`, and `RecoveryWork` finds no work in it: the
  start stops before any repair and the owner runs `init` again.
- **Load checks that the files belong together.** The certificate is for the node
  of the identity key and names the network key; the serial file is this node's
  and agrees with the certificate (`record.DecideSerial`); the self-revocation is
  the statement of this identity key. A serial file behind the certificate, or at
  its serial with another digest, is refused: it would let a serial be issued
  twice. One function (`checkSerial`) states this rule for `Load` and for
  `RecoveryWork`, and its error names the reason.
- **The serial never goes back.** A renewal reserves the next serial in
  `network-serial` first, and that write is the commit point of the transaction:
  from then on the serial is never issued again, whatever happens to the
  certificate. Then it writes the certificate. A crash between the two leaves a
  gap, which peers accept, and a certificate whose serial is below the serial
  file's: an *interrupted renewal*. `Load` does not refuse it. It reports it,
  `RenewalDue` says due at once whatever the remaining validity, and `Renew`
  repairs it at the serial after the reserved one. Memory follows the disk: the
  serial is remembered when it is durable, the certificate when the transaction
  has committed.
- **No signing while the clock is in doubt.** `Renew` refuses during a rollback
  pause and when the clock is behind the persisted mark by more than the clock
  tolerance, and writes nothing.
- **The clock record never goes back.** `SaveClock` refuses a lower mark, a lifted
  or moved pause flag, and a state without a mark. Only `PutClockCleared` resets
  the mark and lifts the pause, in one replacement.
- **No file is written that its own decoder refuses.** A time in `keys/` is UTC in
  whole seconds from 1970-01-01T00:00:01Z to the end of year 9999, and every write
  (`SaveClock` for both of its times, `PutClockCleared`, `PlanInit` for `now`)
  applies the rule the decoders apply before it writes anything. A clock that was
  reset to zero by a dead battery is the case it is for: a pause that started at
  that time would otherwise be written and then read as damage, and the owner's
  only way out would be to delete the file and lose the pause and the mark.
- **`init` looks before it writes and never overwrites.** A key file that exists
  and parses is reused; one that does not parse is refused, naming it. The
  self-revocation is reused only if it verifies under the identity key that is
  current now, otherwise refused. The clock record is reused whatever it says. The
  certificate is the one file that may be replaced, because it can be issued
  again: at a serial above every serial known, the serial file written first.
  `init` that finished is refused. `PlanInit` writes nothing; the plan's writes
  are made by the composer's transaction.
- **Randomness is the reader that is passed.** A key is 32 bytes read from it
  with `io.ReadFull` and built with `ed25519.NewKeyFromSeed`. `ed25519.GenerateKey`
  and the generators of `crypto/ecdh` are not used: the first reads the system
  source when given nil, and since Go 1.26 the second ignores the reader. A
  reader that fails or runs dry fails `init` with `ErrRandom`; there is no
  fallback.
- **The self-revocation is handed out verified, and never written here.**
  `Keys.SelfRevocation` returns the statement `Load` verified as the statement of
  this node's identity key, as bytes, digest and node: a running node retires
  itself and exports with it, so no caller compares a node of its own. `ReadSelfRevocation`
  reads and verifies the file for a caller that has not loaded the keys (an export
  while the node is stopped). Its fields are not exported, so a statement whose
  bytes and digest disagree cannot be built outside the package. Nothing here
  writes into `keys/` for a retirement; `CheckNotRetired` refuses to run a node whose
  identity is among the loaded revocations.

## What the caller must guarantee

- Pass `*txn.Writer` after `Recover`, and build the table from `Transactions()`
  together with the `init` and `clock-clear` declarations whose writes are
  `InitWrites()`, `InitMarkerWrite()` and the `RoleClock` write.
- Serialise the transactions of one declaration (the single writer loop does).
- Treat `txn.IsFatal` on any error of `Renew` or `SaveClock` as the end of the
  process.
- Read the clock once and pass it: `now` is a parameter of `Renew`, `PlanInit`
  and `PutClockCleared`. The clock check before any signing (startup step 4) is
  the daemon's; `Renew` repeats only the refusal that protects the signature.
- Make the `init` transaction `Begin`, `PlanInit`'s plan `WriteKeys`, the other
  owners' writes, `WriteMarker`, `Commit`, in that order, and create the `keys`
  directory (`Dir`) first when it does not exist.
- Call `Initialized` first at startup, to tell an `init` that never ran from one that
  stopped before the marker, and to say so before the configuration is read.
- Retire and export a running node with `Keys.SelfRevocation`, never with a
  statement read again.
- After `Load`, call `CheckNotRetired` with the loaded revocations, and renew at
  startup when `RenewalDue` says so.
- Zero means unset: the zero `ClockState` is the first start, a zero
  `RenewVerdict` or `RenewReason` is never returned, a zero certificate is due for
  renewal.

## Platform notes

- Windows (pending verification by CI on main): the permission check is
  `fsx.Info.OwnerOnly`, a DACL with only the owner, SYSTEM and Administrators
  granted. `permissions_windows_test.go` widens the DACL of each file and expects
  the refusal; it runs only in Windows CI. Reading `self-revocation.rec` for the
  export relies on `fsx.ReadFile`, which reads and closes at once. The order of
  two renames at a power loss is not documented for Windows: if the certificate
  survived and the serial file did not, `Load` refuses naming `network-serial`,
  which is the fail-closed answer; the error says how to mend it (delete
  `keys/init.rec` and run `init` again, which signs a new certificate above both
  serials). The file system layer does not flush a directory on Windows, so the two
  renames can reach the disk in either order.

## Where the tests are

The named tests of the row: `TestPermissionsTooBroadAreRefusedNamingTheFile`
(and the Unix and Windows files), `TestSerialNeverGoesBackAcrossRestartsAndCrashes`,
`TestInterruptedRenewalIsRecognisedAndRepaired`, `TestInitRerunNeverOverwritesAKey`,
`TestInitReusesAValidSelfRevocation`,
`TestInitRefusesASelfRevocationThatDoesNotVerifyUnderTheCurrentKey`,
`TestExportFormatGolden`, the crash-state tests
(`TestRenewalReplaysToAConsistentStateFromEveryCrashState`,
`TestInitReplaysToAConsistentStateFromEveryCrashState`,
`TestClockHighWaterIsOldOrNewAfterEveryCrashState`,
`TestClockClearIsPausedOrClearedAfterEveryCrashState`),
`TestRecoveryPredicatesAreEmptyOnCleanScenarios`,
`TestRecoveryWorkOfAnUnfinishedInitIsEmpty`, the tests of the rules named above
(`TestLoadedKeysHandOutTheVerifiedSelfRevocationOfThisNode`,
`TestLoadAndRecoveryWorkRefuseADisagreeingSerialFileWithTheSameWords`,
`TestPlanInitRefusesATimeTheDecodersRefuse`,
`TestClearingTheRollbackRefusesATimeTheDecoderRefuses`,
`TestAFileOverTheLargestRecordIsRefusedWithoutBeingRead`), and the randomness tests
(`TestKeyGenerationWithAnInjectedReaderIsReproducible`,
`TestKeysSourceNeverReachesAGlobalRandomSource`). Fuzz targets for the decoders
are in `codec_internal_test.go`.
