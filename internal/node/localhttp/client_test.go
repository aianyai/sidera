package localhttp_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/ipc"
	"github.com/aianyai/sidera/internal/node/localhttp"
	"github.com/aianyai/sidera/sdk/go/strictjson"
)

func localClient(t testing.TB, home string) *localhttp.Client {
	t.Helper()
	c, err := localhttp.NewClient(localhttp.ClientConfig{Home: home, Kind: ipc.KindManage, PathPrefix: "/v1/manage/"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

// No daemon is needed: unsafe text must be refused before any connection, with
// a fixed error that cannot reveal a body or a sensitive map key.
type embeddedRequestText struct {
	Body string `json:"body"`
}

func TestClientRejectsUnsafeDocumentsBeforeConnecting(t *testing.T) {
	c := localClient(t, socketHome(t))
	for _, tc := range []struct {
		name string
		body any
	}{
		{"string", "secret\xff"},
		{"embedded struct", struct{ embeddedRequestText }{embeddedRequestText{Body: "secret\xff"}}},
		{"embedded pointer", struct{ *embeddedRequestText }{&embeddedRequestText{Body: "secret\xff"}}},
		{"map key", map[string]any{"secret\xff": true}},
		{"nested interface", map[string]any{"outer": []any{map[string]string{"body": "secret\xff"}}}},
		{"raw UTF-8", json.RawMessage("{\"body\":\"secret\xff\"}")},
		{"raw surrogate", json.RawMessage(`{"body":"secret\ud800"}`)},
		{"raw duplicate", json.RawMessage(`{"secret":1,"secret":2}`)},
		{"raw trailing", json.RawMessage(`{} {"secret":1}`)},
		{"invalid number", json.Number("secret")},
		{"nested raw", map[string]any{"body": json.RawMessage(`{"secret":1,"secret":2}`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := c.Call(context.Background(), localhttp.Request{Method: "known", Body: tc.body, Timeout: time.Second})
			e := apiwire.FromError(err)
			if e == nil || e.Category != apiwire.CategoryInvalidArgument || e.Code != "malformed_request" {
				t.Fatalf("Call: %v; want malformed_request", err)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("request data appeared in an encoding error")
			}
		})
	}
}

func TestClientPreservesRequestValuesAndReadsTheWholeResponse(t *testing.T) {
	home := socketHome(t)
	addr, err := ipc.Locate(home, ipc.KindManage)
	if err != nil {
		t.Fatal(err)
	}
	l, err := ipc.Listen(addr, held{}, ipc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	wantBody := `{"body":"<>&","empty":[],"flag":false,"nil":null,"number":1.5,"raw":{"zero":0},"unsigned":18446744073709551615}` + "\n"
	wantResponse := " {\"body\":\"" + strings.Repeat("a", (1<<20)+1) + "\"}\n"
	s := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if r.URL.Path != "/v1/manage/known" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer credential" || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Idempotency-Key") != "" || r.Header.Get("X-Idempotency-Key") != "" || string(raw) != wantBody {
			t.Errorf("unexpected request: path %s, body %q", r.URL.Path, raw)
		}
		io.WriteString(w, wantResponse)
	})}
	done := make(chan error, 1)
	go func() { done <- s.Serve(l) }()
	t.Cleanup(func() { s.Close(); <-done })
	c := localClient(t, home)
	text := "<>&"
	body := map[string]any{"body": &text, "empty": []string{}, "flag": false, "nil": nil, "number": 1.5, "raw": json.RawMessage(`{"zero":0}`), "unsigned": ^uint64(0)}
	got, err := c.Call(context.Background(), localhttp.Request{Method: "known", Body: body, BearerToken: "credential", Timeout: time.Second * 10})
	if err != nil || string(got) != wantResponse {
		t.Fatalf("Call: response length %d, %v; want all %d unchanged bytes", len(got), err, len(wantResponse))
	}
}

func TestClientDoesNotFollowRedirects(t *testing.T) {
	home := socketHome(t)
	addr, err := ipc.Locate(home, ipc.KindManage)
	if err != nil {
		t.Fatal(err)
	}
	l, err := ipc.Listen(addr, held{}, ipc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	s := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Location", "http://elsewhere/private")
		w.WriteHeader(http.StatusTemporaryRedirect)
		io.WriteString(w, `{"error":{"category":"refused","code":"redirect_refused","message":"refused"}}`)
	})}
	done := make(chan error, 1)
	go func() { done <- s.Serve(l) }()
	t.Cleanup(func() { s.Close(); <-done })
	_, err = localClient(t, home).Call(context.Background(), localhttp.Request{Method: "known", Body: struct{}{}, BearerToken: "credential", Timeout: 10 * time.Second})
	if e := apiwire.FromError(err); e == nil || e.Code != "redirect_refused" || calls.Load() != 1 {
		t.Fatalf("Call: %v, calls %d; want original redirect answer without another request", err, calls.Load())
	}
}

