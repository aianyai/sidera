// Package preauth holds the pre-authentication contract: what a freshly
// connected peer may do before and after the certificate exchange, the
// exchange's message format, the decision that turns a completed exchange into
// a connection state, and the accounting model that bounds how many
// connections may sit in the unauthenticated and non-member states.
//
// Everything here is pure data and decisions. The network layer owns the
// libp2p runtime, the clock and the timers; it feeds this package the facts and
// applies the answers. See README.md for the rules and for what the network
// layer must guarantee.
package preauth
