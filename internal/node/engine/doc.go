// Package engine is the single writer of the node's runtime state: one loop
// that owns the in-memory view of every record and runs every command that
// changes state, one after another. Each command first makes its change
// durable through the transaction layer, then updates memory, then answers.
//
// This package holds the loop, the scheduler, the registration of endpoints
// with their cap and tokens, the one capacity check, the storage of alerts and
// the two recovery entries. The behaviour that later streams add (sending,
// receiving, prompting, control signals, feedback) joins the same loop as
// further commands. See README.md for the rules and docs/references/engine for
// the reasons.
package engine
