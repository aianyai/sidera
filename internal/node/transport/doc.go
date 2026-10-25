// Package transport is the seam between the engine and whatever carries an
// envelope to the node it is for, and the loopback that carries it when that
// node is this one.
//
// The seam has two sides, each defined here so that neither the engine nor an
// implementation imports the other: a [Sender], which the engine calls to put an
// envelope on its way or to pull the signed reports of a delivery, and a
// [Handler], which an implementation calls to hand what arrived to the engine.
// A [Handler] is told where a request came from as an [Origin]. Only the
// loopback in this package can make a local origin; a transport of another
// package can make a remote one only. That is how a request is known to come
// from an endpoint of this node: the transport decides it, never the peer's own
// statement of who it is.
//
// [Loopback] is the single-node path: what leaves the sender's side arrives on
// the receiving side of the same node, through the same two methods a remote
// transport would use. It can be made to fail on purpose with [Knobs]: drop a
// receipt, make a node unreachable, hold the status reports of a message, delay
// every call, and replay a message with other content. The knobs are part of
// the package and need no build tag, so tests that run without tags use them.
// Each knob counts its hits so that a test can show the fault was reached.
//
// The package starts no goroutine: a call runs in the caller's goroutine and
// ends with its context. Time comes from the clock the [Knobs] were made with.
package transport
