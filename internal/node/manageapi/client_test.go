package manageapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/ipc"
)

func managementClient(t testing.TB, handler http.HandlerFunc) *Client {
	t.Helper()
	home := newHome(t)
	addr, err := ipc.Locate(home, ipc.KindManage)
	if err != nil {
		t.Fatal(err)
	}
	l, err := ipc.Listen(addr, held{}, ipc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s := &http.Server{Handler: handler}
	done := make(chan error, 1)
	go func() { done <- s.Serve(l) }()
	t.Cleanup(func() { s.Close(); <-done })
	c, err := NewClient(ClientConfig{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func TestClientEnvelopesPreserveCompleteAnswers(t *testing.T) {
	var id admin.OperationID
	id[0], id[15] = 0xab, 0xff
	zero, version := admin.Version(0), admin.Version(17)
	for _, tc := range []struct {
		name, method, body, answer string
		in                         Request
	}{
		{"read without params", "node.show", `{}`, ` {"result":{"version":17}} `, Request{}},
		{"read params", "operation.result", `{"params":{"operation_id":"ab0000000000000000000000000000ff"}}`, `{"result":{"operation_id":"ab0000000000000000000000000000ff","result":{}}}`, Request{Params: map[string]any{"operation_id": "ab0000000000000000000000000000ff"}}},
		{"change", "node.set", `{"operation_id":"ab0000000000000000000000000000ff","expected_version":17,"params":{"settings":{"limit":18446744073709551615}}}`, ` {"replayed":false,"result":{"version":18},"operation_id":"ab0000000000000000000000000000ff"}` + "\n", Request{OperationID: id, ExpectedVersion: &version, Params: map[string]any{"settings": map[string]any{"limit": ^uint64(0)}}}},
		{"explicit zero version", "node.set", `{"operation_id":"ab0000000000000000000000000000ff","expected_version":0}`, `{"operation_id":"ab0000000000000000000000000000ff","replayed":true,"result":{}}`, Request{OperationID: id, ExpectedVersion: &zero}},
		{"install", "adapters.install", `{"operation_id":"ab0000000000000000000000000000ff","params":{"source":"/packages/relay"}}`, `{"operation_id":"ab0000000000000000000000000000ff","replayed":false,"result":{}}`, Request{OperationID: id, Params: json.RawMessage(`{"source":"/packages/relay"}`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			c := managementClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				body, err := io.ReadAll(r.Body)
				if err != nil || string(body) != tc.body+"\n" || r.URL.Path != "/v1/manage/"+tc.method || r.Header.Get("Authorization") != "" || r.Header.Get("Idempotency-Key") != "" || r.Header.Get("X-Idempotency-Key") != "" {
					t.Errorf("request = %s, body %s, %v", r.URL.Path, body, err)
				}
				io.WriteString(w, tc.answer)
			})
			got, err := c.Call(context.Background(), tc.method, tc.in)
			if err != nil || string(got) != tc.answer {
				t.Fatalf("Call = %s, %v; want complete unchanged %s", got, err, tc.answer)
			}
			if calls.Load() != 1 {
				t.Fatalf("calls = %d", calls.Load())
			}
		})
	}
}

func TestClientErrorsRetainUnknownOperation(t *testing.T) {
	var id admin.OperationID
	id[15] = 1
	for _, tc := range []struct {
		name                 string
		id                   admin.OperationID
		answer               *apiwire.Error
		details              map[string]any
		disconnect, canceled bool
	}{
		{name: "unknown written request", id: id, answer: apiwire.OutcomeUnknown(""), details: map[string]any{"operation_id": opID(1)}, disconnect: true},
		{name: "unknown read", answer: apiwire.OutcomeUnknown(""), disconnect: true},
		{
			name: "server unknown", id: id,
			answer:  &apiwire.Error{Category: apiwire.CategoryOutcomeUnknown, Code: apiwire.CodeOutcomeUnknown, Message: "the server cannot determine the outcome", Details: map[string]any{"reason": "failed"}},
			details: map[string]any{"reason": "failed", "operation_id": opID(1)},
		},
		{
			name: "storage failure", id: id,
			answer:  &apiwire.Error{Category: apiwire.CategoryOutcomeUnknown, Code: "storage_fatal", Message: "disk stopped", Details: map[string]any{"reason": "failed"}},
			details: map[string]any{"reason": "failed", "operation_id": opID(1)},
		},
		{name: "refusal", id: id, answer: apiwire.OperationUnknown()},
		{name: "before writing", id: id, answer: apiwire.FromError(context.Canceled), canceled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			c := managementClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if tc.disconnect {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					conn.Close()
					return
				}
				w.WriteHeader(tc.answer.Category.HTTPStatus())
				json.NewEncoder(w).Encode(apiwire.Body{Error: *tc.answer})
			})
			ctx := context.Background()
			if tc.canceled {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			_, err := c.Call(ctx, "node.set", Request{OperationID: tc.id})
			var got *apiwire.Error
			if !errors.As(err, &got) || got.Category != tc.answer.Category || got.Code != tc.answer.Code || got.Message != tc.answer.Message || !reflect.DeepEqual(got.Details, tc.details) {
				t.Fatalf("Call = %#v; want original category, code and message, details %#v", got, tc.details)
			}
			wantCalls := int32(1)
			if tc.canceled {
				wantCalls = 0
			}
			if calls.Load() != wantCalls {
				t.Fatalf("calls = %d; want %d, must never resend", calls.Load(), wantCalls)
			}
		})
	}
}

func TestClientMalformedAnswersAreUnknown(t *testing.T) {
	var id admin.OperationID
	id[15] = 1
	for _, body := range []string{
		``, `{`, `null`, `[]`, `{}`, `{"result":{}} {}`, `{"result":{},"result":{}}`,
		`{"result":{"value":"\ud800"}}`, "{\"result\":{\"value\":\"\xff\"}}",
		`{"result":{},"extra":1}`, `{"operation_id":"","result":{}}`, `{"error":{"category":"internal","code":"internal","message":"failed"}}`,
		`{"operation_id":"00000000000000000000000000000002","replayed":false,"result":{}}`,
		`{"operation_id":"00000000000000000000000000000001","result":{}}`,
		`{"operation_id":"00000000000000000000000000000001","replayed":null,"result":{}}`,
		`{"operation_id":"00000000000000000000000000000001","replayed":false}`,
	} {
		for _, operation := range []admin.OperationID{{}, id} {
			var calls atomic.Int32
			c := managementClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); io.WriteString(w, body) })
			got, err := c.Call(context.Background(), "node.set", Request{OperationID: operation})
			e := apiwire.FromError(err)
			if got != nil || e == nil || e.Category != apiwire.CategoryOutcomeUnknown || (operation == id && e.Details["operation_id"] != opID(1)) {
				t.Errorf("body %q, id %x: Call = %s, %v", body, operation, got, err)
			}
			if calls.Load() != 1 {
				t.Fatal("malformed response caused resend")
			}
		}
	}
}

