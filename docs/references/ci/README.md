# Continuous integration

What the repository checks on every change, on which platform, what is left
out for now and why, and how to run the same checks on a development machine.
The workflow is [`.github/workflows/ci.yml`](../../../.github/workflows/ci.yml).

## What

The workflow runs on every push to `main`, on every pull request and on manual
dispatch. Every step fails its job on any failure; there is no
`continue-on-error`; the only tests left out of a job are the power-loss tests on
macOS and Windows and the crash walk of the built binary, which has jobs of its
own (see below), and the suite runs without `-short`. The `-race` steps run every
test, the crash walks of `internal/node/store` included.

| Job | Runs on | Steps |
| --- | ------- | ----- |
| `format` | Linux | `gofmt -l .` must print nothing |
| `test` | Linux, macOS, Windows | no build tags: `go vet ./... ./sdk/go/...`, `go test -count=1 -timeout 30m [-skip …] ./... ./sdk/go/...`, `go test -race -count=1 -timeout 30m [-skip …] ./... ./sdk/go/...`, then `go test -count=1 -v -timeout 30m [-skip …] ./internal/node/fsx ./internal/node/ipc` |
| `test-tags` | Linux, macOS, Windows | tagged: `go vet -tags crashtest,testclock,acceptance ./... ./sdk/go/...`, then the two `go test` runs of `test` again with `-tags crashtest,testclock` |
| `acceptance` | Linux, macOS, Windows | the tests that drive built binaries, without the crash walk: `go test -race="$RACE" -count=1 -timeout 25m -tags acceptance,crashtest,testclock -skip '^TestCrashAtEveryCrashPoint' ./cmd/sidera ./cmd/sidera/internal/acceptance/...`, with `RACE` true on Linux only |
| `crashwalk` | Linux (3 shards), macOS (2), Windows (4) | the crash walk of the built binary: first `go test -tags acceptance -list '^TestCrashAtEveryCrashPoint' ./cmd/sidera/internal/acceptance/...`, which must name the walk, then `go test -race="$RACE" -count=1 -v -timeout <limit> -tags acceptance -run '^TestCrashAtEveryCrashPoint' ./cmd/sidera/internal/acceptance/...` with `SIDERA_CRASHWALK` and `SIDERA_CRASHWALK_SHARD` set |
| `sdk-standalone` | Linux | in `sdk/go` with `GOWORK=off`: `go build ./...`, `go test ./...`, `go mod tidy -diff` |
| `cross-compile` | Linux | `go build ./... ./sdk/go/...` with `CGO_ENABLED=0` for darwin/arm64, darwin/amd64, linux/amd64, linux/arm64 and windows/amd64 |

Limits of every job, in minutes. `go test -timeout` is set a few minutes under
the job's limit so that a hang ends with the goroutine dump of the test binary:

| Job | Instances | `timeout-minutes` | `go test -timeout` |
| --- | --------- | ----------------- | ------------------ |
| `format` | 1 | 10 | none |
| `test` | 3 | 40 | 30m per step |
| `test-tags` | 3 | 40 | 30m per step |
| `acceptance` | 3 | 30 | 25m |
| `crashwalk` | 9: Linux 3, macOS 2, Windows 4 | 45 on Linux and macOS, 60 on Windows | 40m on Linux and macOS, 55m on Windows |
| `sdk-standalone` | 1 | 15 | go default |
| `cross-compile` | 5 | 15 | none |

A run starts 25 jobs, five of them on macOS. GitHub limits how many jobs of a
plan run at once, macOS jobs more tightly than the others, so some of them may
wait for a runner.

`./...` at the root covers every package of the root module, `cmd/` binaries
included; `./sdk/go/...` adds the SDK module that `go.work` joins to it. The
five platforms are the ones Sidera ships binaries for.

### Build tags

Three tags select separate concerns. `crashtest` and `testclock` select the
crash observer, deterministic source, controllable clock and their management
methods. `acceptance` selects the tests that build and drive real programs,
including the CLI and MCP tests in `cmd/sidera` and the foreground-process tests
in `cmd/sidera/internal/acceptance`. The ordinary tooling sources and pure trace,
pipe and helper-reexecution tests remain available without `acceptance`.

