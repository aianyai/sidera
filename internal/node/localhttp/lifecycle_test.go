package localhttp_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/ipc"
	"github.com/aianyai/sidera/internal/node/localhttp"
)

type held struct{}

func (held) Held() bool { return true }

type socketServer struct {
	server  *localhttp.Server
	address ipc.Address
	done    chan error
}

func socketHome(t *testing.T) string {
	t.Helper()
	home, err := os.MkdirTemp("", "lh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"run", "manage"} {
		if err := os.Mkdir(filepath.Join(home, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

func serveSocket(t *testing.T, h http.HandlerFunc, log *slog.Logger) *socketServer {
	t.Helper()
	s, err := localhttp.New(localhttp.Config{Kind: ipc.KindManage, PathPrefix: "/v1/manage/", Lookup: func(name string) http.HandlerFunc {
		if name == "known" {
			return h
		}
		return nil
	}, Log: log})
	if err != nil {
		t.Fatal(err)
	}
	addr, err := ipc.Locate(socketHome(t), ipc.KindManage)
	if err != nil {
		t.Fatal(err)
	}
	l, err := localhttp.Listen(ipc.KindManage, addr, held{}, ipc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	r := &socketServer{s, addr, make(chan error, 1)}
	go func() { r.done <- s.Serve(l) }()
	t.Cleanup(func() {
		s.Close()
		select {
		case err := <-r.done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			t.Error("Serve did not return")
		}
	})
	return r
}

func (s *socketServer) client(t *testing.T) *http.Client {
	t.Helper()
	c := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return ipc.Dial(ctx, s.address, ipc.Options{})
	}}, Timeout: 10 * time.Second}
	t.Cleanup(c.CloseIdleConnections)
	return c
}

// The request remains alive during the grace; a successful shutdown lets its
// answer finish, while an expired grace cancels it and closes the connection.
func TestShutdownWaitsForRequestsAndClosesThemWhenTheGraceEnds(t *testing.T) {
	for _, expires := range []bool{false, true} {
		t.Run(map[bool]string{false: "request finishes", true: "grace expires"}[expires], func(t *testing.T) {
			entered := make(chan struct{})
			release := make(chan struct{})
			exited := make(chan struct{})
			canceled := make(chan struct{})
			r := serveSocket(t, func(w http.ResponseWriter, req *http.Request) {
				close(entered)
				defer close(exited)
				select {
				case <-release:
					io.WriteString(w, "done")
				case <-req.Context().Done():
					close(canceled)
				}
			}, nil)
			response := make(chan error, 1)
			c := r.client(t)
			go func() {
				resp, err := c.Post("http://sidera/v1/manage/known", "application/json", nil)
				if err == nil {
					body, e := io.ReadAll(resp.Body)
					resp.Body.Close()
					err = e
					if string(body) != "done" {
						err = errors.New("incomplete response")
					}
				}
				response <- err
			}()
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("request did not enter")
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stopped := make(chan error, 1)
			go func() { stopped <- r.server.Shutdown(ctx) }()
			select {
			case err := <-stopped:
				t.Fatalf("Shutdown returned before the request: %v", err)
			case <-time.After(20 * time.Millisecond):
			}
			if expires {
				cancel()
			} else {
				close(release)
			}
			select {
			case err := <-stopped:
				if expires && !errors.Is(err, context.Canceled) || !expires && err != nil {
					t.Fatalf("Shutdown returned %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("Shutdown did not return")
			}
			select {
			case <-exited:
			case <-time.After(10 * time.Second):
				t.Fatal("handler did not exit")
			}
			if expires {
				select {
				case <-canceled:
				default:
					t.Fatal("remaining request was not canceled")
				}
			}
			select {
			case err := <-response:
				if expires == (err == nil) {
					t.Fatalf("request returned %v, expires %v", err, expires)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("client did not return")
			}
			if resp, err := c.Post("http://sidera/v1/manage/known", "application/json", nil); err == nil {
				resp.Body.Close()
				t.Fatal("a stopped server accepted a request")
			}
		})
	}
}

type captured struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (c *captured) Write(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.b.Write(b)
}
func (c *captured) String() string { c.mu.Lock(); defer c.mu.Unlock(); return c.b.String() }

func TestHTTPDiagnosticsUseTheConfiguredLogger(t *testing.T) {
	var log captured
	r := serveSocket(t, func(http.ResponseWriter, *http.Request) { panic("handler diagnostic") }, slog.New(slog.NewTextHandler(&log, nil)))
	_, err := r.client(t).Post("http://sidera/v1/manage/known", "application/json", nil)
	if err == nil {
		t.Fatal("a panicking handler answered successfully")
	}
	got := log.String()
	if !strings.Contains(got, "http: panic serving") || !strings.Contains(got, "handler diagnostic") || !strings.Contains(got, "interface=manage") || !strings.Contains(got, "level=ERROR") {
		t.Fatalf("net/http diagnostic missing from configured log: %s", got)
	}
}

func TestListenerKindCannotCrossInterfaces(t *testing.T) {
	home := socketHome(t)
	addr, err := ipc.Locate(home, ipc.KindAgent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := localhttp.Listen(ipc.KindManage, addr, held{}, ipc.Options{}); err == nil {
		t.Fatal("management listener accepted an Agent address")
	}
	l, err := localhttp.Listen(ipc.KindAgent, addr, held{}, ipc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := newServer(t).Serve(l); err == nil {
		t.Fatal("management server accepted an Agent listener")
	}
	// Refusing it did not take or close it: its proper owner can still serve it.
	s, err := localhttp.New(localhttp.Config{Kind: ipc.KindAgent, PathPrefix: "/v1/agent/", Lookup: func(string) http.HandlerFunc { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Serve(l) }()
	s.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return after Close")
	}
}
