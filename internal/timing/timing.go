// Package timing holds the time bounds that every node must apply identically.
//
// A bound lives here when two nodes compare a timestamp against it and a
// disagreement about the result would be a protocol fork. Packages import it
// instead of declaring their own copy.
package timing

import "time"

// ClockTolerance is the bound on clock skew between two nodes. Wherever a
// comparison across nodes must allow for clock skew, this value is used and no
// other, so two nodes never disagree about which side of a deadline a timestamp
// falls. Validity bounds of certificates, credentials and reachability proofs
// are compared without it: the tolerance only moves a window of days or hours
// by two minutes, and a failure there is explicit, not a silent disagreement.
const ClockTolerance = 2 * time.Minute
