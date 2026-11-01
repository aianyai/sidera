# Continuous integration

What the repository checks on every change, on which platform, what is left
out for now and why, and how to run the same checks on a development machine.
The workflow is [`.github/workflows/ci.yml`](../../../.github/workflows/ci.yml).
Releases are built by a second workflow, [`release.yml`](../../../.github/workflows/release.yml);
see "Release" below.

## What

The workflow runs on every push to `main`, on every pull request and on manual
dispatch. The `crashwalk` job runs on manual dispatch only; every other job runs
on all three triggers. Every step fails its job on any failure; there is no
`continue-on-error`; the only tests left out of a job are the power-loss tests on
macOS and Windows and the crash walk of the built binary, which has jobs of its
own (see below), and the suite runs without `-short`. The `-race` steps run every
test, the crash walks of `internal/node/store` included.

| Job | Runs on | Steps |
| --- | ------- | ----- |
| `format` | Linux | `gofmt -l .` must print nothing |
| `test` | Linux, macOS, Windows | no build tags: `go vet ./... ./sdk/go/...`, `go test -count=1 -timeout 30m [-skip …] ./... ./sdk/go/...`, `go test -race -count=1 -timeout 30m [-skip …] ./... ./sdk/go/...`, then `go test -count=1 -v -timeout 30m [-skip …] ./internal/node/fsx ./internal/node/ipc` and the three single-sample benchmark commands below |
| `test-tags` | Linux, macOS, Windows | tagged: `go vet -tags crashtest,testclock,acceptance,netacceptance ./... ./sdk/go/...`, then the two `go test` runs of `test` again with `-tags crashtest,testclock` |
| `acceptance` | Linux, macOS, Windows | the tests that drive built binaries, without the crash walk: `go test -race="$RACE" -count=1 -timeout 25m -tags acceptance,crashtest,testclock -skip '^TestCrashAtEveryCrashPoint' ./cmd/sidera ./cmd/sidera/internal/acceptance/...`, with `RACE` true on Linux only |
| `crashwalk` | Linux (3 shards), macOS (2), Windows (4); manual dispatch only | the crash walk of the built binary, without the race detector: first `go test -tags acceptance -list '^TestCrashAtEveryCrashPoint' ./cmd/sidera/internal/acceptance/...`, which must name the walk, then `go test -count=1 -v -timeout <limit> -tags acceptance -run '^TestCrashAtEveryCrashPoint' ./cmd/sidera/internal/acceptance/...` with `SIDERA_CRASHWALK` and `SIDERA_CRASHWALK_SHARD` set |
| `sdk-standalone` | Linux | in `sdk/go` with `GOWORK=off`: `go build ./...`, `go test ./...`, `go mod tidy -diff` |
| `cross-compile` | Linux | `go build ./... ./sdk/go/...` with `CGO_ENABLED=0` for darwin/arm64, darwin/amd64, linux/amd64, linux/arm64 and windows/amd64 |
| `release-dry-run` | Linux | `scripts/release.sh 0.0.0-dryrun "$RUNNER_TEMP/dist"`, then `sha256sum -c checksums.txt` in that directory and a check that it names five archives; nothing is published |

Limits of every job, in minutes. `go test -timeout` is set a few minutes under
the job's limit so that a hang ends with the goroutine dump of the test binary:

| Job | Instances | `timeout-minutes` | `go test -timeout` |
| --- | --------- | ----------------- | ------------------ |
| `format` | 1 | 10 | none |
| `test` | 3 | 40 | 30m per test step; benchmarks 10m, 10m, 30m |
| `test-tags` | 3 | 40 | 30m per step |
| `acceptance` | 3 | 30 | 25m |
| `crashwalk` | 9 on manual dispatch: Linux 3, macOS 2, Windows 4 | 45 on Linux and macOS, 60 on Windows | 40m on Linux and macOS, 55m on Windows |
| `sdk-standalone` | 1 | 15 | go default |
| `cross-compile` | 5 | 15 | none |
| `release-dry-run` | 1 | 20 | none |

