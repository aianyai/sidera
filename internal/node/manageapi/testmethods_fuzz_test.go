//go:build testclock || crashtest

package manageapi

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aianyai/sidera/internal/node/clock/clocktest"
)

// FuzzTestMethodParams drives the tagged rows' typed params through the same
// strict decoder as a request, including unknown and repeated members.
func FuzzTestMethodParams(f *testing.F) {
	for _, raw := range []string{`{}`, `null`, `{"to_ms":0}`, `{"to_ms":1,"to_ms":2}`, `{"to_ms":9223372036854775807}`, `{"timeout_ms":1,"expect":{"fake":{"notifications":1,"held":["session.create","session.create"]}}}`, `{"timeout_ms":9223372036854775807}`, `{"timeout_ms":1,"expect":{"fake":{"notifications":-1}}}`} {
		for _, m := range testMethods() {
			f.Add(m.name, []byte(raw))
		}
	}
	s, err := New(Config{Service: &fakeService{}, Node: ownNode, Clock: clocktest.NewFake(t0)})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, name string, raw []byte) {
		m := s.methods[name]
		if m == nil || m.test == nil {
			return
		}
		result, err := m.test(s, context.Background(), json.RawMessage(raw))
		if err == nil {
			if _, err := json.Marshal(result); err != nil {
				t.Fatalf("unencodable result: %v", err)
			}
		}
	})
}
