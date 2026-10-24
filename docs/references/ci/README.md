# Continuous integration

What the repository checks on every change, on which platform, what is left
out for now and why, and how to run the same checks on a development machine.
The workflow is [`.github/workflows/ci.yml`](../../../.github/workflows/ci.yml).

## What

The workflow runs on every push to `main`, on every pull request and on manual
dispatch. Every step fails its job on any failure; there is no
`continue-on-error` and no test is skipped, so the suite runs without `-short`.

| Job | Runs on | Steps |
| --- | ------- | ----- |
| `format` | Linux | `gofmt -l .` must print nothing |
| `test` | Linux, macOS, Windows | `go vet ./... ./sdk/go/...`, `go test -count=1 ./... ./sdk/go/...`, `go test -race -count=1 ./... ./sdk/go/...` |
| `sdk-standalone` | Linux | in `sdk/go` with `GOWORK=off`: `go build ./...`, `go test ./...`, `go mod tidy -diff` |
| `cross-compile` | Linux | `go build ./... ./sdk/go/...` with `CGO_ENABLED=0` for darwin/arm64, darwin/amd64, linux/amd64, linux/arm64 and windows/amd64 |

`./...` at the root covers every package of the root module, `cmd/` binaries
included; `./sdk/go/...` adds the SDK module that `go.work` joins to it. The
five platforms are the ones Sidera ships binaries for.

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
- **Race and plain runs are separate steps**: the race detector changes timing
  and memory use; a failure in one mode must be visible without the other.
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
  run; runs on `main` and manual runs are never cancelled, so every commit on
  `main` has its verdict.
- **Go version**: `actions/setup-go` reads it from `go.mod`; `go.work` and
  `sdk/go/go.mod` carry the same line. Module and build caches are keyed on every
  `go.sum` in the tree. The cross-compile matrix does not use the cache, because
  its five jobs would try to save the same entry.

## Where

- Workflow: `.github/workflows/ci.yml`.
- Line endings: `.gitattributes`.

## Run the same checks locally

From the repository root (zsh and bash alike; on Windows use Git Bash):

```sh
gofmt -l .   # must print nothing
go vet ./... ./sdk/go/...
go test -count=1 ./... ./sdk/go/...
go test -race -count=1 ./... ./sdk/go/...

(cd sdk/go && GOWORK=off go build ./... && GOWORK=off go test ./... && GOWORK=off go mod tidy -diff)

for p in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64; do
  CGO_ENABLED=0 GOOS=${p%/*} GOARCH=${p#*/} go build ./... ./sdk/go/... || echo "build failed: $p"
done
```

`gofmt -l .` lists the files that need formatting; `gofmt -w` fixes them.