A push to `main` or a pull request starts 17 jobs, three of them on macOS; a
manual dispatch adds the nine `crashwalk` jobs, 26 in all, five of them on macOS.
GitHub limits how many jobs of a plan run at once, macOS jobs more tightly than
the others, so some of them may wait for a runner.

`./...` at the root covers every package of the root module, `cmd/` binaries
included; `./sdk/go/...` adds the SDK module that `go.work` joins to it. The
five platforms are the ones Sidera ships binaries for.

### Build tags

Four tags select separate concerns. `crashtest` and `testclock` select the
crash observer, deterministic source, controllable clock and their management
methods. `acceptance` selects the tests that build and drive real programs,
including the CLI and MCP tests in `cmd/sidera` and the foreground-process tests
in `cmd/sidera/internal/acceptance`. The ordinary tooling sources and pure trace,
pipe and helper-reexecution tests remain available without `acceptance`.

| Tag | Selects | Compiled by |
| --- | ------- | ----------- |
| `crashtest` | binary-level crash points, which stop a real process at a named step | `test-tags`, `acceptance` |
| `testclock` | the test clock a binary reads from its environment | `test-tags`, `acceptance` |
| `netacceptance` | the test of two real daemons, which pair with each other and exchange messages over libp2p; it changes nothing in the binaries and only keeps that test out of the regular `acceptance` run. The test needs `acceptance` too and is run by hand, with `go test -count=1 -tags acceptance,netacceptance -run TestTwoNodes ./cmd/sidera/internal/acceptance` | `go vet` in `test-tags` |
| `acceptance` | the test files that drive built binaries | `go vet` in `test-tags`; their tests run only in `acceptance` and `crashwalk` |

`go test` in `test-tags` leaves `acceptance` off on purpose: those tests start
binaries and have their own jobs, limits and timeouts. `go vet` in `test-tags`
adds it so the files are at least type-checked on every platform.

The regular `acceptance` job enables all three outer test tags, so
`TestTaggedProcessPrerequisites` executes alongside the other binary tests.
The child build commands choose their own tags explicitly: the tagged program
uses `crashtest,testclock`, and the plain program receives no test tags. Outer
tags do not change this child selection. The crash-walk job needs only
`acceptance`, because it does not run that separate prerequisite regression.

### Single-sample benchmarks

Each platform's existing `test` job runs these three benchmark commands once,
without build tags or the race detector. `-run '^$'` omits ordinary tests and
`-benchtime=1x -count=1` takes one iteration of every named sub-benchmark:

| Benchmark | Package | Command timeout |
| --- | --- | --- |
| `BenchmarkFilesystemWrites` | `internal/node/fsx` | 10m |
| `BenchmarkTransactionWrites` | `internal/node/testkit/acceptance/inproc` | 10m |
| `BenchmarkColdStartScan` | `internal/node/store` | 30m |

These are platform cost records with no performance threshold or statistical
regression claim. Transaction results use `ns/write-window` for the interval
from the first actual write observation to the logical commit observation,
alongside `commits/op`. Work before the first write and after logical commit
falls outside the window, including preparation before `Begin` and the remaining
return path of `Commit`; this is not the complete transaction duration.
The cold scan uses the real filesystem with a warm page cache and includes
opening the filesystem and constructing the table, writer and store for each
scan; it does not measure a cold page cache or complete daemon startup.
The existing 40-minute job limit remains unchanged; command timeouts do not
establish that the combined job fits that limit. Hosted timings must establish
the remaining margin.

### The crash walk of the binary

The walk kills a real `sidera` at each crash point (a transaction, the role of
a file in it, a step, and which time that step was reached) and checks what the
restart finds. The tests of the walk are the ones whose names start with
`TestCrashAtEveryCrashPoint`: that prefix is what `acceptance` skips and
`crashwalk` runs, so each test runs in exactly one of the two, and a new walk
test must keep the prefix.

Two environment variables choose what a run takes; the parts that read them are
`crashwalk.ParseMode` and `crashwalk.ParseShard` in
`internal/node/testkit/crashwalk`, which take the value of the variable from the
test that reads it and also define what each value means:

