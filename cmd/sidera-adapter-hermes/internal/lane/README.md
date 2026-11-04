# lane

The decisions of the lane, as functions over the `state` records and a time. None
does I/O. The caller records the transition with `state.Store.Update`, then acts.

| Entry point | Decides |
| ----------- | ------- |
| `EndpointKey`, `KindOf` | The conversation endpoint of a card's source; the kind of an endpoint from its key. |
| `PlaceEndpoint`, `IdleEndpoints` | Which endpoint a card sends from under the cap; which to retire for idleness. |
| `MatchReply` | Which card a received message answers (waiting, or blocked for no reply or a transient failure, or finished by an earlier hand-out of this very message), or to read pending creations first. |
| `ShouldQueue`, `NextQueued` | Whether a send waits behind another card on the same endpoint and peer; who goes next. |
| `IdempotencyKey`, `OnReady`, `OnReplyCard`, `AttemptExpired` | The key of an attempt; what to do with a known card, or the record of a reply card, that is ready again; the one rule for whether an attempt may still be sent again. |
| `OnFailure`, `BlockKindOf`, `NoReplyDue` | Block or complete a card (or a reply card) that cannot go on, from its blocks so far; the Hermes block kind of a category; whether the wait has run out. |
| `RouteFollowUp` | Where a follow-up that names no alias goes. |
| `SessionsToClose` | Which remote sessions to ask to close now. |
| `ParentFor` | The parent of an inbound continuation's request card; none for an endpoint that is not a conversation. |

Rules: `Reason` and `state.Category` are this adapter's own vocabulary and say nothing
about how the board names a block; `BlockKindOf` is the only place that maps one to
the other (invalid card to `capability`, transient to `transient`, no reply to
`needs_input`). The three kinds stay distinct because Hermes sends a card to triage on
the second block of one kind. A card that awaits a reply (`state.Card.AwaitsReply`)
is matched to the reply even when blocked, and is completed from the blocked state
as it stands, with no unblock or new claim before; a blocked card does not hold its
peer for `ShouldQueue` and `NextQueued`. Cards are passed in claim order (`Data.CardList`).
`AttemptExpired` takes the idempotency window from the SDK (`adapterproto.SentKeyWindow`); it
holds only while the adapter's sends name no `accept_before_s`.
A rule here that changes what is sent, or how often, updates the reference.
