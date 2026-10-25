// Package fakeruntime is the stand-in for a native agent runtime such as Claude
// Code or Codex: the program an adapter drives, which the daemon never talks to.
//
// A Runtime holds sessions. A session keeps a record of what happened to it (a
// JSON-lines file, the way a native runtime keeps one), takes prompts, runs turns
// and reports turn starts and ends to the adapter that watches it. It differs from
// a real runtime in the one way that makes tests deterministic: a turn never ends
// because time passed. Turns start when a prompt arrives (unless the script holds
// the start back) or when the script says so, and end only when the script says so
// or when the plan of the turn says to end after its last action. What the model
// does inside a turn is a list of actions, run in order by an ActionRunner the
// fixture supplies: in the engine layer it calls the fake agent, in the binary
// layer it runs the real command-line client.
//
// Every time it writes comes from the shared test clock, and it writes a ledger
// entry for every effect before it confirms the effect to the adapter. The
// package imports only the standard library, the SDK module and the other
// packages of the third-party side of the testkit.
package fakeruntime