| Variable | Values | Meaning |
| -------- | ------ | ------- |
| `SIDERA_CRASHWALK` | `full`, `smoke`; unset is `smoke` | `full` takes every crash point of the window, a kill after every step of the scenario and every recovery case; `smoke` takes one point per transaction and one recovery case per recovery transaction |
| `SIDERA_CRASHWALK_SHARD` | `i/n`; unset is `1/1` | the shard takes every `n`-th run of the walk, starting at the `i`-th; the shards together take every run once |

The walk runs on manual dispatch only, and the mode of a run is the `crashwalk`
input of `workflow_dispatch`, `full` or `smoke`; a dispatch that leaves the input
out takes `full`, because the workflow reads `inputs.crashwalk || 'full'`. `smoke`
is for running again, after a failure, with the fast part of the walk:

```sh
gh workflow run ci.yml --ref main -f crashwalk=smoke
```

Dispatch is used on `main` only. The local default is the other way round: a
walk run without the variable takes `smoke`, so that a walk on a development
machine is fast unless it asks for `full`.

The package-local walks of `internal/node/store`, `internal/node/engine`,
`internal/node/config`, `internal/node/keys` and `internal/node/daemon` retain
complete transaction coverage and run in `test` and `test-tags`. Binary walk
selection belongs to the outer catalog test; the package and its process fixture
are present. A missing formal walk remains an error caught by the name guard,
not a reason to skip the job.

The first step of `crashwalk` exists because `go test -run` with a pattern that
matches no test in an existing package passes: a renamed walk would otherwise
leave the job green and empty. The step lists the tests and `grep` exits 1 if
none starts with the prefix.

The `-v` of the `crashwalk` step puts the time of every rerun and of the whole
walk into the log of each shard. Those numbers, not the estimates the shard
counts were first set from, are what the counts and limits are adjusted by.

### Release

A release is made by pushing a tag `v<version>` (`v0.8.0`); nothing else starts
it. The version is the tag without the `v`, and `CHANGELOG.md` must have a section
for it, `## [0.8.0] - <date>`, because that section is the text of the release.

To cut a release:

1. Move what the release holds from `## [Unreleased]` into a new `## [<version>]`
   section of `CHANGELOG.md`, with the date, and commit it on `main`.
2. Check the archives on a development machine (`scripts/release.sh`, below) and
   that CI is green on that commit.
3. `git tag -a v<version> -m "Sidera <version>"` on that commit and
   `git push origin v<version>`.

`release.yml` checks out the tag, installs Go as `ci.yml` does, takes the release
notes from the changelog (a tag without a section fails here, before anything is
built), runs `scripts/release.sh`, and creates the GitHub Release with
`gh release create`, using the job's `GITHUB_TOKEN`. A version with a pre-release
part (`0.9.0-rc.1`) is marked as a pre-release. It does not run the tests: they ran
on `main`.

`scripts/release.sh <version> <outdir>` builds, with `CGO_ENABLED=0` and the
version stamped into every program (see
[`internal/release`](../../../internal/release/README.md)), for darwin/arm64,
darwin/amd64, linux/amd64, linux/arm64 and windows/amd64: `sidera`,
`sidera-adapter-codex` and `sidera-adapter-console`, and `sidera-adapter-claude` on
darwin only, since its manifest lists darwin only. Each adapter's `package`
subcommand then writes its package, which is where a package gets the release
version in its manifest. An adapter's program is shipped inside its package and
nowhere else, except the console's, which is also the top-level
`sidera-adapter-console` because the same program is the owner's command-line
client and the owner runs it from the archive. `<outdir>` ends up with exactly:

| File | Holds |
| ---- | ----- |
| `sidera_<version>_<os>_<arch>.tar.gz` (`.zip` for Windows), five of them | at the top level, with no directory around them: `sidera` and `sidera-adapter-console` (`.exe` on Windows), `LICENSE`, `THIRD_PARTY_NOTICES`, `README.txt`, which names the install guide of that version, `https://github.com/aianyai/sidera/blob/v<version>/docs/guide/install.md`, and `adapters/<name>/` with the package of each adapter built (`sidera-adapter.json` and `bin/<program>`; `adapters/console/`, `adapters/codex/` and, in the darwin archives only, `adapters/claude/`) |
| `checksums.txt` | `sha256sum`-style lines, the SHA-256 and the name of every archive |