// The error a newer daemon reports may carry members this client does not know; the
// category and code it has are still read, and acted on. An error body the client
// cannot read at all, from an answer that is not a success, tells nothing about
// whether the request took effect, so it stays an unknown outcome.
func TestClientReadsAnErrorBodyThatANewerDaemonExtended(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		wantCode   string // empty: an unknown outcome
	}{
		{"an unknown member of the body", `{"error":{"category":"refused","code":"too_large","message":"m"},"future":1}`, "too_large"},
		{"an unknown member of the error", `{"error":{"category":"refused","code":"too_large","message":"m","future":{"a":[1]}}}`, "too_large"},
		{"details", `{"error":{"category":"refused","code":"too_large","message":"m","details":{"a":{"b":1}}}}`, "too_large"},
		{"no code", `{"error":{"category":"refused","message":"m"}}`, ""},
		{"not an object", `[]`, ""},
		{"not JSON", `<html>`, ""},
		{"a repeated member", `{"error":{"category":"refused","code":"a","code":"b","message":"m"}}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := socketHome(t)
			addr, err := ipc.Locate(home, ipc.KindManage)
			if err != nil {
				t.Fatal(err)
			}
			l, err := ipc.Listen(addr, held{}, ipc.Options{})
			if err != nil {
				t.Fatal(err)
			}
			s := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusUnprocessableEntity)
				io.WriteString(w, tc.body)
			})}
			done := make(chan error, 1)
			go func() { done <- s.Serve(l) }()
			t.Cleanup(func() { s.Close(); <-done })
			_, err = localClient(t, home).Call(context.Background(), localhttp.Request{Method: "known", Body: struct{}{}, Timeout: 10 * time.Second})
			e := apiwire.FromError(err)
			if tc.wantCode == "" {
				if e.Code != apiwire.CodeOutcomeUnknown {
					t.Fatalf("Call: %v; want outcome_unknown", err)
				}
				return
			}
			if e.Category != apiwire.CategoryRefused || e.Code != tc.wantCode {
				t.Fatalf("Call: %v; want refused/%s", err, tc.wantCode)
			}
		})
	}
}

func FuzzClientRequestDocuments(f *testing.F) {
	for _, seed := range []string{`{}`, `{"body":"<>&"}`, `{"body":"\ud800"}`, `{"body":1,"body":2}`, "bad\xff"} {
		f.Add(seed)
	}
	// The path need not exist: valid documents reach the unavailable peer, while
	// invalid documents must fail locally instead of being normalized by JSON.
	c := localClient(f, socketHome(f))
	f.Fuzz(func(t *testing.T, text string) {
		var raw json.RawMessage
		validRaw := strictjson.Decode([]byte(text), &raw) == nil
		for _, tc := range []struct {
			body  any
			valid bool
		}{
			{json.RawMessage(text), validRaw},
			{map[string]any{text: []any{&text}}, utf8.ValidString(text)},
		} {
			_, err := c.Call(context.Background(), localhttp.Request{Method: "known", Body: tc.body, Timeout: time.Second})
			e := apiwire.FromError(err)
			want := "malformed_request"
			if tc.valid {
				want = "daemon_not_running"
			}
			if e == nil || e.Code != want {
				t.Fatalf("Call: %v; want %s", err, want)
			}
		}
	})
}

// A connection that is lost is not an internal error of the client. Whether the
// request was written or not, the caller is told the outcome is not known, which is
// what every caller that waits or sends again already acts on; a daemon that is not
// there and a context that ended keep their own words.
func TestClientReportsALostConnectionAsAnUnknownOutcome(t *testing.T) {
	listen := func(t *testing.T, home string, serve func(net.Conn)) {
		t.Helper()
		addr, err := ipc.Locate(home, ipc.KindManage)
		if err != nil {
			t.Fatal(err)
		}
		l, err := ipc.Listen(addr, held{}, ipc.Options{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { l.Close() })
		go func() {
			for {
				conn, err := l.Accept()
				if err != nil {
					return
				}
				go serve(conn)
			}
		}()
	}
	call := func(ctx context.Context, c *localhttp.Client) *apiwire.Error {
		_, err := c.Call(ctx, localhttp.Request{Method: "known", Body: struct{}{}, Timeout: 10 * time.Second})
		return apiwire.FromError(err)
	}

	t.Run("closed before the request is read", func(t *testing.T) {
		// Whether the client has written the request by the time it finds the
		// connection closed is a race, and the answer must be the same either way, so
		// the call is made often enough for both to happen.
		home := socketHome(t)
		listen(t, home, func(conn net.Conn) { conn.Close() })
		c := localClient(t, home)
		for i := range 300 {
			if e := call(context.Background(), c); e.Category != apiwire.CategoryOutcomeUnknown || e.Code != apiwire.CodeOutcomeUnknown {
				t.Fatalf("call %d: %v; want %s/%s", i, e, apiwire.CategoryOutcomeUnknown, apiwire.CodeOutcomeUnknown)
			}
		}
	})
	t.Run("closed after the request is read", func(t *testing.T) {
		home := socketHome(t)
		listen(t, home, func(conn net.Conn) {
			http.ReadRequest(bufio.NewReader(conn))
			conn.Close()
		})
		if e := call(context.Background(), localClient(t, home)); e.Category != apiwire.CategoryOutcomeUnknown {
			t.Fatalf("call: %v; want %s", e, apiwire.CategoryOutcomeUnknown)
		}
	})
	t.Run("no daemon", func(t *testing.T) {
		if e := call(context.Background(), localClient(t, socketHome(t))); e.Code != "daemon_not_running" {
			t.Fatalf("call: %v; want daemon_not_running", e)
		}
	})
	t.Run("a context that ended", func(t *testing.T) {
		home := socketHome(t)
		listen(t, home, func(conn net.Conn) { conn.Close() })
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if e := call(ctx, localClient(t, home)); e.Code != "request_canceled" {
			t.Fatalf("call: %v; want request_canceled", e)
		}
	})
}
