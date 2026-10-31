# proc

The adapter's layer over the operating system's processes: the process table,
starting a process detached from the adapter, and signals. Part of the runtime,
so it does I/O; read [../../../README.md](../../../README.md).

## Entry points

| Name | Does |
| ---- | ---- |
| `Table` (`NewTable`) | `Lookup(pid)` and `Snapshot()` of the caller's user's processes with their argument vectors. |
| `Launcher` (`NewLauncher(table)`) | Starts a process in a session of its own, reading its input from a file and writing its output to files. |
| `Signaller` (`NewSignaller`) | `Interrupt(pid)`: SIGINT. |
| `Info.StartKey()` | The start as the protocol's opaque start key, `<sec>.<usec>`. |

## Rules

- A process is named by PID and start. A zombie is not alive.
- A process whose arguments cannot be read is an error, never "not running": it
  could be a turn process of a session.
- A process started by the launcher leads a process group of its own, has no
  controlling terminal and outlives the adapter. Its input is a regular file, so
  reaching the end of it closes the input and no pipe ties it to the adapter.
- Only macOS is implemented (`kern.proc` and `kern.procargs2`). On every other
  platform each call fails with `ErrUnsupported`, naming the operation.
