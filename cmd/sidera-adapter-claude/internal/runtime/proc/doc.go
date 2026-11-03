// Package proc is the adapter's view of the operating system's processes: the
// process table, starting a detached process and interrupting one. It is the
// adapter's own interface over sdk/go/adapterkit/process, which does the work on
// every platform, so that the runtime's flows run against fakes.
package proc
