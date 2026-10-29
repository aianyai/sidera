package mcp_test

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/cmd/sidera/internal/mcp"
)

func TestFirstCompliantRequestProbesWithTheSeparateCredential(t *testing.T) {
	for _, tc := range []struct{ name, request, response string }{
		{"legacy initialize", `{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{}}}`, `{"jsonrpc":"2.0","id":2,"result":{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"sidera","version":"test"},"instructions":"Use me to inspect your endpoint and receive limits. Receive pages must be read before acknowledging them."}}`},
		{"modern discovery", `{"jsonrpc":"2.0","id":2,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`, `{"jsonrpc":"2.0","id":2,"result":{"resultType":"complete","supportedVersions":["2026-07-28"],"capabilities":{"tools":{}},"instructions":"Use me to inspect your endpoint and receive limits. Receive pages must be read before acknowledging them.","ttlMs":3600000,"cacheScope":"public","_meta":{"io.modelcontextprotocol/serverInfo":{"name":"sidera","version":"test"}}}}`},
		{"modern ping", `{"jsonrpc":"2.0","id":2,"method":"ping","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`, `{"jsonrpc":"2.0","id":2,"error":{"code":-32601,"message":"method not found"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reached := make(chan struct{}, 2)
			c := httpAgent(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/agent/probe" || r.Header.Get("Authorization") != "Bearer BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBA" {
					t.Errorf("wrong probe credential or method: %s", r.URL.Path)
				}
				raw, _ := io.ReadAll(r.Body)
				if string(raw) != "{\"frontend\":\"mcp\"}\n" {
					t.Errorf("probe: %s", raw)
				}
				io.WriteString(w, `{"ok":true,"preset":"default","frontend":"mcp"}`)
				reached <- struct{}{}
			}))
			s := start(t, mcp.Config{Version: "test", Agent: c, ProbeOnConnect: true})
			s.exchange(t, `{"jsonrpc":"2.0","id":1,"method":"ping"}`, `{"jsonrpc":"2.0","id":1,"result":{}}`)
			select {
			case <-reached:
				t.Fatal("uninitialized legacy ping consumed the probe credential")
			case <-time.After(100 * time.Millisecond):
			}
			s.exchange(t, tc.request, tc.response)
			select {
			case <-reached:
			case <-time.After(5 * time.Second):
				t.Fatal("first compliant request did not probe")
			}
			s.exchange(t, tc.request, tc.response)
			s.exchange(t, `{"jsonrpc":"2.0","id":3,"method":"ping"}`, `{"jsonrpc":"2.0","id":3,"result":{}}`)
			select {
			case <-reached:
				t.Fatal("later request started a second automatic probe")
			case <-time.After(100 * time.Millisecond):
			}
		})
	}
}

// Hold the probe's final diagnostic to expose any use of borrowed Stderr after Run.
type heldProbeDiagnostic struct {
	entered chan struct{}
	release chan struct{}
}

func (w *heldProbeDiagnostic) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("MCP probe failed")) {
		close(w.entered)
		<-w.release
	}
	return len(p), nil
}

func TestRunJoinsTheCancelledAutomaticProbe(t *testing.T) {
	entered := make(chan struct{})
	c := httpAgent(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		close(entered)
		<-r.Context().Done()
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in, input := io.Pipe()
	output, out := io.Pipe()
	diagnostic := &heldProbeDiagnostic{make(chan struct{}), make(chan struct{})}
	releaseDiagnostic := sync.OnceFunc(func() { close(diagnostic.release) })
	t.Cleanup(releaseDiagnostic)
	done := make(chan error, 1)
	go func() {
		done <- mcp.Run(ctx, mcp.Config{Stdin: in, Stdout: out, Stderr: diagnostic, Agent: c, ProbeOnConnect: true})
	}()
	t.Cleanup(func() { cancel(); input.Close(); output.Close() })
	fmt.Fprintln(input, `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`)
	// Drain the reply so that its write cannot be the reason Run remains alive.
	if _, err := bufio.NewReader(output).ReadBytes('\n'); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("automatic probe did not start")
	}
	input.Close()
	select {
	case <-diagnostic.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("probe cancellation did not reach its final diagnostic")
	}
	// Cancellation drives the fixture. A generous observation window checks
	// that Run cannot return while the borrowed writer is still in use.
	select {
	case <-done:
		releaseDiagnostic()
		t.Fatal("Run returned while the automatic probe was writing Stderr")
	case <-time.After(time.Second):
	}
	releaseDiagnostic()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not join the completed probe")
	}
}
