# timing

Time bounds that every node applies identically. Read
[../README.md](../README.md) first for the conventions every contract package
follows, and
[../../docs/references/timing/README.md](../../docs/references/timing/README.md)
for the reasoning.

## Entry points

| Name             | Purpose                                                                                      |
| ---------------- | -------------------------------------------------------------------------------------------- |
| `ClockTolerance` | The bound on clock skew between nodes (2 minutes), used wherever a comparison across nodes must allow for it. |

## Invariants

- The value is a protocol constant, not a node setting: a node that chose its
  own tolerance would disagree with its peers about voucher freshness, delivery
  deadlines and offline-directive signing times.
- A package never declares its own copy and never takes the tolerance as a
  parameter. A new comparison across nodes that must allow for clock skew
  imports `timing.ClockTolerance`.
- Validity bounds of certificates, credentials and reachability proofs are
  compared without the tolerance: it would only move a 30-day or 2-hour window
  by two minutes, and an expired or not-yet-valid one fails explicitly.
- Changing the value changes behavior in every package that imports it; see the
  reference document for the list.
