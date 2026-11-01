package appserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/appserver"
	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/appserver/appservertest"
)

func ctx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return c
}

// servers lists the two transports the client dials; every behaviour below holds
// on both.
func servers(t *testing.T) map[string]*appservertest.Server {
	return map[string]*appservertest.Server{
		"unix": appservertest.NewUnix(t),
		"tcp":  appservertest.NewTCP(t, "secret"),
	}
}

func TestCallsAndNotificationsOverBothTransports(t *testing.T) {
	for name, srv := range servers(t) {
		t.Run(name, func(t *testing.T) {
			srv.Handle("thread/start", func(json.RawMessage) (any, *appserver.RPCError) {
				// The notification is sent before the answer: the client keeps
				// their order.
				srv.Notify("thread/started", map[string]string{"id": "t1"})
				return map[string]any{"thread": map[string]any{"id": "t1", "status": map[string]string{"type": "idle"}}}, nil
			})
			got := make(chan appserver.Notification, 4)
			c, err := appserver.Dial(ctx(t), srv.Endpoint, appserver.Handlers{Notification: func(n appserver.Notification) { got <- n }})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			th, err := c.ThreadStart(ctx(t), appserver.ThreadStartParams{CWD: "/x"})
			if err != nil || th.Thread.ID != "t1" || th.Thread.Status.Active() {
				t.Fatalf("ThreadStart = %+v, %v", th, err)
			}
			select {
			case n := <-got:
				if n.Method != "thread/started" {
					t.Fatalf("notification = %s", n.Method)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("no notification")
			}
		})
	}
}

func TestTCPWithoutTheTokenIsRefused(t *testing.T) {
	srv := appservertest.NewTCP(t, "secret")
	ep := srv.Endpoint
	ep.Token = "wrong"
	if _, err := appserver.Dial(ctx(t), ep, appserver.Handlers{}); err == nil {
		t.Fatal("dial with a wrong token succeeded")
	}
}

func TestServerErrorIsAnRPCError(t *testing.T) {
	srv := appservertest.NewUnix(t)
	srv.Handle("thread/resume", func(json.RawMessage) (any, *appserver.RPCError) {
		return nil, &appserver.RPCError{Code: -32600, Message: "no rollout found for thread id x"}
	})
	c, err := appserver.Dial(ctx(t), srv.Endpoint, appserver.Handlers{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = c.ThreadResume(ctx(t), appserver.ThreadResumeParams{ThreadID: "x"})
	var rpc *appserver.RPCError
	if !errors.As(err, &rpc) || !strings.Contains(rpc.Message, "no rollout found") {
		t.Fatalf("err = %v", err)
	}
}

func TestServerRequestsAreAnsweredFromTheHandler(t *testing.T) {
	srv := appservertest.NewUnix(t)
	answer := func(method string, params json.RawMessage) (any, *appserver.RPCError) {
		if method == "item/commandExecution/requestApproval" {
			return map[string]string{"decision": "decline"}, nil
		}
		return nil, &appserver.RPCError{Code: appserver.CodeMethodNotFound, Message: "unknown"}
	}
	c, err := appserver.Dial(ctx(t), srv.Endpoint, appserver.Handlers{ServerRequest: answer})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// A round trip first, so the server has registered the connection.
	srv.Handle("account/read", func(json.RawMessage) (any, *appserver.RPCError) {
		return map[string]any{"account": nil, "requiresOpenaiAuth": false}, nil
	})
	if _, err := c.AccountRead(ctx(t)); err != nil {
		t.Fatal(err)
	}
	raw, err := srv.Request(ctx(t), "item/commandExecution/requestApproval", map[string]string{"threadId": "t"})
	if err != nil || !strings.Contains(string(raw), `"decline"`) {
		t.Fatalf("answer = %s, %v", raw, err)
	}
}

func TestPendingCallsFailWhenTheConnectionDrops(t *testing.T) {
	srv := appservertest.NewUnix(t)
	release := make(chan struct{})
	defer close(release)
	srv.Handle("turn/start", func(json.RawMessage) (any, *appserver.RPCError) {
		<-release
		return map[string]any{}, nil
	})
	c, err := appserver.Dial(ctx(t), srv.Endpoint, appserver.Handlers{})
	if err != nil {
		t.Fatal(err)
	}
	errc := make(chan error, 1)
	go func() {
		_, err := c.TurnStart(ctx(t), appserver.TurnStartParams{ThreadID: "t", Input: []appserver.UserInput{appserver.TextInput("hi")}})
		errc <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for len(srv.Calls()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	srv.DropConnections()
	select {
	case err := <-errc:
		if !errors.Is(err, appserver.ErrClosed) {
			t.Fatalf("err = %v, want ErrClosed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pending call not failed")
	}
	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done not closed")
	}
	if _, err := c.AccountRead(ctx(t)); !errors.Is(err, appserver.ErrClosed) {
		t.Fatalf("call after the end = %v", err)
	}
}

// The library's default read limit is 32 KiB; a thread read is far larger.
func TestLargeMessagesAreRead(t *testing.T) {
	srv := appservertest.NewUnix(t)
	big := strings.Repeat("x", 4<<20)
	srv.Handle("thread/turns/list", func(json.RawMessage) (any, *appserver.RPCError) {
		return map[string]any{"data": []map[string]any{{"id": "u1", "status": "completed", "items": []map[string]any{{"type": "agentMessage", "id": "i", "text": big}}}}}, nil
	})
	c, err := appserver.Dial(ctx(t), srv.Endpoint, appserver.Handlers{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	r, err := c.ThreadTurnsList(ctx(t), appserver.ThreadTurnsListParams{ThreadID: "t"})
	if err != nil || len(r.Data) != 1 {
		t.Fatalf("turns = %+v, %v", r, err)
	}
}

// A page read with full items must show what a prompt is found by; a page in
// another form is an error, not a page without prompts.
func TestThreadTurnsListFullRecordForm(t *testing.T) {
	userMessage := map[string]any{"type": "userMessage", "id": "i", "clientId": "a1", "content": []any{}}
	for _, tc := range []struct {
		name      string
		turn      map[string]any
		itemsView string
		ok        bool
	}{
		{"recognized", map[string]any{"itemsView": "full", "items": []any{userMessage}}, "full", true},
		{"a prompt without a client id", map[string]any{"itemsView": "full", "items": []any{map[string]any{"type": "userMessage", "id": "i", "clientId": nil}}}, "full", true},
		{"a turn without items", map[string]any{"itemsView": "full"}, "full", false},
		{"items not all loaded", map[string]any{"itemsView": "summary", "items": []any{}}, "full", false},
		{"a turn that does not say how much it holds", map[string]any{"items": []any{userMessage}}, "full", false},
		{"a prompt whose client id is renamed", map[string]any{"itemsView": "full", "items": []any{map[string]any{"type": "userMessage", "id": "i", "clientRef": "a1"}}}, "full", false},
		{"not asked for in full", map[string]any{"itemsView": "notLoaded", "items": []any{}}, "notLoaded", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := appservertest.NewUnix(t)
			turn := map[string]any{"id": "u1", "status": "completed"}
			for k, v := range tc.turn {
				turn[k] = v
			}
			srv.Handle("thread/turns/list", func(json.RawMessage) (any, *appserver.RPCError) {
				return map[string]any{"data": []any{turn}}, nil
			})
			c, err := appserver.Dial(ctx(t), srv.Endpoint, appserver.Handlers{})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			r, err := c.ThreadTurnsList(ctx(t), appserver.ThreadTurnsListParams{ThreadID: "t", ItemsView: tc.itemsView})
			if tc.ok && (err != nil || len(r.Data) != 1) {
				t.Fatalf("turns = %+v, %v", r, err)
			}
			if !tc.ok && !errors.Is(err, appserver.ErrUnrecognizedRecord) {
				t.Fatalf("err = %v, want ErrUnrecognizedRecord", err)
			}
		})
	}
}

func TestServerVersion(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"sidera/0.164.0 (Mac OS 27.0.1; arm64) unknown (sidera; 0.1.0)", "0.164.0"},
		{"codex/0.165.0-alpha.1 (Linux)", "0.165.0-alpha.1"},
		{"nothing", ""},
	} {
		if got := appserver.ServerVersion(tc.in); got != tc.want {
			t.Errorf("ServerVersion(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
