# state

What the adapter has done, on disk: `Data` (cards, endpoints, remote sessions,
requests made of inbound messages, the answers to them, processed inbound messages) and `Store` (`Open`,
`View`, `Update`).

- `Update` writes before it returns. Call it before the effect the change describes,
  never after.
- `View` and `Update` work on copies. A pointer into `Data` is not kept past the
  function it was given to.
- The file is replaced by temporary file and rename, owner only, without a flush to
  disk; that is deliberate (see the reference).
- A card changes phase only through its methods (`BeginAttempt`, `Sent`, `Block`,
  `Finish`), which keep the attempt number, the message id and the block counts
  together. `Unblock` takes a blocked card, or one only intended to be claimed (phase
  `claiming`), to waiting its turn.
- How long a processed message is remembered is the SDK's
  (`adapterproto.ReceivedRedeliveryWindow`), applied in `MarkProcessed`; it is not a
  setting.
- A reply record keeps what the node's notices are matched against: the message id of the
  answer, the request card it is for (`Answered` says when that request is dealt with:
  the answer is sent, or the card is over), when it was sent and whether its failure was
  reported. `Reply.Watched` says whether a notice still looks at it (within
  `adapterproto.SentKeyWindow` of the send); the window is the SDK's, not a setting.
- A card keeps where it came from (`Origin`, written once before the claim, because the
  board later holds a subscription the adapter made) and the id of the reply being
  written to it (`ReplyID`, written before the completion, so that a redelivered reply is
  recognised). An endpoint keeps when a card of it last ended for a failed delivery and
  when a notice about it was last handled (`FailedAt`, `NoticedAt`).
- This package decides nothing else; `lane` does.
