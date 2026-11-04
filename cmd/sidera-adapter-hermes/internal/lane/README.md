# lane

The decisions of the lane, as functions over the `state` records and a time. None
does I/O. The caller records the transition with `state.Store.Update`, then acts.

| Entry point | Decides |
| ----------- | ------- |
| `EndpointKey`, `KindOf` | The conversation endpoint of a card's source; the kind of an endpoint from its key. |
| `PlaceEndpoint`, `IdleEndpoints` | Which endpoint a card sends from under the cap; which to retire for idleness. |
| `MatchReply` | Which waiting card a received message answers, or to read pending creations first. |
| `ShouldQueue`, `NextQueued` | Whether a send waits behind another card on the same endpoint and peer; who goes next. |
| `IdempotencyKey`, `OnReady` | The key of an attempt; what to do with a known card that is ready again. |
| `OnFailure`, `NoReplyDue` | Block or complete a card that cannot go on; whether the wait has run out. |
| `RouteFollowUp` | Where a follow-up that names no alias goes. |
| `SessionsToClose` | Which remote sessions to ask to close now. |
| `ParentFor` | The parent of an inbound continuation's request card. |

Rules: `Reason` and `state.Category` are this adapter's own vocabulary and say nothing
about how the board names a block. Cards are passed in claim order (`Data.CardList`).
A rule here that changes what is sent, or how often, updates the reference.
