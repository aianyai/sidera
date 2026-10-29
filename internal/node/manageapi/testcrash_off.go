//go:build !crashtest

package manageapi

// TestConfig is empty in builds without crash-test support.
type TestConfig struct{}

func crashMethods() []*method { return nil }
