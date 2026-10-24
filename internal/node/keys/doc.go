// Package keys owns the keys/ directory of the data directory: the identity
// and network private keys, the network-key certificate and its serial, the
// self-revocation statement, the wall-clock high water mark with the rollback
// pause flag, and the marker that says init finished.
//
// The package loads and checks these files, issues and renews the certificate,
// plans the key side of init, and offers the write functions of the
// transactions that are not its own. It never reads the time or the random
// source itself: both are parameters. Every write goes through a transaction of
// the txn package. See README.md for the rules and docs/references/identity for
// the reasons.
package keys
