package peer

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// verdict is the outcome of classifying a line, reduced to what the wire
// shows: a kind of message, or the error code the line earns.
type verdict struct {
	kind kind
	code int
}

// oracleClassify decides the same question with a different decoder
// (generic values rather than raw members), written from the rules alone.
func oracleClassify(line []byte) verdict {
	if !utf8.Valid(line) || !json.Valid(line) {
		return verdict{code: CodeParseError}
	}
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return verdict{code: CodeParseError}
	}
	m, ok := v.(map[string]any)
	if !ok || repeatsTopLevelMember(line) {
		return verdict{code: CodeInvalidRequest}
	}
	if m["jsonrpc"] != "2.0" {
		return verdict{code: CodeInvalidRequest}
	}
	validID := func(id any) bool {
		switch x := id.(type) {
		case string:
			n := utf8.RuneCountInString(x)
			return n >= 1 && n <= 128
		case json.Number:
			n, err := strconv.ParseInt(string(x), 10, 64)
			return err == nil && n >= -(1<<53-1) && n <= 1<<53-1
		}
		return false
	}
	method, hasMethod := m["method"]
	_, hasResult := m["result"]
	_, hasErr := m["error"]
	id, hasID := m["id"]
	if hasMethod {
		if s, isString := method.(string); !isString || s == "" || hasResult || hasErr {
			return verdict{code: CodeInvalidRequest}
		}
		if !hasID {
			return verdict{kind: kindNotification}
		}
		if !validID(id) {
			return verdict{code: CodeInvalidRequest}
		}
		return verdict{kind: kindRequest}
	}
	if (!hasResult && !hasErr) || !hasID || (id != nil && !validID(id)) {
		return verdict{code: CodeInvalidRequest}
	}
	return verdict{kind: kindResponse}
}

// repeatsTopLevelMember reports whether the object in line names one of its own
// members twice. It walks the tokens and counts depth by hand, without sharing
// anything with the code under test.
func repeatsTopLevelMember(line []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(line))
	depth := 0
	seen := map[string]bool{}
	expectKey := false
	for {
		tok, err := dec.Token()
		if err != nil {
			return false
		}
		switch x := tok.(type) {
		case json.Delim:
			switch x {
			case '{', '[':
				depth++
				expectKey = depth == 1 && x == '{'
			default:
				depth--
				expectKey = depth == 1 // a nested value ended: the next token is a key
			}
			if depth == 0 {
				return false
			}
			continue
		case string:
			if depth == 1 && expectKey {
				if seen[x] {
					return true
				}
				seen[x] = true
				expectKey = false
				continue
			}
		}
		// A value at depth 1 ends the pair; the next token is a key again.
		if depth == 1 {
			expectKey = true
		}
	}
}

