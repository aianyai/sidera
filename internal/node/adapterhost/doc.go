// Package adapterhost supervises the adapter processes of the node and carries
// the adapter protocol between them and the engine.
//
// One connection is one adapter instance: the host starts the program, speaks
// the protocol over its standard input and output with sdk/go/peer, takes the
// adapter on (handshake, admission, adapter.start, adapter.probe), forwards what
// the adapter says to the engine, calls the adapter on the engine's behalf, and
// watches it with pings. An adapter that stops answering, or whose connection
// ends, is reported as gone and started again after a backoff; two adapter
// processes of one instance never control sessions at once, because the adapter
// takes the instance lock before it answers initialize.
//
// The package sits below the engine: it defines [Engine], the few things it asks
// of it, and the engine defines the interface it asks of the host, so that
// neither imports the other for its behaviour. See README.md for the rules and
// docs/references/adapter-host for the reasons.
package adapterhost
