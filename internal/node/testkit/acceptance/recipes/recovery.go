package recipes

import (
	"time"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/fakeruntime"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// RecoveryOps exposes the final disk audit after the external drain.
// The binding resolves the current process each time, including after a crash.
type RecoveryOps struct {
	CheckClosedRecords func(scenario.Env) error
}

func recoveryCases(trigger crashwalk.Trigger, names ...string) []crashwalk.RecoveryCase {
	var out []crashwalk.RecoveryCase
	for _, name := range names {
		key, err := fsx.ParseKey(name)
		if err != nil {
			panic(err)
		}
		out = append(out, crashwalk.RecoveryCase{Item: key, After: trigger})
	}
	return out
}

func recoveryCrash(name string) crashwalk.Trigger {
	point, err := fsx.ParsePoint(name)
	if err != nil {
		panic(err)
	}
	return crashwalk.CrashAt(point)
}

func recoveryRecipes(b Bindings) []ScriptRecipe {
	var out []ScriptRecipe
	for _, variant := range []struct {
		name  string
		cases []crashwalk.RecoveryCase
	}{
		{"recovery-send", append(recoveryCases(recoveryCrash("intake/out.create#1"), "orphan-gc/body.remove", "orphan-gc/commit"), recoveryCases(recoveryCrash("intake/out.write#1"), "txn-cleanup/tmp.remove", "txn-cleanup/commit")...)},
		{"recovery-source-trusted", recoveryCases(crashwalk.CorruptOffline("source", "preserve-header"), "quarantine/seq-floor.create", "quarantine/seq-floor.write", "quarantine/seq-floor.rename", "quarantine/overwrite.create", "quarantine/overwrite.write", "quarantine/overwrite.rename", "quarantine/alert.create", "quarantine/alert.write", "quarantine/alert.rename", "quarantine/commit")},
		{"recovery-source-unreadable", recoveryCases(crashwalk.CorruptOffline("source", "unreadable"), "quarantine/out-lost.create", "quarantine/out-lost.write", "quarantine/out-lost.rename")},
		{"recovery-target", recoveryCases(crashwalk.CorruptOffline("target", "unreadable"), "quarantine/record.remove")},
	} {
		var evidence DeliveryEvidence
		out = append(out, ScriptRecipe{Script: scenario.Script{Name: variant.name, Setup: scenario.Setup{Direct: []string{"sender", "receiver"}}, Crash: scenario.CrashPlan{MaxPoints: 54, Recovery: variant.cases}, Steps: []scenario.Step{{Name: "send", Do: func(e scenario.Env) error {
			result, err := e.Agent("sender").Send(e.Ctx(), fakeagent.SendInput{To: []string{e.Addr("receiver")}, Body: "Recovery must retain a real delivery obligation."})
			if err == nil {
				evidence.Accepted(result, "Recovery must retain a real delivery obligation.")
			}
			return err
		}}}}, Drain: recoveryDrain(b, false, &evidence), Check: func(e scenario.Env) error {
			if err := evidence.Check(e); err != nil {
				return err
			}
			return b.Recovery.CheckClosedRecords(e)
		}})
	}
	for _, held := range []bool{false, true} {
		name := "recovery-create"
		budget := 89
		cases := append(recoveryCases(recoveryCrash("control-intake/ctl.create#1"), "orphan-gc-endpoint/endpoint.remove", "orphan-gc-endpoint/commit"), recoveryCases(recoveryCrash("control-intake/derived.create#1"), "derived-replay/derived.create", "derived-replay/derived.write", "derived-replay/derived.rename", "derived-replay/commit")...)
		cases = append(cases, recoveryCases(recoveryCrash("control-result/endpoint.create#1"), "ctl-effect-replay/endpoint.create", "ctl-effect-replay/endpoint.write", "ctl-effect-replay/endpoint.rename", "ctl-effect-replay/commit")...)
		if held {
			name = "recovery-creating"
			budget = 111
			cases = recoveryCases(crashwalk.CorruptOffline("creating-control", "unreadable"), "quarantine/session.create", "quarantine/session.write", "quarantine/session.rename")
		}
		out = append(out, ScriptRecipe{Script: scenario.Script{Name: name, Setup: scenario.Setup{Direct: []string{"sender", "receiver"}}, Crash: scenario.CrashPlan{MaxPoints: budget, Recovery: cases}, Steps: []scenario.Step{{Name: "create", Do: func(e scenario.Env) error {
			rt, err := e.Runtime()
			if err != nil {
				return err
			}
			if err = rt.SetFirstTurn(fakeruntime.TurnPlan{Actions: []fakeruntime.TurnAction{{Kind: fakeruntime.ActionRecv}, {Kind: fakeruntime.ActionAck}}, EndAfter: true}); err != nil {
				return err
			}
			if held {
				c, err := e.Adapter("chat")
				if err != nil {
					return err
				}
				if err = c.SetOutcome(e.Ctx(), fakeadapter.RequestCreate, fakeadapter.Outcome{Kind: fakeadapter.OutcomeHold}); err != nil {
					return err
				}
			}
			_, err = e.Agent("sender").Control(e.Ctx(), fakeagent.ControlInput{To: b.Sessions.RuntimeAddr(e, "chat"), Signal: adapterproto.SignalSessionCreate, Preset: "default", Body: "A real initial message."})
			return err
		}}}}, Drain: recoveryDrain(b, true, &DeliveryEvidence{}), Check: b.Recovery.CheckClosedRecords})
	}
	return out
}

func recoveryDrain(b Bindings, runtime bool, evidence *DeliveryEvidence) func(scenario.Env) error {
	return func(e scenario.Env) error {
		if runtime {
			c, err := e.Adapter("chat")
			if err != nil {
				return err
			}
			if err = c.ClearOutcomes(e.Ctx()); err != nil {
				return err
			}
			if err = c.ReleaseHeld(e.Ctx()); err != nil {
				return err
			}
		}
		for i := 0; i < 3; i++ {
			if runtime {
				log, err := e.Ledger()
				if err != nil {
					return err
				}
				seen := map[string]bool{}
				completed := map[string]bool{}
				for _, created := range log.Kind(ledger.KindRuntimeSessionCreated) {
					completed[created.Attrs.Attempt] = true
				}
				for _, created := range log.Kind(ledger.KindSessionCreateReceived) {
					endpoint := created.Attrs.Endpoint
					if !completed[created.Attrs.Attempt] || endpoint == "" || seen[endpoint] {
						continue
					}
					seen[endpoint] = true
					if err := evidence.RecvAndAck(e, b.Sessions.Session(e, endpoint)); err != nil {
						return err
					}
				}
			}
			e.Advance(time.Minute)
			if err := e.Quiesce(); err != nil {
				return err
			}
			for _, name := range []string{"sender", "receiver"} {
				if err := evidence.RecvAndAck(e, e.Agent(name)); err != nil {
					return err
				}
			}
		}
		return nil
	}
}

func recoveryStartupRecipes() []StartupRecipe {
	return []StartupRecipe{{Name: "recovery-layout", Kind: Start, MaxPoints: 2, Recovery: recoveryCases(crashwalk.CorruptOffline("state/checkpoints", "remove-empty-directory"), "layout/dir.mkdir", "layout/commit")}}
}
