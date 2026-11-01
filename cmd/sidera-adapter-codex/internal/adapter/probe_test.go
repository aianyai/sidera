package adapter

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The MCP probe starts the program as `<program> mcp`, speaks MCP to it and reads
// what it says of its own probe. A shell script stands in for the program.
func TestRunMCPProbe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in program is a shell script")
	}
	defer func(d time.Duration) { probeGrace = d }(probeGrace)
	probeGrace = 200 * time.Millisecond
	for _, tc := range []struct {
		name    string
		script  string
		wantErr string
	}{
		{"answers and probes", `read line; echo '{"jsonrpc":"2.0","id":1,"result":{}}'; echo 'level=INFO msg="MCP probe succeeded"' >&2; cat >/dev/null`, ""},
		{"answers, probe failed", `read line; echo '{"jsonrpc":"2.0","id":1,"result":{}}'; echo 'level=WARN msg="MCP probe failed"' >&2; cat >/dev/null`, "could not reach the daemon"},
		{"answers, says nothing of its probe", `read line; echo '{"jsonrpc":"2.0","id":1,"result":{}}'; cat >/dev/null`, ""},
		{"refuses initialize", `read line; echo '{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"no"}}'; cat >/dev/null`, "refused initialize"},
		// Read initialize before exiting so the probe can diagnose a missing response.
		{"ends without answering initialize", `read line; exit 3`, "without answering"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			program := filepath.Join(t.TempDir(), "sidera")
			if err := os.WriteFile(program, []byte("#!/bin/sh\n"+tc.script+"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			err := runMCPProbe(ctx, program, os.Environ())
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("err = %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
			}
		})
	}
}
