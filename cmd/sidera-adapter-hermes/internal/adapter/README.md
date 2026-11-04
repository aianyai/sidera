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
| `Flows`, `Handlers`, `MessageHandler`, `CardHandler`, `Verdict`, `LogOnly` | The seam the flows plug into. `LogOnly` is the stand-in that logs and acknowledges nothing. |

## Files

| File | Holds |
| ---- | ----- |
| `app.go` | `App`; the capability check, `Start`, `Probe`, `Shutdown`. |
| `endpoints.go` | The instance endpoint, registration on first use, hiding, adoption of the endpoints the daemon lists, idle retirement. |
| `receive.go` | The receive loop. |
| `poll.go` | The board poll loop. |
| `gate.go` | The check that Hermes can be used, and the availability report. |
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
- The timings are fields (`timing`) so tests run in milliseconds; they are not settings.
- Tests drive the real `App` over pipes against a stub daemon (`harness_test.go`) and
  use `kanban/kanbantest` as `hermes`.
