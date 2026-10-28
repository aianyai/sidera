//go:build realclient

package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
)

// This opt-in test spends the user's authenticated client quota. It is never
// run by ordinary tests or CI; the user runs it once against the finished binary.
func TestRealClientsAcceptMCPResponses(t *testing.T) {
	if os.Getenv("SIDERA_REALCLIENT") != "1" {
		t.Skip("set SIDERA_REALCLIENT=1 to run the authenticated local clients")
	}
	for _, name := range []string{"claude", "codex"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Fatal(err)
		}
	}
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../../../.."))
	binary := filepath.Join(t.TempDir(), "sidera")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-o", binary, "./cmd/sidera")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build sidera: %v\n%s", err, output)
	}
	for _, name := range []string{"claude", "codex"} {
		t.Run(name, func(t *testing.T) {
			version, err := exec.Command(name, "--version").CombinedOutput()
			if err != nil {
				t.Fatalf("%s version: %v", name, err)
			}
			t.Logf("%s", strings.TrimSpace(string(version)))
			var eventsMu sync.Mutex
			events := make(map[string][]int)
			h, _, address, home, token := agentNodeDetails(t, func(path string, status int) {
				eventsMu.Lock()
				events[path] = append(events[path], status)
				eventsMu.Unlock()
			})
			const marker = "SIDERA_REALCLIENT_BODY_628491"
			if _, err := h.Agent("creator").Send(h.Ctx(), fakeagent.SendInput{To: []string{address}, Body: marker}); err != nil {
				t.Fatal(err)
			}
			h.Quiesce()
			dir := t.TempDir()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			prompt := fmt.Sprintf("Use only the sidera MCP server. First call me and report the endpoint address. Then call recv with wait_s 0 and quote the complete message body. Finally call status with exactly these JSON arguments: %s. The message_id must be an explicit empty string; do not omit or replace it. Report the error code. Do not use file, shell, network, or other tools. You must make the three actual MCP calls; do not infer their answers.", emptyStatusArguments)
			var args []string
			if name == "claude" {
				config := map[string]any{"mcpServers": map[string]any{"sidera": map[string]any{"type": "stdio", "command": binary, "args": []string{"mcp"}, "env": map[string]string{"SIDERA_HOME": home, "SIDERA_TOKEN": string(token), "SIDERA_PROBE_TOKEN": ""}}}}
				raw, err := json.Marshal(config)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(dir, "mcp.json")
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
				args = []string{"--print", "--output-format", "stream-json", "--verbose", "--strict-mcp-config", "--mcp-config", path, "--tools", "", "--allowedTools", "mcp__sidera__me,mcp__sidera__recv,mcp__sidera__status", "--no-session-persistence", "--setting-sources", "", prompt}
			} else {
				config := fmt.Sprintf("mcp_servers.sidera={command=%s,args=[\"mcp\"],env={SIDERA_HOME=%s,SIDERA_TOKEN=%s,SIDERA_PROBE_TOKEN=\"\"},enabled=true,required=true,enabled_tools=[\"me\",\"recv\",\"status\"],default_tools_approval_mode=\"auto\"}", strconv.Quote(binary), strconv.Quote(home), strconv.Quote(string(token)))
				args = []string{"exec", "--ignore-user-config", "--ephemeral", "--skip-git-repo-check", "--json", "--sandbox", "read-only", "-c", config, "-c", `mcp_servers.sidera.tools.recv.approval_mode="approve"`, "-c", "mcp_optional_startup_grace_ms=0", prompt}
			}
			cmd := exec.CommandContext(ctx, name, args...)
			cmd.Dir = dir
			var output bytes.Buffer
			cmd.Stdout = &output
			cmd.Stderr = &output
			err = cmd.Run()
			text := strings.ReplaceAll(output.String(), string(token), "[fixture token]")
			if err != nil {
				t.Fatalf("%s: %v\n%s", name, err, text)
			}
			for _, want := range []string{address, marker, "malformed_request"} {
				if !strings.Contains(text, want) {
					t.Fatalf("%s did not report %q\n%s", name, want, text)
				}
			}

			eventsMu.Lock()
			defer eventsMu.Unlock()
			for method, status := range map[string]int{"me": 200, "recv": 200, "status": 400} {
				seen := false
				for _, actual := range events["/v1/agent/"+method] {
					seen = seen || actual == status
				}
				if !seen {
					t.Fatalf("%s did not actually call Agent %s with expected HTTP status %d; observed %v", name, method, status, events)
				}
			}
			t.Logf("%s accepted discovery, identity, receive text and tool errors", name)
		})
	}
}
