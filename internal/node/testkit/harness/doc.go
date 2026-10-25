// Package harness is the engine-layer fixture: the engine on the real store, the
// real configuration and the real transaction layer, on the in-memory file system
// with its crash model, driven by one shared test clock, with a ledger beside it
// that the agents and the fakes write to.
//
// A test builds a node with New, registers direct endpoints through the engine's
// own entry, acts as their agents, moves the clock, waits for quiet, crashes the
// process or the power and restarts, and judges what happened from the ledger.
// *H implements scenario.Env, so the scripts the binary layer will run run here.
package harness
