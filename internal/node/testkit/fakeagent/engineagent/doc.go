// Package engineagent is the engine-layer backend of the fake agent: it does what
// an agent does by calling the engine's service interface in process.
//
// It implements all eleven methods of fakeagent.Agent. The methods the engine
// offers are mapped to its calls and its typed refusals to the categories and
// codes of the agent interface; the others answer that the method is not known,
// so that a script written against the whole interface runs now and a method
// starts working when the engine learns it, without a change here or in the
// interface.
package engineagent
