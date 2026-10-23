# Timing

Time bounds shared by every contract package. At present one value:
`ClockTolerance`.

## What

`timing.ClockTolerance` is 2 minutes: the largest difference between two nodes'
clocks that a time comparison tolerates. Every place where a node judges a
timestamp produced on another node against its own clock adds or subtracts this
bound, and no other.

## Why

Nodes are independent machines whose clocks drift. A timestamp near a deadline
falls on one side of it for one node and on the other side for another unless
both use the same margin. A voucher accepted by one node and refused by its peer,
or a delivery deadline that one target honors and the sender has already given
up on, is a protocol fork that no retry resolves.

- **One bound, one home.** Each package once held its own copy. Copies that
  start equal drift apart when one is edited, and no test in any single package
  notices. The value now has a single definition that every user imports.
- **A constant, not a parameter.** A per-call or per-node tolerance would let two
  nodes judge the same timestamp differently. The offline-directive check had a
  tolerance parameter that no caller ever varied; it was removed for that reason.
- **2 minutes.** Long enough to absorb ordinary clock error on machines that
  synchronize with network time, short enough that it adds little to the
  windows it widens (the voucher grace floor, the delivery report-by time).

## Where

| Use | Code |
| --- | ---- |
| A voucher may be stamped at most this far ahead of the receiver's clock; part of the grace-period floor | `internal/trust` (`Vouchers.Accept`, `Evaluate`, `MinGracePeriod`) |
| A delivery deadline may exceed the cap by this much; the expiry judgement and `report_by` add it | `internal/delivery` (`CheckIntake`, `ReportByOffset`, expiry in `timing.go`) |
| An offline directive's signing time may be this far ahead of now and this far before the node lost contact | `internal/admin` (`AcceptDirective`) |

The value itself: `internal/timing/timing.go`. Test: `TestClockTolerance`.
