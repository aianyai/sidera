package peer

import (
	"bytes"
	"encoding/json"
	"testing"
	"unicode/utf8"

	"github.com/aianyai/sidera/sdk/go/strictjson"
)

var documentCorpus = []struct {
	name, line  string
	defaultCode int
	strictCode  int
	strictID    string
}{
	{"top duplicate", `{"jsonrpc":"2.0","id":7,"method":"m","future":1,"future":2}`, CodeInvalidRequest, CodeInvalidRequest, "7"},
	{"nested duplicate", `{"jsonrpc":"2.0","id":7,"method":"m","params":{"x":{"a":1,"a":2}}}`, 0, CodeInvalidRequest, "7"},
	{"array duplicate", `{"jsonrpc":"2.0","id":7,"method":"m","params":[{"a":1,"a":2}]}`, 0, CodeInvalidRequest, "7"},
	{"unknown extension duplicate", `{"jsonrpc":"2.0","id":7,"method":"m","future":{"a":1,"a":2}}`, 0, CodeInvalidRequest, "7"},
	{"unknown method duplicate", `{"jsonrpc":"2.0","id":7,"method":"unknown","future":{"a":1,"a":2}}`, 0, CodeInvalidRequest, "7"},
	{"notification duplicate", `{"jsonrpc":"2.0","method":"m","future":{"a":1,"a":2}}`, 0, CodeInvalidRequest, ""},
	{"response duplicate", `{"jsonrpc":"2.0","id":7,"result":{"a":1,"a":2}}`, 0, CodeInvalidRequest, ""},
	{"error response duplicate", `{"jsonrpc":"2.0","id":7,"error":{"code":1,"data":{"a":1,"a":2}}}`, 0, CodeInvalidRequest, ""},
	{"duplicate method", `{"jsonrpc":"2.0","id":7,"method":"m","method":"m"}`, CodeInvalidRequest, CodeInvalidRequest, ""},
	{"escaped duplicate method", `{"jsonrpc":"2.0","id":7,"method":"m","m\u0065thod":"m"}`, CodeInvalidRequest, CodeInvalidRequest, ""},
	{"unknown duplicate before method duplicate", `{"future":1,"future":2,"id":7,"method":"m","method":"m"}`, CodeInvalidRequest, CodeInvalidRequest, ""},
	{"non-string method", `{"jsonrpc":"2.0","id":7,"method":1,"future":{"a":1,"a":2}}`, CodeInvalidRequest, CodeInvalidRequest, ""},
	{"null method", `{"jsonrpc":"2.0","id":7,"method":null,"future":{"a":1,"a":2}}`, CodeInvalidRequest, CodeInvalidRequest, ""},
	{"method name case", `{"jsonrpc":"2.0","id":7,"Method":"m","future":{"a":1,"a":2}}`, CodeInvalidRequest, CodeInvalidRequest, ""},
	{"nested method is not a request", `{"jsonrpc":"2.0","id":7,"result":{"method":"m","a":1,"a":2}}`, 0, CodeInvalidRequest, ""},
	{"empty string method", `{"jsonrpc":"2.0","id":7,"method":"","future":{"a":1,"a":2}}`, CodeInvalidRequest, CodeInvalidRequest, "7"},
	{"duplicate before bad version", `{"jsonrpc":"1.0","id":7,"method":"m","future":{"a":1,"a":2}}`, CodeInvalidRequest, CodeInvalidRequest, "7"},
	{"surrogate value", `{"jsonrpc":"2.0","id":7,"method":"m","future":"\ud800"}`, 0, CodeInvalidRequest, "7"},
	{"surrogate key", `{"jsonrpc":"2.0","id":7,"method":"m","future":{"\ud800":1}}`, 0, CodeInvalidRequest, "7"},
	{"surrogate id", `{"jsonrpc":"2.0","id":"\ud800","method":"m"}`, 0, CodeInvalidRequest, ""},
	{"duplicate id", `{"jsonrpc":"2.0","id":7,"id":8,"method":"m"}`, CodeInvalidRequest, CodeInvalidRequest, ""},
	{"escaped duplicate id", `{"jsonrpc":"2.0","id":7,"i\u0064":7,"method":"m"}`, CodeInvalidRequest, CodeInvalidRequest, ""},
	{"unknown duplicate before id duplicate", `{"future":1,"future":2,"jsonrpc":"2.0","id":7,"id":8,"method":"m"}`, CodeInvalidRequest, CodeInvalidRequest, ""},
	{"id duplicate before unknown duplicate", `{"id":7,"id":8,"future":1,"future":2,"jsonrpc":"2.0","method":"m"}`, CodeInvalidRequest, CodeInvalidRequest, ""},
	{"nested id does not duplicate top id", `{"jsonrpc":"2.0","id":7,"method":"m","future":{"id":1,"id":2}}`, 0, CodeInvalidRequest, "7"},
	{"escaped string id", `{"jsonrpc":"2.0","id":"a\u0062","method":"m","future":{"a":1,"a":2}}`, 0, CodeInvalidRequest, `"a\u0062"`},
	{"negative zero id", `{"jsonrpc":"2.0","id":-0,"method":"m","future":{"a":1,"a":2}}`, 0, CodeInvalidRequest, "-0"},
	{"invalid id", `{"jsonrpc":"2.0","id":1e0,"method":"m","future":{"a":1,"a":2}}`, CodeInvalidRequest, CodeInvalidRequest, ""},
	{"null id", `{"jsonrpc":"2.0","id":null,"method":"m","future":{"a":1,"a":2}}`, CodeInvalidRequest, CodeInvalidRequest, ""},
	{"huge unknown number before id", `{"future":1e700,"id":7,"jsonrpc":"2.0","method":"m","params":{"a":1,"a":2}}`, 0, CodeInvalidRequest, "7"},
	{"unknown values", `{"jsonrpc":"2.0","id":7,"method":"m","future":[null,true,1e700,"x",{},[]]}`, 0, 0, ""},
	{"unknown object response", `{"jsonrpc":"2.0","id":7,"result":{},"future":{"id":null}}`, 0, 0, ""},
	{"valid Unicode", `{"jsonrpc":"2.0","id":7,"method":"m","future":["\ud83d\ude00","\ufffd","\\ud800"]}`, 0, 0, ""},
	{"syntax error before duplicate", `{"jsonrpc":"2.0","id":7,"method":"m","future":{"a":1,"a":2},}`, CodeParseError, CodeParseError, ""},
}

