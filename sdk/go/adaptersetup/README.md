# adaptersetup

The contract between the core CLI and an adapter's client setup, and the code every
adapter's setup shares. The rules and their reasons are in
[the adapter-setup reference](../../../docs/references/adapter-setup/README.md); this
package applies them once.

It imports the standard library, `adapterkit` (for the exit codes and the streams of
a program's commands) and `termout` (to lay out its usage), so it links `golang.org/x/sys`
and `golang.org/x/text` through them.

## Entry points

| Name | What it is |
| ---- | ---------- |
| `Request`, `Result`, `Item`, `Outcome`, `Drift` and their enumerations | The contract's JSON, versions `MinVersion` to `Version` (1) |
| `Result.Validate(op, version)` | What the CLI relies on in a result: version, op, a known `status`, unique item ids, the ids and words it passes through held to single words (`IsWord`: letters, digits and `. _ : -`, starting with a letter or digit), `requires` naming earlier items, `recorded` on check and refresh, consent on a plan's removals. A value of `action`, `problem` or an outcome status it does not know passes (`Outcome.Failed` reads an unknown status as a failure) |
| `Handler` | What an adapter implements: `Plan`, `Apply`, `Remove`, `Undo`, `Refresh`, `Check` |
| `Noter` | Optional on a `Handler`: advice for the person that is neither an item nor drift; `Handle` puts it in `Result.Notes` of a plan and a check, and it never changes the status |
| `Command(program, handler)` | The `setup` command of an adapter program, for `adapterkit.Program.Commands`; the handler runs under a context that SIGINT, SIGTERM (and SIGHUP on Linux and macOS) cancel, which kills the client a `ClientCommand` is running |
| `ReadRequest`, `Handle` | The parts of `Command`, for a caller that has its own streams |
| `Record`, `ReadRecord`, `WriteRecord`, `RemoveRecord`, `RecordPath` | The record under `<state_dir>/setup/`. `Record.Installed` (it holds an item setup did; what `recorded` means) and `Record.IsDeclined` are nil-safe; `Record.Declined` is kept by `Handle`, not by the adapter |
| `RequireStateDir`, `ErrNoStateDir` | The state-directory precondition; `Handle` applies it |
| `Reconcile`, `Wanted`, `Probe`, `Reconciliation` | The classification a check and a refresh share: stale, changed, missing, needs setup; what the person declined is `Reconciliation.Declined`, not drift |
| `Digest` | The text a record keeps of a file's content |
| `WriteFile`, `WritePrivateFile` | Atomic write (temp file, fsync, rename) with the file and directory modes the caller names, a link at the path followed; `WritePrivateFile` is its 0600/0700 case |
| `ClientCommand` (`Run`, `Failure`), `ClientOutput`, `NotStartedError`, `NotInstalled` | One bounded run of a client's command line: the program by absolute path and without a shell, a working directory the caller names and that must exist, the null device as standard input, a time limit that reports itself as a timeout and kills the client's process group (Linux, macOS), a non-zero exit returned as a result; `Failure` words a failed command with the end of its standard error |
| `EditJSONFile`, `ReadJSONFile`, `Object` (`ObjectAt`, `HasString`, `AddString`, `RemoveString`), `NewObject`, `ErrInvalidJSON`, `ErrChangedDuringEdit`, `ErrUnexpectedType` | Conservative edit of a strict-JSON file that a client owns: links followed from the directory they are really in, strict parse (UTF-8, no lone surrogate), order and style kept, mode kept, content compared before the rename; typed navigation that refuses a wrong type instead of overwriting it |

## Rules

- **Nothing here asks a person.** The CLI asks; a handler never reads the terminal, and
  logs to standard error.
- **The status is derived, not reported.** `Handle` sets it from the lists the handler
  returned, and refuses a result that does not validate (a repeated item id is the
  adapter's bug, exit 1, nothing written). It also holds a refresh to the record: no
  outcome or drift without one, and nothing `done` that the record did not hold.
- **Declines have one writer, and `recorded` one source.** After an adapter's `Apply`
  returns, `Handle` merges `Request.Declined` into the declines the record held before the
  call (minus what was accepted or is recorded), so a record the adapter rebuilt from
  scratch loses none, and after an `Undo` it removes a record that holds no item. It also
  sets `recorded` of a check and a refresh from `Record.Installed`, whatever `Check.Recorded`
  and `Refresh.Recorded` say. An adapter reads and writes `Items` only and, in `Plan`,
  lists declined items like any other.
- **Unknown members are ignored; an unsupported version is refused.** A request of a
  version outside `MinVersion` to `Version` is exit 2 and the handler is not called; the
  result carries the version of the request.
- **Setup never makes the state directory.**
