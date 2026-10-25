// Package fakeagent is the interface of the fake agent: what a test does as the
// agent of an endpoint, with its inputs, its results and its errors, independent
// of the layer the test runs in.
//
// An agent sends, replies, receives, acknowledges, sends control signals, declares
// and asks for the status of what it sent. In the engine layer a backend calls the
// engine's service interface directly; in the binary layer a backend runs the real
// command-line client or speaks MCP to the real server. Because the results and
// errors are types of this package, the same scenario runs in both layers and the
// layers can be compared. The errors carry the categories and codes of the agent
// interface, so a test can say "refused because the backlog was full" without
// parsing text.
//
// Recording wraps any backend so that every call leaves its two ledger entries, the
// attempt before the action and the result after it. The package imports the
// standard library, the SDK module and the ledger and fake runtime of the testkit.
package fakeagent