func TestDialectDocumentClassification(t *testing.T) {
	for _, tc := range documentCorpus {
		t.Run(tc.name, func(t *testing.T) {
			for _, reject := range []bool{false, true} {
				wantCode := tc.defaultCode
				if reject {
					wantCode = tc.strictCode
				}
				d := Dialect{RejectDuplicateKeysAndInvalidUnicode: reject}
				_, cerr := d.classify([]byte(tc.line))
				if wantCode == 0 {
					if cerr != nil {
						t.Fatalf("reject=%v: unexpected error %+v", reject, cerr)
					}
					continue
				}
				if cerr == nil || cerr.err.Code != wantCode {
					t.Fatalf("reject=%v: error=%+v, want code %d", reject, cerr, wantCode)
				}
				if reject && string(cerr.id) != tc.strictID {
					t.Errorf("id=%q, want %q", cerr.id, tc.strictID)
				}
				if reject && wantCode == CodeInvalidRequest && cerr.err.Message != "duplicate member or invalid Unicode" {
					t.Errorf("message=%q", cerr.err.Message)
				}
			}
		})
	}
}

func FuzzDialectDocumentClassification(f *testing.F) {
	for _, tc := range classifyCorpus {
		f.Add([]byte(tc.line))
	}
	for _, tc := range documentCorpus {
		f.Add([]byte(tc.line))
	}
	f.Fuzz(func(t *testing.T, line []byte) {
		msg, cerr := (Dialect{RejectDuplicateKeysAndInvalidUnicode: true}).classify(line)
		want := oracleClassify(line)
		if utf8.Valid(line) && json.Valid(line) && bytes.TrimSpace(line)[0] == '{' {
			var top map[string]json.RawMessage
			if strictjson.Decode(line, &top) != nil {
				want = verdict{code: CodeInvalidRequest}
			}
		}
		got := verdict{kind: msg.kind}
		if cerr != nil {
			got = verdict{code: cerr.err.Code}
			if msg.kind != 0 {
				t.Fatal("both message and error")
			}
			if len(cerr.id) > 0 {
				var id RequestID
				if strictjson.Decode(cerr.id, &id) != nil {
					t.Fatalf("unusable reply id %q", cerr.id)
				}
			}
			reply := errorLine(cerr.id, cerr.err)
			if !json.Valid(reply) || bytes.Count(reply, []byte("\n")) != 1 {
				t.Fatalf("bad reply %q", reply)
			}
		}
		if got != want {
			t.Fatalf("classify=%+v, oracle=%+v for %q", got, want, line)
		}
	})
}
