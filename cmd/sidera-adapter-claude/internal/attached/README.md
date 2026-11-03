# attached

How a Claude Code session that a person runs shows itself to the adapter, as pure functions and a
state machine over what the caller read. Read [../../README.md](../../README.md) and
[the reference](../../../../docs/references/adapter-claude/README.md) ("Attached sessions") for where
it sits and the reasons.

This package and `record.Index` are the only places that rely on forms Claude Code does not document:
the registry file, the socket line and the transcript's `turn_duration` record. A form that stops
matching is an error here (`ErrRegistryForm`), never a guess.

## Entry points

| Name | Does |
| ---- | ---- |
| `ParseRegistry(data)`, `RegistryFile(dir, pid)` | The registry entry of a running process (`sessions/<pid>.json`): session id, working directory, kind, process start, status and when it changed. |
| `RegistryDir(dir)`, `RegistryPID(name)` | The registry directory, and the process a file name of it names: only `<pid>.json` does; `<pid>.<hash>.key` and the like are no entry. |
| `Registry.SameProcess(startUnix, nativeStart)` | Whether a process of the table is the one the entry was written by: a number reused by another process is not. The entry's `procStart` is kept as written; macOS compares `ps -o lstart` text within two seconds, Linux (clock ticks) and Windows (FILETIME) compare the native start for equality. |
| `Status`, `Status.Active()` | `busy` and `waiting` are a turn running; `idle`, `shell` and a status not known are not. |
| `ParseWake(info)`, `Wake` | The three wake keys; a key that is not valid is named by its key. `Wake` prints as redacted, whatever the verb or log handler. |
| `Wake.Problem(goos)` | Why the wake cannot deliver on that operating system, empty when it can: no socket, or no token on Windows, where Claude Code drops an unauthenticated message without a word. |
| `Wake.Lines(uuid, text)` | What is written to the inbox socket: the authentication line when there is a token, then the user message carrying the attempt as its uuid. |
| `Follower` (`Expect`, `Forget`, `Step`, `End`, `Finish`, `Adopt`, `Open`, `Expected`) | The notifications (`prompt.persisted`, `turn.started`, `turn.ended`) that one look at the registry and the transcript calls for. |

## Rules

- A turn the registry or the transcript shows has no attempt; a turn begins with an attempt only when the user record of
  a prompt the adapter wrote is seen. A prompt that arrives in a running turn is persisted and starts
  no turn.
- The transcript decides the end of a turn; only without it does the registry, after `IdleGrace`,
  and then the end is `interrupted`.
- A status that belongs to a turn already ended is not a new turn: the follower remembers the latest
  status it saw during the turn.
- A user record of origin `human`, `peer` or `task-notification` begins a turn when none runs, for a
  session that stays busy from one turn to the next and shows no new status; in a running turn it
  begins none.
- The registry that goes into a look (`Observation`) is read after the transcript, so that it is never
  older than the markers; the caller may read it once more earlier, for the session id.
- `Finish` tells the markers a caller still holds before it ends the turn, for a session that was
  cleared or is gone.
- Nothing secret is returned in an error or printed.
- `Wake` redacts itself only when it is printed as a value of its own type. In an unexported field of
  a struct that is printed with `%v` or `%+v`, `fmt` does not call its methods and prints the
  secrets: never print such a struct (log its fields by name, leaving the wake out).
