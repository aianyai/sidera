// Package process is what an adapter needs from the operating system to run a
// native agent runtime as a process of its own: starting it so that it outlives
// the adapter, naming it so that it can be found again after a restart, listing
// the processes of the adapter's user and interrupting it the way Ctrl-C in its
// terminal would.
//
// The daemon uses Start and HostDetachment as well, to restart itself on Windows,
// where a program cannot replace itself in place; "adapter" then means the process
// that calls Start.
//
// It covers macOS, Linux and Windows; on another platform the package compiles and
// Start, Interrupt and the process table fail with ErrUnsupported. The reference in
// docs/references/adapter-protocol ("Native processes") is the specification of
// the identities and of how far Start detaches on each platform.
package process
