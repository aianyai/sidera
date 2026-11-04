# state

What the adapter has done, on disk: `Data` (cards, endpoints, remote sessions,
processed inbound messages) and `Store` (`Open`, `View`, `Update`).

- `Update` writes before it returns. Call it before the effect the change describes,
  never after.
- `View` and `Update` work on copies. A pointer into `Data` is not kept past the
  function it was given to.
- The file is replaced by temporary file and rename, owner only, without a flush to
  disk; that is deliberate (see the reference).
- A card changes phase only through its methods (`BeginAttempt`, `Sent`, `Block`,
  `Finish`), which keep the attempt number, the message id and the block counts
  together.
- This package decides nothing; `lane` does.
