//go:build crashtest && !testclock

package manageapi

import "testing"

func TestCrashTagDoesNotEnableClockMethods(t *testing.T) {
	r := newRig(t)
	for _, name := range []string{"test.clock.set", "test.quiesce"} {
		status, out := r.post(name, `{}`)
		r.wantError(404, "not_found", "method_unknown", status, out)
	}
}
