package cli

import (
	"strings"
	"testing"
)

// A usage error is reported before the Agent client is opened, so that a daemon that
// cannot be reached does not hide it. No daemon runs in the data directory here, which
// makes opening the client fail on every platform, whether on a socket path that is
// unusable or on a daemon that is not there.
func TestAgentUsageErrorsComeBeforeTheClientIsOpened(t *testing.T) {
	env := map[string]string{"SIDERA_HOME": t.TempDir()}
	code, _, stderr := runCLI(t, env, "me")
	if code == 0 || code == 2 {
		t.Fatalf("the client was expected to fail to open: code %d, stderr %q", code, stderr)
	}
	for _, args := range [][]string{
		{"send", "--body", "x"},
		{"send", "--to", "a", "--body", "x", "extra"},
		{"send", "--to", "a", "--body", "x", "--body-file", "-"},
		{"reply"},
		{"control", "a"},
		{"status", "a", "b"},
		{"ack"},
		{"ack", "lease", "no-part"},
		{"recv", "extra"},
		{"me", "extra"},
		{"directory", "--bogus"},
		{"mcp", "extra"},
		{"nonsense"},
	} {
		code, stdout, stderr := runCLI(t, env, args...)
		if code != 2 || stdout != "" || !strings.HasPrefix(stderr, "error: ") {
			t.Errorf("%v: code %d, stdout %q, stderr %q", args, code, stdout, stderr)
		}
	}
}
