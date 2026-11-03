package server

import (
	"runtime"
	"slices"
	"testing"
)

func TestEnviron(t *testing.T) {
	in := []string{
		"PATH=/usr/bin",
		"HOME=/home/u",
		"SIDERA_HOME=/data",
		"SIDERA_TOKEN=secret",
		"SIDERA_PROBE_TOKEN=secret",
		"CODEX_THREAD_ID=t",
		"CODEX_SESSION_ID=s",
		"CODEX_SANDBOX=seatbelt",
		"CODEX_SANDBOX_NETWORK_DISABLED=1",
		"CODEX_CI=1",
		"CODEX_TOOL_CALL_ID=c",
		"CODEX_HOME=/home/u/.codex",
		"CLAUDECODE=1",
		"CLAUDE_CODE_ENTRYPOINT=cli",
		"CLAUDE_CONFIG_DIR=/c",
		"BAD=\xff\xfe",
		"\xffBAD=x",
		"EMPTY=",
	}
	want := []string{"PATH=/usr/bin", "HOME=/home/u", "SIDERA_HOME=/data", "CODEX_HOME=/home/u/.codex", "CLAUDE_CONFIG_DIR=/c", "EMPTY="}
	if got := Environ(in); !slices.Equal(got, want) {
		t.Fatalf("Environ = %q, want %q", got, want)
	}
}

// Windows compares names without case, so a lower-case spelling of a dropped
// variable is dropped too.
func TestEnvironDropsANameInAnyCaseOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("names are compared as written on this platform")
	}
	got := Environ([]string{"Path=C:\\bin", "sidera_token=secret", "Codex_Sandbox_Network_Disabled=1"})
	if want := []string{"Path=C:\\bin"}; !slices.Equal(got, want) {
		t.Fatalf("Environ = %q, want %q", got, want)
	}
}
