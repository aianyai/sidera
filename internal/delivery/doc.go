// Package delivery holds the rules that decide what happens to one message
// delivery between a sender endpoint and one recipient endpoint: its states
// and legal transitions, its deadlines and retention periods, the lease and
// acknowledgement protocol of the pull interface, deduplication, and which
// transitions the sender must be told about.
//
// The package is pure: no I/O, no clock reads (callers pass now), no global
// state. A Record is a value; every method returns the updated copy together
// with a machine-readable Outcome. Persisting the copy is the caller's job and
// must be atomic with the other effects the caller attaches to the outcome.
//
// Facts that other packages decide are inputs here and are documented as
// already verified by the caller: the authorization verdict (Authorization),
// whether an acknowledgement carries the owning endpoint's token, the number of
// chunks a message was split into, and when handing a prompt to a session has
// been failing.
package delivery
