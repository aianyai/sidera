// Package ipc is how the Agent and management interfaces are reached on this
// machine: Unix domain sockets on macOS and Linux, named pipes on Windows. It
// creates the listeners, dials them, and checks that the other side is the
// current user, so that neither a stranger on the machine can use the
// interfaces nor can a stranger who got there first collect a client's token.
//
// See README.md for the rules and docs/references/local-ipc for the reasons.
package ipc
