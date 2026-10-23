package peer_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aianyai/sidera/sdk/go/peer"
)

// wireModel is an independent record of what crossed the connection. It
// parses every line from the raw bytes, without the peer package, and counts
// requests and responses per sender and id.
type wireModel struct {
	mu        sync.Mutex
	requests  map[string]map[string]int // sender -> id -> count
	responses map[string]map[string]int // sender -> id -> count
	cancels   map[string]int            // sender -> $/cancel_request count
	codes     map[int]int               // error code -> responses carrying it
}

func newWireModel() *wireModel {
	return &wireModel{
		requests:  map[string]map[string]int{"a": {}, "b": {}},
		responses: map[string]map[string]int{"a": {}, "b": {}},
		cancels:   map[string]int{},
		codes:     map[int]int{},
	}
}

func (m *wireModel) tap(from string, line []byte) {
	var f map[string]json.RawMessage
	if err := json.Unmarshal(line, &f); err != nil {
		panic(fmt.Sprintf("peer wrote a line that is not a JSON object: %q", line))
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, hasMethod := f["method"]
	_, hasID := f["id"]
	switch {
	case hasMethod && hasID:
		m.requests[from][string(f["id"])]++
	case hasMethod:
		if string(f["method"]) == `"`+peer.MethodCancelRequest+`"` {
			m.cancels[from]++
		}
	default:
		m.responses[from][string(f["id"])]++
		if raw, ok := f["error"]; ok {
			var e struct{ Code int }
			json.Unmarshal(raw, &e)
			m.codes[e.Code]++
		}
	}
}

func other(side string) string {
	if side == "a" {
		return "b"
	}
	return "a"
}

// settled reports whether every request has been answered.
func (m *wireModel) settled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range []string{"a", "b"} {
		for id := range m.requests[s] {
			if m.responses[other(s)][id] == 0 {
				return false
			}
		}
	}
	return true
}

// verify asserts that every request got exactly one response and that no
// response exists for a request that was never sent.
func (m *wireModel) verify(t *testing.T) (requests int) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range []string{"a", "b"} {
		for id, n := range m.requests[s] {
			if n != 1 {
				t.Errorf("%s sent request id %s %d times", s, id, n)
			}
			if got := m.responses[other(s)][id]; got != 1 {
				t.Errorf("request %s from %s was answered %d times, want exactly once", id, s, got)
			}
			requests++
		}
		for id := range m.responses[s] {
			if m.requests[other(s)][id] == 0 {
				t.Errorf("%s answered id %s that %s never sent", s, id, other(s))
			}
		}
	}
	return requests
}

type callPlan struct {
	tag      string
	workUS   int
	fail     bool
	cancelUS int // 0: never cancel
	gapUS    int // pause before the call
}

type outcome int

const (
	outCompleted outcome = iota + 1
	outFailedApp
	outOverloadedRemote
	outOverloadedLocal
	outCancelledLocal
	outCancelledRemote
	outOther
)

func (o outcome) String() string {
	return [...]string{"", "completed", "failed_app", "overloaded_remote", "overloaded_local", "cancelled_local", "cancelled_remote", "other"}[o]
}