| Tag | Selects | Compiled by |
| --- | ------- | ----------- |
| `crashtest` | binary-level crash points, which stop a real process at a named step | `test-tags`, `acceptance` |
| `testclock` | the test clock a binary reads from its environment | `test-tags`, `acceptance` |
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

The mode of a CI run is the `crashwalk` input of `workflow_dispatch`, `full` or
`smoke`. A run that has no input (a push to `main`, a pull request) takes `full`,
because the workflow reads `inputs.crashwalk || 'full'`. `smoke` is for running
again, after a failure, with the fast part of the walk:

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
- **The inner binary is built with `-race` on Linux only**: the test binary of
  the acceptance tests builds the binaries it drives, and gives them `-race`
  when it was built with `-race` itself (a constant set by the `race` build
  constraint, so that no flag has to be passed twice). The `acceptance` and
  `crashwalk` steps therefore pass `-race` on Linux, where the race detector
  watches real processes talking to each other and is cheapest, and not on macOS
  and Windows, where the point of the jobs is the behaviour of renames and
  processes, and where `-race` would multiply the time of every rerun. The
  `RACE` variable is a boolean passed as `-race="$RACE"`, because a flag
  that is sometimes empty would need an unquoted expansion in the script; with
  `GOFLAGS` the flag would also reach the `go build` that the test binary runs.
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
that the five macOS jobs of a run start together; that `-race` on the Linux
walk costs what was assumed; that `inputs.crashwalk` is empty on a push and a
pull request, so that the mode is `full`. The foreground-process acceptance
package exists and its focused tests run locally. A complete binary catalog walk
and its hosted timings must be verified separately; focused tests do not establish the full-walk budget.

## Where

- Workflow: `.github/workflows/ci.yml`.
- Line endings: `.gitattributes`.

## Run the same checks locally

From the repository root (zsh and bash alike; on Windows use Git Bash):

```sh
gofmt -l .   # must print nothing
go vet ./... ./sdk/go/...
go test -count=1 -timeout 30m ./... ./sdk/go/...
go test -race -count=1 -timeout 30m ./... ./sdk/go/...

# the tagged job; on macOS and Windows CI also adds -skip '^TestPowerLossClone'
go vet -tags crashtest,testclock,acceptance ./... ./sdk/go/...
go test -count=1 -timeout 30m -tags crashtest,testclock ./... ./sdk/go/...
go test -race -count=1 -timeout 30m -tags crashtest,testclock ./... ./sdk/go/...

# the jobs of the binary; CLI/MCP tests and the foreground-process fixture
go test -race=false -count=1 -timeout 25m -tags acceptance,crashtest,testclock -skip '^TestCrashAtEveryCrashPoint' ./cmd/sidera ./cmd/sidera/internal/acceptance/...
SIDERA_CRASHWALK=smoke go test -count=1 -v -tags acceptance -run '^TestCrashAtEveryCrashPoint' ./cmd/sidera/internal/acceptance/...

# the whole walk, or one shard of it, as a CI job takes it (on Linux add -race)
SIDERA_CRASHWALK=full go test -count=1 -v -timeout 55m -tags acceptance -run '^TestCrashAtEveryCrashPoint' ./cmd/sidera/internal/acceptance/...
SIDERA_CRASHWALK=full SIDERA_CRASHWALK_SHARD=1/3 go test -count=1 -v -timeout 40m -tags acceptance -run '^TestCrashAtEveryCrashPoint' ./cmd/sidera/internal/acceptance/...

(cd sdk/go && GOWORK=off go build ./... && GOWORK=off go test ./... && GOWORK=off go mod tidy -diff)

for p in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64; do
  CGO_ENABLED=0 GOOS=${p%/*} GOARCH=${p#*/} go build ./... ./sdk/go/... || echo "build failed: $p"
done
```

The workflow file is checked with `actionlint .github/workflows/ci.yml`.

`gofmt -l .` lists the files that need formatting; `gofmt -w` fixes them.
