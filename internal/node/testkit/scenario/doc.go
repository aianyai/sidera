// Package scenario is the language of test scripts, independent of the layer a
// script runs in.
//
// A script is Go code: a setup, a list of steps, the judgements it is held to
// and the crash plan the walk takes from it. A driver (Run) runs the steps
// against an Env, which a fixture implements: the engine-layer fixture calls the
// engine in process; the binary-layer fixture will drive the built program. A
// script that uses only Env runs unchanged in both.
//
// The package imports no engine, no fixture and no backend of the fake agent:
// that is what lets the binary-layer fixture implement Env as well. A test in
// the parent directory enforces it.
package scenario
