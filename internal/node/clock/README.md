# clock

The time source of the daemon's runtime packages, and the judgment of whether
the wall clock has been set back. Runtime package: it may read the process
clock, which no contract package may. See
[`../../timing/README.md`](../../timing/README.md) for the tolerance used here.

## Entry points

| Name                                   | Purpose                                                                                           |
| -------------------------------------- | ------------------------------------------------------------------------------------------------- |
| `Clock`, `Timer`                       | The injected time source: `Now` and one single-shot timer.                                        |
| `System()`                             | The real clock.                                                                                   |
| `CheckWall(now, highWater)`            | Pure judgment of a reading against the persisted high water mark: a `WallVerdict` and a `WallReason`. |
| `HighWater`, `NewHighWater`            | The in-memory mark: `Observe`, `Value`, `Reset`.                                                  |
| [`clocktest.Fake`](clocktest/README.md) | The fake clock that tests inject.                                                                 |

## Invariants

- **In the daemon's runtime packages (`internal/node/...`), only this package
  reads the process clock.** Everything else takes a `Clock` (or, for code that
  only reports times, a `func() time.Time`) from its caller. Protocol and state deadlines use the injected clock; I/O
  timeouts, ping, restart and retry backoff and shutdown limits name `System()`
  explicitly.
- **`Now` is UTC with no monotonic reading.** Two times that both carry a
  monotonic reading are compared by it, so a wall clock set back would never
  show in `now.Before(highWater)` within one process. `System().Now()` is
  `time.Now().UTC()`, which drops the reading.
- **Timers follow the standard library since Go 1.23.** After `Stop` or `Reset`
  returns, a value from the earlier setting is never received, and a value that
  is due but not yet received counts as pending: `Stop` and `Reset` report
  `true` for it. `System()` wraps `time.Timer`; the fake reproduces the same
  results.
- **A deadline is armed with `ResetAt`, never with `Reset(t.Sub(Now()))`.**
  `ResetAt(t)` reads the clock and arms the timer in one step, so the time that
  passes between a reading and the arming cannot stretch the wait. On the real
  clock that is a delay of microseconds; on the fake it is the distance a test
  moved the clock in between, which would push the deadline out by that distance
  and leave the timer silent. A `t` that is not after the clock's reading fires at
  once. `Reset(d)` is for a wait that is a duration by nature (a backoff).
- **Deliberately absent: `Sleep`, `AfterFunc`, `Ticker`.** A sleep cannot be
  cancelled; an `AfterFunc` callback runs on an arbitrary goroutine, which
  breaks the single-writer loop; periodic work is an entry in the scheduler,
  which needs one timer to sleep until its earliest deadline.
- **The wall clock can jump while a timer runs.** `System()` timers count
  monotonic time, so after a forward jump an entry that is due by the wall clock
  waits for its timer to expire. A loop that wakes always reads `Now` again and
  compares it with its schedule, so the lateness is bounded by the longest timer
  it sleeps on (about a minute for the periodic tasks), which is acceptable for
  minute-scale deadlines. After a set-back, `System()` timers still fire on
  schedule; the loop re-reads `Now`, sees that the wall-clock deadline is not yet
  reached, and the entry is delayed by the set-back. Within the tolerance that is
  at most about two minutes; beyond it the node is in rollback pause anyway.

## Wall-clock rollback

`CheckWall` compares a reading with the persisted high water mark:

| Situation                                           | Verdict          | Reason                  |
| --------------------------------------------------- | ---------------- | ----------------------- |
| No mark recorded (first start; zero `highWater`)    | `WallOK`         | `ReasonNoHighWater`     |
| `now` is at or after the mark                       | `WallOK`         | `ReasonNotBehind`       |
| `now` is behind by at most `timing.ClockTolerance`  | `WallOK`         | `ReasonWithinTolerance` |
| `now` is behind by more than `timing.ClockTolerance`| `WallRolledBack` | `ReasonBehindHighWater` |

- The boundary is inclusive: exactly `timing.ClockTolerance` behind is still
  accepted; one nanosecond more is a rollback.
- The tolerance is `timing.ClockTolerance` and never a parameter or a copy.
  Peers already accept timestamps up to `ClockTolerance` from their own clock, so
  a set-back within it (including NTP stepping a drifted clock) yields signed
  times and deadline judgments indistinguishable from skew peers already
  tolerate, and pausing for it would only cost availability. A larger set-back
  would let the node sign or judge times earlier than what it already issued,
  beyond any peer's tolerance. Using the same constant makes the threshold
  exactly the peers' tolerance.
- `HighWater.Observe` raises the mark only for a reading that is at or ahead of
  it. A reading behind the mark, within the tolerance or rolled back, never
  lowers it; this is why a clock set back cannot drag the persisted mark down
  and let the node sign in the past after a restart.
- `Observe` does not remember a rejected reading. Each call is judged against
  the same mark, so a clock that comes back within the tolerance is accepted
  again. Holding the node paused until the owner clears the rollback is the
  caller's state, not the mark's.
- `Reset(now)` accepts `now` as the new mark even when it is lower. It is what
  clearing a rollback does.
- The zero `WallVerdict` and `WallReason` mean unset; a zero time as the mark
  means nothing is recorded.

## What the caller must guarantee

- Pass UTC readings taken from a `Clock` (comparisons are by instant, so another
  location is judged correctly, but the convention keeps readings comparable).
- Own the `HighWater` from the single writer: it is not safe for concurrent
  use. Read the clock once per command, `Observe` it, and judge everything in
  that command by that one reading.
- Persist `Value()` periodically and construct the mark from the persisted value
  at start. Persistence is not done here: this package writes nothing.
- Reasons are for the caller to act on and to log; none of them is an error.

The reasoning for these rules is recorded here; the `daemon` reference topic,
created with the daemon, refers to this README.
