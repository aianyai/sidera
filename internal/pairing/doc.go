// Package pairing is the protocol by which two nodes establish mutual trust,
// and the short verification code the two owners compare while they do.
//
// It is a contract package: pure rules and state machines, no I/O, no clock
// reads, no goroutines, no libp2p. A node's connection layer feeds it the
// facts of a connection and the frames of a stream, performs the actions it
// returns in order, and persists the records it hands out.
//
// The package has four layers, each built only on the one before:
//
//   - the transcript (transcript.go, code.go): what both sides bind, the
//     commitment, the pairing identifier, the verification code and the
//     credential nonce;
//   - the messages (message.go): the typed, size-bounded frames of the online
//     segment and of the result-fetch segment;
//   - the limits (limits.go, expectation.go): the reveal budget, the
//     verification-failure pause and the expectations, as persisted state;
//   - the state machines (state.go, target.go, initiator.go, command.go):
//     the target side, the initiator side and the administrative operations
//     that confirm, reject or pre-confirm a pairing.
package pairing
