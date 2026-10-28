package mcp_test

import (
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aianyai/sidera/cmd/sidera/internal/mcp"
)

func TestFirstCompliantRequestProbesWithTheSeparateCredential(t *testing.T) {
	reached := make(chan struct{}, 1)
	var calls atomic.Int32
	c := httpAgent(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
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
	case <-time.After(5 * time.Second):
		t.Fatal("first request did not probe")
	}
	s.exchange(t, `{"jsonrpc":"2.0","id":2,"method":"ping"}`, `{"jsonrpc":"2.0","id":2,"result":{}}`)
	if got := calls.Load(); got != 1 {
		t.Fatalf("automatic probes: %d", got)
	}
}
