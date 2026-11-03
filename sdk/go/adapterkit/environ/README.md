# adapterkit/environ

The environment an adapter gives a child process: the adapter's own, less what the
child must not inherit, plus what the adapter adds. Pure: no I/O, and it imports the
standard library only, so a package that must stay pure can use it.

## Entry points

| Name | Purpose |
| ---- | ------- |
| `Compose(base, Spec)` | `base` changed as the `Spec` says: invalid pairs left out, then `Drop`, then `Set`, then `PathPrefix`. Always a new slice, never nil. |
| `Spec` | `Drop` (which variables to leave out), `Set` (variables that replace or are appended) and `PathPrefix` (a directory put in front of `PATH`). |

## Rules

- **Names are compared as the platform does.** On Windows without regard to case,
  and `Drop` receives the name in upper case, so a list of upper-case names works
  on every platform. Elsewhere names are compared as written. A leading `=` is part
  of a name on Windows (`=C:` keeps the working directory of a drive) and makes an
  empty name elsewhere.
- **A pair the child could not read is left out first**: not valid UTF-8, no `=`, or
  an empty name. `Drop` never sees it.
- **A name appears once.** One that `base` holds twice keeps its last value at its
  last place, as `os/exec` does.
- **What `Set` gives wins.** It is applied after `Drop`, so a variable `Set` gives is
  never dropped; it replaces a pair of the same name in place and is otherwise
  appended, in name order.
- **`PathPrefix` is applied last**, to the `PATH` the environment holds (on Windows
  under whatever spelling, such as `Path`), including one `Set` gave. `PATH` becomes
  just the prefix when it is unset or empty.
- **What to drop is the caller's.** The package knows no variable by name; each
  adapter says which ones its child must not inherit.
