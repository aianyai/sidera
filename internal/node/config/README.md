# config

The configuration the node's owner edits: node settings, the access policy,
presets, adapter instances with their endpoint settings, the stable endpoint
ids, the revocation records the node keeps, and the installed adapter packages.
Read
[../../../docs/references/configuration/README.md](../../../docs/references/configuration/README.md)
for the file formats, the reasons and what the tests prove.

This is a runtime package, layer 3 of [`internal/node/README.md`](../README.md):
it imports `txn` and the contract packages, and of `fsx` only types, so it
cannot obtain a writable file system. Every write is a step of a transaction
declared here or in the package that composes it. It reads no clock.

## Entry points

| Name | Purpose |
| ---- | ------- |
| `Load(w)` | Reads and checks all of `config/`, then repairs the adapter directory. Returns a `*Config`, or a `*FileError` that names the file that is wrong. |
| `Config` accessors: `Node`, `Policy`, `Presets`, `Instances`, `EndpointIDs`, `EndpointSettings`, `Backup`, `Revocations`, `RevocationSet`, `Adapters`, `LastOperations` | Copies of the loaded state, manifests of `Adapters` included: nothing a caller changes reaches the configuration or another caller. |
| `NodeSettings.ArchiveRetention()` | The retention of the archive as a duration: the one conversion of `archive_retention_days`. |
| `NodeSettings.WireJSON()`, `EndpointOverride.WireJSON()`, `Preset.WireJSON()` | The settings and presets as the owner's commands show them: the compact object the file holds (under `"settings"`, or the preset's own document), with the names `node set`, `endpoint set` and `preset add` take, so the names are written once. |
| `SetNodeSettings`, `AddPreset`, `RemovePreset`, `EnableAdapter`, `DisableAdapter`, `SetEndpointSettings` | The versioned changes. A `Request` names the operation by `admin.RequestKey`, which only `admin.KeyFor` should make, so the person and the id cannot be put together wrongly. Each checks the version the request names, validates, records the operation and its result in the file it changes, in the same replacement, and returns the new state. Refusals are `*ChangeError` with a typed `ChangeReason`. |
| `RuntimeID`, `DirectID`, `PrepareBackup`, `PrepareRevocation`, `PlanInstall`, `PutEndpointIDs`, `PutBackup`, `PutRevocation`, `Install.Point` | Write functions for transactions of other packages. The prepare steps (`RuntimeID` and `DirectID` encode the ids they mint, `PrepareBackup`, `PrepareRevocation` and `PlanInstall` check and encode) run before `Begin`; the put steps take a `*txn.Tx` and the prepared value, write under an exported role, can fail only as a write of the transaction does, and return an `AfterCommit` that the caller calls once `Commit` returned nil. |
| `StageAdapter`, `Unstage`, `PlanInstall`, `Install` | The installation of an adapter package: copy it next to the versions, drop it, plan the writes that make it current. |
| `AdapterGCWork(r)` | The garbage of the adapter directory as a pure function of the disk: what the repair removes, and what a clean run must leave empty. |
| `PlanInit(r)`, `InitPlan` | The configuration side of `init`: what is missing, refusing an invalid file that exists. |
| `Decode…`/`Encode` of every file, `DecodePreset`, `DecodeNodePatch`, `DecodeEndpointPatch` | The strict decoders and the canonical encoders; the management layer decodes what it receives with these and with nothing else. |
| `InitWrites()`, `EndpointIDsWrite()`, `BackupWrite()`, `RevocationWrite()`, `InstallWrites()`, `InstallOldWrite()` | The writes this package contributes to the transactions its composers declare, in the order they are made; a composer builds its `Decl` from them, as from `keys.InitWrites()`, and adds its own writes around them. |
| `Transactions()`, the `Role…` constants, `LayoutDirs()`, the path helpers | The static declarations, the roles other packages compose, and where things live. |
| `Default…` constants, `DefaultNodeSettings()` | The values `init` writes. |

## Rules

- **Strict decoding, one rule for every file.** Decoding goes through
  `strictjson`: a key matches exactly, a field without `omitempty` is required,
  `null` is no value, an explicit zero in an optional field is an error, unknown
  and repeated keys are errors. A file that fails refuses startup and the error
  names the file and the JSON path of the value. Nothing is written when a file
  is refused, the repair of the adapter directory included.
- **Every file has `format` (1); versioned files also have `version` (from 1).**
  `node.json`, `policy.json`, `presets.json` and `instances.json` have one
  version each, for the whole collection: a change of any member raises it by
  one. The files for which a request names no version (endpoint ids, the backup
  confirmation, the current pointer) carry only `format`.
