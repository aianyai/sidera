// Package peer is a symmetric JSON-RPC 2.0 peer over a pair of byte streams.
//
// Messages are single-line JSON objects terminated by "\n". Either side may
// send requests and notifications; both register handlers for what the other
// side may call. The package knows nothing about any particular protocol:
// method names, parameter schemas and the meaning of results belong to the
// layer above.
//
// The peer is built for counterparts that are buggy or hostile. Every queue is
// bounded, a message longer than Params.MaxLineBytes ends the connection, a
// request that arrives while the request queue is full is refused before its
// parameters are looked at, and a write that does not finish within
// Params.WriteTimeout closes the transport. See the package README for the
// rules in full.
package peer
