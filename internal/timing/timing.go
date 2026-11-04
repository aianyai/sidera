// Package timing holds the time bounds that every node must apply identically.
//
// A bound lives here when two nodes compare a timestamp against it and a
// disagreement about the result would be a protocol fork. Packages import it
// instead of declaring their own copy.
package timing

import "github.com/aianyai/sidera/sdk/go/adapterproto"

// ClockTolerance is the bound on clock skew between two nodes. Wherever a
// comparison across nodes must allow for clock skew, this value is used and no
// other, so two nodes never disagree about which side of a deadline a timestamp
// falls. Validity bounds of certificates, credentials and reachability proofs
// are compared without it: the tolerance only moves a window of days or hours
// by two minutes, and a failure there is explicit, not a silent disagreement.
//
// The value is defined in package adapterproto, because an adapter that keeps
// its own records needs it to state how long they must be held.
const ClockTolerance = adapterproto.ClockTolerance