- **A versioned change checks the version itself.** The version a request names
  is compared with the current one by `admin.CheckVersion`, inside the change,
  before the content is looked at: a stale and invalid request is a conflict.
  The management layer does not check it separately, because the check and the
  write have to be one step. Whether the request was already answered is not
  decided here: the management layer runs `admin.CheckReplay` first, and calls a
  change only when it says to execute. The lookup runs against all stored
  results: each versioned file's `last_operation` (what `LastOperations()`
  returns) and the results kept under `state/admin/`, whose key spaces do not
  overlap, by `admin.KeyFor(actor, id)`, in the same loop command as the change.
- **No file is written that its own decoder refuses.** A versioned encoder
  refuses version 0 and an operation with a zero id, by the decoder's own
  check. A change whose request has a zero operation id, or that would raise
  the largest version past it, is refused with `ChangeInvalid` before anything
  is written: a file the next start refuses to read would leave the node unable
  to start.
- **The operation is recorded in the file it changed.** `last_operation` holds
  the operation id, the person, the fingerprint and the result, written in the
  same replacement as the new version. The management layer builds the result
  through a `ResultFunc`, which sees the state the change produces; the shape of
  a result is its own.
- **A preset must state whether local sources may start a session with it.**
  `local_sources` is `"allow"` or `"deny"`, required, with no default. It decodes
  to `authz.PresetLocalAccess`, whose zero value is not a statement; no decoder
  returns a preset with the zero value.
- **Settings are judged by their owners.** The queue time and backlog limit of
  an endpoint are checked with `delivery`, the node's concurrent prompt turns
  with `handoff`; the error a setting gets is the owner's, wrapped. The numbers
  of the defaults come from the same packages. Only what no other package
  decides is a number here (below).
- **A patch is merged, then validated whole, and applies whole.** A setting is
  judged against the others it depends on, and one bad value changes nothing.
- **A name that begins with a dot is never configuration.** Reading
  `endpoint-ids/`, `revocations/` and `adapters/` ignores every such name:
  the leftovers of interrupted operations (`.tmp-`, `.trash-`, which the cleanup
  at start removes) and the files an operating system or an editor puts into a
  directory (`.DS_Store`). Any other name in these three directories that is
  not what belongs there refuses startup and is named. Revocations and stable
  ids fail closed: a record that was not read would revive a retired identity,
  and an id file that was not read would give an endpoint a new address. The
  root of `config/` and the package directories under `adapters/` are not
  checked for extra files.
- **The disk reserve covers the queue and the logs.** `disk_reserve_bytes` must
  be at least `log_total_bytes` plus `OpenDeliveryDiskGrowth` (4 KiB) times
  `node_queue_limit`, computed without overflow: below that, new work would be
  refused only after the disk was already full.
- **Memory follows the disk, after the commit.** A change updates the
  in-memory configuration only after its transaction returned nil. A write
  function for another package's transaction returns an `AfterCommit` instead;
  an abandoned or failed transaction leaves memory as it was.
- **The settings of closed session endpoints go with the owner's next write of
  `instances.json`.** An endpoint whose id is not a stable id of the instance and
  that is not open now is a closed session endpoint; its id is random and never
  used again. Stable ids (runtime and direct) are kept. A write that is given no
  knowledge of the open endpoints (`nil`) drops nothing.
- **A setting that is not given takes the node default.** An endpoint's own
  settings name only what the owner set; `EndpointSettings` returns what
  applies.
- **Stable endpoint ids are minted once.** `DirectID` returns the same id for the
  same key for ever; the id is written before the endpoint record exists, and an
  id without a record is not an endpoint.
- **Adapter packages are installed by a pointer.** `adapters/<id>/versions/<v>/`
  is immutable; `adapters/<id>/current` names the installed version, and
  replacing it is the commit point of an installation. A package is copied to
  `versions/.stage-<hex>` next to the versions (a rename needs one parent), the
  copy is what is parsed, and the manifest read from the source and from the
  copy must be of the same adapter and version. A name in a package that begins
  with `.tmp-`, `.trash-` or `.stage-` is refused: the cleanup would remove it.
- **No pointer means not installed, and never a reason to refuse startup.** An
  adapter directory without `current` is an installation that did not finish;
  the repair removes it. So does it remove staging directories and version
  directories the pointer does not name. `AdapterGCWork` is that list and is the
  only definition of it.
- **`init` never overwrites.** For each of the four files it writes, a missing
  one is written with the defaults, a valid one stays byte for byte, an invalid
  one makes the plan fail naming the file.
- **A directory missing from `config/` refuses startup**, naming it: it is the
  owner removing what `init` made.

## Decided numbers

