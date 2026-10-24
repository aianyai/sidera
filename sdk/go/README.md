# sdk/go

The Go module that adapters and the daemon share: the JSON-RPC peer that frames
the adapter protocol, its message types and the adapter package manifest. It is
a separate module, `github.com/aianyai/sidera/sdk/go`, and depends on the
standard library only, so a third-party adapter can import it without pulling in
the daemon's dependency graph.

| Package | Holds |
| ------- | ----- |
| [`peer`](peer/README.md) | The symmetric JSON-RPC 2.0 peer: line framing and limits, queues and overload, cancellation, the request id rule, the JSON-RPC and server-range error codes and the effect rule |
| [`adapterproto`](adapterproto/README.md) | Protocol version and negotiation, method names, the parameter and result type of every method, enums, the application error codes, capabilities |
| [`manifest`](manifest/README.md) | The adapter package manifest: types, strict parsing and structural validation |
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
