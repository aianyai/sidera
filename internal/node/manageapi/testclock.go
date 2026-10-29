//go:build testclock

package manageapi

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/engine"
)

func clockMethods() []*method { return []*method{testClockSet, testQuiesce} }

var testClockSet = &method{name: "test.clock.set", test: func(s *Server, _ context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		ToMS *int64 `json:"to_ms"`
	}
	if err := decodeParams(raw, &p); err != nil {
		return nil, err
	}
	if p.ToMS == nil {
		return nil, apiwire.Malformed("an absolute millisecond time is required", "params.to_ms")
	}
	clock, ok := s.cfg.Clock.(interface{ Set(time.Time) })
	if !ok {
		return nil, fmt.Errorf("manageapi: test.clock.set requires a settable clock")
	}
	clock.Set(time.UnixMilli(*p.ToMS))
	return struct {
		NowMS int64 `json:"now_ms"`
	}{s.cfg.Clock.Now().UnixMilli()}, nil
}}

var testQuiesce = &method{name: "test.quiesce", test: func(s *Server, ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Expect map[string]struct {
			Notifications int      `json:"notifications"`
			Held          []string `json:"held,omitempty"`
		} `json:"expect,omitempty"`
		TimeoutMS int64 `json:"timeout_ms"`
	}
	if err := decodeParams(raw, &p); err != nil {
		return nil, err
	}
	if p.TimeoutMS <= 0 || p.TimeoutMS > math.MaxInt64/int64(time.Millisecond) {
		return nil, apiwire.Malformed("a positive timeout in milliseconds is required", "params.timeout_ms")
	}
	svc, ok := s.cfg.Service.(interface {
		Quiesce(context.Context, engine.Expect) (engine.Report, error)
	})
	if !ok {
		return nil, fmt.Errorf("manageapi: test.quiesce requires a quiesce service")
	}
	x := engine.Expect{Adapters: map[string]engine.AdapterExpect{}}
	for name, expected := range p.Expect {
		if expected.Notifications < 0 {
			return nil, apiwire.Malformed("a notification count cannot be negative", "params.expect."+name+".notifications")
		}
		x.Adapters[name] = engine.AdapterExpect{Notifications: expected.Notifications, Held: expected.Held}
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(p.TimeoutMS)*time.Millisecond)
	defer cancel()
	report, err := svc.Quiesce(ctx, x)
	result := struct {
		InboxLen        int    `json:"inbox_len"`
		Continuations   int    `json:"continuations"`
		DueEntries      int    `json:"due_entries"`
		Parked          int    `json:"parked"`
		Workers         int    `json:"workers"`
		AdaptersPending int    `json:"adapters_pending"`
		Satisfied       bool   `json:"satisfied"`
		Error           string `json:"error,omitempty"`
	}{InboxLen: report.InboxLen, Continuations: report.Continuations, DueEntries: report.DueEntries, Parked: report.Parked, Workers: report.Workers, AdaptersPending: report.AdaptersPending, Satisfied: report.Satisfied}
	if err != nil {
		result.Error = err.Error()
	}
	return result, nil
}}
