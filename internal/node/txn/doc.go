// Package txn is the single entry point of every persistent write of keys/,
// config/ and state/. It owns the only writable file system a node has: the
// packages that persist anything hold a *Writer, write through the Tx it
// opens, and read through the fsx.Reader it hands out.
//
// A transaction is declared statically, as a Decl: its name, its kind and its
// writes in order. The declarations of the whole program are collected in one
// Table by the composition root. The Table is where the crash points of the
// program are known before anything runs, so a test can compare what a run
// reached with what was declared, and a write that was never declared panics
// the first time it runs.
//
// The layer knows names, roles, operations, order, and the difference between
// preparatory writes and the rest. It knows nothing of what the files mean:
// replaying a half-finished transaction is the business of the package that
// owns the files, as a Recovery declaration of its own. See README.md for the
// rules and docs/references/storage for the reasons.
package txn
