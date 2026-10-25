//go:build !testclock && !crashtest

package manageapi

import "testing"

// A binary built without a test tag has no test row: asking for one is the same as
// asking for a method that does not exist, so a test can assert that the way in is
// missing from a shipped binary.
func TestAnUntaggedBuildHasNoTestRow(t *testing.T) {
	if rows := testMethods(); len(rows) != 0 {
		t.Fatalf("an untagged build has %d test rows", len(rows))
	}
	r := newRig(t)
	for _, name := range []string{"test.ping", "test.clock.set", "test.quiesce", "test.crashpoints"} {
		status, out := r.post(name, `{}`)
		r.wantError(404, "not_found", "method_unknown", status, out)
	}
}
