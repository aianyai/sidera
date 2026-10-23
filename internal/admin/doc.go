// Package admin decides who may administer a node.
//
// Administration rights belong to people, not to nodes: every node keeps its
// own list of authorized persons, each with a level, and only the local owner
// can change that list. This package holds the rules that turn a list, a
// caller and a requested operation into an allow/deny verdict, the rule for
// accepting an administrator's offline directive, and the pure checks behind
// idempotent, conflict-safe management requests.
//
// Everything here is pure: no I/O, no clock reads, no global state. Callers
// verify signatures and certificate chains first and pass the verified facts
// in. See README.md for the invariants and for what callers must guarantee.
package admin
