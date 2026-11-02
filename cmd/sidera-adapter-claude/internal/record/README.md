# record

How Claude Code keeps a session on disk, as pure functions over bytes the caller
read. Read [../../README.md](../../README.md) and
[the reference](../../../../docs/references/adapter-claude/README.md).

## Entry points

| Name | Does |
| ---- | ---- |
| `Sanitize(realCwd)`, `Dir(projectsDir, realCwd)` | The record directory of a working directory: the projects directory the runtime reports plus the working directory in normal form C with every character outside `[A-Za-z0-9]` replaced by `-`. A name over `MaxDirName` is refused (`ErrDirNameTooLong`). |
| `File(recordDir, session)` | `<recordDir>/<session>.jsonl`. |
| `Index` (`NewIndex`, `Watch`, `Unwatch`, `Feed`, `Write`, `Offset`, `SessionSeen`, `UserSeen`, `Recognized`, `Track`, `Markers`) | Finds, in a session file fed in order and in any chunking, whether the file names the session and whether a user record with a watched uuid is there, and whether every user line carries a uuid and a session id. After `Track`, it also hands out, in file order, the watched prompts it finds, the user records of origin `human`, `peer` or `task-notification` that are not watched prompts (the messages that begin a turn) and the system `turn_duration` records that end a turn (`Marker`). `Unwatch` forgets a uuid, so that a long-lived index does not keep every one it was given. |
| `LastResult(r)` | The outcome (`Succeeded`, `Errored`, `NoResult`) of the last `result` line of a turn process's output stream. |

## Rules

- The caller resolves symbolic links before it derives the directory: the runtime
  names the directory by the real path.
- The package does not reproduce the runtime's shortening of long names: it
  refuses them.
- Only a line of type `user` with the watched uuid is the prompt. Every line of the
  raw file is looked at, not only the current branch of the conversation.
- A file with a user line that has no string `uuid` or `sessionId` is not `Recognized`: the
  lookup of a prompt in it cannot show the prompt absent. Lines that do not parse are skipped.
- A last line without its newline is not judged: it may be half written.
- The last `result` decides, not the first: a process can write several.
- Markers are collected only after `Track`, so a caller that joins a file in progress feeds the
  history first and tracks after it: the history is passed over, not replayed.
- Only a line of type `system` and subtype `turn_duration` ends a turn; a user line that quotes the
  word does not.
