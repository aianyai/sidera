// Package fakectl is the control channel between the test fixture and the fake
// processes it starts.
//
// In the binary layer the fake adapter and the fake runtime are separate
// processes. The fixture has to give them the same test clock the daemon runs on,
// tell them which script step is running, turn their knobs and hear what they
// report (for instance that an adapter stopped at a named pause point). This
// package carries those few messages over a loopback TCP connection, as
// newline-delimited JSON-RPC on the SDK's peer, so a fake process needs nothing
// but the standard library and the SDK.
//
// The fixture runs a Server; a fake process dials it with Dial, says hello, and
// registers handlers for what it accepts. A fake process that loses the
// connection ends itself, so no test leaves an orphan behind. A Server can also
// serve requests of its own (Handle), which is how the fake runtime process
// answers the adapter that created its session.
package fakectl
