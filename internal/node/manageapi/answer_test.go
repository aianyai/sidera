package manageapi

import (
	"errors"
	"testing"

	"github.com/aianyai/sidera/internal/node/apiwire"
)

// A result is read as encoding/json reads it. When the node answered success and
// the result cannot be read, the error is answer_unreadable, which says a change
// was accepted exactly when the answer has an operation identifier.
func TestDecodeResult(t *testing.T) {
	const id = "ab0000000000000000000000000000ff"
	for _, tc := range []struct {
		name, raw string
		want      int // the value read; -1 for an error
		changed   bool
		operation string
	}{
		{"a read", `{"result":{"n":7}}`, 7, false, ""},
		{"a change", `{"operation_id":"` + id + `","replayed":false,"result":{"n":7}}`, 7, false, ""},
		{"a member that is not named is ignored", `{"future":1,"result":{"n":7,"later":[1]}}`, 7, false, ""},
		{"a member of another type, in a read", `{"result":{"n":"7"}}`, -1, false, ""},
		{"a member of another type, in a change", `{"operation_id":"` + id + `","replayed":false,"result":{"n":"7"}}`, -1, true, id},
		{"no result, in a change", `{"operation_id":"` + id + `","replayed":false}`, -1, true, id},
		{"an envelope that is not an object", `[]`, -1, false, ""},
		{"an envelope that is cut off", `{"result":`, -1, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var v struct {
				N int `json:"n"`
			}
			err := DecodeResult([]byte(tc.raw), &v)
			if tc.want >= 0 {
				if err != nil || v.N != tc.want {
					t.Fatalf("read %d with error %v, want %d", v.N, err, tc.want)
				}
				return
			}
			var wire *apiwire.Error
			if !errors.As(err, &wire) || wire.Code != apiwire.CodeAnswerUnreadable || wire.Category != apiwire.CategoryInternal {
				t.Fatalf("error %v, want internal/answer_unreadable", err)
			}
			if got, _ := wire.Details["operation_id"].(string); got != tc.operation {
				t.Errorf("operation_id %q, want %q", got, tc.operation)
			}
			if accepted := wire.Message != apiwire.AnswerUnreadable(false, "").Message; accepted != tc.changed {
				t.Errorf("says a change was accepted: %v, want %v", accepted, tc.changed)
			}
		})
	}
}

func TestReadAnswerKeepsTheEnvelope(t *testing.T) {
	a, err := ReadAnswer([]byte(`{"operation_id":"x","replayed":true,"result":{"n":1},"future":2}`))
	if err != nil || a.OperationID != "x" || !a.Replayed || string(a.Result) != `{"n":1}` {
		t.Fatalf("answer %+v, error %v", a, err)
	}
}
