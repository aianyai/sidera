package peer_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/sdk/go/peer"
)

// The peer sends only frames that the protocol's message schema accepts. The
// root module's TestPeerOutputMatchesSchema validates every line these tests
// make it write; the tests here pin each rule.

func TestNilParamsAreSentAsAnEmptyObject(t *testing.T) {
	leakGuard(t)
	p, r, _ := newRaw(t, peer.Params{}, nil)
	ctx := context.Background()
	go p.Call(ctx, "m", nil, nil)
	if f := r.recv(); string(f.Params) != "{}" {
		t.Fatalf("a call without params was sent with params %q, want {}", f.Params)
	}
	var nilMap map[string]int
	go p.Call(ctx, "m", nilMap, nil)
	if f := r.recv(); string(f.Params) != "{}" {
		t.Fatalf("a nil map was sent with params %q, want {}", f.Params)
	}
	if err := p.Notify(ctx, "n", nil); err != nil {
		t.Fatal(err)
	}
	if f := r.recv(); f.Method != "n" || string(f.Params) != "{}" {
		t.Fatalf("a notification without params was sent as %+v", f)
	}
	if err := p.Notify(ctx, "n", struct{}{}); err != nil {
		t.Fatal(err)
	}
	if f := r.recv(); string(f.Params) != "{}" {
		t.Fatalf("empty params were sent as %q", f.Params)
	}
}

func TestParamsThatAreNotAnObjectAreRefused(t *testing.T) {
	leakGuard(t)
	p, r, _ := newRaw(t, peer.Params{}, nil)
	// Nobody answers: a call that is wrongly sent ends by the deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	for name, params := range map[string]any{
		"a list": []int{1}, "an empty list": []string{},
		"a string": "x", "a number": 5, "a bool": true,
	} {
		if err := p.Call(ctx, "m", params, nil); !errors.Is(err, peer.ErrNotSent) || !peer.NoEffect(err) {
			t.Errorf("%s: Call = %v, want ErrNotSent", name, err)
		}
		if err := p.Notify(ctx, "m", params); !errors.Is(err, peer.ErrNotSent) {
			t.Errorf("%s: Notify = %v, want ErrNotSent", name, err)
		}
	}
	r.expectNoLineFor(100 * time.Millisecond)
}

func TestMethodNameLength(t *testing.T) {
	leakGuard(t)
	p, r, _ := newRaw(t, peer.Params{}, nil)
	ctx := context.Background()
	for _, tc := range []struct {
		name, method string
		ok           bool
	}{
		{"empty", "", false},
		{"one character", "m", true},
		{"128 characters", strings.Repeat("m", 128), true},
		{"129 characters", strings.Repeat("m", 129), false},
		{"128 characters of two bytes", strings.Repeat("é", 128), true},
		{"129 characters of two bytes", strings.Repeat("é", 129), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			notifyErr := p.Notify(ctx, tc.method, nil)
			if tc.ok {
				if notifyErr != nil {
					t.Fatalf("Notify = %v", notifyErr)
				}
				if f := r.recv(); f.Method != tc.method {
					t.Fatalf("sent %q", f.Method)
				}
				return
			}
			if !errors.Is(notifyErr, peer.ErrNotSent) {
				t.Fatalf("Notify = %v, want ErrNotSent", notifyErr)
			}
			if err := p.Call(ctx, tc.method, nil, nil); !errors.Is(err, peer.ErrNotSent) || !peer.NoEffect(err) {
				t.Fatalf("Call = %v, want ErrNotSent", err)
			}
			r.expectNoLineFor(50 * time.Millisecond)
		})
	}
}

