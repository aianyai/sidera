package cli

import (
	"strings"
	"testing"
)

// A usage error is reported before the Agent client is opened, so that a data
// directory or socket that cannot be used does not hide it. The data directory here
// is too long for a socket path, which makes opening the client fail.
func TestAgentUsageErrorsComeBeforeTheClientIsOpened(t *testing.T) {
	env := map[string]string{"SIDERA_HOME": "/" + strings.Repeat("a", 200)}
	code, _, stderr := runCLI(t, env, "me")
	if code == 2 || !strings.Contains(stderr, "socket") {
		t.Fatalf("the client was expected to fail on the long path: code %d, stderr %q", code, stderr)
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
		if code != 2 || stdout != "" || !strings.HasPrefix(stderr, "error: ") || strings.Contains(stderr, "socket") {
			t.Errorf("%v: code %d, stdout %q, stderr %q", args, code, stdout, stderr)
		}
	}
}
