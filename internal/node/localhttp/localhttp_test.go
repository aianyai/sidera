package localhttp_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/ipc"
	"github.com/aianyai/sidera/internal/node/localhttp"
)

func newServer(t *testing.T) *localhttp.Server {
	t.Helper()
	s, err := localhttp.New(localhttp.Config{Kind: ipc.KindManage, PathPrefix: "/v1/manage/", Lookup: func(string) http.HandlerFunc { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// If an error's details cannot be encoded, the fallback must not encode that
// error again or publish part of the original answer.
func TestEncodingFailureWritesOnlyTheFixedInternalAnswer(t *testing.T) {
	s := newServer(t)
	for _, value := range []any{map[string]any{"ok": true, "bad": make(chan int)}, apiwire.Body{Error: apiwire.Error{Category: apiwire.CategoryInternal, Code: "internal", Message: "unencodableDetails", Details: map[string]any{"bad": make(chan int)}}}} {
		w := httptest.NewRecorder()
		s.JSON(w, http.StatusOK, value)
		const want = `{"error":{"category":"internal","code":"internal","message":"internal error"}}` + "\n"
		if w.Code != 500 || w.Body.String() != want {
			t.Fatalf("HTTP %d %s; want fixed internal answer", w.Code, w.Body)
		}
		if w.Header().Get("Content-Length") != strconv.Itoa(len(want)) {
			t.Fatal("wrong content length")
		}
	}
	details := &unencodableDetails{}
	e := apiwire.New(apiwire.CategoryInternal, "internal", "must not be published")
	e.Details = map[string]any{"bad": details}
	details.err = e
	w := httptest.NewRecorder()
	s.Fail(w, e)
	const want = `{"error":{"category":"internal","code":"internal","message":"internal error"}}` + "\n"
	if w.Code != 500 || w.Body.String() != want || details.encodes != 1 {
		t.Fatalf("HTTP %d %s, details encoded %d times; want fixed fallback after one encoding", w.Code, w.Body, details.encodes)
	}
}

// Returning the wire error itself makes recursive error handling observable.
// A second encoding succeeds so a broken implementation fails without looping.
type unencodableDetails struct {
	encodes int
	err     error
}

func (d *unencodableDetails) MarshalJSON() ([]byte, error) {
	d.encodes++
	if d.encodes > 1 {
		return []byte(`{}`), nil
	}
	return nil, d.err
}

func TestOnlyAPostToAKnownMethodIsTaken(t *testing.T) {
	calls := 0
	s, err := localhttp.New(localhttp.Config{Kind: ipc.KindManage, PathPrefix: "/v1/manage/", Lookup: func(name string) http.HandlerFunc {
		if name != "known" {
			return nil
		}
		return func(http.ResponseWriter, *http.Request) { calls++ }
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path string
		status       int
		category     apiwire.Category
		code         string
	}{
		{"GET", "/v1/manage/known", 405, apiwire.CategoryInvalidArgument, "method_not_allowed"},
		{"PUT", "/v1/manage/known", 405, apiwire.CategoryInvalidArgument, "method_not_allowed"},
		{"POST", "/v1/manage/unknown", 404, apiwire.CategoryNotFound, "method_unknown"},
		{"GET", "/v1/manage/unknown", 404, apiwire.CategoryNotFound, "method_unknown"},
		{"POST", "/v1/agent/known", 404, apiwire.CategoryNotFound, "method_unknown"},
		{"POST", "/v2/manage/known", 404, apiwire.CategoryNotFound, "method_unknown"},
		{"POST", "/v1/manage/", 404, apiwire.CategoryNotFound, "method_unknown"},
		{"POST", "/v1/manage/known/x", 404, apiwire.CategoryNotFound, "method_unknown"},
		{"POST", "/v1/manage//known", 404, apiwire.CategoryNotFound, "method_unknown"},
		{"POST", "/v1/manage/../known", 404, apiwire.CategoryNotFound, "method_unknown"},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			s.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
			wantError(t, w, tc.status, tc.category, tc.code)
			wantAllow := ""
			if tc.status == 405 {
				wantAllow = "POST"
			}
			if got := w.Header().Get("Allow"); got != wantAllow {
				t.Fatalf("Allow %q, want %q", got, wantAllow)
			}
		})
	}
	if calls != 0 {
		t.Fatalf("a refused request reached the handler %d times", calls)
	}
	s.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/manage/known", nil))
	if calls != 1 {
		t.Fatalf("a known POST reached the handler %d times", calls)
	}
}

func wantError(t *testing.T, w *httptest.ResponseRecorder, status int, category apiwire.Category, code string) {
	t.Helper()
	var b apiwire.Body
	if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	if w.Code != status || b.Error.Category != category || b.Error.Code != code {
		t.Fatalf("HTTP %d %s; want %d %s/%s", w.Code, w.Body, status, category, code)
	}
}

func TestRequestBodyIsBoundedAndReadFailuresAreMalformed(t *testing.T) {
	for _, tc := range []struct {
		name string
		body io.Reader
		code string
	}{
		{"over the limit", strings.NewReader(strings.Repeat("x", (1<<20)+1)), "request_too_large"},
		{"cannot be read", failedReader{}, "malformed_request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			_, err := localhttp.ReadBody(w, httptest.NewRequest("POST", "/", tc.body))
			if err == nil {
				t.Fatal("ReadBody accepted the request")
			}
			newServer(t).Fail(w, err)
			wantError(t, w, 400, apiwire.CategoryInvalidArgument, tc.code)
		})
	}
	body := strings.Repeat("x", 1<<20)
	got, err := localhttp.ReadBody(httptest.NewRecorder(), httptest.NewRequest("POST", "/", strings.NewReader(body)))
	if err != nil || string(got) != body {
		t.Fatalf("a body exactly at the limit: length %d, %v", len(got), err)
	}
}

type failedReader struct{}

func (failedReader) Read([]byte) (int, error) { return 0, errors.New("read diagnostic") }

func TestJSONAndRawJSONWriteCompleteUnescapedAnswers(t *testing.T) {
	s := newServer(t)
	for _, raw := range []bool{false, true} {
		w := httptest.NewRecorder()
		want := `{"text":"<>&"}` + "\n"
		if raw {
			s.RawJSON(w, 202, []byte(want))
		} else {
			s.JSON(w, 202, map[string]string{"text": "<>&"})
		}
		if w.Code != 202 || w.Body.String() != want || w.Header().Get("Content-Type") != "application/json" || w.Header().Get("Content-Length") != strconv.Itoa(len(want)) {
			t.Fatalf("raw %v: HTTP %d, headers %v, body %q", raw, w.Code, w.Header(), w.Body.String())
		}
	}
}

func TestInternalCauseIsLoggedAndNeverSent(t *testing.T) {
	var log bytes.Buffer
	s, err := localhttp.New(localhttp.Config{Kind: ipc.KindManage, PathPrefix: "/v1/manage/", Lookup: func(string) http.HandlerFunc { return nil }, Log: slog.New(slog.NewTextHandler(&log, nil))})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	const cause = "unexpected storage diagnostic"
	s.Fail(w, fmt.Errorf("serving: %w", errors.New(cause)))
	wantError(t, w, 500, apiwire.CategoryInternal, "internal")
	if strings.Contains(w.Body.String(), cause) || !strings.Contains(log.String(), "serving: "+cause) || !strings.Contains(log.String(), "interface=manage") {
		t.Fatalf("body %s, log %s", w.Body, &log)
	}
}
