# datahome

Which directory is the node's data directory when the owner did not name one. Read
[../README.md](../README.md) first for the conventions every contract package
follows, and
[../../docs/references/command-line/README.md](../../docs/references/command-line/README.md)
for the rule as the owner sees it.

## Entry points

| Name | Purpose |
| ---- | ------- |
| `Resolve(explicit, Host)` | The absolute data directory: `explicit` (`--home`), then `SIDERA_HOME`, then the default of the platform. |
| `Host` | What resolution reads of the machine: the operating system, the environment, the user's home directory and a `Lstat`. The caller passes `runtime.GOOS`, `os.UserHomeDir` and `os.Lstat`, so the rules are tested on every platform. |
| `MovedError` | The refusal of the Windows default when the former one is the only one that exists; its message says how to move it. |
| `Env` | The name of the environment variable, `SIDERA_HOME`. |

## Invariants

- **One home.** The command line and the console bridge resolve the directory with
  this function and nothing else, so a client never resolves a directory other than
  the daemon's.
- **The default is `~/.sidera`, and on Windows `%LOCALAPPDATA%\Sidera`.** A directory
  made there with a protected access list does not take the entries that a
  sandbox's setup adds to the first-level folders of the user's profile.
- **Nothing is moved or made.** On Windows, a missing `%LOCALAPPDATA%\Sidera` with an
  existing `%USERPROFILE%\.sidera` is a `MovedError`; a node that started empty beside
  the one holding the identity is worse than one that does not start. The flag and
  `SIDERA_HOME` are not subject to it, and the new directory existing settles the
  question whatever the old one is.
- **A failure to look is not an absence.** An error from `Lstat` other than "does not
  exist" is returned.