The script refuses a non-empty `<outdir>`, and stops if the `sidera` it just built
does not report the version it was given.

`THIRD_PARTY_NOTICES` is written by `scripts/third-party-notices.sh` before anything
is built, and every archive holds the same file. It lists the Go standard library
and every module that `go list -deps` reports for the four programs
(`sidera` and the three adapters) on all five platforms, built with
`CGO_ENABLED=0` as the release is, so a module that only one platform or one
program links is listed. For each it gives the path, the version, the name of the
licence and the text of every licence file (`LICENSE*`, `COPYING*`) and notice file
(`NOTICE*`, `PATENTS*`) at the top of the module's directory in the module cache,
verbatim. A module with an MPL-2.0 licence (`github.com/libp2p/go-yamux`) also
carries the statement that its source code, at that version, is available at its
repository. Nothing modifies the module, so the repository is its source.

The script fails, listing every module concerned, when a linked module has no
licence file, when a licence file is none of the texts the script knows, or when a
licence is not MIT, BSD-2-Clause, BSD-3-Clause, Apache-2.0, ISC or MPL-2.0. A new
module under another licence therefore stops the release until the maintainers
decide whether it may be bundled; the script never decides that. A module that
only says it is under either of two licences (`go-base36` says Apache-2.0 or MIT and
ships neither text) is listed as `Apache-2.0 OR MIT` with the file it ships.

The `release-dry-run` job runs the script a tag runs, so that a change which breaks
it, such as a program that no longer packages or a module whose licence the
notices refuse, fails on the commit that makes it and not at the first release. Its
version, `0.0.0-dryrun`, is a placeholder that no tag will carry: the script needs
a well-formed version and nothing else, and reading the latest changelog section
instead would tie the job to the changelog's layout and to which version was last
released. It writes into `$RUNNER_TEMP`, outside the checkout, and has no token
beyond the read-only one of the workflow, so it cannot publish.

### Not run yet

Two checks of the root module wait for the first release:

- `GOWORK=off go build ./...`
- `go mod tidy -diff`

The root module reaches `sdk/go` only through `go.work` until the SDK is
tagged: its `go.mod` does not require the SDK, so without the workspace the
build and the tidy check cannot resolve `github.com/aianyai/sidera/sdk/go/...`.
Once the SDK is tagged, the root `go.mod` requires it and both checks are added
to the workflow.

## Why

- **`sdk-standalone`**: `sdk/go` is consumed as its own module by adapters
  outside this repository. With `go.work` in effect, a missing requirement or an
  import of a root-module package would go unnoticed, so the module is built,
  tested and tidied with the workspace switched off.
- **`CGO_ENABLED=0` in `cross-compile`**: released binaries are static, so a
  dependency that needs cgo must fail here and not at release time. The `test`
  job's `-race` is the one place cgo is on: the race detector needs it and a C
  compiler, which all three hosted runner images carry.
