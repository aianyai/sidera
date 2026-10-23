// Package revocation defines the identity-level revocation statements, the
// queries that turn a set of verified revocations into the facts the judging
// packages read, and the rule that decides how long a node keeps a revocation.
//
// It sits on package record: each statement is a record.Descriptor, and nothing
// here is signed or stored without going through record. See README.md.
package revocation
