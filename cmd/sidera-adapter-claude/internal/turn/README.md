# turn

The state machine of the events one turn process gives rise to, as a pure
function. Read [../../README.md](../../README.md) and
[the reference](../../../../docs/references/adapter-claude/README.md).

`State.Step(Observation)` returns the new state and the notifications to send, in
order (`prompt.persisted`, `turn.started`, `turn.ended`). `State` is what the
session's checkpoint stores for the last turn, as JSON, so a restarted adapter
picks the turn up from it.

## Rules

- A Sidera turn is the life of one turn process.
- `turn.started` is sent when the prompt is first seen in the session record,
  never when the process starts: it carries the attempt as proof of delivery.
- A process that ends before that reports nothing (`Abandoned`).
- `turn.ended` is sent once, when the process is gone and the turn had started;
  its final is `completed` if the last result of the output stream is a success
  (even when the adapter interrupted the process: the turn finished first),
  otherwise `interrupted` if the adapter interrupted it, otherwise `failed` (an
  error or no result).
- Observe the process before the record: a process seen as gone has written
  everything it will.
- Found again after a restart, a turn begun but not ended and gone is ended, and a
  start that was lost may be sent again. The daemon tolerates both.
