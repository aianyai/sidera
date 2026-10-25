# Continuous integration

What the repository checks on every change, on which platform, what is left
out for now and why, and how to run the same checks on a development machine.
The workflow is [`.github/workflows/ci.yml`](../../../.github/workflows/ci.yml).

## What

The workflow runs on every push to `main`, on every pull request and on manual
dispatch. Every step fails its job on any failure; there is no
`continue-on-error`; the only tests skipped are the power-loss tests on macOS
and Windows (see below), and the suite runs without `-short`. The `-race` steps
run every test, but the crash walks of `internal/node/store` take two kinds of
crash instead of four there (see below).

| Job | Runs on | Steps |
| --- | ------- | ----- |
| `format` | Linux | `gofmt -l .` must print nothing |
| `test` | Linux, macOS, Windows | no build tags: `go vet ./... ./sdk/go/...`, `go test -count=1 -timeout 30m [-skip …] ./... ./sdk/go/...`, `go test -race -count=1 -timeout 30m [-skip …] ./... ./sdk/go/...` |
| `test-tags` | Linux, macOS, Windows | tagged: `go vet -tags crashtest,testclock,acceptance ./... ./sdk/go/...`, then the two `go test` runs of `test` again with `-tags crashtest,testclock` |
| `sdk-standalone` | Linux | in `sdk/go` with `GOWORK=off`: `go build ./...`, `go test ./...`, `go mod tidy -diff` |
| `cross-compile` | Linux | `go build ./... ./sdk/go/...` with `CGO_ENABLED=0` for darwin/arm64, darwin/amd64, linux/amd64, linux/arm64 and windows/amd64 |

`./...` at the root covers every package of the root module, `cmd/` binaries
included; `./sdk/go/...` adds the SDK module that `go.work` joins to it. The
five platforms are the ones Sidera ships binaries for.

### Build tags

Three tags exist; none selects code yet, so until tagged files land the
`test-tags` job compiles and runs exactly what `test` does.

| Tag | Selects | Compiled by |
| --- | ------- | ----------- |
| `crashtest` | binary-level crash points, which stop a real process at a named step | `test-tags` |
| `testclock` | the test clock a binary reads from its environment | `test-tags` |
| `acceptance` | the test files that drive built binaries | `go vet` in `test-tags`; their tests run only in the acceptance jobs |

`go test` in `test-tags` leaves `acceptance` off on purpose: those tests start
binaries and have their own jobs, limits and timeouts. `go vet` in `test-tags`
adds it so the files are at least type-checked on every platform.

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
- **Race runs take fewer crash states in the store's crash walks**: the walks of
  `internal/node/store` replay every crash point of every transaction, and each
  scenario is single-threaded on an in-memory file system of its own, so the
  race detector finds nothing there and only multiplies the cost of every crash
  state. Under `-race` (the `race` build tag, which the test package reads
  through `raceEnabled`) a walk takes the two kinds of crash that draw nothing,
  the process crash and the worst power loss, and leaves out the two seeded power
  losses; the steps without `-race` take all four, so no kind of crash goes
  unrun. Every other test, the concurrent ones included, runs in both modes. A
  `-skip` pattern could not express this: the kinds of crash are taken in a loop
  inside one test, not as subtests. The reason is the per-package limit: `-timeout`
  covers one test binary, and without the narrowing the store's race run took
  79% of it on the slowest job (the baseline below), with more store tests due.
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
package `internal/node/store`, per job, before the race runs were narrowed:

| Runner | plain | with tags | `-race` | `-race` with tags |
| ------ | ----- | --------- | ------- | ----------------- |
| ubuntu | 297 s | 497 s | 1342 s | 1414 s |
| windows | 328 s | 415 s | 1187 s | 1147 s |
| macos | 235 s | 272 s | 990 s | 1119 s |

The limit is 1800 s per test binary, so the slowest step used 79% of it. Almost
all of that time is the crash walks. Re-measure after a change to the store's
tests with
`GOMAXPROCS=4 go test -race -count=1 -timeout 30m -tags crashtest,testclock ./internal/node/store`.

## Where

- Workflow: `.github/workflows/ci.yml`.
- Line endings: `.gitattributes`.

## Run the same checks locally

From the repository root (zsh and bash alike; on Windows use Git Bash):

```sh
gofmt -l .   # must print nothing
go vet ./... ./sdk/go/...
go test -count=1 -timeout 30m ./... ./sdk/go/...
go test -race -count=1 -timeout 30m ./... ./sdk/go/...   # the store's crash walks take two kinds of crash here

# the tagged job; on macOS and Windows CI also adds -skip '^TestPowerLossClone'
go vet -tags crashtest,testclock,acceptance ./... ./sdk/go/...
go test -count=1 -timeout 30m -tags crashtest,testclock ./... ./sdk/go/...
go test -race -count=1 -timeout 30m -tags crashtest,testclock ./... ./sdk/go/...

(cd sdk/go && GOWORK=off go build ./... && GOWORK=off go test ./... && GOWORK=off go mod tidy -diff)

for p in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64; do
  CGO_ENABLED=0 GOOS=${p%/*} GOARCH=${p#*/} go build ./... ./sdk/go/... || echo "build failed: $p"
done
```

`gofmt -l .` lists the files that need formatting; `gofmt -w` fixes them.
