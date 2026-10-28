package daemon_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/daemon"
	"github.com/aianyai/sidera/internal/node/ipc"
)

func TestAgentListenerStartsWithProbesAndEnablesMethodsOnlyWhenServing(t *testing.T) {
	noLeaks(t)
	c, _ := initialized(t)
	check := func(wantStatus int, wantCode string) func(context.Context, *daemon.Env) error {
		return func(context.Context, *daemon.Env) error {
			status, body, err := callAgent(c.Home)
			if err != nil {
				t.Errorf("Agent request: %v", err)
			} else if status != wantStatus || !strings.Contains(string(body), `"code":"`+wantCode+`"`) {
				t.Errorf("Agent response = %d %s; want %d %s", status, body, wantStatus, wantCode)
			}
			return nil
		}
	}
	c.Attach.AgentProbeOnly = check(http.StatusServiceUnavailable, "probe_only")
	c.Attach.AgentServe = check(http.StatusUnauthorized, "token_missing")
	d := started(t, c)
	d.RequestStop("test complete")
	if err := d.Wait(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := callAgent(c.Home); err == nil {
		t.Error("Agent listener still answers after the daemon stopped")
	}
}

func callAgent(home string) (int, []byte, error) {
	addr, err := ipc.Locate(home, ipc.KindAgent)
	if err != nil {
		return 0, nil, err
	}
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return ipc.Dial(ctx, addr, ipc.Options{})
		},
	}, Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	res, err := client.Post("http://sidera/v1/agent/me", "application/json", strings.NewReader("{}"))
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	return res.StatusCode, body, err
}
