// Package crashwalk is the core of the crash-point walk: the part that does not
// depend on how a scenario is run.
//
// A walk takes a scenario, runs it once without a crash to record every write step
// it reaches, and then runs it again once per crash point, killing the system at
// that point, restarting it, draining it and checking what a client was promised.
// The system under test is behind a Backend, which has two implementations in
// time: the engine in process on a crashable in-memory file system, and the built
// binary killed from outside. Everything else is here: what a crash point is, which
// points a walk takes in each mode, how a run is compared with its recording, how
// big a scenario may grow, which writes the walk must have reached, and how the
// recovery of a crash is itself crashed.
//
// The package reads only the declarations of the transaction layer and the crash
// point types of the file layer; it runs nothing and reads no clock: whoever calls
// it times the runs through the observer.
package crashwalk
