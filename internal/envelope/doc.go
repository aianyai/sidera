// Package envelope defines the message envelope that travels between nodes and
// the two signed records that report on a delivery: the status report and the
// control reply. It also holds the one rule by which a sender's node applies a
// control reply (HandleControlReply) and the one rule by which it applies a
// status report (Outbound.ApplyReport).
//
// The envelope is not a signed record: the sender is known from the connection
// and the node's own policy decides what to accept. The status report and the
// control reply are signed by the target node's network key, because they are
// the evidence on which a reply is allowed back.
//
// The package is pure: no I/O, no clock reads, no global state.
package envelope