func TestHandlerResultMustBeAnObject(t *testing.T) {
	leakGuard(t)
	var result any
	_, r, logs := newRaw(t, peer.Params{}, func(p *peer.Peer) {
		p.Handle("m", func(context.Context, json.RawMessage) (any, error) { return result, nil })
	})
	for _, tc := range []struct {
		name   string
		result any
		want   string // the result as sent, empty for an internal error
	}{
		{"an empty struct", struct{}{}, `{}`},
		{"a map", map[string]int{"n": 1}, `{"n":1}`},
		{"a struct", struct {
			N int `json:"n"`
		}{2}, `{"n":2}`},
		{"a raw object", json.RawMessage(`{"a":[1,2]}`), `{"a":[1,2]}`},
		{"nil", nil, ""},
		{"a nil map still marshals to null", map[string]int(nil), ""},
		{"a string", "text", ""},
		{"a number", 7, ""},
		{"a bool", true, ""},
		{"a list", []int{1}, ""},
		{"a raw list", json.RawMessage(`[1]`), ""},
		{"a raw null", json.RawMessage(`null`), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result = tc.result
			r.send(`{"jsonrpc":"2.0","id":1,"method":"m"}`)
			f := r.recv()
			if tc.want != "" {
				if f.Error != nil || string(f.Result) != tc.want {
					t.Fatalf("got %+v, want result %s", f, tc.want)
				}
				return
			}
			if f.errCode() != peer.CodeInternalError || f.Error.Message != "internal error" || f.Result != nil {
				t.Fatalf("got %+v, want a generic -32603", f)
			}
		})
	}
	if !strings.Contains(logs.String(), "result is not a JSON object") {
		t.Errorf("the refusal was not logged: %s", logs)
	}
}

func TestTypedNilErrorIsAnInternalError(t *testing.T) {
	leakGuard(t)
	_, r, _ := newRaw(t, peer.Params{}, func(p *peer.Peer) {
		p.Handle("nil", func(context.Context, json.RawMessage) (any, error) {
			var e *peer.Error
			return nil, e // a non-nil error interface holding a nil pointer
		})
		p.Handle("wrapped", func(context.Context, json.RawMessage) (any, error) {
			var e *peer.Error
			return nil, errors.Join(errors.New("context"), e)
		})
		p.Handle("ping", pingHandler)
	})
	for _, method := range []string{"nil", "wrapped"} {
		r.send(`{"jsonrpc":"2.0","id":1,"method":"` + method + `"}`)
		if f := r.recv(); f.errCode() != peer.CodeInternalError || f.Error.Message != "internal error" {
			t.Fatalf("%s: got %+v, want -32603", method, f)
		}
	}
	r.send(`{"jsonrpc":"2.0","id":2,"method":"ping"}`)
	if f := r.recv(); f.Error != nil {
		t.Fatalf("the peer did not survive: %+v", f)
	}
}

