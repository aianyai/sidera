// Package process is what an adapter needs from the operating system to run a
// native agent runtime as a process of its own: starting it so that it outlives
// the adapter, naming it so that it can be found again after a restart, listing
// the processes of the adapter's user and interrupting it the way Ctrl-C in its
// terminal would.
//
// It covers macOS, Linux and Windows; on another platform every operation of the
// process table fails with ErrUnsupported. The reference in
// docs/references/adapter-protocol ("Native processes") is the specification of
// the identities and of how far Start detaches on each platform.
package process
