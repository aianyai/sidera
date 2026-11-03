# proc

The adapter's layer over the operating system's processes: the process table,
starting a process detached from the adapter, and interrupting one. Part of the
runtime, so it does I/O; read [../../../README.md](../../../README.md). It is the
adapter's own interface, so that the runtime's flows run against fakes, over
[`sdk/go/adapterkit/process`](../../../../../sdk/go/adapterkit/process/README.md),
which does the work on macOS, Linux and Windows; the identities and the way a
process is detached on each platform are specified in the adapter-protocol reference
("Native processes").

## Entry points

| Name | Does |
| ---- | ---- |
| `Table` (`NewTable`) | `Lookup(pid)`, `Running(identity)` and `List()` of the caller's user's processes with their argument vectors. |
| `Launcher` (`NewLauncher`) | Starts a process detached, reading its input from a file and writing its output to files, with a console of its own on Windows so that it can be interrupted. Returns its identity, its `Container` and `Done`. An error that wraps `ErrStartedUnidentified` means the process started and its identity could not be read; any other error means nothing started. |
| `Signaller` (`NewSignaller`) | `Interrupt(ctx, identity)`: Ctrl-C (SIGINT, or `CTRL_C_EVENT` on Windows). An error that wraps `ErrNotRunning` means the identity names no running process. |
| `Info` | The identity (PID and start key), the start in Unix seconds, and the start in the platform's native form (what a registry file holds on Linux and Windows). |

## Rules

- A process is named by PID and start key, never by PID alone. On macOS the key is
  `<sec>.<usec>` of the kernel's start time, the text the first checkpoints stored;
  it must not change.
- **Whether a known process still runs is `Running`'s to say**, and an error of it
  is an error, never "not running". `List` only finds candidates by their arguments:
  it leaves out what is denied, so absence from it proves nothing.
- A process started by the launcher outlives the adapter and has no pipe to it: its
  input is a regular file, so reaching the end of it closes the input. Where it runs
  is in `Started.Container`, which the runtime stores and reports.
- The program to start is an absolute path, started as written.
- `main` calls `process.HandleHelperInvocation()` first: on Windows the interrupt is
  sent by a helper that is the adapter's own executable.
