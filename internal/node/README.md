# internal/node

Runtime packages: the code that does I/O, runs goroutines and talks to the
operating system. They sit on top of the pure contract packages in
[`internal/`](../README.md), apply the rules those packages define, and are
private to this module, so the daemon, the CLI and the mobile and web clients
that may later live in it can import them while nothing outside the module can.

Code that belongs only to the command line lives in `cmd/sidera/internal/` and
is not part of this tree.

## Layers

Runtime packages form a directed acyclic graph. A package imports only
packages of a lower layer, never one of its own layer or a higher one. The
table is the layout the tree grows into: a package is added in the layer that
fits what it imports, and packages that are not in the tree yet are planned.

| Layer | Packages | May import |
| ----- | -------- | ---------- |
| 0 | contract packages in `internal/` (this tree excluded), `sdk/go/...` | the standard library, each other, approved third-party dependencies |
| 1 | `fsx`, `clock` | layer 0, the standard library, `golang.org/x/sys`; `fsx` may import `clock` only to obtain the real clock |
| 1 | `daemonerr` | the standard library only: daemon error declarations shared with the wire mapper; no lifecycle or wire policy |
| 1, test support | `fsx/fsxtest`, `clock/clocktest` | their parent package, layer 0 |
| 2 | `txn` | `fsx` |
| 2 | `recfile` | the standard library only: the framing of a record file (first line, checksum, file name), shared by the owners of records |
| 2 | `logfile` | `fsx`: the restricted handle that writes and rotates the logs under `logs/`, which are not crash points; the daemon's log and the adapter hosts' go through it |
| 3 | `store`, `keys`, `config` | `txn`, `recfile`, layer 0; from `fsx` and `clock` only types, so none of them can obtain a writable file system or read the time |
| 4 | `transport`, `adapterhost`, `ipc` | layers 0 to 3; `ipc` checks directories only through the read-only part of `fsx` |
| 5 | `engine` | layers 0 to 4; it reaches `store`, `transport` and `clock` through interfaces |
| 6 | `apiwire` | `engine`, `config`, `ipc`, `adapterhost`, `daemonerr`, layer 0: the words of the wire (category, code, exit code; the adapter protocol's codes for the requests of an adapter) for what the engine and the layers below say; the engine never speaks them |
| 7 | `localhttp` | `apiwire`, `ipc`, layer 0: the shared local HTTP transport, exact method routing, bounded bodies and responses |
| 8 | `agentapi`, `manageapi` | the `engine` service interface, `localhttp`, `ipc`, `apiwire`, layer 0 |
| 9 | `daemon` | everything above; the composition root that wires the packages together |
| none | `testkit/ledger`, `testkit/fakectl`, `testkit/fakeruntime`, `testkit/fakeadapter` | the standard library, `sdk/go/...` and each other only, so they use the adapter protocol as a third party would |
| none | `testkit/fakeagent`, `testkit/fakeagent/engineagent`, `testkit/harness`, `testkit/scenario`, `testkit/crashwalk` | any runtime package and contract package: they drive and judge the daemon; `scenario` imports neither the engine nor a fixture, so that every layer's fixture can implement its environment; `testkit/README.md` says which packages of the testkit are on which side |
| none | `cmd/sidera/...` | any package of this tree |

Each side of a seam defines the interface it needs, and the graph stays acyclic
because of it. `engine` defines `AgentService` and `ManageService`, which
`agentapi` and `manageapi` call, and `Adapters`, which it calls on the adapter
host; `adapterhost` defines `Engine`, which it calls on the engine. `*engine.Engine`
implements the first two and `adapterhost`'s `Engine`, and the adapter host
implements `engine.Adapters`, without either package importing the other: `daemon`
hands each the other's implementation, and nothing but `daemon` connects across
layers.

## Rules for every runtime package

- **Time only through the injected clock.** No call of `time.Now`, `time.Since`,
  `time.Until`, `time.After`, `time.AfterFunc`, `time.NewTimer`,
  `time.NewTicker`, `time.Tick` or `time.Sleep`. Code that needs the real time
  says so by calling `clock.System()`, which keeps real time visible at the call
  site; tests inject a fake clock and drive time themselves. Contexts with a
  timeout and connection deadlines computed from the clock are fine.
- **Files only through `fsx`.** No direct use of the file functions of `os`,
  `io/ioutil`, `path/filepath` (`Walk`, `WalkDir`, `Glob`, `EvalSymlinks`),
  `syscall` or `golang.org/x/sys`. Every file operation goes through `fsx`, so
  the in-memory crash model sees every write and no write path escapes the
  crash tests.
- **Randomness only as an `io.Reader`.** No `math/rand`. `crypto/rand` appears
  only in the composition root (`daemon`) and in `cmd/`, which pass its reader
  down. The scan looks at imports, so it has a known blind spot:
  `ed25519.GenerateKey(nil)` reads the system random source without importing
  `crypto/rand`, and since Go 1.26 `ecdh.Curve.GenerateKey` ignores the reader
  it is given unless `GODEBUG=cryptocustomrand=1` is set. Key generation
  therefore takes the injected reader explicitly, and deterministic test keys
  are built with `NewKeyFromSeed`.
- **Every goroutine has an owner.** It runs under a `context` and stops on
  shutdown; tests check for leaks.

Each rule has a few exceptions, because some package has to be the one that
touches the real thing: `clock` and `clock/clocktest` read the time, `fsx`,
`ipc` and the testkit packages `testkit/ledger`, `testkit/fakeadapter` and
`testkit/fakeruntime` touch files, `daemon` reads `crypto/rand`, and the
crash model in `fsx/fsxtest` takes a seeded `math/rand` source injected by the
test, which the engine-layer fixture `testkit/harness` makes, with the engine's own
source, from the seed a test names. The exception lists are written in `layout_test.go`; extending one is a
change to that file and gets reviewed as such. The third-party packages of the
testkit (`ledger`, `fakectl`, `fakeruntime`, `fakeadapter`) stand in for the
outside world in tests: a third-party adapter, a native runtime, the ledger of
what happened. They import only the standard library, `sdk/go` and each other, as
a real third party would, so they cannot reach `fsx`; the files they touch are
not daemon state; and the ledger is a test oracle that must stay independent of
the `fsx` under test. A test in `testkit` enforces the import rule. No testkit
package has a time exemption. The sole method-scoped wait is one direct
`time.NewTimer` call in `testkit/fakeruntime/launcher.go`, in
`(*ProcessLauncher).waitForProcessInfo`, to sample the same native's existing
atomic ProcessInfo publication every 10 ms. It cannot establish readiness,
set a completion deadline, retry creation or advance logical time. The layout
scan enforces that exact boundary. Future waits must first seek an event
mechanism and cannot reuse this exception.

## Enforcement

`layout_test.go` holds the tests that scan the module, and further tests
that prove the scans themselves detect what they should:

| Test | Rule |
| ---- | ---- |
| `TestContractPackagesDoNotImportRuntime` | No contract package, tests included, depends on `internal/node/` or `cmd/`, directly or transitively, on any of darwin, linux and windows, with and without the `crashtest,testclock,acceptance` tags. A platform file or a tagged file can bring in a dependency the default build does not. |
| `TestRuntimeReadsTimeOnlyThroughClock` | The clock rule above. |
| `TestRuntimeTouchesFilesOnlyThroughFsx` | The file rule above. |
| `TestRuntimeTakesRandomnessAsReader` | The randomness rule above. |
| `TestTheActorOfTheLocalOwnerIsMadeOnlyByTheManagementInterface` | `admin.LocalOwner`, `admin.ActorLocalOwner` and `pairing.LocalAuthor` appear only in `manageapi`, in the runtime tree and in `cmd/`. A connection on the management listener is the local owner, and nothing else may say so. The packages below `testkit/` are exempt: they stand in for the owner when they build a test home, because the `config` write functions they seed it through take the owner's request key, and the next rule keeps them out of every production binary. |
| `TestTestToolingReachesNoProductionBinary` | Test tooling consists of the `internal/node/testkit` and `cmd/sidera/internal/acceptance` subtrees, plus the exact program directories `cmd/sidera-fake-adapter` and `cmd/sidera-fake-runtime`. Tooling may import tooling; no other package's non-test Go files may import any tooling, even if no executable imports that package yet. The scan covers the whole module regardless of platform or build constraints. Tooling builds test homes and plays the owner and the outside world; it never ships in the daemon or the command line. The local-owner exemption remains limited to the testkit subtree. |
| `TestLocalSourcesAreJudgedOnlyWhereTheLoopbackDeliversThem` | `authz.EvaluateLocal`, `LocalCreate` and `LocalRequest` appear only in `engine` and `transport`: only what the loopback transport delivers is a request of a local source. |

The three source scans parse every non-test file below this directory,
whatever its build constraints, and flag any use of a forbidden function, also
as a function value and under an import alias. They exist because a violation
fails no other test at once: it makes a fake-clock test fail now and then, or
lets the crash model miss a write path.