// TestConcurrentCallsInBothDirections drives many concurrent calls with
// random work, failures and cancellations in both directions, and checks the
// outcomes against a bookkeeping model that does not use the peer package.
func TestConcurrentCallsInBothDirections(t *testing.T) {
	leakGuard(t)
	const perDirection = 600
	rng := rand.New(rand.NewSource(20261023))

	small := peer.Params{MaxPendingRequests: 3, MaxConcurrentHandlers: 2}
	model := newWireModel()
	a, b := newPair(t, small, small, model.tap)

	var runsMu sync.Mutex
	runs := map[string]int{}
	handler := func(ctx context.Context, params json.RawMessage) (any, error) {
		var in struct {
			Tag    string `json:"tag"`
			WorkUS int    `json:"work_us"`
			Fail   bool   `json:"fail"`
		}
		if err := json.Unmarshal(params, &in); err != nil {
			return nil, peer.NewError(peer.CodeInvalidParams, "bad params")
		}
		runsMu.Lock()
		runs[in.Tag]++
		runsMu.Unlock()
		select {
		case <-time.After(time.Duration(in.WorkUS) * time.Microsecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if in.Fail {
			return nil, peer.NewError(1003, "failed on purpose")
		}
		return map[string]string{"tag": in.Tag}, nil
	}
	a.Handle("work", handler)
	b.Handle("work", handler)
	a.Start()
	b.Start()

	// The plan is generated up front from the seed.
	plans := map[string][]callPlan{}
	for _, side := range []string{"a", "b"} {
		for i := range perDirection {
			p := callPlan{tag: fmt.Sprintf("%s-%d", side, i), workUS: rng.Intn(3000), fail: rng.Intn(8) == 0, gapUS: rng.Intn(2500)}
			if rng.Intn(4) == 0 {
				p.cancelUS = 1 + rng.Intn(3000)
			}
			plans[side] = append(plans[side], p)
		}
	}

	var outcomesMu sync.Mutex
	outcomes := map[string]outcome{}
	var setCount atomic.Int64
	record := func(tag string, o outcome) {
		outcomesMu.Lock()
		defer outcomesMu.Unlock()
		if _, dup := outcomes[tag]; dup {
			t.Errorf("call %s ended twice", tag)
		}
		outcomes[tag] = o
		setCount.Add(1)
	}

	classify := func(err error, ctxCancelled bool) outcome {
		var re *peer.Error
		switch {
		case err == nil:
			return outCompleted
		case errors.Is(err, peer.ErrOverloaded) && errors.As(err, &re):
			return outOverloadedRemote
		case err == peer.ErrOverloaded:
			return outOverloadedLocal
		case errors.As(err, &re) && re.Code == 1003:
			return outFailedApp
		case errors.As(err, &re) && re.Code == peer.CodeRequestCancelled:
			return outCancelledRemote
		case ctxCancelled && errors.Is(err, context.Canceled):
			return outCancelledLocal
		}
		return outOther
	}

	var wg sync.WaitGroup
	for _, side := range []struct {
		name   string
		caller *peer.Peer
	}{{"a", a}, {"b", b}} {
		work := make(chan callPlan, perDirection)
		for _, p := range plans[side.name] {
			work <- p
		}
		close(work)
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for p := range work {
					time.Sleep(time.Duration(p.gapUS) * time.Microsecond)
					ctx, cancel := context.WithCancel(context.Background())
					if p.cancelUS > 0 {
						time.AfterFunc(time.Duration(p.cancelUS)*time.Microsecond, cancel)
					}
					var out struct{ Tag string }
					err := side.caller.Call(ctx, "work", map[string]any{"tag": p.tag, "work_us": p.workUS, "fail": p.fail}, &out)
					o := classify(err, ctx.Err() != nil)
					if o == outCompleted && out.Tag != p.tag {
						t.Errorf("call %s got the result of %q", p.tag, out.Tag)
					}
					if o == outOther {
						t.Errorf("call %s ended with unexpected error %v", p.tag, err)
					}
					cancel()
					record(p.tag, o)
				}
			}()
		}
	}
	wg.Wait()

	// Every request is eventually answered, even those whose caller gave up.
	deadline := time.Now().Add(15 * time.Second)
	for !model.settled() {
		if time.Now().After(deadline) {
			t.Fatal("some requests were never answered")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Let any last duplicate answer arrive before judging.
	time.Sleep(50 * time.Millisecond)

	if got := setCount.Load(); got != 2*perDirection {
		t.Fatalf("%d outcomes recorded for %d calls", got, 2*perDirection)
	}
	requests := model.verify(t)

	counts := map[outcome]int{}
	runsMu.Lock()
	defer runsMu.Unlock()
	for tag, o := range outcomes {
		counts[o]++
		n := runs[tag]
		if n > 1 {
			t.Errorf("handler for %s ran %d times, want at most once", tag, n)
		}
		switch o {
		case outCompleted, outFailedApp:
			if n != 1 {
				t.Errorf("%s ended %v but its handler ran %d times", tag, o, n)
			}
		case outOverloadedRemote, outOverloadedLocal:
			if n != 0 {
				t.Errorf("%s was refused as overloaded but its handler ran %d times", tag, n)
			}
		}
	}
	for tag, n := range runs {
		if _, ok := outcomes[tag]; !ok {
			t.Errorf("handler ran for unknown call %s (%d times)", tag, n)
		}
	}
	t.Logf("wire: -32001 responses=%d, -32800 responses=%d", model.codes[peer.CodeOverloaded], model.codes[peer.CodeRequestCancelled])
	t.Logf("calls=%d requests on the wire=%d completed=%d failed_app=%d overloaded(remote)=%d overloaded(local)=%d cancelled(local)=%d cancelled(remote)=%d cancel notifications=%d",
		2*perDirection, requests, counts[outCompleted], counts[outFailedApp], counts[outOverloadedRemote], counts[outOverloadedLocal],
		counts[outCancelledLocal], counts[outCancelledRemote], model.cancels["a"]+model.cancels["b"])

	// Coverage: the run must have exercised each path, or it proves nothing.
	for _, c := range []struct {
		name string
		got  int
		min  int
	}{
		{"completed", counts[outCompleted], 100},
		{"failed with an application error", counts[outFailedApp], 10},
		{"overloaded", counts[outOverloadedRemote] + counts[outOverloadedLocal], 20},
		{"cancelled", counts[outCancelledLocal] + counts[outCancelledRemote], 20},
	} {
		if c.got < c.min {
			t.Errorf("only %d calls %s, want at least %d: the generator no longer reaches that path", c.got, c.name, c.min)
		}
	}
	// Cancelled requests are answered with -32800 on the wire even though
	// their callers have already gone.
	if model.codes[peer.CodeRequestCancelled] < 5 {
		t.Errorf("only %d requests were answered with -32800", model.codes[peer.CodeRequestCancelled])
	}
	if model.codes[peer.CodeOverloaded] < counts[outOverloadedRemote] {
		t.Errorf("callers saw %d overload answers but only %d were on the wire", counts[outOverloadedRemote], model.codes[peer.CodeOverloaded])
	}
	if model.cancels["a"]+model.cancels["b"] == 0 {
		t.Error("no $/cancel_request was ever sent")
	}
}
