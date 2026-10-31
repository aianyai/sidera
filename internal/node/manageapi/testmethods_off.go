//go:build !testclock && !crashtest

package manageapi

// testMethods is empty in a binary built without a test tag: there is no test row
// on the management socket of a shipped binary, and a request for one is
// method_unknown.
func testMethods() []*method { return nil }
