package ipc

import "testing"

// testHome is the data directory a test uses.
func testHome(t *testing.T) string { return t.TempDir() }
