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
| `LaneFlows`, `Inbound` | The lane's flows (delegating cards, writing replies, timers) and the seam of the inbound half: `HandleInbound(ctx, Message) (Verdict, error)` receives every message the delegation flows do not own, `HandleReplyCard(ctx, kanban.Card)` every ready card whose mode is `reply`. |

## Files

| File | Holds |
| ---- | ----- |
| `app.go` | `App`; the capability check, `Start`, `Probe`, `Shutdown`. |
| `endpoints.go` | The instance endpoint, registration on first use, hiding, adoption of the endpoints the daemon lists, idle retirement. |
| `receive.go` | The receive loop. |
| `poll.go` | The board poll loop. |
| `gate.go` | The check that Hermes can be used, and the availability report. |
| `timers.go` | The timer loop: recovery once, then a tick. |
| `laneflows.go`, `delegate.go`, `reply.go`, `upkeep.go` | The lane's flows: shared plumbing and the `Inbound` seam; cards (plan, claim, send, settle, fail); replies; recovery, ticks and closing sessions. |
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
  round of both loops). A read that is not followed by an acknowledgement uses up
  one of the message's leases.
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
  metadata (`completedBy`).
- **A card is claimed after everything that only reads or can be repeated** (`take`).
- **Remote text is never in a summary, a comment or a block reason**; the summary's first
  line is what Hermes wakes the creator with. Block reasons that quote a card are cut
  (`clip`).
- **The inbound half owns every message that matches no card the lane can complete**,
  and every ready card whose mode is `reply`; the delegation flows never claim those.
- The timings are fields (`timing`) so tests run in milliseconds; they are not settings.
- Tests drive the real `App` over pipes against a stub daemon (`harness_test.go`) and
  use `kanban/kanbantest` as `hermes`.
