package harness_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/node/clock"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func TestInstanceInputsInstallDisabledRuntimeAndProbeOnlyAfterEnable(t *testing.T) {
	for _, mode := range []harness.FSMode{harness.MemFS, harness.RealFS} {
		t.Run(map[harness.FSMode]string{harness.MemFS: "memory", harness.RealFS: "real"}[mode], func(t *testing.T) {
			const late = "late-enabled"
			declaration := harness.RuntimeAdapterManifestOf(late, 2)
			h := harness.New(t, harness.Options{FS: mode, Adapter: &harness.AdapterOptions{
				ExtraInstances:  []string{late, "after-late"},
				InstanceOptions: map[string]harness.AdapterInstanceOptions{late: {Manifest: declaration, Disabled: true}},
			}})
			inputs := fakeadapter.ExternalInputs{Availability: []adapterproto.Availability{{Condition: adapterproto.ConditionRuntimeAuth, SinceMS: h.Clock().Now().Add(-time.Minute).UnixMilli()}}}
			if err := h.SetAdapterInputs(late, inputs); err != nil {
				t.Fatal(err)
			}
			if len(h.AdapterLaunches(late)) != 0 {
				t.Fatal("disabled input launched before enable")
			}
			// Other enabled instances must not wait for the disabled predecessor.
			if len(h.AdapterLaunches("after-late")) != 1 {
				t.Fatal("enabled instance after disabled input did not launch")
			}
			_, err := h.Engine().Execute(bg(), engine.ManagementCommand{
				Key:         admin.KeyFor(admin.LocalOwner(), admin.OperationID{88}),
				Fingerprint: sha256.Sum256([]byte("enable late")), Name: "adapters.enable",
				Expected:  h.Config().Instances().Version,
				Operation: engine.EnableAdapterOp(late, late, nil, func(v engine.InstanceChange) ([]byte, error) { return json.Marshal(v) }),
			})
			if err != nil {
				t.Fatal(err)
			}
			// Admission precedes the probe; quiescence includes both the host and engine.
			ctx, cancel := context.WithTimeout(bg(), 10*time.Second)
			defer cancel()
			for {
				view, err := h.Engine().Adapter(ctx, late)
				if err != nil {
					t.Fatal(err)
				}
				if view.Probed {
					break
				}
				timer := clock.System().NewTimer(time.Millisecond)
				select {
				case <-timer.C():
				case <-ctx.Done():
					timer.Stop()
					t.Fatal(ctx.Err())
				}
			}
			if err := h.Quiesce(); err != nil {
				t.Fatal(err)
			}
			view, err := h.Engine().Adapter(bg(), late)
			if err != nil || !view.Probed || view.Runtime == nil || view.Runtime.Version != declaration.RuntimeVersion {
				t.Fatalf("real probe did not report the runtime: %+v err=%v", view, err)
			}
			for generation := 0; generation < 2; generation++ {
				if err := h.Quiesce(); err != nil {
					t.Fatal(err)
				}
				view, err := h.Engine().Adapter(bg(), late)
				if err != nil || len(view.Conditions) != 1 || view.Conditions[0].SinceMS != inputs.Availability[0].SinceMS {
					t.Fatalf("late-enabled input after restart %d: %+v, %v", generation, view.Conditions, err)
				}
				h.Restart()
			}
			if err := h.SetAdapterInputs(late, fakeadapter.ExternalInputs{}); err != nil {
				t.Fatal(err)
			}
			h.Restart()
			if err := h.Quiesce(); err != nil {
				t.Fatal(err)
			}
			cleared, err := h.Engine().Adapter(bg(), late)
			if err != nil || len(cleared.Conditions) != 0 {
				t.Fatalf("cleared inputs revived: %+v, %v", cleared.Conditions, err)
			}

		})
	}
}

func TestInstanceInputSeedCacheIncludesManifestAndDisabledState(t *testing.T) {
	const instance = "seed-input"
	for _, tt := range []struct {
		version  string
		disabled bool
	}{{"1.0.0", true}, {"2.0.0", true}, {"2.0.0", false}} {
		declaration := harness.AdapterManifestOf(instance, 1)
		declaration.Version = tt.version
		declaration.Program = "custom/adapter"
		h := harness.NewStopped(t, harness.Options{Adapter: &harness.AdapterOptions{
			ExtraInstances:  []string{instance},
			InstanceOptions: map[string]harness.AdapterInstanceOptions{instance: {Manifest: declaration, Disabled: tt.disabled}},
		}})
		raw, err := h.Disk().ReadFile(config.InstancesPath)
		if err != nil {
			t.Fatal(err)
		}
		instances, err := config.DecodeInstances(raw)
		if err != nil || instances.Items[instance].Enabled == tt.disabled {
			t.Fatalf("cached enabled state: %+v err=%v", instances.Items[instance], err)
		}
		raw, err = h.Disk().ReadFile(config.AdapterCurrentPath(instance))
		if err != nil {
			t.Fatal(err)
		}
		version, err := config.DecodeAdapterCurrent(instance, raw)
		if err != nil || version.Version != tt.version {
			t.Fatalf("cached manifest: version=%s want=%s err=%v", version.Version, tt.version, err)
		}
	}
}
