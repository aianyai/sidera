// Package nodeproto is the contract of the node-to-node request protocols: the
// protocol identifiers, the request and answer of each, the byte limit of
// each message and the framing rules. A message is deterministic CBOR with
// integer keys, decoded with the strict rules of package record, so two nodes
// that agree on a message agree on its bytes.
//
// The package carries records and envelopes as opaque bytes or as decoded
// shapes and never judges them: signatures, the envelope's own rules and
// authorization belong to the packages that own those rules. It opens no
// stream; the network layer applies the limits and the framing. See README.md.
package nodeproto
