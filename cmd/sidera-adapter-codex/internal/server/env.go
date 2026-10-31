package server

import (
	"strings"
	"unicode/utf8"
)

// Environ returns the environment the App Server is started with: the adapter's
// own, less what must not reach a server that outlives this process and hands its
// environment to every thread it runs.
//
//   - A pair that is not valid UTF-8: the server reads the environment as text and
//     refuses to start on one.
//   - The session credentials, which belong to one session and are given to a
//     thread through its own configuration; one left in the server's environment
//     would be seen by every thread.
//   - The identity of a Codex session the daemon happens to be running inside, and
//     of the Claude Code that may have started it: the server would take them for
//     its own.
func Environ(in []string) []string {
	out := make([]string, 0, len(in))
	for _, kv := range in {
		if !utf8.ValidString(kv) {
			continue
		}
		name, _, _ := strings.Cut(kv, "=")
		if dropped(name) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func dropped(name string) bool {
	switch name {
	case "SIDERA_TOKEN", "SIDERA_PROBE_TOKEN",
		"CODEX_THREAD_ID", "CODEX_SESSION_ID", "CODEX_CI", "CODEX_TOOL_CALL_ID",
		"CLAUDECODE":
		return true
	}
	return strings.HasPrefix(name, "CODEX_SANDBOX") || strings.HasPrefix(name, "CLAUDE_CODE_")
}
