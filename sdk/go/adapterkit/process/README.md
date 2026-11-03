# process

What an adapter needs from the operating system to run a native agent runtime as a
process of its own: start it so that it outlives the adapter, name it so that it is
found again after a restart, list the processes of the adapter's user, and
interrupt it the way Ctrl-C would. It does I/O, and it is the one package of the
SDK that reaches into the operating system, through `golang.org/x/sys`; adapters
that import only `adapterkit` do not pull it in. The identities and the way a
process is detached on each platform are specified in
[the adapter-protocol reference](../../../../docs/references/adapter-protocol/README.md)
("Native processes"); this file is the guide to the code.

## Entry points

| Name | Purpose |
| ---- | ------- |
| `Start(Spec)` | Start a process detached, with its standard streams on files or the null device. Returns its identity (read right after the start), the `adapterproto.Container` to persist, how far it escaped (`Detachment`), `Done` (the kit always waits, so no zombie) and `Handle` (for `Kill`; never `Wait` or `Release`). |
| `HostDetachment()` | What `Start` achieves on this host, before anything has started; for a probe. |
| `Lookup(pid)`, `Running(Identity)`, `List()` | The process table: one process, "does this identity still name a running process", and the user's processes with their arguments. |
| `Interrupt(ctx, Identity)` | Ctrl-C: `SIGINT` on Unix; on Windows a helper process sends `CTRL_C_EVENT` to the console of a process that was started interruptible. |
| `HandleHelperInvocation()` | The first statement of `main` of an adapter that calls `Interrupt`. |
| `Identity`, `Process` | PID and an opaque start key; the key is what `adapterproto.ProcessIdentity.StartKey` carries. |

## Rules

- **A process is named by PID and start key**, never by PID alone. Persist both and
  ask `Running`. Start keys keep their format across versions.
- **"Not found" and "could not ask" are different answers.** `Lookup` returns an
  error when the platform did not answer, access denied included. Never treat an
  error as "the process is gone": that starts a second writer beside one that runs.
  `List` is the exception by design: it leaves out what is denied, which the
  adapter cannot have started.
- **Argv[0] is a path.** It is started as written and not looked up in `PATH`.
- **`Start` has two kinds of error.** One that wraps `ErrStartedUnidentified` means
  the process runs and only its identity could not be read: the returned `Started`
  has `PID`, `Done` and `Handle`, and the caller must not act as if nothing
  started. Any other error means nothing started.
- **Falling back never fails a start.** Read `Started.Detachment`: when
  `OutlivesDaemon` is false the process will end with the adapter's job (Windows),
  and `Caveat` says in words what can still end it.
- **On Linux the environment of a start must keep `XDG_RUNTIME_DIR` and
  `DBUS_SESSION_BUS_ADDRESS`.** `systemd-run` reaches the user manager through them
  and a start in a scope fails (as an exit status of `Done`) without them. A
  process started with `environ.Compose` from the adapter's own environment has
  them.
- **Stdio are files.** A pipe would tie the process to the adapter. The caller
  closes its copies after `Start` returns.
- **An adapter that calls `Interrupt` calls `HandleHelperInvocation` first in
  `main`.** On Windows the helper is the adapter's own executable started with
  `--sidera-adapterkit-helper`; without the call the helper would serve the adapter
  protocol instead, and `Interrupt` reports that the call is missing. Nothing else
  runs before it, so the helper starts fast and takes no instance lock.
- **`Interrupt` returns when the request is delivered, not when the process
  stops.** Wait for `Done` or poll `Running`. Sending twice is harmless. On Windows
  it works only for a process started with `Spec.Interruptible` and fails for any
  other; the interrupt reaches every process on that console.

## Files

| File | Holds |
| ---- | ----- |
| `identity.go`, `identity_{darwin,linux,windows,other}.go` | The types and errors, and the process table of each platform |
| `start.go`, `start_unix.go`, `start_linux.go`, `start_unixother.go`, `start_windows.go` | `Spec`, `Start` and its shared flow; the session on Unix; the systemd scope probe and rule on Linux; creation flags, breakaway fallback and the job query on Windows |
| `interrupt.go`, `interrupt_unix.go`, `interrupt_windows.go` | `Interrupt`: the identity check, `SIGINT`, the helper's sender |
| `helper.go`, `helper_windows.go`, `helper_other.go` | `HandleHelperInvocation` and the Windows helper that attaches to the console |
| `kernel32_windows.go` | The console and job calls `x/sys/windows` has no wrapper for |

## Testing

The contract tests run the test binary as its own child (`SIDERA_PROCESS_TEST_CHILD`
names the mode). CI runners have no systemd user manager and no interactive
desktop, so the systemd scope test path and the console interrupt in a desktop
session are run by hand: on Linux once from a plain SSH shell and once inside a
tmux session (under `user@<uid>.service`), on Windows once over SSH and once from a
one-shot scheduled task (Interactive, session 1).