var classifyCorpus = []struct {
	name string
	line string
}{
	{"request with number id", `{"jsonrpc":"2.0","id":1,"method":"m","params":{"a":1}}`},
	{"request with string id", `{"jsonrpc":"2.0","id":"x","method":"m"}`},
	{"request with array params", `{"jsonrpc":"2.0","id":1,"method":"m","params":[1,2]}`},
	{"notification", `{"jsonrpc":"2.0","method":"m","params":{}}`},
	{"result response", `{"jsonrpc":"2.0","id":1,"result":{}}`},
	{"null result response", `{"jsonrpc":"2.0","id":1,"result":null}`},
	{"error response", `{"jsonrpc":"2.0","id":1,"error":{"code":-32001,"message":"overloaded"}}`},
	{"error response with null id", `{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"x"}}`},
	{"leading whitespace", ` 	{"jsonrpc":"2.0","method":"m"}`},
	{"batch", `[{"jsonrpc":"2.0","method":"m"}]`},
	{"empty batch", `[]`},
	{"scalar", `7`},
	{"string", `"x"`},
	{"null", `null`},
	{"invalid json", `{"jsonrpc":`},
	{"empty line", ``},
	{"trailing garbage", `{"jsonrpc":"2.0","method":"m"} x`},
	{"invalid utf-8", "{\"jsonrpc\":\"2.0\",\"method\":\"m\",\"params\":{\"s\":\"\xff\"}}"},
	{"missing jsonrpc", `{"id":1,"method":"m"}`},
	{"wrong version", `{"jsonrpc":"2.1","id":1,"method":"m"}`},
	{"escaped version is still 2.0", `{"jsonrpc":"2.0","method":"m"}`},
	{"capitalised member", `{"jsonrpc":"2.0","Method":"m"}`},
	{"request with null id", `{"jsonrpc":"2.0","id":null,"method":"m"}`},
	{"request with fractional id", `{"jsonrpc":"2.0","id":1.5,"method":"m"}`},
	{"request with exponent id", `{"jsonrpc":"2.0","id":1e0,"method":"m"}`},
	{"request with huge id", `{"jsonrpc":"2.0","id":99999999999999999999,"method":"m"}`},
	{"request with the largest exact id", `{"jsonrpc":"2.0","id":9007199254740991,"method":"m"}`},
	{"request with the smallest exact id", `{"jsonrpc":"2.0","id":-9007199254740991,"method":"m"}`},
	{"request with id beyond the exact range", `{"jsonrpc":"2.0","id":9007199254740992,"method":"m"}`},
	{"request with negative id beyond the exact range", `{"jsonrpc":"2.0","id":-9007199254740992,"method":"m"}`},
	{"request with the largest int64 id", `{"jsonrpc":"2.0","id":9223372036854775807,"method":"m"}`},
	{"request with empty string id", `{"jsonrpc":"2.0","id":"","method":"m"}`},
	{"request with 128-character string id", `{"jsonrpc":"2.0","id":"` + strings.Repeat("é", 128) + `","method":"m"}`},
	{"request with 129-character string id", `{"jsonrpc":"2.0","id":"` + strings.Repeat("é", 129) + `","method":"m"}`},
	{"response with id beyond the exact range", `{"jsonrpc":"2.0","id":9007199254740992,"result":{}}`},
	{"response with empty string id", `{"jsonrpc":"2.0","id":"","result":{}}`},
	{"request with object id", `{"jsonrpc":"2.0","id":{},"method":"m"}`},
	{"request with bool id", `{"jsonrpc":"2.0","id":true,"method":"m"}`},
	{"empty method", `{"jsonrpc":"2.0","method":""}`},
	{"numeric method", `{"jsonrpc":"2.0","method":1}`},
	{"method and result", `{"jsonrpc":"2.0","id":1,"method":"m","result":1}`},
	{"method and error", `{"jsonrpc":"2.0","id":1,"method":"m","error":{}}`},
	{"neither method nor result", `{"jsonrpc":"2.0","id":1}`},
	{"response without id", `{"jsonrpc":"2.0","result":{}}`},
	{"response with bool id", `{"jsonrpc":"2.0","id":false,"result":{}}`},
	{"result and error", `{"jsonrpc":"2.0","id":1,"result":{},"error":{"code":1,"message":"x"}}`},
	{"error without code", `{"jsonrpc":"2.0","id":1,"error":{"message":"x"}}`},
	{"repeated jsonrpc", `{"jsonrpc":"2.0","jsonrpc":"2.0","method":"m"}`},
	{"repeated jsonrpc with a wrong first value", `{"jsonrpc":"1.0","jsonrpc":"2.0","method":"m"}`},
	{"repeated id", `{"jsonrpc":"2.0","id":1,"id":2,"method":"m"}`},
	{"repeated method", `{"jsonrpc":"2.0","method":"a","method":"b"}`},
	{"repeated params", `{"jsonrpc":"2.0","method":"m","params":{},"params":[]}`},
	{"repeated result", `{"jsonrpc":"2.0","id":1,"result":{},"result":{}}`},
	{"repeated member inside params is the next layer's business", `{"jsonrpc":"2.0","method":"m","params":{"a":1,"a":2}}`},
	{"same member name at two depths", `{"jsonrpc":"2.0","method":"m","params":{"method":"x","jsonrpc":"y"}}`},
	{"deeply nested params", `{"jsonrpc":"2.0","method":"m","params":` + string(bytes.Repeat([]byte("["), 20000)) + string(bytes.Repeat([]byte("]"), 20000)) + `}`},
}

func TestClassify(t *testing.T) {
	for _, tc := range classifyCorpus {
		t.Run(tc.name, func(t *testing.T) {
			msg, cerr := classify([]byte(tc.line))
			want := oracleClassify([]byte(tc.line))
			got := verdict{kind: msg.kind}
			if cerr != nil {
				got = verdict{code: cerr.err.Code}
			}
			if got != want {
				t.Fatalf("classify = %+v, oracle = %+v", got, want)
			}
		})
	}
}

