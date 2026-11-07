package strictjson_test

import (
	"strings"
	"testing"

	"github.com/aianyai/sidera/sdk/go/strictjson"
)

type tolerantItem struct {
	N    int              `json:"n"`
	Meta map[string]inner `json:"meta,omitempty"`
}

type tolerantDoc struct {
	Name  string         `json:"name"`
	Sub   *inner         `json:"sub,omitempty"`
	Items []tolerantItem `json:"items"`
	Any   any            `json:"any,omitempty"`
}

func TestDecodeTolerant(t *testing.T) {
	const known = `"name":"a","items":[{"n":1}]`
	tests := []struct {
		name    string
		in      string
		want    string // the document decoded, as JSON; empty when an error is wanted
		wantErr string // substring of the error
	}{
		{"decodes a document without extras", `{` + known + `}`, `{"name":"a","items":[{"n":1}]}`, ""},
		{"ignores an unknown member at the top level", `{` + known + `,"future":{"x":[1,2]}}`, `{"name":"a","items":[{"n":1}]}`, ""},
		{"ignores an unknown member nested in an object", `{` + known + `,"sub":{"n":2,"future":1}}`, `{"name":"a","sub":{"n":2},"items":[{"n":1}]}`, ""},
		{"ignores an unknown member of a list element", `{"name":"a","items":[{"n":1,"future":null},{"n":2,"other":[]}]}`, `{"name":"a","items":[{"n":1},{"n":2}]}`, ""},
		{"ignores an unknown member of a map value", `{"name":"a","items":[{"n":1,"meta":{"k":{"n":3,"future":true}}}]}`, `{"name":"a","items":[{"n":1,"meta":{"k":{"n":3}}}]}`, ""},
		{"keeps every member of an opaque position", `{` + known + `,"any":{"future":[null,{"n":1}]}}`, `{"name":"a","items":[{"n":1}],"any":{"future":[null,{"n":1}]}}`, ""},
		{"never fills a field from a key that differs only in case", `{"name":"a","items":[{"n":1,"N":2}],"Name":"b"}`, `{"name":"a","items":[{"n":1}]}`, ""},
		{"ignores a member that differs only in case when the field is missing", `{"Name":"a","items":[]}`, "", "name: required field is missing"},

		{"still requires a known member", `{"name":"a","items":[{"future":1}]}`, "", "items[0].n: required field is missing"},
		{"still refuses a wrong type of a known member", `{"name":1,"items":[]}`, "", "name: expected a string"},
		{"still refuses a wrong type below a list", `{"name":"a","items":[{"n":"x"}]}`, "", "items[0].n"},
		{"still refuses null for a known member", `{"name":"a","items":null}`, "", "items: null"},
		{"still refuses a value that is not an object", `[]`, "", "expected an object"},
		{"still refuses a repeated key", `{` + known + `,"name":"b"}`, "", "name: duplicate key"},
		{"still refuses a repeated key among the ignored members", `{` + known + `,"future":1,"future":2}`, "", "future: duplicate key"},
		{"still refuses a repeated key inside an ignored member", `{` + known + `,"future":{"k":1,"k":2}}`, "", "future.k: duplicate key"},
		{"still refuses an unpaired surrogate in an ignored member", `{` + known + `,"future":"\ud800"}`, "", "surrogate"},
		{"still refuses invalid UTF-8 in an ignored member", "{" + known + ",\"future\":\"\xff\"}", "", "UTF-8"},
		{"still refuses data after the value", `{` + known + `} {}`, "", "data after"},
		{"still refuses truncated input", `{` + known + `,"future":`, "", "strictjson"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var d tolerantDoc
			err := strictjson.DecodeTolerant([]byte(tc.in), &d)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("got %v, want an error containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := toJSON(t, d); got != tc.want {
				t.Fatalf("decoded %s, want %s", got, tc.want)
			}
		})
	}
}

// Decode stays strict for the same inputs: the mode is opt-in.
func TestDecodeStillRefusesWhatDecodeTolerantIgnores(t *testing.T) {
	var d tolerantDoc
	err := strictjson.Decode([]byte(`{"name":"a","items":[{"n":1,"future":1}]}`), &d)
	if err == nil || !strings.Contains(err.Error(), "items[0].future: unknown field") {
		t.Fatalf("got %v, want an unknown field error", err)
	}
}

func TestDecodeTolerantNeedsPointer(t *testing.T) {
	if err := strictjson.DecodeTolerant([]byte(`{}`), tolerantDoc{}); err == nil {
		t.Error("DecodeTolerant into a non-pointer succeeded")
	}
}
