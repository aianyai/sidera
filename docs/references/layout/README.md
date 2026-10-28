# Code layout

Where code lives and which way it may depend on other code. The rules here
keep the shared rules of the protocol pure and testable, and keep every source
of nondeterminism in the daemon behind a seam that tests replace.

## What

Three locations of Go code matter for the daemon. The rest of the repository
is bound by none of these rules: `protocol/` holds the schema conformance tests,
`cmd/sidera-adapter-claude/` is an adapter that imports only
`sdk/go/adapterproto`, `cmd/sidera-fake-adapter/` and `cmd/sidera-fake-runtime/`
are the two test programs that stand in for an adapter and for a native runtime
(built from `internal/node/testkit`, see [testkit](../testkit/README.md), never
installed), and `sdk/go` is a separate Go module that the daemon and third-party
adapters share.

| Location | Kind | Does I/O, goroutines, clock reads |
| -------- | ---- | --------------------------------- |
| `internal/<package>` (every directory except `node/`) | Contract package: rules and formats that every component must apply identically | No. Time is a `now` parameter, randomness an injected `io.Reader`, there is no global mutable state and no storage |
| `internal/node/<package>` | Runtime package: the daemon's file layer, clock, transactions, storage, transport, engine, interfaces and composition root | Yes, under the rules below |
| `cmd/sidera/internal/<package>` | Code that belongs only to the command line | Yes |

Import directions:

- A contract package imports only the standard library, other contract
  packages, approved third-party dependencies and the `sdk/go` module. It never
  imports `internal/node/` or `cmd/`, directly or through another package, on
  any supported operating system, with or without the build tags used for
  crash and clock tests.
- Runtime packages are meant to form a layered graph: file layer and clock at
  the bottom, then transactions, then storage, keys and configuration, then
  transport, adapter host and local IPC, then the engine, then the two local
  interfaces, then the daemon that wires them. A package imports only lower
  layers. This layering is a convention recorded, with its table and the
  exceptions, in [`internal/node/README.md`](../../../internal/node/README.md);
  no test checks it yet.
- `cmd/sidera/...` may import any runtime package.

Three rules bind every runtime package:

- Time comes only from the injected clock. Code that needs the real time calls
  `clock.System()`.
- Files are touched only through the `fsx` file layer.
- Randomness arrives as an `io.Reader`; only the composition root and `cmd/`
  read `crypto/rand`.

## Why

- **Contract packages stay pure** because two nodes evaluating the same inputs
  must reach the same verdict, and property tests must be able to replay any
  order of events. One import of a runtime package would bring the clock, the
  disk or a goroutine into a rule that has to be a function of its arguments.
- **The runtime lives under `internal/node/`, not in `cmd/sidera/internal/`**
  because the mobile and web clients and the CLI's embedded management client
  need to import it from outside the executable's directory. Go's internal rule
  lets anything in this module import `internal/node/` and nothing outside the
  module. A public top-level directory was rejected because it would publish a
  Go API.
- **The three runtime rules are enforced by scanning source** because a
  violation fails no other test at once. A direct clock read makes fake-clock
  tests fail now and then; a direct file write is a write the crash model never
  sees, so the crash tests pass while the write path is untested. A short
  scan per rule is cheaper than finding either in a late failure.
- **The scans cover selectors, not only calls, and ignore build constraints**
  so that `now := time.Now`, an import alias and a file that only builds on one
  platform cannot slip past.
- **The import scan runs per platform and tag set** because a platform file or a
  tagged file can import a package the default build does not.
- **Exception lists live in the test.** Some package must touch the real
  thing: the clock packages read the time, the file layer, local IPC and the
  testkit packages that stand in for the outside world (`testkit/ledger`,
  `testkit/fakeadapter`, `testkit/fakeruntime`) touch files, the composition root reads `crypto/rand`, and the
  crash model's seeded `math/rand` source is injected by the test. Writing the
  lists in the test file means widening one is a visible change. The third-party
  packages of the testkit import only the standard library, `sdk/go` and each
  other, as a real third party would, so they cannot reach `fsx`; their files are
  not daemon state; and the ledger is a test oracle that must stay independent of
  the `fsx` under test (see [testkit](../testkit/README.md)).
- **Test tooling never reaches a production binary.** Tooling consists of the
  `internal/node/testkit` and `cmd/sidera/internal/acceptance` subtrees, plus the
  exact program directories `cmd/sidera-fake-adapter` and
  `cmd/sidera-fake-runtime`. Tooling may import tooling, so acceptance drivers
  share the testkit's fake agents. No other package's non-test source may import
  any tooling. A source scan covers the whole module regardless of platform or
  build constraints, including packages no executable imports yet; checking only
  executable dependency graphs would leave those packages unprotected.
  `internal/node/testkit/...` seeds test homes through the real `config` write
  functions, which take the owner's request key, so it makes the actor of the
  local owner. Only that subtree is exempt from the local-owner scan; acceptance
  receives no such privilege. Keeping all tooling out of production makes that
  exemption safe.
- **Pure scans, not a linter dependency.** `go/parser` and `go list` are in the
  standard toolchain; a linter would add a dependency for rules this small.

## Where

- `internal/README.md`: the contract-package conventions.
- `internal/node/README.md`: the layer table, the runtime rules, the
  exceptions and the list of tests.
- `internal/node/layout_test.go`:
  - `TestContractPackagesDoNotImportRuntime`: runs `go list -deps -test` for
    every contract package for darwin, linux and windows, each with no tags and
    with `crashtest,testclock,acceptance`.
  - `TestRuntimeReadsTimeOnlyThroughClock`,
    `TestRuntimeTouchesFilesOnlyThroughFsx`,
    `TestRuntimeTakesRandomnessAsReader`: scan every non-test Go file below
    `internal/node/`. The exception lists are the variables at the top of the
    file.
  - `TestTheActorOfTheLocalOwnerIsMadeOnlyByTheManagementInterface` and
    `TestLocalSourcesAreJudgedOnlyWhereTheLoopbackDeliversThem`: the actor of the
    local owner is made only in `internal/node/manageapi`, and the judgments of a
    local source (`authz.EvaluateLocal`, `LocalCreate`, `LocalRequest`) only in the
    engine and in the transport, over the runtime tree and `cmd/`; the packages
    below `internal/node/testkit/` are exempt from the first, which stands in for
    the owner when it builds a test home;
    `TestTheLocalOwnerAndLocalSourceScansDetectViolations` checks both scans on
    inline sources.
  - `TestTestToolingReachesNoProductionBinary`: no non-test file of the module
    outside the two tooling subtrees and the two exact fake program directories
    imports any tooling package; `isTestTooling` classifies both importers and
    imports. `TestTestToolingImportCheckDetectsViolations` checks the scan on
    inline sources.
  - `TestRuntimeImportersDetectViolations` and
    `TestRuntimeScansDetectViolations`: check, on inline sources, that each
    check reports what its rule forbids and passes what it allows, so a clean
    tree means a clean tree and not a blind scan.
  - `TestRuntimeScanWalksTree`: builds a tree in a temporary directory and
    checks that the walk reaches nested directories and platform files, honors
    the exact-directory exemptions, and skips test files and `testdata`.
- `internal/node/doc.go`: the package that gives these tests a home inside the
  tree they scan.

The runtime scans cover an empty set until the first runtime package is added;
every package added afterwards is bound automatically.
