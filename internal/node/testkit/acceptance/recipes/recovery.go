package recipes

import (
	"errors"
	"fmt"
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

// RecoveryOps exposes read-only damage facts and the final disk audit.
// The binding resolves the current process each time, including after a crash.
type RecoveryOps struct {
	CheckClosedRecords func(scenario.Env) error
	InspectDamage      func(scenario.Env) (DamageFacts, error)
}

// DamageFacts contains read-only observations about the actual offline damage.
type DamageFacts struct {
	Applied              bool
	Endpoint             string
	Body                 string
	OwnerAlerts          int
	ControlUnknownAlerts int
	CorruptBodyAlerts    int
}

func checkRecoveryDamage(b Bindings, e scenario.Env) error {
	facts, err := b.Recovery.InspectDamage(e)
	if err != nil {
		return err
	}
	if !facts.Applied {
		return nil
	}
	if facts.Body != "" {
		if facts.CorruptBodyAlerts != 1 {
			return fmt.Errorf("damaged source body has %d corresponding owner alerts", facts.CorruptBodyAlerts)
		}
		return nil
	}
	if facts.OwnerAlerts == 0 {
		return errors.New("quarantined record has no owner alert")
	}
	if facts.Endpoint != "" {
		if err := b.Sessions.CheckClosedSession(e, facts.Endpoint); err != nil {
			return err
		}
		if facts.ControlUnknownAlerts == 0 {
			return errors.New("creating quarantine did not report control uncertainty")
		}
	}
	return nil
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
		name   string
		damage DamageKind
		cases  []crashwalk.RecoveryCase
	}{
		{"recovery-send", "", append(recoveryCases(recoveryCrash("intake/out.create#1"), "orphan-gc/body.remove", "orphan-gc/commit"), recoveryCases(recoveryCrash("intake/out.write#1"), "txn-cleanup/tmp.remove", "txn-cleanup/commit")...)},
		{"recovery-source-trusted", DamageSource, recoveryCases(crashwalk.CorruptOffline("source", "preserve-header"), "quarantine/seq-floor.create", "quarantine/seq-floor.write", "quarantine/seq-floor.rename", "quarantine/overwrite.create", "quarantine/overwrite.write", "quarantine/overwrite.rename", "quarantine/alert.create", "quarantine/alert.write", "quarantine/alert.rename", "quarantine/commit")},
		{"recovery-source-unreadable", DamageSource, recoveryCases(crashwalk.CorruptOffline("source", "unreadable"), "quarantine/out-lost.create", "quarantine/out-lost.write", "quarantine/out-lost.rename")},
		{"recovery-target", DamageTarget, recoveryCases(crashwalk.CorruptOffline("target", "unreadable"), "quarantine/record.remove")},
	} {
		evidence := newDeliveryEvidence(b)
		out = append(out, ScriptRecipe{Needs: Needs{Damage: variant.damage}, Script: scenario.Script{Name: variant.name, Setup: scenario.Setup{Direct: []string{"sender", "receiver"}}, Crash: scenario.CrashPlan{MaxPoints: 72, Recovery: variant.cases}, Steps: []scenario.Step{{Name: "send", Do: func(e scenario.Env) error {
			result, err := e.Agent("sender").Send(e.Ctx(), fakeagent.SendInput{To: []string{e.Addr("receiver")}, Body: "Recovery must retain a real delivery obligation."})
			if err == nil {
				evidence.Accepted(result, "Recovery must retain a real delivery obligation.")
			}
			return err
		}}}}, Drain: recoveryDrain(b, false, evidence), Check: func(e scenario.Env) error {
			if err := evidence.Check(e); err != nil {
				return err
			}
			if err := checkRecoveryDamage(b, e); err != nil {
				return err
			}
			return b.Recovery.CheckClosedRecords(e)
		}})
	}
	for _, held := range []bool{false, true} {
		name := "recovery-create"
		budget := 107
		cases := append(recoveryCases(recoveryCrash("control-intake/ctl.create#1"), "orphan-gc-endpoint/endpoint.remove", "orphan-gc-endpoint/commit"), recoveryCases(recoveryCrash("control-intake/derived.create#1"), "derived-replay/derived.create", "derived-replay/derived.write", "derived-replay/derived.rename", "derived-replay/commit")...)
		cases = append(cases, recoveryCases(recoveryCrash("control-result/endpoint.create#1"), "ctl-effect-replay/endpoint.create", "ctl-effect-replay/endpoint.write", "ctl-effect-replay/endpoint.rename", "ctl-effect-replay/commit")...)
		if held {
			name = "recovery-creating"
			budget = 113
			cases = recoveryCases(crashwalk.CorruptOffline("creating-control", "unreadable"), "quarantine/session.create", "quarantine/session.write", "quarantine/session.rename")
		}
		facts := newDeliveryEvidence(b)
		needs := Needs{RuntimeAdapter: true}
		if held {
			needs.Damage = DamageCreatingControl
		}
		out = append(out, ScriptRecipe{Needs: needs, Script: scenario.Script{Name: name, Setup: scenario.Setup{Direct: []string{"sender", "receiver"}}, Crash: scenario.CrashPlan{MaxPoints: budget, Recovery: cases}, Steps: []scenario.Step{{Name: "create", Do: func(e scenario.Env) error {
			rt, err := e.Runtime()
			if err != nil {
				return err
			}
			// Creation starts a real turn before its initial delivery is readable.
			// Keep it working until the drain reads and acknowledges that delivery;
			// announcing idle earlier races a second prompt against the direct receive.
			if err = rt.SetFirstTurn(fakeruntime.TurnPlan{}); err != nil {
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
			result, err := e.Agent("sender").Control(e.Ctx(), fakeagent.ControlInput{To: b.Sessions.RuntimeAddr(e, "chat"), Signal: adapterproto.SignalSessionCreate, Preset: "default", Body: "A real initial message."})
			if err == nil {
				facts.AcceptedCreation(result, "A real initial message.", "sender")
			}
			return err
		}}}}, Drain: recoveryDrain(b, true, facts), Check: func(e scenario.Env) error {
			if err := facts.Check(e); err != nil {
				return err
			}
			log, err := e.Ledger()
			if err != nil {
				return err
			}
			if len(log.Kind(ledger.KindPromptDeliverReceived)) != 0 {
				return errors.New("recovery creation emitted an unplanned prompt before its drain")
			}
			ended := map[string]bool{}
			for _, entry := range log.Kind(ledger.KindRuntimeTurnEnded) {
				ended[entry.Writer+"/"+entry.Attrs.Turn] = true
			}
			for _, entry := range log.Kind(ledger.KindRuntimeTurnStarted) {
				if !ended[entry.Writer+"/"+entry.Attrs.Turn] {
					return fmt.Errorf("recovery creation left runtime turn %s/%s unfinished", entry.Writer, entry.Attrs.Turn)
				}
			}
			if err := checkRecoveryDamage(b, e); err != nil {
				return err
			}
			return b.Recovery.CheckClosedRecords(e)
		}})
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
				completed := map[string]string{}
				for _, created := range log.Kind(ledger.KindRuntimeSessionCreated) {
					completed[created.Attrs.Attempt] = created.Attrs.NativeRef
				}
				for _, created := range log.Kind(ledger.KindSessionCreateReceived) {
					endpoint := created.Attrs.Endpoint
					native := completed[created.Attrs.Attempt]
					if native == "" || endpoint == "" || seen[endpoint] {
						continue
					}
					seen[endpoint] = true
					if err := evidence.RecvAndAck(e, b.Sessions.Session(e, endpoint)); err != nil {
						return err
					}
					controller, err := e.Runtime()
					if err != nil {
						return err
					}
					// A restart can repeat this drain after the same turn already ended.
					// Every other failure still means the promised runtime round did not finish.
					if err := controller.EndTurn(native, fakeruntime.FinalCompleted); err != nil && !errors.Is(err, fakeruntime.ErrNoTurn) {
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
	return []StartupRecipe{{Needs: Needs{Damage: DamageLayout}, Name: "recovery-layout", Kind: Start, MaxPoints: 2, Recovery: recoveryCases(crashwalk.CorruptOffline("state/checkpoints", "remove-empty-directory"), "layout/dir.mkdir", "layout/commit")}}
}
