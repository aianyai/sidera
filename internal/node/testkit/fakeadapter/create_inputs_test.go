package fakeadapter_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func TestCreateInputsContractInBothLayers(t *testing.T) {
	for _, binary := range []bool{false, true} {
		name := "inproc"
		if binary {
			name = "binary"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*waitLimit)
			defer cancel()
			inputs := fakeadapter.ExternalInputs{CreateRefusal: adapterproto.CreateFailRuntimeAuth}
			owner, err := fakeadapter.NewInputRegistry([]string{"fake"}, map[string]fakeadapter.ExternalInputs{"fake": inputs})
			if err != nil {
				t.Fatal(err)
			}
			var ctl fakeadapter.Controller
			var call func(string, any, adapterproto.Message) error
			var create func(int, string) adapterproto.SessionCreateParams
			var readLog func() *ledger.Log
			var restart func()
			if binary {
				f := newProcFixture(t)
				owner.HandleControl(f.srv)
				f.cfg.RestoreInputs = true
				var a *procAdapter
				restart = func() {
					if a != nil {
						a.stop()
					}
					a = f.startAdapter()
					a.initialize()
					ctl = fakeadapter.RemoteKnobs{Conn: a.conn}
					call, create = a.call, a.createParams
				}
				readLog = func() *ledger.Log {
					l, err := ledger.ReadDir(f.cfg.LedgerDir)
					if err != nil {
						t.Fatal(err)
					}
					return l
				}
			} else {
				r := newRig(t, rigOptions{inputs: owner})
				first := true
				restart = func() {
					if !first {
						if err := r.disconnect(); err != nil && !errors.Is(err, context.Canceled) {
							t.Fatal(err)
						}
						r.knobs = fakeadapter.NewKnobs()
						detach, err := owner.Attach(ctx, "fake", r.knobs)
						if err != nil {
							t.Fatal(err)
						}
						t.Cleanup(detach)
						r.connect()
					}
					first = false
					r.initialize()
					ctl = r.knobs.Controller()
				}
				call = func(method string, params any, out adapterproto.Message) error {
					raw, err := r.call(method, params)
					if err == nil {
						err = adapterproto.Decode(raw, out)
					}
					return err
				}
				create, readLog = r.createParams, r.ledgerLog
			}
			start := func() {
				restart()
				var started adapterproto.StartResult
				if err := call(adapterproto.MethodAdapterStart, adapterproto.StartParams{Node: "node-1", RuntimeEndpoint: "rt-1", Config: adapterproto.ConfigValues{}, Presets: []adapterproto.PresetConfig{{Name: "default", Config: adapterproto.ConfigValues{}}}, Sessions: []adapterproto.SessionAttach{}, DirectEndpoints: []adapterproto.DirectAttach{}}, &started); err != nil {
					t.Fatal(err)
				}
			}
			request := func(want adapterproto.SessionCreateResult) adapterproto.SessionCreateResult {
				t.Helper()
				// A missing checkpoint is a supported retry, including in a new adapter.
				p := create(1, "sess-1")
				if len(p.Checkpoint) != 0 {
					t.Fatal("retry unexpectedly carries a checkpoint")
				}
				var got adapterproto.SessionCreateResult
				if err := call(adapterproto.MethodSessionCreate, p, &got); err != nil {
					t.Fatal(err)
				}
				if want.Outcome == adapterproto.CreateCreated && want.NativeRef == "" {
					if got.Outcome != want.Outcome || got.NativeRef == "" || got.Process == nil {
						t.Fatalf("created result: %+v", got)
					}
				} else if !reflect.DeepEqual(got, want) {
					t.Fatalf("creation result %+v, want %+v", got, want)
				}
				return got
			}
			refused := adapterproto.SessionCreateResult{Outcome: adapterproto.CreateFailed, Reason: adapterproto.CreateFailRuntimeAuth}
			start()
			request(refused)
			start()
			if err := ctl.ClearOutcomes(ctx); err != nil {
				t.Fatal(err)
			}
			if err := ctl.QueueOutcome(ctx, fakeadapter.RequestCreate, fakeadapter.Outcome{Kind: fakeadapter.OutcomeFail, Reason: string(adapterproto.CreateFailPresetInvalid)}); err != nil {
				t.Fatal(err)
			}
			request(refused)
			if got := readLog().Kind(ledger.KindRuntimeSessionCreated); len(got) != 0 {
				t.Fatal("external refusal created a native session")
			}
			if err := owner.Set(ctx, "fake", fakeadapter.ExternalInputs{}); err != nil {
				t.Fatal(err)
			}
			request(adapterproto.SessionCreateResult{Outcome: adapterproto.CreateFailed, Reason: adapterproto.CreateFailPresetInvalid})
			created := request(adapterproto.SessionCreateResult{Outcome: adapterproto.CreateCreated})
			inputs.CreateRefusal = adapterproto.CreateFailNativeError
			if err := owner.Set(ctx, "fake", inputs); err != nil {
				t.Fatal(err)
			}
			request(created)
			start()
			request(created)
			var another adapterproto.SessionCreateResult
			if err := call(adapterproto.MethodSessionCreate, create(2, "sess-2"), &another); err != nil || !reflect.DeepEqual(another, adapterproto.SessionCreateResult{Outcome: adapterproto.CreateFailed, Reason: adapterproto.CreateFailNativeError}) {
				t.Fatalf("explicit native error: %+v, %v", another, err)
			}
			if got := readLog().Kind(ledger.KindRuntimeSessionCreated); len(got) != 1 || got[0].Attrs.NativeRef != string(created.NativeRef) {
				t.Fatalf("retry changed the actual native session: %+v", got)
			}
		})
	}
}

func TestExternalCreateRefusalRejectsUnknownEffectReasons(t *testing.T) {
	for _, reason := range []string{"control_unknown", "invalid"} {
		t.Run(reason, func(t *testing.T) {
			in := fakeadapter.ExternalInputs{CreateRefusal: adapterproto.CreateFailure(reason)}
			if _, err := fakeadapter.NewInputRegistry([]string{"fake"}, map[string]fakeadapter.ExternalInputs{"fake": in}); err == nil {
				t.Fatal("registry accepted an invalid create refusal")
			}
			raw, err := json.Marshal(in)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fakeadapter.DecodeKnobArgs(fakeadapter.KnobExternalInputs, raw); err == nil {
				t.Fatal("remote input decoder accepted an invalid create refusal")
			}
		})
	}
}
