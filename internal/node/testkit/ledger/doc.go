// Package ledger is the append-only record of what the fakes did, and the oracle
// of the integration tests.
//
// A test never asks the daemon what happened; it asks the ledger. The fake
// adapter writes an entry for every effect it caused (a native session, a
// delivered prompt), the fake runtime for every turn and every message the model
// saw, the fake agent for every call and its result, and the fixture for every
// step it ran and every process it killed. Entries are lines of JSON in a fixed
// shape (see Entry); a file writer syncs every entry before it returns, so an
// entry that was written survives the death of the process that wrote it.
//
// The package imports only the standard library and the SDK module. It is on the
// third-party side of the testkit: it must stay independent of the code under
// test, including its file layer.
package ledger
