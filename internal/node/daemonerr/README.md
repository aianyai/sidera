# daemonerr

The daemon's error declarations, shared with the wire mapper without importing
its composition root. This is a layer 1 leaf package: it imports only the standard
library and performs no I/O or lifecycle work. See the
[daemon reference](../../../docs/references/daemon/README.md#errors-and-command-output).

## Entry points

| Name | Purpose |
| ---- | ------- |
| `ErrAlreadyRunning`, `ErrAlreadyInitialized`, `ErrNotInitialized`, `ErrPermissions`, `ErrEngineNotStopped` | The existing sentinel identities for initialization, startup and shutdown failures. |
| `FatalError` | Wrap a fatal storage cause with the daemon's original explanation and expose it through `Unwrap`. |

`daemon` re-exports the sentinel values and aliases `FatalError`, so existing
callers continue using its names, `errors.Is` and `errors.As`. Declarations live
here to let `apiwire` recognize them without a dependency cycle; composition and
`ExitCode` remain in `daemon`. Categories, stable codes and safe wire messages
belong only to `apiwire`. This package defines no wire policy or error registry.

The public behavior remains covered by the existing `daemon` tests and by the
`apiwire` mapping tests, which construct errors through the `daemon` exports.