func TestClientIncompleteAndMalformedErrorAnswersAreUnknown(t *testing.T) {
	var id admin.OperationID
	id[15] = 1
	for _, tc := range []struct {
		name   string
		status int
		length string
		body   string
	}{
		{"truncated success", 200, "100", `{"result":`},
		{"truncated error", 500, "100", `{"error":`},
		{"malformed error", 500, "", `{"error":{}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			c := managementClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if tc.length != "" {
					w.Header().Set("Content-Length", tc.length)
				}
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			})
			got, err := c.Call(context.Background(), "daemon.stop", Request{OperationID: id})
			e := apiwire.FromError(err)
			if got != nil || e == nil || e.Category != apiwire.CategoryOutcomeUnknown || e.Details["operation_id"] != opID(1) {
				t.Fatalf("Call = %s, %v", got, err)
			}
			if calls.Load() != 1 {
				t.Fatalf("calls = %d; must never resend", calls.Load())
			}
		})
	}
}

func FuzzClientResponse(f *testing.F) {
	for _, body := range []string{`{"result":{}}`, `{"operation_id":"00000000000000000000000000000001","replayed":false,"result":{}}`, `{}`, `null`, `{"result":{"x":"\ud800"}}`, `{"result":{},"result":{}}`} {
		f.Add([]byte(body))
	}
	var id admin.OperationID
	id[15] = 1
	f.Fuzz(func(t *testing.T, body []byte) {
		for _, operation := range []admin.OperationID{{}, id} {
			err := decodeResponse(body, operation)
			if err == nil {
				if !json.Valid(body) {
					t.Fatal("successful response was invalid")
				}
			} else if e := apiwire.FromError(err); e.Category != apiwire.CategoryOutcomeUnknown || (operation == id && e.Details["operation_id"] != opID(1)) {
				t.Fatalf("invalid response: %v", err)
			}
		}
	})
}