// TestClassifyDetails pins what the oracle does not compare: the id echoed in
// an error reply, the canonical id key and the handling of params.
func TestClassifyDetails(t *testing.T) {
	t.Run("error reply echoes the id of a request and of nothing else", func(t *testing.T) {
		for line, want := range map[string]string{
			// A message with "method" is a request: its id is echoed.
			`{"id":7,"method":"m"}`:                             "7",
			`{"id":"a","jsonrpc":"1.0","method":"m"}`:           `"a"`,
			`{"id":1.5,"jsonrpc":"1.0","method":"m"}`:           "",
			`{"jsonrpc":"2.0","id":null,"method":"m"}`:          "",
			`{"jsonrpc":"2.0","id":3,"method":"m","result":{}}`: "3",
			`{"jsonrpc":"2.0","id":3,"method":""}`:              "3",
			// Without "method" it may be a response to a call of the other side:
			// answering with its id would land in their call-id space.
			`{"jsonrpc":"2.0","id":3}`:                     "",
			`{"id":3,"result":{}}`:                         "",
			`{"jsonrpc":"1.0","id":3,"result":{}}`:         "",
			`{"jsonrpc":"2.0","id":3,"Method":"m"}`:        "",
			`{"id":3,"error":{"code":1001,"message":"x"}}`: "",
			`not json`: "",
			`[{"jsonrpc":"2.0","id":3,"method":"m"}]`: "",
		} {
			_, cerr := classify([]byte(line))
			if cerr == nil {
				t.Fatalf("%s classified as a message", line)
			}
			if string(cerr.id) != want {
				t.Errorf("%s: reply id %q, want %q", line, cerr.id, want)
			}
		}
	})
	t.Run("equal ids share a key", func(t *testing.T) {
		a, _ := classify([]byte(`{"jsonrpc":"2.0","id":"a","method":"m"}`))
		b, _ := classify([]byte(`{"jsonrpc":"2.0","id":"a","method":"m"}`))
		c, _ := classify([]byte(`{"jsonrpc":"2.0","id":-0,"method":"m"}`))
		d, _ := classify([]byte(`{"jsonrpc":"2.0","id":0,"method":"m"}`))
		e, _ := classify([]byte(`{"jsonrpc":"2.0","id":"0","method":"m"}`))
		if a.key != b.key || c.key != d.key || d.key == e.key || a.key == "" {
			t.Fatalf("keys: %q %q %q %q %q", a.key, b.key, c.key, d.key, e.key)
		}
		if string(b.id) != `"a"` {
			t.Fatalf("the id must be echoed as received, got %s", b.id)
		}
	})
	t.Run("null params are absent", func(t *testing.T) {
		m, _ := classify([]byte(`{"jsonrpc":"2.0","method":"m","params":null}`))
		if m.params != nil {
			t.Fatalf("params = %q", m.params)
		}
	})
	t.Run("malformed responses keep their id", func(t *testing.T) {
		for _, line := range []string{
			`{"jsonrpc":"2.0","id":4,"result":{},"error":{"code":1,"message":"x"}}`,
			`{"jsonrpc":"2.0","id":4,"error":"nope"}`,
			`{"jsonrpc":"2.0","id":4,"error":{"code":"x"}}`,
		} {
			m, cerr := classify([]byte(line))
			if cerr != nil || m.kind != kindResponse || !m.malformed || m.key != "n4" {
				t.Errorf("%s: %+v %v", line, m, cerr)
			}
		}
		m, _ := classify([]byte(`{"jsonrpc":"2.0","id":null,"result":{}}`))
		if !m.malformed || m.key != "" {
			t.Errorf("a result with a null id: %+v", m)
		}
	})
}

func FuzzClassify(f *testing.F) {
	for _, tc := range classifyCorpus {
		f.Add([]byte(tc.line))
	}
	f.Fuzz(func(t *testing.T, line []byte) {
		msg, cerr := classify(line)
		want := oracleClassify(line)
		if cerr != nil {
			if msg.kind != 0 {
				t.Fatalf("both a message and an error for %q", line)
			}
			if cerr.err == nil || (cerr.err.Code != CodeParseError && cerr.err.Code != CodeInvalidRequest) {
				t.Fatalf("undefined error %+v for %q", cerr.err, line)
			}
			if want != (verdict{code: cerr.err.Code}) {
				t.Fatalf("classify = error %d, oracle = %+v for %q", cerr.err.Code, want, line)
			}
			if cerr.id != nil && !json.Valid(cerr.id) {
				t.Fatalf("reply id %q is not JSON", cerr.id)
			}
			// The reply built from it must itself be a valid single line.
			reply := errorLine(cerr.id, cerr.err)
			if bytes.Count(reply, []byte("\n")) != 1 || reply[len(reply)-1] != '\n' || !json.Valid(reply) {
				t.Fatalf("bad reply %q", reply)
			}
			return
		}
		if want != (verdict{kind: msg.kind}) {
			t.Fatalf("classify = kind %d, oracle = %+v for %q", msg.kind, want, line)
		}
		switch msg.kind {
		case kindRequest:
			if msg.method == "" || msg.key == "" || !json.Valid(msg.id) {
				t.Fatalf("incomplete request %+v", msg)
			}
		case kindNotification:
			if msg.method == "" || msg.id != nil {
				t.Fatalf("incomplete notification %+v", msg)
			}
		case kindResponse:
			if msg.key == "" && string(msg.id) != "null" {
				t.Fatalf("response without a usable id %+v", msg)
			}
		default:
			t.Fatalf("unknown kind %d", msg.kind)
		}
	})
}
