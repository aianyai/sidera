package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/manageapi"
	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func TestRuntimeProfileProbesAcrossGeneration(t *testing.T) {
	for _, preset := range []string{"default", "fixture"} {
		t.Run(preset, func(t *testing.T) {
			f, ctx := profiledFixture(t, fixtureProfile{mainInstance: "chat", preset: preset, runtimeAdapter: true, extraInstances: []string{"managed"}, probeWarning: true})
			for gen := 1; gen <= 2; gen++ {
				if err := f.Start(ctx, GenerationSpec{Number: gen}); err != nil {
					t.Fatal(err)
				}
				assertRuntimeProbeFrontends(t, ctx, f, preset)
				if err := f.Kill(ctx); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func assertRuntimeProbeFrontends(t *testing.T, ctx context.Context, f *Fixture, preset string) {
	t.Helper()
	raw, err := f.Manage(ctx, "adapters.list", manageapi.Request{})
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Instances []struct {
			Instance string `json:"instance"`
			Enabled  bool   `json:"enabled"`
			Probe    struct {
				State     string `json:"state"`
				Frontends []struct {
					Preset   string                `json:"preset"`
					Frontend adapterproto.Frontend `json:"frontend"`
				} `json:"frontends"`
			} `json:"probe"`
		} `json:"instances"`
	}
	if err := resultBody(raw, &result); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, in := range result.Instances {
		if !in.Enabled {
			continue
		}
		count++
		if in.Probe.State != "passed" {
			t.Fatalf("instance %s: probe %s", in.Instance, in.Probe.State)
		}
		seen := map[string]bool{}
		for _, reached := range in.Probe.Frontends {
			if reached.Frontend != adapterproto.FrontendCLI {
				t.Fatalf("profile claimed an unexecuted frontend: %+v", reached)
			}
			seen[reached.Preset] = true
		}
		want := 1
		if preset != "default" {
			want++
		}
		if !seen["default"] || !seen[preset] || len(seen) != want {
			t.Fatalf("instance %s: presented presets %v", in.Instance, seen)
		}
	}
	if count != 2 {
		t.Fatalf("enabled runtime instances: %d", count)
	}
}

func TestSuccessfulCommandWithoutTokenPresentationDoesNotPassProbe(t *testing.T) {
	f, ctx := profiledFixture(t, fixtureProfile{runtimeAdapter: true})
	for i := range f.inputs {
		f.inputs[i].process.ToolProbe = &fakeadapter.ToolProbeConfig{Commands: map[adapterproto.Frontend][]string{
			adapterproto.FrontendCLI: {f.binaries.Plain, "--help"},
		}}
	}
	startCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err := f.Start(startCtx, GenerationSpec{Number: 1})
	if !errors.Is(err, context.DeadlineExceeded) || f.bootstrapNext != len(f.bootstrapSteps) {
		t.Fatalf("successful command without presentation must remain pending: %v", err)
	}
	// The static input persists commands only, never the opaque request Env.
	for _, in := range f.inputs {
		raw, err := json.Marshal(in.process)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "SIDERA_PROBE_TOKEN") {
			t.Fatal("probe credential persisted in process config")
		}
	}
}
