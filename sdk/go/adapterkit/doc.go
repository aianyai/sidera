// Package adapterkit is the part of an adapter that every adapter has the same
// way: the serve loop of the adapter protocol. It negotiates the version, takes
// the instance lock before it answers initialize, lets through only what the
// protocol lets through before initialize and after shutdown, decodes every
// request strictly, validates every result before it is sent, and hands what is
// left to the application, an implementation of Application.
//
// It adds no message, no rule and no state to the protocol; the reference in
// docs/references/adapter-protocol is the specification and this package only
// applies it once, so that adapters do not each copy it.
package adapterkit
