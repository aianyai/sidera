# clocktest

The fake clock for tests. It is imported by test code, test fixtures, and the
daemon's test clock in builds with the `testclock` tag; production builds
without the tag never import it. See [`../README.md`](../README.md) for the
interface it implements.

## Entry points

| Name                     | Purpose                                                                          |
| ------------------------ | -------------------------------------------------------------------------------- |
| `NewFake(start)`         | A fake clock reading `start`; panics on the zero time. `start` is converted to UTC: a start from `time.UnixMilli` is local time and `NewFake` converts it. |
| `Fake.Now`               | The current fake time: UTC, no monotonic reading, like `clock.System().Now`.     |
| `Fake.NewTimer(d)`       | A timer due at `Now()+d`; with `d <= 0` it has fired when the call returns.      |
| `Fake.Advance(d)`        | Moves forward by `d >= 0` and fires every timer due at or before the new time.   |
| `Fake.Set(t)`            | Moves to the absolute time `t`; forward behaves like `Advance`, backward fires nothing and keeps each timer's remaining duration. |
| `Fake.Timers()`          | The number of armed timers, so a test can wait until a waiter has armed its deadline. |

## Invariants

- **Time moves only when the test says so.** Nothing sleeps; a deadline is
  crossed by `Advance` or `Set`.
- **Firing is synchronous and ordered.** When `Advance` or a forward `Set`
  returns, every due timer has delivered its value, earliest deadline first and
  equal deadlines in the order the timers were armed (a `Reset` arms again). Each
  timer has its own channel, so receivers cannot observe that order: code and
  tests must not rely on which ready timer is read first. What the code woken by
  those values then does runs on its own goroutines, so a test that drives a
  daemon waits for it to settle after advancing.
- **A timer delivers its deadline** (for `d <= 0`, the current time), not the
  time the clock was moved to, so a large jump still tells each timer apart.
- **`Set` may go backward and then fires nothing.** Every armed timer keeps its
  remaining duration, because timers measure elapsed time and a wall-clock
  set-back does not move them, as with `clock.System()`. This is how a test sets
  the wall clock behind the high water mark. The fake has one time axis, so
  moving forward is elapsed time: a round trip (set back, then set forward to the
  original instant) fires the timers whose remaining duration has elapsed.
- **`Advance` with a negative duration panics**; going back is `Set`'s job.
- **Timer semantics match Go 1.23 timers**: `Stop` and `Reset` return `true` for
  a timer that is armed or whose due value was not received, and discard that
  value; they return `false` once the value was received or the timer was
  already stopped. `Reset` counts from the current fake time.
- **A fired timer's channel reports `len == 1` and `cap == 1`;** a real Go 1.23+
  timer reports 0 and 0, so code must not use `len(C())`.
- **Safe for concurrent use**, including `Now` read while another goroutine
  advances. Run tests that share a fake with `-race`.

## Using one fake everywhere

One fake serves the code under test and the fake adapters and runtimes: pass
the method value `f.Now` as a `func() time.Time` to code that must not import
`clock` and only reports times, so every time in a scenario comes from the same
clock. Move the daemon's clock first and the fakes' after it, so a time an
adapter reports is never later than the daemon's now.

## Why not a library

A library such as `clockwork` or `k8s.io/utils/clock/testing` offers the same
`Advance`/`Set`/waiter-count abilities. The interface here has two methods and
the fake is about a hundred lines, so it fails the test "the problem is not
simple enough to solve in a few lines of our own". `testing/synctest` is not
used as the mechanism because it fakes all time inside a bubble, which cannot
reach other processes and would also fake the real clock that I/O timeouts need.
