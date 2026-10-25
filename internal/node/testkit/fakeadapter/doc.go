// Package fakeadapter is an adapter that speaks the adapter protocol exactly as a
// third party would write it, and does only what a test tells it to.
//
// It imports the standard library, the SDK module and the other packages of the
// third-party side of the testkit, and nothing else of the repository: that is
// what makes a test against it a test of the protocol boundary and not of a
// private agreement between two internal packages. A test enforces it.
//
// Serve runs the adapter side of the protocol over a reader and a writer: in a
// test process over a pair of io.Pipe, in a binary over standard input and
// output. It declares a runtime and direct endpoints (ManifestOptions says what
// else), answers every method the daemon may call, and drives a native runtime
// through a fakeruntime.Launcher. Everything it does that has an effect is
// written to the ledger before it is confirmed. Its Knobs decide what a request
// gets (a created session, a refusal, an unknown outcome, a hold), stop it at
// named pause points, make it hang or disconnect, and let a test act as the chat
// bridge of a direct endpoint.
//
// Every time it reports comes from the shared test clock it is given.
package fakeadapter
