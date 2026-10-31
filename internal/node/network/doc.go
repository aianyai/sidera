// Package network is the node's connection to the other nodes: a libp2p host
// with its connection table, the pre-authentication gate in front of every
// stream, the certificate exchange that turns a connection into a member's,
// dialing by address book and certificate, and the client and server of the
// send, pull and directory protocols.
//
// The package applies the rules of internal/preauth and speaks the messages of
// internal/nodeproto; it decides nothing about trust. The engine hands it the
// facts of a decision (SetMembership) and receives what arrived (Node), and
// Router is the transport the engine calls. See README.md.
package network
