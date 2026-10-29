//go:build testclock

package manageapi

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/engine"
)

type quietService struct {
	*fakeService
	expect   engine.Expect
	deadline bool
}

func (s *quietService) Quiesce(ctx context.Context, x engine.Expect) (engine.Report, error) {
	s.expect = x
	_, s.deadline = ctx.Deadline()
	return engine.Report{Workers: 2}, context.DeadlineExceeded
}

func TestClockSetUsesAbsoluteMillisecondsAndAllowsRollback(t *testing.T) {
	r := newRig(t)
	for _, ms := range []int64{t0.Add(time.Hour).UnixMilli(), t0.Add(-time.Hour).UnixMilli()} {
		status, out := r.post("test.clock.set", `{"params":{"to_ms":`+jsonNumber(ms)+`}}`)
		if status != 200 || r.srv.cfg.Clock.Now().UnixMilli() != ms {
			t.Fatalf("set %d: %d %s", ms, status, out)
		}
	}
	for _, body := range []string{`{}`, `{"params":{"to_ms":null}}`, `{"params":{"to_ms":1,"To_ms":2}}`} {
		status, out := r.post("test.clock.set", body)
		r.wantError(400, "invalid_argument", "malformed_request", status, out)
	}
	if len(r.svc.calls()) != 0 {
		t.Fatal("test clock entered the operation ledger")
	}
}
func jsonNumber(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestQuiesceRetainsHeldMultiplicityAndFailureReport(t *testing.T) {
	s := &quietService{fakeService: &fakeService{}}
	r := newRig(t, func(server *Server) { server.cfg.Service = s })
	status, out := r.post("test.quiesce", `{"params":{"expect":{"fake":{"notifications":3,"held":["session.create","session.create"]}},"timeout_ms":1000}}`)
	var result struct {
		Result struct {
			Workers int    `json:"workers"`
			Error   string `json:"error"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatal(err)
	}
	if status != 200 || result.Result.Workers != 2 || result.Result.Error == "" || !s.deadline {
		t.Fatalf("quiesce: %d %s", status, out)
	}
	got := s.expect.Adapters["fake"]
	if got.Notifications != 3 || len(got.Held) != 2 || got.Held[0] != got.Held[1] {
		t.Fatalf("expectation changed: %+v", got)
	}
	if len(s.calls()) != 0 {
		t.Fatal("quiesce entered the operation ledger")
	}
}
