# record

How Claude Code keeps a session on disk, as pure functions over bytes the caller
read. Read [../../README.md](../../README.md) and
[the reference](../../../../docs/references/adapter-claude/README.md).

## Entry points

| Name | Does |
| ---- | ---- |
| `Sanitize(realCwd)`, `Dir(projectsDir, realCwd)` | The record directory of a working directory: the projects directory the runtime reports plus the working directory in normal form C with every character outside `[A-Za-z0-9]` replaced by `-`. A name over `MaxDirName` is refused (`ErrDirNameTooLong`). |
| `File(recordDir, session)` | `<recordDir>/<session>.jsonl`. |
| `Index` (`NewIndex`, `Watch`, `Feed`, `Write`, `Offset`, `SessionSeen`, `UserSeen`) | Finds, in a session file fed in order and in any chunking, whether the file names the session and whether a user record with a watched uuid is there. |
| `LastResult(r)` | The outcome (`Succeeded`, `Errored`, `NoResult`) of the last `result` line of a turn process's output stream. |

## Rules

- The caller resolves symbolic links before it derives the directory: the runtime
  names the directory by the real path.
- The package does not reproduce the runtime's shortening of long names: it
  refuses them.
- Only a line of type `user` with the watched uuid is the prompt. Every line of the
  raw file is looked at, not only the current branch of the conversation.
- A last line without its newline is not judged: it may be half written.
- The last `result` decides, not the first: a process can write several.
