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
| `App` methods | `Initialize`, `Start`, `Probe`, `CreateSession`, `DeliverPrompt`, `Reconcile`, `CloseSession`, `Interrupt`, `AttachSession`, `SessionDetached`, `Shutdown`. `Interrupt` (`interrupt.go`) signals the recorded turn process and waits for it to end; it shares the signal and the wait with `CloseSession` (`close.go`). An attached endpoint answers `1002` to both. `Shutdown` (`app.go`) ends the monitors and, for the intent `retire`, interrupts the recorded turn process of each session through the same `interruptProcess`. |
| `Daemon`, `Files`, `Runner`, `Inbox` | What the adapter sends to the daemon, how it reaches the file system, how it runs short programs and how it writes to the inbox socket (on Windows the named pipe) of a session someone else runs. `proc.Table`, `proc.Launcher` and `proc.Signaller` are the rest of its outside. |
| `attached.go` | Attached sessions: `AttachSession`, `SessionDetached`, the entries of `adapter.start`, the monitor that follows a session, delivery to its inbox and its reconciliation. |
| `availability.go` | The one snapshot of conditions the daemon takes as the whole truth: the probe's and those of the attached endpoints. |

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
- Every turn of a session, the first and each later one, takes its model and effort from
  `loadPreset`: the pair the session's requester chose when it chose, else the preset's own values.
  A member neither gives is not passed to `claude`.
- Nothing the adapter logs or stores holds a token or a prompt's text (a digest is stored).
- The turn files (`turns/<session>/<attempt>.{in,out,err}.jsonl`) are private to the user.
- An attached session is the person's: the adapter never starts, signals or closes anything of it
  (`session.close` and `session.interrupt` for its endpoint are `1002`), and writes to it only a
  prompt, to its inbox socket, after the attempt's dispatch checkpoint is stored.
- The wake (inbox socket and token) lives in memory only. `attached.Wake` prints as redacted
  whatever the verb or log handler, errors from the socket writer carry neither path nor data, and the
  attached session's checkpoint holds the process id alone.
- An attached session is followed by one monitor goroutine under the session's lock, which also
  orders its notifications. A look reads the registry, then the transcript, then the registry again and
  acts on the last reading, so that the status is never older than the markers; the session's `dropped`
  flag, set under the same lock when its endpoint goes, keeps a look or a delivery that was in progress
  from reporting anything for it afterwards.
- A start that cannot outlive the daemon is logged (`logDetachment`, from the start and from the probe), never silently accepted.
- Every condition the adapter reports goes through `availability.go`, never straight to the daemon:
  a snapshot replaces the last, so a lone report would drop the others.

## Testing

`fake_test.go` is an in-memory world that implements every interface; the tests drive the
handlers against it. Attached sessions add `attached_test.go`, which drives them against the same world: registry files
and a process in the fake file system and process table, an `Inbox` that records what was written, and
the session record appended to by the test as Claude Code would. `real_test.go` (`-tags realclaude`, `SIDERA_REALCLAUDE=1`) runs against the
installed Claude Code and gives it no input, so it costs no model turn.
