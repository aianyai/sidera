// Package adapterproto defines the messages of the adapter protocol: how the
// Sidera daemon and an adapter, a separate program that drives a native agent
// runtime or bridges a chat tool, talk to each other.
//
// The two sides exchange newline-delimited JSON-RPC 2.0 over the adapter's
// standard input and output, and either may send requests and notifications.
// This package knows nothing about framing or transport; the JSON-RPC rules of
// a frame, such as the request id, belong to package peer. It holds what is specific to the protocol:
//
//   - the version range and its negotiation (Range, Negotiate);
//   - the name, direction and kind of every method (Methods, Lookup) and the
//     type of its parameters and result;
//   - the enums, written as strings (EnumTables), the error codes (ErrorCodes)
//     and the identifier types;
//   - the capabilities an adapter declares, the native process identity and the
//     tool access a session is given;
//   - the values of the control signals and their replies (ControlReply and the
//     Control* enums);
//   - strict decoding and validation (Decode, Encode, and a Validate method on
//     every message type).
//
// The language-neutral JSON Schema under protocol/adapter/v1 is the source of
// truth for other languages. Everything Validate rejects that the schema can
// state, the schema rejects too, so a message that Decode accepts is one the
// schema accepts; the few rules the schema cannot state (a range with min above
// max, a state larger than MaxCheckpointBytes, ...) are reported with an error
// that matches ErrBeyondSchema. The conformance tests in protocol/ hold the two
// together.
//
// Identifiers are opaque strings here. The daemon hands out endpoint references
// and addresses and the adapter returns them unchanged, so the SDK does not
// parse or check their syntax; only the daemon does.
package adapterproto
