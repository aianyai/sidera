# sidera-adapter-console

The console bridge adapter and its client in one program: the adapter registers
the node owner's direct endpoint, and the client, run in a terminal, starts
sessions on runtime endpoints, sends messages and reads replies. What it is for,
the commands, how replies arrive and why it is built this way are in
[`docs/references/adapter-console/`](../../docs/references/adapter-console/README.md).

Build it with `go build ./cmd/sidera-adapter-console`; `package` writes a package
for `sidera adapter install` (see the reference for the three commands).

## Layout

| Path | Holds |
| ---- | ----- |
| `main.go` | The process: its streams and environment into `console.Main`, and the only `os.Exit`. |
| `internal/console/` | Everything else: both roles, the local protocol, the control channel per platform and the package writer. |

## Conventions

- The adapter imports only `sdk/go` (`adapterkit`, `adapterproto`, `manifest`), as a
  third-party adapter would, and `internal/release` for the version alone; it never imports the daemon's packages, so
  the layout of the data directory is spelled once, in `internal/console/paths.go`,
  from the adapter-host reference.
- `adapter.start` is answered without waiting on any `endpoint.*` request: the daemon
  serves them only once it is running. The owner endpoint is registered after the
  answer (`registerOwner`), and `dispatch` refuses local requests until it is.
- `directory` and `peers` are refused by the adapter, not the client, when the daemon
  did not declare `direct_endpoint_access`: the SDK reports it without sending anything
  and `notSupported` words it, so the client has one path for every failure.
- `recv` prints a message with the page's `feedback` member as a channel notice
  (`printMessage`); the console decides nothing from it, and the member reaches it only
  because the kit declares the direct capability `feedback`. The body is the one field it
  prints that a peer writes freely, so `printMessage` prints it through `termout.SanitizeText` and indents every line of it, so
  that a body shaped like a header cannot pass for the start of another message.
- `send`, `reply`, `status`, `directory` and `peers` are laid out by `agentview` (`client.view`), the
  SDK package `sidera` uses for the same results; the console passes `invocation.command`, which
  spells the commands the hints name for the node and instance the command acted on (`--home` when
  it was given, `--instance` when it is not `console`, both after the command name), and keeps in `sessionAddress` the completion of a session endpoint that
  only `new` needs. `quote` stays for the timeout hint of `new`: it always uses single quotes, even for a plain
  word and on Windows, where `agentview.ShellWord` leaves a plain word bare and uses double quotes.
- The client's output goes through one `termout` stream built from `Env` when the client is made
  (`newClient`); the adapter programs have no `--color` flag, so the environment variables alone decide
  the decoration. Error lines (`sidera-adapter-console: <message>`) are written as they are.
- The program's help is `help` in `console.go` (an `adapterkit.Help`); the kit adds the commands it runs.
- `internal/console` holds the adapter's identity (`AdapterID`, and the release
  version from `internal/release`) once; the manifest and `initialize` both read it.
- A change to the commands, the local protocol or the control channel updates the
  reference in the same commit.
