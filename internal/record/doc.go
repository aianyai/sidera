// Package record defines the signed-record envelope that every signed
// statement in Sidera uses, its strict decoding rules, and the two records
// that bind keys to identifiers: the network-key certificate and the device
// certificate.
//
// A record is a deterministic CBOR map {1: type, 2: payload, 3: signatures}.
// The payload is a byte string whose exact bytes are signed and hashed, so two
// parties that never exchange anything but the payload agree on the digest.
// Each record type is described by a Descriptor exported by the package that
// defines the type; there is no registry. See README.md for the rules.
package record
