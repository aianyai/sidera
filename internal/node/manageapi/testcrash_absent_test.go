//go:build testclock && !crashtest

package manageapi

import "testing"

func TestClockTagDoesNotEnableCrashPointQuery(t *testing.T) {
	r := newRig(t)
	status, out := r.post("test.crashpoints", `{}`)
	r.wantError(404, "not_found", "method_unknown", status, out)
}
