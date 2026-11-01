# runtime

The Claude adapter as an `adapterkit.Application`: it answers the daemon by reading
session files, watching the process table, starting `claude -p` processes and running
the probes. It is the part of the adapter that does I/O and runs goroutines; the
decisions are in the pure packages beside it. Read [../../README.md](../../README.md)
for where it sits and [the reference](../../../../docs/references/adapter-claude/README.md)
for the rules and their reasons.

## Entry points

| Name | Does |
| ---- | ---- |
| `New(Options)` | Builds the adapter. Every `Options` field left zero takes the operating system's implementation; tests pass fakes. |
| `App` methods | `Initialize`, `Start`, `Probe`, `CreateSession`, `DeliverPrompt`, `Reconcile`, `CloseSession`, `Shutdown`; `Interrupt` and the rest are `adapterkit.Base`'s (not declared). |
| `Daemon`, `Files`, `Runner` | What the adapter sends to the daemon, how it reaches the file system and how it runs short programs. `proc.Table`, `proc.Launcher` and `proc.Signaller` are the rest of its outside. |

## Invariants

- One writer per session: a turn process is started only when no process of the session
  runs, found by the recorded PID and start and by the process table's command lines.
- The state of an attempt or a turn is stored with the daemon before it is acted on, and
  before it is reported where a lost report can be sent again; a notification that was
  lost is sent again from the stored state, never skipped.
- A session's turn is followed by one monitor goroutine under the session's lock, which also
  orders the notifications; every handler that changes the turn holds the same lock.
- A session's checkpoint is stored when its turn changes, not at every poll; each store is a
  durable transaction with the daemon.
- Nothing the adapter logs or stores holds a token or a prompt's text (a digest is stored).
- The turn files (`turns/<session>/<attempt>.{in,out,err}.jsonl`) are private to the user.

## Testing

`fake_test.go` is an in-memory world that implements every interface; the tests drive the
handlers against it. `real_test.go` (`-tags realclaude`, `SIDERA_REALCLAUDE=1`) runs against the
installed Claude Code and gives it no input, so it costs no model turn.
