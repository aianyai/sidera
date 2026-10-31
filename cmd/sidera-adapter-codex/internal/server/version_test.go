package server

import "testing"

func TestParseCLIVersion(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
		ok   bool
	}{
		{"codex-cli 0.164.0\n", "0.164.0", true},
		{"codex-cli 0.165.0-alpha.1\n", "0.165.0-alpha.1", true},
		{"codex 1.2.3 (abc)", "1.2.3", true},
		{"no number", "", false},
	} {
		got, err := ParseCLIVersion(tc.in)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("ParseCLIVersion(%q) = %q, %v", tc.in, got, err)
		}
	}
}
