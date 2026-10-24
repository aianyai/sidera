package protocol_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
)

// requestIDCases is the request id rule written out once more, for the tests to
// hold the schema and the Go side to. schema is whether the schema accepts the
// id, wire whether the Go side does. They differ in exactly two places, which
// JSON Schema cannot tell apart from integers: a number spelled with a fraction
// or an exponent. Go refuses them because "1", "1.0" and "1e0" would be three
// spellings of one in-flight request.
var requestIDCases = []struct {
	name         string
	id           string
	schema, wire bool
}{
	{"zero", `0`, true, true},
	{"negative zero", `-0`, true, true},
	{"small", `7`, true, true},
	{"negative", `-12`, true, true},
	{"largest exact", `9007199254740991`, true, true},
	{"smallest exact", `-9007199254740991`, true, true},
	{"first beyond the exact range", `9007199254740992`, false, false},
	{"first negative beyond the exact range", `-9007199254740992`, false, false},
	{"largest int64", `9223372036854775807`, false, false},
	{"beyond int64", `99999999999999999999`, false, false},
	{"integral fraction", `1.0`, true, false},
	{"exponent", `1e2`, true, false},
	{"fraction", `1.5`, false, false},
	{"null", `null`, false, false},
	{"boolean", `true`, false, false},
	{"array", `[1]`, false, false},
	{"object", `{}`, false, false},
	{"one character", `"a"`, true, true},
	{"empty string", `""`, false, false},
	{"128 characters", `"` + strings.Repeat("a", 128) + `"`, true, true},
	{"129 characters", `"` + strings.Repeat("a", 129) + `"`, false, false},
	{"128 two-byte characters", `"` + strings.Repeat("é", 128) + `"`, true, true},
	{"129 two-byte characters", `"` + strings.Repeat("é", 129) + `"`, false, false},
	{"128 characters outside the basic plane", `"` + strings.Repeat("😀", 128) + `"`, true, true},
	{"129 characters outside the basic plane", `"` + strings.Repeat("😀", 129) + `"`, false, false},
	{"128 escaped characters", `"` + strings.Repeat(`é`, 128) + `"`, true, true},
	{"129 escaped characters", `"` + strings.Repeat(`é`, 129) + `"`, false, false},
}

// TestRequestIDRuleMatchesSchema: the schema and the Go rule for a request id
// agree, at the id itself, in a request frame and in the params of
// $/cancel_request, and the peer and the message types apply the one rule.
func TestRequestIDRuleMatchesSchema(t *testing.T) {
	s := schemas(t)
	wire := newWire(t)
	for _, tc := range requestIDCases {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := parse([]byte(tc.id))
			if err != nil {
				t.Fatal(err)
			}
			if got := s.accepts(t, "types.schema.json#/$defs/RequestID", doc) == nil; got != tc.schema {
				t.Errorf("the schema accepts the id: %v, want %v", got, tc.schema)
			}
			frame := `{"jsonrpc":"2.0","id":` + tc.id + `,"method":"ping","params":{}}`
			if err := schemaVerdict(t, "frame", "", []byte(frame)); (err == nil) != tc.schema {
				t.Errorf("the schema accepts a request with the id: %v, want %v", err == nil, tc.schema)
			}
			cancel := `{"jsonrpc":"2.0","method":"$/cancel_request","params":{"request_id":` + tc.id + `}}`
			if err := schemaVerdict(t, "notification", "$/cancel_request", []byte(cancel)); (err == nil) != tc.schema {
				t.Errorf("the schema accepts a cancel with the id: %v, want %v", err == nil, tc.schema)
			}

			var id peer.RequestID
			if err := json.Unmarshal([]byte(tc.id), &id); (err == nil) != tc.wire {
				t.Errorf("peer.RequestID accepts the id: %v, want %v", err == nil, tc.wire)
			}
			var cancelParams adapterproto.CancelRequestParams
			if err := adapterproto.Decode([]byte(`{"request_id":`+tc.id+`}`), &cancelParams); (err == nil) != tc.wire {
				t.Errorf("adapterproto accepts the id in cancel params: %v, want %v", err == nil, tc.wire)
			}
			if got := wire.answersRequest(t, frame); got != tc.wire {
				t.Errorf("the peer answers a request with the id: %v, want %v", got, tc.wire)
			}
		})
	}
}

// wireTester feeds frames to a peer that answers every "ping".
type wireTester struct {
	toPeer *io.PipeWriter
	lines  *bufio.Reader
}

func newWire(t *testing.T) *wireTester {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	p, err := peer.New(inR, outW, peer.Config{})
	if err != nil {
		t.Fatal(err)
	}
	p.Handle("ping", func(_ context.Context, _ json.RawMessage) (any, error) { return struct{}{}, nil })
	p.Start()
	t.Cleanup(func() {
		p.Close()
		inW.Close()
		outR.Close()
	})
	return &wireTester{toPeer: inW, lines: bufio.NewReader(outR)}
}

// answersRequest sends one frame and reports whether the peer took it as a
// request and answered it, as against refusing it as an invalid request.
func (w *wireTester) answersRequest(t *testing.T, frame string) bool {
	t.Helper()
	done := make(chan struct{})
	var reply struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	var readErr error
	go func() {
		defer close(done)
		line, err := w.lines.ReadBytes('\n')
		if err != nil {
			readErr = err
			return
		}
		readErr = json.Unmarshal(line, &reply)
	}()
	if _, err := w.toPeer.Write([]byte(frame + "\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the peer did not answer")
	}
	if readErr != nil {
		t.Fatal(readErr)
	}
	switch {
	case reply.Error == nil:
		return true
	case reply.Error.Code == peer.CodeInvalidRequest:
		return false
	}
	t.Fatalf("the peer answered with code %d", reply.Error.Code)
	return false
}