// Whatever error a handler returns, the reply has a code, a message of 1 to 1024
// characters and data that is an object or absent.
func TestErrorRepliesFitTheSchema(t *testing.T) {
	leakGuard(t)
	var returned error
	_, r, _ := newRaw(t, peer.Params{MaxLineBytes: 10000}, func(p *peer.Peer) {
		p.Handle("m", func(context.Context, json.RawMessage) (any, error) { return nil, returned })
	})
	long := strings.Repeat("é", 1500)
	for _, tc := range []struct {
		name     string
		err      *peer.Error
		code     int
		message  string // exact, or "" to skip
		wantData string // "" for none
		check    func(t *testing.T, f frame)
	}{
		{"empty message gets the name of the code", &peer.Error{Code: peer.CodeInvalidParams}, peer.CodeInvalidParams, "invalid_params", "", nil},
		{"empty message of an application code", &peer.Error{Code: 1003}, 1003, "error", "", nil},
		{"message at the limit is kept", peer.NewError(1003, strings.Repeat("m", 1024)), 1003, strings.Repeat("m", 1024), "", nil},
		{"message over the limit is cut at a character", peer.NewError(1003, long), 1003, "", "", func(t *testing.T, f frame) {
			if got := []rune(f.Error.Message); len(got) != 1024 || string(got) != string([]rune(long)[:1024]) {
				t.Errorf("message of %d characters", len(got))
			}
		}},
		{"data on invalid params", peer.NewError(peer.CodeInvalidParams, "bad").WithData(map[string]string{"field": "x"}), peer.CodeInvalidParams, "bad", `{"field":"x"}`, nil},
		{"data on an internal error", peer.NewError(peer.CodeInternalError, "bad").WithData(map[string]int{"n": 1}), peer.CodeInternalError, "bad", `{"n":1}`, nil},
		{"data on an application code", peer.NewError(1005, "bad").WithData(struct{}{}), 1005, "bad", `{}`, nil},
		{"data that is not an object is left out", &peer.Error{Code: 1003, Message: "bad", Data: json.RawMessage(`"text"`)}, 1003, "bad", "", nil},
		{"data that is not JSON is left out", &peer.Error{Code: 1003, Message: "bad", Data: json.RawMessage(`{oops`)}, 1003, "bad", "", nil},
		{"data of a list is left out", &peer.Error{Code: 1003, Message: "bad", Data: json.RawMessage(`[1]`)}, 1003, "bad", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			returned = tc.err
			r.send(`{"jsonrpc":"2.0","id":1,"method":"m"}`)
			f := r.recv()
			if f.errCode() != tc.code {
				t.Fatalf("got %+v, want code %d", f, tc.code)
			}
			if tc.message != "" && f.Error.Message != tc.message {
				t.Errorf("message %q, want %q", f.Error.Message, tc.message)
			}
			if string(f.Error.Data) != tc.wantData {
				t.Errorf("data %q, want %q", f.Error.Data, tc.wantData)
			}
			if tc.check != nil {
				tc.check(t, f)
			}
		})
	}
}

func TestWithDataKeepsOnlyObjects(t *testing.T) {
	for _, code := range []int{peer.CodeInvalidParams, peer.CodeInternalError, peer.CodeUnsupportedProtocolVersion, 1001, 1009} {
		e := peer.NewError(code, "x").WithData(map[string]string{"a": "b"})
		if string(e.Data) != `{"a":"b"}` || e.Code != code {
			t.Errorf("code %d: %+v", code, e)
		}
	}
	base := peer.NewError(1003, "x").WithData(map[string]int{"n": 1})
	for name, data := range map[string]any{"a string": "s", "a list": []int{1}, "null": nil, "a channel": make(chan int)} {
		if e := base.WithData(data); e.Data != nil {
			t.Errorf("%s was kept: %s", name, e.Data)
		}
	}
	if base.Data == nil {
		t.Error("WithData changed its receiver")
	}
}

// A reply, error or result, that does not fit the line limit is an internal
// error, so the other side never has to read a line over the limit it enforces.
func TestErrorReplyTooLargeIsAnInternalError(t *testing.T) {
	leakGuard(t)
	_, r, _ := newRaw(t, peer.Params{MaxLineBytes: 300}, func(p *peer.Peer) {
		p.Handle("m", func(context.Context, json.RawMessage) (any, error) {
			return nil, peer.NewError(1003, "bad").WithData(map[string]string{"s": strings.Repeat("x", 500)})
		})
		p.Handle("ok", func(context.Context, json.RawMessage) (any, error) {
			return nil, peer.NewError(1003, "bad").WithData(map[string]string{"s": strings.Repeat("x", 100)})
		})
	})
	r.send(`{"jsonrpc":"2.0","id":1,"method":"m"}`)
	if f := r.recv(); f.errCode() != peer.CodeInternalError || f.Error.Data != nil {
		t.Fatalf("got %+v, want an internal error without the data", f)
	}
	r.send(`{"jsonrpc":"2.0","id":2,"method":"ok"}`)
	if f := r.recv(); f.errCode() != 1003 || f.Error.Data == nil {
		t.Fatalf("an error that fits was replaced: %+v", f)
	}
}
