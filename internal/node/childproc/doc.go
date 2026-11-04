// Package childproc holds the one rule for how the daemon starts a console
// program as its child (an adapter, the restart check): it must not get a window
// of its own. The daemon that a restart starts in its place is the one exception:
// it is started detached by sdk/go/adapterkit/process, on purpose.
package childproc
