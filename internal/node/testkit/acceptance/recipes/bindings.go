package recipes

// Bindings contains only domain-specific operations missing from scenario.Env.
// Each domain adds named, typed callbacks when its recipes need them; callbacks
// must resolve the current process on every invocation, including after restart.
// An Env operation is used directly rather than wrapped in a duplicate callback.
type Bindings struct {
	Sessions SessionOps
	Recovery RecoveryOps
}
