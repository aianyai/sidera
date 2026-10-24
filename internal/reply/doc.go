// Package reply decides whether a message that claims to answer one of this
// node's own messages is allowed back in, whatever the node's policy says
// about its sender. The verdict is the fact the policy reads as
// authz.Request.Reply.
//
// The decision looks only at the node's own record of the message it sent
// (envelope.Outbound), at the evidence the reply carries, and at the clock. It
// is pure: no I/O, no clock reads, no global state.
package reply
