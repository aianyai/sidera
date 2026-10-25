# fakectl

The control channel between the test fixture and the fake processes it starts.
Read [`../README.md`](../README.md) first for where this package sits and the
rules every testkit package follows.

It imports the standard library and the SDK module (`sdk/go/peer`,
`sdk/go/strictjson`) and nothing else, so a fake process built on it is a program
a stranger could have written.

## Why it exists

In the binary layer the fake adapter and the fake runtime are separate processes.
Three things must reach them that a function call carries in process: the test
clock (their reported times must come from the clock the daemon runs on, or tests
about blocked deliveries depend on how two clocks happen to relate), the name of
the script step that is running (it is stamped on every ledger entry), and the
turning of their knobs. They also need to say something back: that an adapter has
stopped at a named pause point.

## Entry points

| Name | Purpose |
| ---- | ------- |
| `Listen(ctx)` → `Server` | The fixture's end: a listener on `127.0.0.1` and a free port. `Addr`, `Wait(ctx, id)`, `Conns`, `Broadcast`, `Handle`, `Close`. |
| `Conn` | One connected process: `Hello`, `SetClock`, `SetStep`, `Knob`, `NextReport`, `Notify`, `Call`, `Done`. |
| `Dial(ctx, addr)` → `Client` | A fake process's end. Register `HandleClock`, `HandleStep`, `HandleKnob` (and `Handle`, `HandleNotification` for requests of its own), then `Start(ctx, hello)`. `Report`, `Call`, `Notify`, `Done`, `Close`. |
| `Clock` | The clock of a fake process: `NewClock(ms)`, `Now()`, `SetMS`. |
| `DecodeParams(method, data)` | Strict decoding of the params of the five messages. |

## The messages

Newline-delimited JSON-RPC on the SDK's `peer`, over loopback TCP. TCP and not a
Unix socket: it is the same on all three platforms and has no path-length limit,
and nothing on the channel needs protecting.

| Method | Direction | Params | Meaning |
| ------ | --------- | ------ | ------- |
| `hello` | client to server | `role` (`adapter` or `runtime`), `id`, `pid` | The first request. Everything else is refused with `-32003` until it was answered. |
| `clock.set` | server to client | `to_ms` | Set the test clock to an absolute time; may go backwards. Answered once the client has set it. |
| `step.set` | server to client | `step` | The script step that is about to run. Answered once the client knows it. |
| `knob` | server to client | `name`, `args` (a non-empty object, or absent) | Turn a named knob. The names and arguments belong to the fake that has them; the answer is an object. |
| `report` | client to server, notification | `name`, `detail?` | Something happened in the process, for instance a pause point was reached. |

A server can serve requests of its own with `Handle`; the fake runtime process
uses this to answer the adapter that created its session and the fixture that
drives its turns, on one listener, and each handler is told which connection
asked.

## Rules

- **Strict.** Params are decoded with `strictjson`: unknown members, missing
  members, repeats and `null` are errors, and so are an unknown role, a name
  outside the vocabulary and a negative number. A value that decodes encodes and
  decodes again to the same value (`FuzzDecodeParams`).
- **Absolute time.** The clock is set to a time, never advanced by a step, so a
  log and a replay say what time it was, and a roll-back is expressible.
- **The fixture moves the daemon's clock first and the fakes' after it.** A time
  an adapter reports that is later than the daemon's now is treated as unknown by
  the daemon. `clock.set` is answered only after the client has set its clock,
  so the order can be kept by waiting for each answer.
- **A fake process ends when its control connection ends.** A fake that loses
  the fixture exits (it watches `Client.Done`), so no test leaves an orphan, on
  Windows least of all, where an orphan holds files open.
- **Ownership.** The server's accept loop and every connection's peer stop on
  `Close` or when the context given to `Listen` ends; the tests fail on a goroutine
  left behind.
- **No real time.** Nothing here sleeps or reads the clock; waiting is on
  channels and contexts.

## What the caller must guarantee

- Register every handler before `Start`: a request that arrives before its
  handler exists is answered `method not found`.
- Give `Wait` a context with a limit; a process that never starts would
  otherwise be waited for for ever.
- Do not log params of `knob` or messages built on this channel with bodies:
  nothing here carries a message body, and callers keep it that way.

## Tests

`TestGoldenParams`, `TestDecodeParamsRejects`, `FuzzDecodeParams`,
`TestHelloIdentifiesTheConnection`, `TestClockAndStepReachTheClientBeforeTheCallReturns`,
`TestKnobCallsTheClientsHandler`, `TestReportsReachTheServer`,
`TestRequestsBeforeHelloAreRefused`, `TestServerHandlersSeeTheConnectionTheyServe`,
`TestBroadcastReachesEveryConnectionOfARole`, `TestClientEndsWhenTheControlConnectionDrops`,
`TestCloseEndsEveryGoroutine`, `TestListenIsOnTheLoopbackOnly`.
