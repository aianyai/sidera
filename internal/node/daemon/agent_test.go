package daemon_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/clock"
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

// A request still uploading its body keeps management shutdown waiting, but must
// not keep the Agent listener open for the rest of the shared grace.
func TestAgentListenerStopsWhileAManagementRequestIsStillInFlight(t *testing.T) {
	noLeaks(t)
	c, _ := initialized(t)
	c.ShutdownGrace = 30 * time.Second
	d := started(t, c)
	addr, err := ipc.Locate(c.Home, ipc.KindManage)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := ipc.Dial(ctx, addr, ipc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(clock.System().Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprint(conn, "POST /v1/manage/endpoints.list HTTP/1.1\r\nHost: sidera\r\nContent-Length: 2\r\nExpect: 100-continue\r\n\r\n")
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil || resp.StatusCode != http.StatusContinue {
		t.Fatalf("management has not begun reading the body: %v, %v", resp, err)
	}
	d.RequestStop("test concurrent interface shutdown")
	// Successful requests return immediately. Once shutdown closes the listener,
	// dial fails; the bounded context fails this test before the management grace.
	for {
		if _, _, err := callAgent(c.Home); err != nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("the Agent listener stayed open behind management shutdown")
		default:
		}
	}
	if _, err := fmt.Fprint(conn, "{}"); err != nil {
		t.Fatal(err)
	}
	resp, err = http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if err := d.Wait(); err != nil {
		t.Fatal(err)
	}
}
