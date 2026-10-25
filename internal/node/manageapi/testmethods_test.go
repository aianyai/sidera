//go:build testclock || crashtest

package manageapi

import (
	"strings"
	"testing"
)

// In a binary built with a test tag the table has its test rows, answered without
// an operation identifier.
func TestATaggedBuildHasItsTestRows(t *testing.T) {
	r := newRig(t)
	status, out := r.post("test.ping", `{}`)
	if status != 200 || strings.TrimSpace(string(out)) != `{"result":{"pong":true}}` {
		t.Fatalf("answered %d %s", status, out)
	}
	status, out = r.post("test.ping", `{"params":{"x":1}}`)
	r.wantError(400, "invalid_argument", "malformed_request", status, out)
	if len(testMethods()) == 0 {
		t.Error("a tagged build has no test row")
	}
}
