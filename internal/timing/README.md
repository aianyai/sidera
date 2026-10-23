# timing

Time bounds that every node applies identically. Read
[../README.md](../README.md) first for the conventions every contract package
follows, and
[../../docs/references/timing/README.md](../../docs/references/timing/README.md)
for the reasoning.

## Entry points

| Name             | Purpose                                                                                      |
| ---------------- | -------------------------------------------------------------------------------------------- |
| `ClockTolerance` | The bound on clock skew between nodes (2 minutes) that every cross-node time comparison allows. |

## Invariants

- The value is a protocol constant, not a node setting: a node that chose its
  own tolerance would disagree with its peers about voucher freshness, delivery
  deadlines and offline-directive signing times.
- A package never declares its own copy and never takes the tolerance as a
  parameter. A new cross-node time comparison imports `timing.ClockTolerance`.
- Changing the value changes behavior in every package that imports it; see the
  reference document for the list.
