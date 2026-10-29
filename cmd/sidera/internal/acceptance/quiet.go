package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/aianyai/sidera/internal/node/manageapi"
	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
)

func (f *Fixture) sample(ctx context.Context) (map[string]fakeadapter.StatsReply, error) {
	instances, err := f.instances(ctx)
	if err != nil {
		return nil, err
	}
	stats := map[string]fakeadapter.StatsReply{}
	for _, in := range instances {
		if !in.Enabled {
			continue
		}
		ctl, err := f.Adapter(ctx, in.Instance)
		if err != nil {
			return nil, err
		}
		s, err := ctl.Stats(ctx)
		if err != nil {
			return nil, err
		}
		s.Held = append([]string(nil), s.Held...)
		slices.Sort(s.Held)
		s.HeldPoints = nil // pause labels are not the held-method multiset
		stats[in.Instance] = s
	}
	return stats, nil
}

func noOutstanding(stats map[string]fakeadapter.StatsReply) bool {
	for _, s := range stats {
		if s.Outstanding != 0 {
			return false
		}
	}
	return true
}

func (f *Fixture) Quiesce(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, scenario.ResolveQuiesce(nil).Timeout)
	defer cancel()
	var report json.RawMessage
	var last map[string]fakeadapter.StatsReply
	var lastErr error
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		before, err := f.sample(ctx)
		if err != nil {
			return fmt.Errorf("acceptance: quiet sample: daemon=%s fakes=%+v: %w", report, last, err)
		}
		last = before
		if noOutstanding(before) && f.Runtime().Idle() {
			type expected struct {
				Notifications int      `json:"notifications"`
				Held          []string `json:"held,omitempty"`
			}
			expect := map[string]expected{}
			for name, s := range before {
				expect[name] = expected{s.NotificationsSent, s.Held}
			}
			deadline, _ := ctx.Deadline()
			timeout := time.Until(deadline).Milliseconds()
			if timeout < 1 {
				timeout = 1
			}
			report, err = f.Manage(ctx, "test.quiesce", manageapi.Request{Params: map[string]any{"expect": expect, "timeout_ms": timeout}})
			lastErr = err
			if err == nil {
				var r struct {
					Satisfied bool `json:"satisfied"`
				}
				if err = resultBody(report, &r); err != nil {
					return err
				}
				after, err := f.sample(ctx)
				if err != nil {
					return fmt.Errorf("acceptance: quiet resample: daemon=%s fakes=%+v: %w", report, last, err)
				}
				last = after
				if r.Satisfied && noOutstanding(after) && reflect.DeepEqual(before, after) && f.Runtime().Idle() {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("acceptance: quiet failed: daemon=%s fakes=%+v last_error=%v: %w", report, last, lastErr, ctx.Err())
		case <-f.current.p.done:
			return f.exitResult(f.current)
		case <-tick.C:
		}
	}
}
