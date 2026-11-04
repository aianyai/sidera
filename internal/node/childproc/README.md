# childproc

How the daemon starts a console program as its child so that it gets no window.
A runtime package of layer 1 of [`../README.md`](../README.md): it binds one
operating-system API, the creation flags of a started program, as `fsx` and
`clock` bind theirs. Read the [daemon](../../../docs/references/daemon/README.md)
reference ("Programs the daemon starts") for the reasoning. The package has one
function, no state and no I/O; it only sets fields of the `exec.Cmd` it is given.

## Entry points

| Name | What it is for |
| ---- | -------------- |
| `Windowless(cmd)` | Call it on a command before it is started. On Windows the program gets a console of its own that has no window (`CREATE_NO_WINDOW`), which what it starts shares; elsewhere it does nothing. |

## Rules

- **It is for every console program the daemon starts as its child**, the
  adapters and the restart check. A daemon can run without a console (restarted
  by `sidera update`, run as a service, started through WMI), and a process
  without a console gives each console program it starts a new visible console,
  which Windows Terminal turns into a window. The one exception is the daemon that
  a restart starts in its place: `sdk/go/adapterkit/process` starts it detached,
  on purpose. The rule is here once, so that a new call site calls `Windowless`
  and does not set creation flags of its own.
- **Call it after anything that assigns `cmd.SysProcAttr`.** An assignment after the
  call replaces the whole struct and loses the flag.
- **Other creation flags and fields are kept.** The flag is added to
  `SysProcAttr.CreationFlags`; a command that already has a `SysProcAttr` keeps the
  rest of it, and one without gets an empty one.

`windowless_windows_test.go` starts the test binary with no console, as the
daemon a restart starts has none, and has that process start a child with
`Windowless` and the child start a grandchild with a plain `exec.Command`. Both
report whether they have a console and a window for it.
