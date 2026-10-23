// Package timing holds the time bounds that every node must apply identically.
//
// A bound lives here when two nodes compare a timestamp against it and a
// disagreement about the result would be a protocol fork. Contract packages
// import it instead of declaring their own copy.
package timing

import "time"

// ClockTolerance is the bound on clock skew between two nodes. Every
// comparison of a timestamp made on another node against the local clock allows
// this much difference, so two nodes never disagree about which side of a
// deadline a timestamp falls.
const ClockTolerance = 2 * time.Minute
