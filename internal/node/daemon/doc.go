// Package daemon is the composition root of the node: it takes the instance lock,
// checks the data directory, loads the layers in the order of the start, wires
// them to one another, serves the management interface, and shuts everything down
// in the order of the shutdown. It is also where `init` is, because the
// transaction that makes a data directory has to write the files of every owner.
// See README.md and docs/references/daemon.
package daemon
