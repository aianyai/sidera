// Package store owns state/, the runtime data of a node: the durable records of
// deliveries, their bodies, the archive of finished ones, the facts kept for
// later decisions, and the quarantine of damaged files.
//
// Every write is a transaction of the txn package, declared in Transactions
// and made through a method of Store or, for the transactions another package
// opens, through a write function that takes the *txn.Tx. Every read goes
// through the txn.Writer's Reader. The package keeps no index: Scan returns
// every decoded record, and the engine builds its view from that.
//
// The package decides nothing about deliveries. It encodes and decodes the
// records, orders the writes of each transaction, and offers the predicates of
// the recovery rules it owns as pure functions of a Scan, so that the engine
// executes them and a test can evaluate them on a copy of the disk. See
// README.md for the rules and docs/references/storage for the reasons.
package store