| Setting | Value | Reason |
| ------- | ----- | ------ |
| `node_queue_limit` | 10,000 unfinished deliveries | Above what ordinary traffic queues in the default queue time, small enough that the disk reserve below covers it. |
| `disk_reserve_bytes` | 256 MiB | Covers the log total and 4 KiB for each of 10,000 deliveries (about 190 MiB) with room. |
| `log_total_bytes` | 150 MiB | Shared by the daemon and every adapter instance; the size at which each log rotates follows from it and from the number of instances. |
| `archive_retention_days` | 30, at most 106,751 | How long summaries stay; the archive is not counted in the reserve. The upper bound is the number of days a `time.Duration` holds: above it the conversion would wrap around to a negative or short retention and the archive would discard its summaries at once. Consumers read the retention only through `ArchiveRetention()`. |
| `OpenDeliveryDiskGrowth` | 4 KiB | An unfinished delivery adds its archive summary and the feedback notification it may cause before it is archived. |

The prompt turns and the endpoint defaults are not here: they are
`handoff.DefaultMaxConcurrentTurns`, `delivery.DefaultEndpointBacklogLimit` and
`delivery.DefaultEndpointMaxQueue`.

## What the caller must guarantee

- Build the transaction table once, from `txn.Internal()`, `Transactions()` and
  the composed declarations, and call `Writer.Recover` before `Load`.
- Serialise the writes of one declaration (the single writer loop does).
  `Config` also serialises its versioned changes and `PlanInstall`, but a second
  writer would only wait.
- Call a versioned change only after `admin.CheckReplay` has said to execute,
  run against all stored results (the operations `LastOperations()` returns and
  the results under `state/admin/`, by `admin.KeyFor(actor, id)`), in the same
  loop command as the change: the version check is inside `config`, the replay
  check is not, and a retry that came between the lookup and the change would
  run twice.
- A transaction that spans owners is declared by the composing package, with the
  roles exported here, and writes config's files only through the functions here:
  `direct-register` and `runtime-admit` (`RoleEndpointIDs`), `backup-confirm`
  (`RoleBackup`), `identity-retire` (`RoleRevocation`), `adapter-install`
  (`RoleStale`, `RoleVersion`, `RoleCurrent`, `RoleOld` in this order, the
  current pointer being the commit point), `init` (`RoleDir`, `RoleNode`,
  `RolePolicy`, `RolePresets`, `RoleInstances`).
- Do every check and encoding a cross-owner transaction needs before `Begin`:
  `RuntimeID` or `DirectID`, `PrepareBackup`, `PrepareRevocation` and
  `PlanInstall` return the error of a value that cannot be written, while the
  put functions inside the transaction fail only as its writes do. An error found
  after the commit point would be fatal, and `Tx.Close` panics for a transaction
  closed there with no result.
- Call the `AfterCommit` of a write function only after `Commit` returned nil.
- Pass `OpenEndpoints` to every write of `instances.json`, with the set of
  endpoints that are open now.
- For `StageAdapter`, pass a `fsx.Reader` rooted at the package directory, a
  reader of at least `StageSuffixBytes` bytes of randomness for the name of the
  staging directory, and check the platform in `Staged.Manifest`
  (`adapterreg.RunsOn`). A directory or file of the package that cannot be read
  is a `*SourceError` naming its path inside the package, and is not a failure of
  the node's own storage. The manifest and each copied file are read through
  `ReadFileLimit`, using the package's remaining `MaxPackageBytes` budget;
  overflow is refused as invalid without reading an oversized file whole.
  Reads follow symbolic links only when their targets are regular files:
  target bytes count against the budget, while directories, FIFOs and devices
  are unreadable sources. The manifest of the copy must be the bytes that were
  judged. `StageAdapter` and `Unstage` are
  called only by the single install worker, one at a time, and may run
  concurrently with the writer loop: they take no lock of `Config` and touch
  only staging and version directories that no change reads.
- Run `PlanInstall` and the transaction it plans in the same loop command, so
  that nothing changes between the plan and its writes. Pass it the `StillEnding`
  that asks the adapter host whether a process of an instance is still ending
  (nil when nothing runs adapters), so that an adapter is not upgraded under it. Call `Unstage` when the
  result is `AlreadyCurrent`, when the installation is refused and when it
  failed with an error that is not fatal; after a fatal error the next start
  removes what is left.
- Read the archive retention only through `ArchiveRetention()`.
- A `Fatal` error from a write means exit and rescan: memory was not changed,
  and the next start reads what is on disk.
- Do not edit `endpoint-ids/` by hand; edit the other files only while the node
  is stopped.

## Where the tests are

The named tests are in `load_test.go` (an invalid file refuses startup, one row
per kind of file and failure), `preset_test.go` (the statement about local
sources), `version_test.go` (expected versions), `writable_test.go` (nothing is written that its decoder refuses), `settings_test.go` (settings
delegate to their owners), `crash_test.go` (every transaction from every crash
state, a crash during the repair, `init` crashing), `clean_test.go` (a clean run
leaves no recovery work), `init_test.go` and `golden_test.go`. `fuzz_test.go` has
one fuzz target for each decoder.