- **The crash walks take what the reliability floor needs**: the walks of
  `internal/node/store` and `internal/node/engine` replay every crash point of
  every transaction, and each scenario is single-threaded on an in-memory file
  system of its own, so the race detector finds nothing there and only multiplies
  the cost. The walks prove three things and take only the crashes those need: an
  acknowledged write survives a power loss that keeps only synced content (the
  worst power loss), recovery after any crash ends with every record valid (the
  process crash and the worst power loss), and a crash during recovery is redone
  (the nested walk). Two seeded random power losses per point used to be taken as
  well; under the transaction discipline they only keep a subset of the one group
  of writes that is not synced yet, between those two crashes, so they remain in
  the property tests and in the walks of the store's batch transactions, which are
  sets of independent single-file transitions. Under `-race` (the `race` build tag,
  read by the store's tests through `raceEnabled`) those batch walks leave the
  seeded power losses out, as the detector adds cost and no coverage there. The
  nested walk crashes again, at every point of its recovery, only the first crash
  state that reaches each point of a recovery rule (the recovery transactions of the
  store, the cleanup and the settle of `txn.Recover`), not every crash state; a
  replay that writes under the declaration of a normal transaction is not such a
  point, because a crash in the middle of it leaves the disk the walk of that
  declaration already crashes at. Being cheap, it runs under `-race` too, and the
  assertions that need it apply in every mode. A `-skip` pattern could not express any of this: the kinds of crash are
  taken inside one test, not as subtests. The reason is the per-package limit:
  `-timeout` covers one test binary, and the store's race run, without any
  narrowing, took 79% of it on the slowest job (the baseline below).
- **A verbose run of the platform packages**: `go test` prints `t.Log` output and
  skip reasons only with `-v` or on failure, and the main steps run without it. The
  platform tests of `internal/node/fsx` and `internal/node/ipc` log what they
  measure on the runner (how long a replace or a lock takes, what a directory
  flush or a security descriptor returns) and skip what a runner cannot do (a
  symbolic link or a second user on Windows, a socket path that is too long on
  Unix). The extra step runs those two packages once more with `-v`, so one CI log
  carries the platform values and skip reasons the next fix needs. It runs whole
  packages and not a name pattern: the platform tests share no name prefix, and a
  package-wide run also covers a platform test added later. It adds no check of its
  own, since the same tests ran in the steps before it. The package list must grow
  with any package that gets platform tests which log or skip.
- **Why the walk of the binary has jobs apart from the tests**: a rerun starts a
  process, kills it and starts it again, so a walk takes minutes where the tests
  of a package take seconds, and its cost grows with the number of declared crash
  points. It is split over shards so that no job's limit has to hold a whole
  platform's walk, and the shard counts differ by platform because the cost of a
  rerun is expected to: starting processes is slowest on Windows. The counts
  (Linux 3, macOS 2, Windows 4) and the limits were estimated from the expected
  number of reruns, not measured: the first run of the workflow is the first
  measurement (see below).
- **The walk of the binary runs on demand, not on every push**: the full walk
  reruns about 2,000 real-process crashes on each of three operating systems.
  Per-operation crash recovery is already checked on every push by the crash
  tests of the store, the engine and the daemon (the `crashtest` tag, in
  `test-tags`), so what the walk of the binary adds is the end-to-end scenario of
  the built program. That is run when it is wanted, such as before a release or
  after a change to recovery code, with a manual dispatch of the workflow.
- **The inner binary is built with `-race` on Linux only, in `acceptance`**: the
  test binary of the acceptance tests builds the binaries it drives, and gives
  them `-race` when it was built with `-race` itself (a constant set by the `race`
  build constraint, so that no flag has to be passed twice). The `acceptance` step
  therefore passes `-race` on Linux, where the race detector watches real
  processes talking to each other and is cheapest, and not on macOS and Windows,
  where the point of the job is the behaviour of renames and processes, and where
  `-race` would multiply the time of every run. The `RACE` variable is a boolean
  passed as `-race="$RACE"`, because a flag that is sometimes empty would need an
  unquoted expansion in the script; with `GOFLAGS` the flag would also reach the
  `go build` that the test binary runs.
- **The walk of the binary does not use the race detector**: race detection
  belongs where it finds races at the lowest layer, and the `-race` steps of
  `test` and `test-tags` and the Linux `acceptance` job drive the same concurrent
  code. The walk checks crash consistency; rerunning thousands of real-process
  crashes under the race detector only repeats that check at about four times the
  cost per crash point.
- **Race and plain runs are separate steps**: the race detector changes timing
  and memory use; a failure in one mode must be visible without the other.
- **Why a second tagged job**: tagged files are not compiled without their tag,
  so `test` alone would never vet or run them. A separate job, not an extra
  step, keeps each failure attributable to the build it came from and lets
  both builds run in parallel.
- **Explicit `-timeout`**: `go test` aborts a package after 10 minutes by
  default, which a slow runner under `-race` can exceed. Every `go test` step
  names `30m`, inside the job's 40 minute limit, so a hang ends with the test
  binary's goroutine dump and not with the job being killed.
- **Power-loss tests run on Linux only**: tests that replay a crash against an
  in-memory file system model the same thing on every platform, so repeating
  them elsewhere only costs time. Their names all start with
  `TestPowerLossClone`; `test` and `test-tags` skip that prefix with
  `-skip '^TestPowerLossClone'` on macOS and Windows and skip nothing on Linux.
  The fast fault-injection tests of the same file system are not named this
  way and run everywhere. Any new power-loss test must keep the prefix, or it
  will run on all three platforms.
- **Full suite on shared runners**: some tests are skipped under `-short`
  (`internal/pairing` property tests, `internal/authz` classification sweeps,
  `protocol` peer wire test, which re-runs the SDK peer tests). CI runs them
  all, so none may depend on the speed of the machine.
- **Line endings**: `.gitattributes` fixes checked-out text to LF. Windows
  runners and developer machines would otherwise check out CRLF, and tests that
  read golden messages byte for byte would differ from Linux.
- **Least privilege**: the workflow's token has `contents: read` only and
  `actions/checkout` does not persist it (`persist-credentials: false`), since
  no step pushes anything.
- **Pinned actions**: each `uses:` names a full commit SHA with the release in a
  comment. The repository is public; a tag can be moved, a commit cannot. To
  update, resolve the new release tag to its commit
  (`gh api repos/<owner>/<repo>/git/ref/tags/<tag>`, or `/tags` for a lightweight
  tag) and replace both the SHA and the comment.
- **Concurrency**: a new push to a pull request cancels that request's older
  run. Every other run, whether a push to `main` or a manual dispatch, is its
  own concurrency group, named by its run id, so no run on `main` cancels,
  replaces or waits for another and every commit on `main` has its verdict.
  A concurrency group allows one running and one pending run, and a newer
  pending run cancels the older one (GitHub's `queue` setting, default
  `single`). Grouping `main` by branch would therefore drop the run of any
  commit pushed while an earlier run is in progress and another is already
  waiting; so would a manual re-run started right after a push. The price is
  that two pushes close together run side by side on separate runners. The
  alternative `queue: max` keeps runs in order but cannot be combined with
  `cancel-in-progress: true`, which pull requests need, and with
  `cancel-in-progress` given as an expression the documentation leaves the
  combination undefined.
- **A script for the notices, not a licence tool**: the build list is what `go list
  -deps` prints for the programs, the licence files are in the module cache, and the
  licences allowed are six texts told apart by a phrase each, which is a few lines
  of shell. A tool such as go-licenses would be a dependency of the release for
  that, and would guess a licence from a text where the script refuses to.
- **The notices cover every platform and program together**: one file is
  shipped with every archive, so a platform's archive never lacks a notice for a
  module that another platform links, and the file does not change with the
  platform it was built on.
- **A script, not a release tool**: the build is a static cross-compile and each
  adapter's own `package` subcommand, which a short script says plainly, runs
  the same on a development machine as on the runner, and adds no tool to learn;
  the workflow is only the runner, the tag and the token. A tool such as GoReleaser
  would bring a configuration language for what these steps already express.
- **Why the archives hold the adapters' programs once**: a program of an adapter
  other than the console is only useful installed, and `sidera adapter install`
  takes the package, so a second copy at the top level would only make the archive
  larger and leave two programs to choose between. The console is the exception
  because its program is also the owner's client.
- **The package commands run on the builder**: a cross-compiled adapter cannot
  run there, so the script builds one native copy of each adapter, with the same
  version, to run `package` with, and gives it the cross-compiled binary to copy.
- **The commit is stated, not found**: Go records the revision of the checkout in
  the binary, but for a build in a linked git worktree it records the main
  checkout's, so `scripts/release.sh` passes `git rev-parse HEAD` to the linker. A
  release built on the runner has the same commit either way.
- **The release job's token**: `contents: read` for the workflow and
  `contents: write` for the one job that creates the release; the checkout does not
  persist the token and only the last step receives it. Its outputs are written
  under `$RUNNER_TEMP`, since a file in the checkout would make the build report
  an uncommitted change.
- **Go version**: `actions/setup-go` reads it from the `toolchain` line of the
  root `go.mod` (setup-go prefers it over the `go` line), so CI installs the same
  patch release a local build uses; with only a `go 1.26.0` line it would install
  exactly 1.26.0 and miss later patch fixes. `go.work` carries the same two lines.
  `sdk/go/go.mod` keeps its own, lower minimum: it is a published module and does
  not depend on go-winio, which is what raises the root module's minimum. Module
  and build caches are keyed on every `go.sum` in the tree. The cross-compile
  matrix does not use the cache, because its five jobs would try to save the same
  entry.

### Observed baseline

The first run on `main` (commit `2196b29`, 2026-10-25) took this long for the
package `internal/node/store`, per job, before the race runs were narrowed. The
second run (commit `fecfee2`, the same day) is the first with the race-only
narrowing of the crash walks; its value follows the first run's in each cell:

| Runner | plain | with tags | `-race` | `-race` with tags |
| ------ | ----- | --------- | ------- | ----------------- |
| ubuntu | 297 s, then 352 s | 497 s, then 329 s | 1342 s, then 867 s | 1414 s, then 1194 s |
| windows | 328 s, then 320 s | 415 s, then 234 s | 1187 s, then 956 s | 1147 s, then 770 s |
| macos | 235 s, then 247 s | 272 s, then 342 s | 990 s, then 1140 s | 1119 s, then 1043 s |

The limit is 1800 s per test binary. In the first run the slowest step used 79% of
it; in the second the slowest, 1194 s (ubuntu, `-race` with tags), used 66%. Runner
times vary by roughly 20% between runs, and by more in single cells: macOS `-race`
without tags took 990 s and then 1140 s with no change to that path, and the
ubuntu plain tagged run took 497 s and then 329 s. The margin is therefore read
from the slowest job, not from a mean. Almost all of the time is the crash walks.
Re-measure after a change to the store's tests with
`GOMAXPROCS=4 go test -race -count=1 -timeout 30m -tags crashtest,testclock ./internal/node/store`.

On a developer machine (4 processors, 2026-10-25) that command took 1116 s with the
two kinds of crash and the nested walk, and 139 s once the race run also skipped the
nested walk (35 762 recoveries after a crash during recovery in the first run, none
in the second). The plain run of the same tests takes about 180 s and still reports
43 588 of them. These are local figures, not CI baselines: CI times are in the table.

The walk of the store alone (`TestEveryTransactionConvergesFromEveryCrashState`, 4
processors, `-tags crashtest,testclock`, on a machine shared with other work, so the
CPU time is the steadier figure) measured as follows before and after the walks were
cut to the crashes the reliability floor needs:

| Run | crash states | recoveries after a crash during recovery | CPU time | wall time |
| --- | ------------ | ---------------------------------------- | -------- | --------- |
| plain, before | 2932 | 43 588 | 754 s | 365 s |
| plain, after | 1792 | 476 | 27 s | 12 to 19 s |
| `-race`, before | 1466 | none (skipped) | 112 s | 34 s |
| `-race`, after | 1466 | 476 | 156 to 163 s | 59 to 115 s |

The `-race` run takes more than before because it now takes the nested walk, which
costs about 50 CPU seconds there and about 25 s of wall time on the shared machine;
the plain run takes less than a twentieth of the CPU time. The walks of the
engine took 3.4 s before and 1.9 s after, with two crash states per point instead of
four or five.

### Pending verification

Nothing in the two jobs above has run on a hosted runner: the workflow is
checked locally with `actionlint`, and each command was run on macOS against a
stand-in package, which proved that the flags, the `-skip` and `-run` split, the
guard and both environment variables work together. Until a run on `main` shows
otherwise these are not verified, and each is pending verification by CI on
`main`: that the four Windows shards fit in 60 minutes and the others in 45;
the time of a rerun on each platform and so whether the shard counts are right;
that the five macOS jobs of a run start together; that a dispatch
that leaves `inputs.crashwalk` out takes `full`. The foreground-process acceptance
package exists and its focused tests run locally. A complete binary catalog walk
and its hosted timings must be verified separately; focused tests do not establish the full-walk budget.

## Where

- Workflow: `.github/workflows/ci.yml`.
- Release: `.github/workflows/release.yml`, `scripts/release.sh`,
  `scripts/release-notes.sh` (prints the changelog section of a version),
  `scripts/third-party-notices.sh` (writes `THIRD_PARTY_NOTICES`) and
  `CHANGELOG.md`.
- Line endings: `.gitattributes`.

## Run the same checks locally

From the repository root (zsh and bash alike; on Windows use Git Bash):

```sh
gofmt -l .   # must print nothing
go vet ./... ./sdk/go/...
go test -count=1 -timeout 30m ./... ./sdk/go/...
go test -race -count=1 -timeout 30m ./... ./sdk/go/...

# one cost sample per platform, without tags or -race
go test -run '^$' -bench '^BenchmarkFilesystemWrites$' -benchtime=1x -count=1 -timeout 10m ./internal/node/fsx
go test -run '^$' -bench '^BenchmarkTransactionWrites$' -benchtime=1x -count=1 -timeout 10m ./internal/node/testkit/acceptance/inproc
go test -run '^$' -bench '^BenchmarkColdStartScan$' -benchtime=1x -count=1 -timeout 30m ./internal/node/store

# the tagged job; on macOS and Windows CI also adds -skip '^TestPowerLossClone'
go vet -tags crashtest,testclock,acceptance,netacceptance ./... ./sdk/go/...
go test -count=1 -timeout 30m -tags crashtest,testclock ./... ./sdk/go/...
go test -race -count=1 -timeout 30m -tags crashtest,testclock ./... ./sdk/go/...

# the jobs of the binary; CLI/MCP tests and the foreground-process fixture
go test -race=false -count=1 -timeout 25m -tags acceptance,crashtest,testclock -skip '^TestCrashAtEveryCrashPoint' ./cmd/sidera ./cmd/sidera/internal/acceptance/...
SIDERA_CRASHWALK=smoke go test -count=1 -v -tags acceptance -run '^TestCrashAtEveryCrashPoint' ./cmd/sidera/internal/acceptance/...

# the whole walk, or one shard of it, as a CI job takes it
SIDERA_CRASHWALK=full go test -count=1 -v -timeout 55m -tags acceptance -run '^TestCrashAtEveryCrashPoint' ./cmd/sidera/internal/acceptance/...
SIDERA_CRASHWALK=full SIDERA_CRASHWALK_SHARD=1/3 go test -count=1 -v -timeout 40m -tags acceptance -run '^TestCrashAtEveryCrashPoint' ./cmd/sidera/internal/acceptance/...

(cd sdk/go && GOWORK=off go build ./... && GOWORK=off go test ./... && GOWORK=off go mod tidy -diff)

# the release archives: five platforms into an empty directory, then the changelog section
dist=$(mktemp -d)
scripts/release.sh 0.8.0 "$dist" && (cd "$dist" && shasum -a 256 -c checksums.txt)
scripts/release-notes.sh 0.8.0

for p in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64; do
  CGO_ENABLED=0 GOOS=${p%/*} GOARCH=${p#*/} go build ./... ./sdk/go/... || echo "build failed: $p"
done
```

The workflow files are checked with `actionlint .github/workflows/*.yml`, and the
scripts with `shellcheck scripts/*.sh`. `scripts/release.sh` needs `go`, `git`,
`tar`, `zip` and `sha256sum` (or `shasum`); the archives are written to the directory
given, which must be empty, and the build reports "modified" after the commit when
the working tree has uncommitted changes.

`gofmt -l .` lists the files that need formatting; `gofmt -w` fixes them.
