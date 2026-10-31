//go:build (testclock || crashtest) && !netacceptance

package manageapi

import "testing"

func TestOtherTagsDoNotEnableCredentialMethods(t *testing.T) {
	r := newRig(t)
	for _, name := range []string{"test.trust.offer", "test.trust.credential"} {
		status, out := r.post(name, `{}`)
		r.wantError(404, "not_found", "method_unknown", status, out)
	}
}
