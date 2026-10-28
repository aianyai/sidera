package agentapi_test

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/agentapi"
	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
)

// FuzzProbeRequests reads arbitrary request bytes at the HTTP boundary. The token
// is well formed but was never issued: no input may authenticate it or panic.
func FuzzProbeRequests(f *testing.F) {
	for _, seed := range []string{`{"frontend":"cli"}`, `{"frontend":"mcp"}`, `{}`, `{"frontend":null}`, `{"frontend":"cli","frontend":"mcp"}`, `{"frontend":"cli","extra":1}`} {
		f.Add(seed)
	}
	h := harness.New(f, harness.Options{})
	s, err := agentapi.New(agentapi.Config{Service: h.Engine()})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, body string) {
		r := httptest.NewRequest("POST", "/v1/agent/probe", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+strings.Repeat("A", 43))
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		var out apiwire.Body
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("non-JSON error: %v", err)
		}
		if (w.Code != 400 && w.Code != 401) || out.Error.Code == "" || w.Code != out.Error.Category.HTTPStatus() {
			t.Fatalf("an unissued token got HTTP %d: %s", w.Code, w.Body)
		}
	})
}
