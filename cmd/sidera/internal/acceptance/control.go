package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/aianyai/sidera/internal/node/manageapi"
	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/internal/node/txn"
)

func (f *Fixture) checkTable(ctx context.Context) error {
	raw, err := f.Manage(ctx, "test.crashpoints", manageapi.Request{})
	if err != nil {
		return err
	}
	type point struct {
		Key  string `json:"key"`
		Kind string `json:"kind"`
	}
	var response struct {
		Points []point `json:"points"`
	}
	if err = resultBody(raw, &response); err != nil {
		return err
	}
	want := []point{}
	for _, k := range f.table.Keys() {
		kind := "normal"
		if k.Kind == txn.Recovery {
			kind = "recovery"
		}
		want = append(want, point{k.Key.String(), kind})
	}
	if !reflect.DeepEqual(response.Points, want) {
		return fmt.Errorf("acceptance: daemon crash point table differs: got %v, want %v", response.Points, want)
	}
	return nil
}

type instance struct {
	Instance string `json:"instance"`
	Enabled  bool   `json:"enabled"`
	Probe    *struct {
		State string `json:"state"`
	} `json:"probe"`
}

func (f *Fixture) instances(ctx context.Context) ([]instance, error) {
	raw, err := f.Manage(ctx, "adapters.list", manageapi.Request{})
	if err != nil {
		return nil, err
	}
	var result struct {
		Instances []instance `json:"instances"`
	}
	err = resultBody(raw, &result)
	return result.Instances, err
}

func (f *Fixture) waitProbes(ctx context.Context) error {
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if f.current.p.exited() {
			return f.exitResult(f.current)
		}
		instances, err := f.instances(ctx)
		if err != nil {
			return err
		}
		ready := true
		for _, in := range instances {
			if !in.Enabled {
				continue
			}
			if in.Probe == nil || in.Probe.State == "pending" {
				ready = false
				continue
			}
			if in.Probe.State != "passed" {
				return fmt.Errorf("acceptance: instance %s probe %s", in.Instance, in.Probe.State)
			}
		}
		if ready {
			return f.syncConnections(ctx)
		}
		select {
		case <-f.current.p.done:
			return f.exitResult(f.current)
		case <-ctx.Done():
			detail, _ := json.Marshal(instances)
			return fmt.Errorf("acceptance: probes incomplete: %s: %w", detail, ctx.Err())
		case <-tick.C:
		}
	}
}

func (f *Fixture) Adapter(ctx context.Context, name string) (fakeadapter.Controller, error) {
	c, err := f.control.Wait(ctx, name)
	if err != nil {
		return nil, err
	}
	if err = f.syncConnections(ctx); err != nil {
		return nil, err
	}
	return fakeadapter.RemoteKnobs{Conn: c}, nil
}
