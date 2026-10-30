package fakeruntime_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/aianyai/sidera/internal/node/testkit/fakeruntime"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func TestCreateRefusalValidation(t *testing.T) {
	for _, tc := range []struct {
		reason adapterproto.CreateFailure
		valid  bool
	}{
		{"", true},
		{"preset_unknown", true},
		{"preset_invalid", true},
		{"runtime_auth", true},
		{"native_unavailable", true},
		{"dependency_missing", true},
		{"native_error", true},
		{"control_unknown", false},
		{"invalid", false},
	} {
		t.Run(string(tc.reason), func(t *testing.T) {
			spec := fakeruntime.ProcessSpec{
				Create: fakeruntime.CreateRequest{Attempt: attempt(1), Endpoint: "sess-1", Preset: "default", FirstAttempt: attempt(1), FirstPrompt: "hello", Refusal: tc.reason},
				Dir:    "records", LedgerDir: "ledger", ClockStartMS: start.UnixMilli(), Control: "control",
				FirstTurn: fakeruntime.KnobOf(fakeruntime.TurnPlan{}),
			}
			raw, err := json.Marshal(spec)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fakeruntime.DecodeProcessSpec(raw); (err == nil) != tc.valid {
				t.Fatalf("create refusal %q: %v, want valid %v", tc.reason, err, tc.valid)
			}
		})
	}
}

func TestCreateRefusalLeavesNoEffectAndClearingAllowsTheSameAttempt(t *testing.T) {
	for _, process := range []bool{false, true} {
		name := "local"
		if process {
			name = "process"
		}
		t.Run(name, func(t *testing.T) {
			var launcher fakeruntime.Launcher
			var dir string
			var log func() *ledger.Log
			if process {
				e := newProcEnv(t)
				launcher, dir = e.launcher, e.dir
				log = func() *ledger.Log {
					l, err := ledger.ReadDir(e.ledgers)
					if err != nil {
						t.Fatal(err)
					}
					return l
				}
			} else {
				e := newEnv(t, nil)
				launcher, dir, log = e.rt, e.dir, e.log
			}
			ctx, cancel := context.WithTimeout(context.Background(), limit)
			defer cancel()
			req := fakeruntime.CreateRequest{Attempt: attempt(1), Endpoint: "sess-1", Preset: "default", FirstAttempt: attempt(1), FirstPrompt: "hello", Refusal: adapterproto.CreateFailRuntimeAuth}
			s, err := launcher.Create(ctx, req)
			var refused *fakeruntime.CreateRefusedError
			if s != nil || !errors.As(err, &refused) || refused.Reason != adapterproto.CreateFailRuntimeAuth {
				t.Fatalf("refused create = %v, %v", s, err)
			}
			files, err := os.ReadDir(dir)
			if err != nil || len(files) != 0 || len(log().Kind(ledger.KindRuntimeSessionCreated)) != 0 {
				t.Fatalf("refusal left runtime effects: files %v, %v", files, err)
			}
			req.Refusal = ""
			created, err := launcher.Create(ctx, req)
			if err != nil || created == nil || !created.Alive() {
				t.Fatalf("same attempt after clearing = %v, %v", created, err)
			}
			req.Refusal = adapterproto.CreateFailNativeError
			again, err := launcher.Create(ctx, req)
			if err != nil || again == nil || again.Ref() != created.Ref() || again.PID() != created.PID() || again.StartKey() != created.StartKey() {
				t.Fatalf("existing native under later refusal = %v, %v", again, err)
			}
			if got := len(log().Kind(ledger.KindRuntimeSessionCreated)); got != 1 {
				t.Fatalf("actual creations = %d, want 1", got)
			}
		})
	}
}
