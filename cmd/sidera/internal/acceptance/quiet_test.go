package acceptance

import (
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/manageapi"
)

func TestRealBinaryEmptyDaemonQuiescesWithOneRequest(t *testing.T) {
	f, ctx := fixture(t)
	if err := f.Init(ctx, GenerationSpec{Number: 1}); err != nil {
		t.Fatal(err)
	}
	// Start the daemon without Start's adapter bootstrap so the expectation is empty.
	g, err := f.begin(GenerationSpec{Number: 2}, "daemon")
	if err != nil {
		t.Fatal(err)
	}
	f.current = g
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		raw, err := f.Manage(ctx, "status", manageapi.Request{})
		var status struct {
			Daemon struct {
				State string `json:"state"`
			} `json:"daemon"`
		}
		if err == nil && resultBody(raw, &status) == nil && status.Daemon.State == "running" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-g.p.done:
			t.Fatal(f.exitResult(g))
		case <-tick.C:
		}
	}
	instances, err := f.instances(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != 0 {
		t.Fatalf("empty daemon has %d adapters", len(instances))
	}
	// Use the fixture's request type once, without its sampling and retry loop.
	raw, err := f.Manage(ctx, "test.quiesce", manageapi.Request{Params: quiesceParams{
		Expect:    map[string]quiesceExpectation{},
		TimeoutMS: quietRound.Milliseconds(),
	}})
	if err != nil {
		t.Fatal(err)
	}
	var quiet struct {
		Satisfied bool `json:"satisfied"`
	}
	if err := resultBody(raw, &quiet); err != nil {
		t.Fatal(err)
	}
	if !quiet.Satisfied {
		t.Fatal("empty daemon is not quiet")
	}
}
