# adapter

The adapter program: `adapterkit.Application` for direct endpoints only, the two
loops, and what the flows build on. What the adapter is and why it behaves as it does
is in [`docs/references/adapter-hermes/`](../../../../docs/references/adapter-hermes/README.md).

## Entry points

| Name | Holds |
| ---- | ----- |
| `Main`, `Serve`, `Manifest` | The program's commands; the serve loop over the daemon's streams; the manifest of a package. |
| `App` | The `Application`: `Start` builds everything and starts the loops, `Probe`, `Shutdown`, `EndpointPending`. |
| `Env` | What the flows work with once started: configuration, the board client, the state store, the clock, and two methods: `Place` (the endpoint a card sends from) and `Send`. |
| `Flows`, `Handlers`, `MessageHandler`, `CardHandler`, `TimerHandler`, `Verdict`, `LogOnly` | The seam the flows plug into. `LogOnly` is the stand-in that logs and acknowledges nothing. |
| `LaneFlows`, `Inbound`, `NewInbound` | The lane's flows (delegating cards, writing replies, timers) and the seam of the inbound half: `HandleInbound(ctx, Message) (Verdict, error)` receives every message the delegation flows do not own, `HandleNotice(ctx, Message, settled)` the node's own notices after the delegation flows have settled the cards in flight, `HandleReplyCard(ctx, kanban.Card)` every ready card whose mode is `reply`. `NewInbound` builds the implementation. |

## Files

| File | Holds |
| ---- | ----- |
| `app.go` | `App`; the capability check, `Start` (it clears the attachment files an earlier run left), `Probe`, `Shutdown`. |
| `endpoints.go` | The instance endpoint, registration on first use, hiding, adoption of the endpoints the daemon lists, idle retirement. |
| `receive.go` | The receive loop. |
| `poll.go` | The board poll loop. |
| `gate.go` | The check that Hermes can be used and that its Kanban notifier is on, and the availability report. |
| `timers.go` | The timer loop: recovery once, then a tick. |
| `laneflows.go`, `delegate.go`, `reply.go`, `upkeep.go` | The lane's flows: shared plumbing and the `Inbound` seam; cards (plan, claim, send, settle, fail); replies; recovery, ticks and closing sessions. |
| `inbound.go`, `answer.go`, `notice.go` | The inbound half: request cards made of messages and continuations of conversations (`wake` is the one place a card is made to wake a conversation); reply cards sent back to the sender; the node's own notices. |
| `handlers.go`, `env.go` | The handler interfaces; `Env`, `Place`, `Send`. |
| `manifest.go`, `program.go` | The manifest and the declared settings; the commands. |
| `pause.go` | Backoff, `sleep` and the loop timings. |

## Invariants

- **Nothing reaches the daemon while `adapter.start` is being answered.** Every call
  to an `endpoint.*` method is made from a goroutine that `Start` leaves running: the
  daemon holds those requests until it is running.
- **A message is acknowledged only for a `Written` verdict, or when its id was already
  processed.** No other path calls `endpoint.ack` (`receiver.process`).
- **Nothing is read while Hermes is unusable** (`gate`, asked at the start of every
  round of the three loops). Unusable includes a release outside the supported range
  and a profile that makes the dispatcher start workers: the gate runs the client's
  whole probe at the start, after every failure and at intervals. A read that is not
  followed by an acknowledgement uses up one of the message's leases.
- **A notifier that is off degrades the report and pauses nothing** (`gate`): cards are
  still read and written, and the daemon is told, with the same condition and a detail
  that names the setting, that nothing wakes a conversation.
- **One full verification at a time** (`gate.verify`): the loops ask together and a check
  that finds one in progress takes its result, so the notifier is read once and an older
  reading never replaces a newer one.
- **Availability reports reach the daemon in the order of the changes**: the gate holds
  its lock from the decision to the end of the report.
- **A send never names a delivery deadline** (`Env.Send`, `Outgoing`). The idempotency
  window of the lane depends on it.
- **A record of an endpoint is written before the endpoint is used**, and removed
  before it is closed (`endpoints.ensure`, `endpoints.retire`).
- **The flows run one at a time** (`laneFlows.mu`): the card handler, the message handler
  and the timers move the same cards.
- **A record is written before the effect it describes, except a block and a
  completion**: the attempt number is written before the send (the idempotency key is
  built from it); a block or completion is made first and the record follows, with the
  card read again when Hermes refuses a repeat (`fail`, `reconcileBlock`), and a reply
  replayed after the completion is recognised by the message id in the completed run's
  metadata (`completedBy`), and by the id recorded on the card before the completion
  (`state.Card.ReplyID`) when recovery has finished the card in the record first.
- **A card is claimed after everything that only reads or can be repeated** (`take`),
  and after a record of the intent to claim it (`intendClaim`): a record in phase
  `claiming` is read at the start and on every tick (`reconcileClaiming`), so a crash
  between the claim and the rest of the record cannot leave a card running unseen.
- **Remote text is never in a summary, a result, a comment or a block reason**, and
  none of them names a path: on a completed card Hermes uploads the local files that
  the summary or the result names, and wakes the creator with the first line of the
  summary. A delegated card's reply is therefore always an attachment (`write`), and
  the result points to it by a name derived from the message id, which the sender
  chose. No reason quotes a card or a peer; `clip` only bounds their length.
- **The inbound half owns every message that matches no card the lane can complete**,
  and every ready card whose mode is `reply`; the delegation flows never claim those.
- **A message to the inbound half is `Written` only once its card is created,
  subscribed and completed** (`inbound.HandleInbound`), in that order: the completion
  wakes only the subscribers that exist by then. The first line of the summary and the
  title are the adapter's words; the sender's text is only in the body, in a fence it
  cannot close.
- **A notice of the node (`Message.Feedback`) is routed before anything else** and is
  never matched as a reply nor made a request (`laneFlows.HandleMessage`). It settles the
  cards in flight on its endpoint (`settleEndpoint`, through `settle`, the code the tick
  uses), then reports an answer that did not arrive, once (`reportLostAnswers`,
  `state.Reply.Reported`), and only when neither changed anything, and no card of the endpoint ended for a
  failed delivery in the last half hour that no notice has explained yet (`state.Endpoint.FailedAt`, `NoticedAt`, `noticeFollowsWithin`), is relayed as a card
  titled "Sidera channel notice" (`HandleNotice`). It is `Written` only once the cards
  it needed are written; a failure of Hermes that may pass is a `Retry`.
- **A reply card is not claimed** (`answer.go`): every step is resumed from its record
  (`state.Reply`, `lane.OnReplyCard`) when the poll lists the card again, so there is no claimed state for
  a timer to watch. It is sent from the endpoint that received the request, with the
  idempotency key of its persisted attempt; only a refusal with the reason
  `message_unknown` turns the reply into a plain message, once.
- **A blocked card is read every few minutes** (`reconcileBlocked`), delegated and reply
  cards alike: Hermes may complete or archive it without the adapter being told, and the
  card would hold its remote session, and the endpoint of the request it was to answer
  (`state.Reply.Answered`), until a restart.
- The timings are fields (`timing`) so tests run in milliseconds; they are not settings.
- Tests drive the real `App` over pipes against a stub daemon (`harness_test.go`) and
  use `kanban/kanbantest` as `hermes`.
