# sdk/go

The Go module that adapters and the daemon share: the JSON-RPC peer that frames
the adapter protocol, its message types, the adapter package manifest and the
words every agent is told about Sidera. It is a separate module,
`github.com/aianyai/sidera/sdk/go`. The packages use the standard library only, so a
third-party adapter can import them without pulling in the daemon's dependency graph,
with three exceptions: `adapterkit/process` and `adapterkit/realpath` use
`golang.org/x/sys`, and `termout` uses `golang.org/x/sys` and `golang.org/x/text`. The
`sidera` binary imports `termout`, so it links both; an adapter that leaves those three
packages out does not.

| Package | Holds |
| ------- | ----- |
| [`peer`](peer/README.md) | The symmetric JSON-RPC 2.0 peer: line framing and limits, queues and overload, cancellation, the request id rule, the JSON-RPC and server-range error codes and the effect rule |
| [`adapterproto`](adapterproto/README.md) | Protocol version and negotiation, method names, the parameter and result type of every method, enums, the application error codes, capabilities |
| [`adaptersetup`](adaptersetup/README.md) | The contract between the core CLI and an adapter's client setup (request, result, ops, versions), the helper that is an adapter program's `setup` command, the record of what setup did, the atomic private write and the conservative edit of a client's JSON file |
| [`adapterkit`](adapterkit/README.md) | The serve loop every adapter shares: version negotiation, the instance lock before the answer to `initialize`, the gating before `initialize` and after `shutdown`, strict decoding and validated results, typed calls to the daemon; the adapter implements an `Application` for the rest |
| [`adapterkit/environ`](adapterkit/environ/README.md) | Composes the environment of a child process of an adapter: the pairs a child cannot read left out, what the adapter drops, what it sets, a directory put in front of `PATH`, with names compared as the platform does |
| [`adapterkit/process`](adapterkit/process/README.md) | Native processes for an adapter that drives an agent runtime: start one detached so that it outlives the adapter, name it by PID and start so it is found again after a restart, list the user's processes, interrupt it the way Ctrl-C would. macOS, Linux and Windows |
| [`adapterkit/realpath`](adapterkit/realpath/README.md) | The path the platform itself reports for an existing path: symbolic links and junctions followed, names in their on-disk case and long form on Windows |
| [`guidance`](guidance/README.md) | What an agent is told about Sidera, written once and composed into three texts: the instructions of the `sidera` MCP server, the statement an adapter gives its sessions, and the `SKILL.md` |
| [`termout`](termout/README.md) | The human-readable terminal output of Sidera programs: whether a stream is decorated or colored, styles and markers, aligned blocks, tables and lists, wrapping, the sanitizing of text that came from outside, and the written form of times, sizes and durations; the one terminal check |
| [`manifest`](manifest/README.md) | The adapter package manifest: types, strict parsing and structural validation |
| [`semver`](semver/README.md) | Semantic Versioning 2.0.0: the syntax of a version and the precedence of two versions; public so that any other reader of a version applies the same rules |
| [`strictjson`](strictjson/README.md) | The strict JSON decoder the packages above share: exact key names, no repeated key, no `null`, required fields; public so that any other reader of the same kinds of documents applies the same rules |

The module root holds no package.

## Working in the repository

The repository root has a committed `go.work` that lists the root module and this
module. Inside the workspace the root module imports `sdk/go` packages straight
from the working tree, with no `replace` directive and no `require` line for
this module:

- `go build`, `go vet`, `go test`, `go test -race`, `go test -fuzz` and `go work sync`
  work in the workspace.
- `go mod tidy` in the repository root does not: it ignores the workspace and
  looks for a published version of this module, which does not exist until the
  first `sdk/go/vX.Y.Z` tag. Use `go get` to add a dependency to the root
  module meanwhile.
- `GOWORK=off go build ./...` fails for the same reason; it becomes the release
  check once a version is tagged and the root `go.mod` requires it.
