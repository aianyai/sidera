# Timing

Time bounds that every node applies identically, imported by the contract
packages and by the daemon's runtime clock package. At present one value:
`ClockTolerance`.

## What

`timing.ClockTolerance` is 2 minutes: the largest difference between two nodes'
clocks that a time comparison tolerates. Wherever a comparison across nodes must
allow for clock skew, this value is used and no other. Validity bounds of
certificates, credentials and reachability proofs are compared without it: two
minutes only moves a 30-day or 2-hour window, and a failure there is explicit
(the certificate or proof is refused), not a silent disagreement about a
deadline.

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
- **The same bound for a set-back wall clock.** Peers already accept timestamps
  up to `ClockTolerance` from their own clock, so a set-back within it (including
  NTP stepping a drifted clock) yields signed times and deadline judgments
  indistinguishable from skew peers already tolerate, and pausing for it would
  only cost availability. A larger set-back would let the node sign or judge
  times earlier than what it already issued, beyond any peer's tolerance. Using
  the same constant makes the threshold exactly the peers' tolerance.
- **2 minutes.** Long enough to absorb ordinary clock error on machines that
  synchronize with network time, short enough that it adds little to the
  windows it widens (the voucher grace floor, the delivery report-by time).

## Where

| Use | Code |
| --- | ---- |
| A voucher may be stamped at most this far ahead of the receiver's clock; part of the grace-period floor | `internal/trust` (`Vouchers.Accept`, `Evaluate`, `MinGracePeriod`) |
| A delivery deadline may exceed the cap by this much; the expiry judgement and `report_by` add it | `internal/delivery` (`Intake`, `ReportByOffset`, `ExpiryJudgeableAfter`) |
| An offline directive's signing time may be this far ahead of now and this far before the node lost contact | `internal/admin` (`AcceptDirective`) |
| A certificate is issued valid from this long before its issue time, because validity is compared without tolerance and a higher serial replaces the old certificate at once | `internal/record` (`IssueNetworkKeyCertificate`, `IssueDeviceCertificate`) |
| A reading of the wall clock may be this far behind the persisted high water mark before the node treats the clock as set back; the boundary is inclusive | `internal/node/clock` (`CheckWall`, `HighWater.Observe`; table `TestCheckWallJudgmentTable`) |

The value itself: `internal/timing/timing.go`. Test: `TestClockTolerance`.
