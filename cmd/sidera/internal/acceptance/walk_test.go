//go:build acceptance

package acceptance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/manageapi"
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
)

func binaryBackend(t *testing.T) (*WalkBackend, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	return NewWalkBackend(runRoot(t), testBinaries, Options{Seed: t.Name()}), ctx
}
func point(t *testing.T, s string) fsx.Point {
	t.Helper()
	p, err := fsx.ParsePoint(s)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func checkRun(t *testing.T, r crashwalk.RunResult, err error, gens int) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Hits) != gens || len(r.Violations) != 0 {
		t.Fatalf("generations=%d want=%d violations=%v", len(r.Hits), gens, r.Violations)
	}
	for i, reached := range r.Reached {
		if !reached {
			t.Fatalf("target %d not interrupted", i)
		}
	}
}
func exchangeRecipe(recovery recipes.RecoveryOps) recipes.ScriptRecipe {
	var accepted string
	facts := recipes.NewDeliveryEvidence(func(e scenario.Env) (ident.NodeID, error) { return envFrom(e).localNode() })
	const body = "Retain a real interrupted obligation."
	return recipes.ScriptRecipe{Script: scenario.Script{Name: "binary-exchange", Setup: scenario.Setup{Direct: []string{"sender", "receiver"}}, Crash: scenario.CrashPlan{MaxPoints: 100}, Steps: []scenario.Step{{Name: "send", Do: func(e scenario.Env) error {
		sent, err := e.Agent("sender").Send(e.Ctx(), fakeagent.SendInput{To: []string{e.Addr("receiver")}, Body: body})
		if err == nil {
			accepted = string(sent.MessageID)
			facts.Accepted(sent, body)
		}
		return err
	}}}}, Drain: func(e scenario.Env) error {
		for i := 0; i < 3; i++ {
			for _, name := range []string{"receiver", "sender"} {
				if err := facts.RecvAndAck(e, e.Agent(name)); err != nil {
					return err
				}
			}
			e.Advance(time.Minute)
			if err := e.Quiesce(); err != nil {
				return err
			}
		}
		return nil
	}, Check: func(e scenario.Env) error {
		if err := recovery.CheckClosedRecords(e); err != nil {
			return err
		}
		log, err := e.Ledger()
		if err != nil {
			return err
		}
		if accepted != "" && len(log.Entries()) == 0 {
			return fmt.Errorf("confirmed send has no ledger")
		}
		return facts.Check(e)
	}}
}

func TestBinaryWalkFreshBindingAndOnlineJudgement(t *testing.T) {
	b, ctx := binaryBackend(t)
	const sentinel = "caller-owned"
	if err := os.WriteFile(filepath.Join(b.root, sentinel), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	binds := 0
	bind := func(recipes.RecoveryOps) (recipes.ScriptRecipe, error) {
		binds++
		completed := false
		return recipes.ScriptRecipe{Script: scenario.Script{Name: "fresh-online", Crash: scenario.CrashPlan{MaxPoints: 10}, Steps: []scenario.Step{{Name: "complete", Do: func(e scenario.Env) error {
			if completed {
				return fmt.Errorf("step evidence leaked between runs")
			}
			completed = true
			return nil
		}}}}, Check: func(e scenario.Env) error {
			if !completed {
				return fmt.Errorf("check lost this run's step evidence")
			}
			f := envFrom(e).fixture
			raw, err := f.Manage(e.Ctx(), "status", manageapi.Request{})
			if err != nil {
				return err
			}
			var status struct {
				Daemon struct {
					State string `json:"state"`
				} `json:"daemon"`
			}
			if err := resultBody(raw, &status); err != nil {
				return err
			}
			if status.Daemon.State != "running" {
				return fmt.Errorf("judgement requires running daemon, got %q", status.Daemon.State)
			}
			return nil
		}}, nil
	}
	descriptor, err := bind(recipes.RecoveryOps{})
	if err != nil {
		t.Fatal(err)
	}
	binds = 0
	s := NewWalkScenario(descriptor, bind)
	for range 2 {
		r, err := b.Run(ctx, s, crashwalk.RunPlan{Record: true})
		checkRun(t, r, err, 1)
		entries, err := os.ReadDir(b.root)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 || entries[0].Name() != sentinel {
			t.Fatalf("run did not remove only its own child: %v", entries)
		}
	}
	if binds != 2 {
		t.Fatalf("binding calls=%d, want 2", binds)
	}
}

func TestBinaryWalkRejectsChangedMetadata(t *testing.T) {
	descriptor := recipes.ScriptRecipe{Script: scenario.Script{Name: "metadata", Crash: scenario.CrashPlan{MaxPoints: 10}, Steps: []scenario.Step{{Name: "complete", Do: func(scenario.Env) error { return nil }}}}}
	for _, tc := range []struct {
		name   string
		change func(*recipes.ScriptRecipe)
	}{
		{"name", func(r *recipes.ScriptRecipe) { r.Script.Name = "changed" }},
		{"steps", func(r *recipes.ScriptRecipe) { r.Script.Steps = nil }},
		{"budget", func(r *recipes.ScriptRecipe) { r.Script.Crash.MaxPoints++ }},
		{"needs", func(r *recipes.ScriptRecipe) { r.Needs.RuntimeAdapter = true }},
		{"recovery", func(r *recipes.ScriptRecipe) { r.Script.Crash.Recovery = []crashwalk.RecoveryCase{{}} }},
		{"extra triggers", func(r *recipes.ScriptRecipe) { r.RecoveryTriggers = []crashwalk.Trigger{{}} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, ctx := binaryBackend(t)
			s := NewWalkScenario(descriptor, func(recipes.RecoveryOps) (recipes.ScriptRecipe, error) {
				r := descriptor
				tc.change(&r)
				return r, nil
			})
			if _, err := b.Run(ctx, s, crashwalk.RunPlan{Record: true}); err == nil || !strings.Contains(err.Error(), "changed scenario metadata") {
				t.Fatalf("changed metadata result: %v", err)
			}
		})
	}
}
func TestBinaryWalkExchangeCutoffsAndRecovery(t *testing.T) {
	b, ctx := binaryBackend(t)
	s := NewWalkScenario(exchangeRecipe(recipes.RecoveryOps{}), func(ops recipes.RecoveryOps) (recipes.ScriptRecipe, error) { return exchangeRecipe(ops), nil })
	clean, err := b.Run(ctx, s, crashwalk.RunPlan{Record: true})
	checkRun(t, clean, err, 1)
	trigger := crashwalk.CrashAt(point(t, "intake/out.write#1"))
	recovered, err := b.Run(ctx, s, crashwalk.RunPlan{Trigger: &trigger})
	checkRun(t, recovered, err, 2)
	if err := crashwalk.CheckRecoveryRecord(clean.Hits[0], trigger, recovered); err != nil {
		t.Fatal(err)
	}
	target := crashwalk.Target{Gen: 2, Point: point(t, "txn-cleanup/tmp.remove#1")}
	twice, err := b.Run(ctx, s, crashwalk.RunPlan{Trigger: &trigger, Targets: []crashwalk.Target{target}})
	checkRun(t, twice, err, 3)
	if err := crashwalk.CheckRecoveryTrigger(clean.Hits[0], trigger, twice); err != nil {
		t.Fatal(err)
	}
	if err := crashwalk.CheckRecoveryTarget(recovered.Hits[1], target, twice); err != nil {
		t.Fatal(err)
	}
	for _, target := range []crashwalk.Target{{Gen: 1, Point: point(t, "intake/out.write#1")}, {Gen: 1, AfterStep: "send"}} {
		r, err := b.Run(ctx, s, crashwalk.RunPlan{Targets: []crashwalk.Target{target}})
		checkRun(t, r, err, 2)
		if target.AfterStep == "" {
			if err := crashwalk.CompareRuns(clean.Hits[0], r.Hits[0], target); err != nil {
				t.Fatal(err)
			}
		}
	}
}
func TestBinaryWalkStartupAndOfflineRecovery(t *testing.T) {
	b, ctx := binaryBackend(t)
	init := &StartupWalkScenario{Recipe: recipes.StartupRecipe{Name: "init", Kind: recipes.Init, MaxPoints: 100}}
	if len(init.Steps()) != 0 {
		t.Fatal("init declared a boundary after exit")
	}
	clean, err := b.Run(ctx, init, crashwalk.RunPlan{Record: true})
	checkRun(t, clean, err, 1)
	target := crashwalk.Target{Gen: 1, Point: point(t, "init/identity.rename#1")}
	// Select a real initialized-file rename from the recording, without assuming
	// the storage role names of the identity implementation.
	found := false
	for _, hit := range clean.Hits[0] {
		if hit.Point.Step == fsx.StepRename {
			target.Point = hit.Point
			found = true
			break
		}
	}
	if !found {
		t.Fatal("init did not rename a file")
	}
	r, err := b.Run(ctx, init, crashwalk.RunPlan{Targets: []crashwalk.Target{target}})
	checkRun(t, r, err, 2)
	if err := crashwalk.CompareRuns(clean.Hits[0], r.Hits[0], target); err != nil {
		t.Fatal(err)
	}
	startup := &StartupWalkScenario{Recipe: recipes.StartupRecipe{Name: "layout", Kind: recipes.Start, MaxPoints: 20, Needs: recipes.Needs{Damage: recipes.DamageLayout}}}
	trigger := crashwalk.CorruptOffline("state/checkpoints", "remove-empty-directory")
	recovered, err := b.Run(ctx, startup, crashwalk.RunPlan{Trigger: &trigger})
	checkRun(t, recovered, err, 2)
	target = crashwalk.Target{Gen: 2, Point: point(t, "layout/dir.mkdir#1")}
	r, err = b.Run(ctx, startup, crashwalk.RunPlan{Trigger: &trigger, Targets: []crashwalk.Target{target}})
	checkRun(t, r, err, 3)
	if err := crashwalk.CheckRecoveryTarget(recovered.Hits[1], target, r); err != nil {
		t.Fatal(err)
	}
	r, err = b.Run(ctx, startup, crashwalk.RunPlan{Targets: []crashwalk.Target{{Gen: 1, AfterStep: "complete"}}})
	checkRun(t, r, err, 2)
}

func TestBinaryWalkStartupClockAndRenewal(t *testing.T) {
	for _, recipe := range recipes.New(recipes.Bindings{}).Startups {
		transaction := ""
		if recipe.AdvanceAfterStart != 0 {
			transaction = "clock-high-water"
		} else if recipe.StartAfter != 0 {
			transaction = "cert-renew"
		} else {
			continue
		}
		t.Run(recipe.Name, func(t *testing.T) {
			b, ctx := binaryBackend(t)
			s := &StartupWalkScenario{Recipe: recipe}
			clean, err := b.Run(ctx, s, crashwalk.RunPlan{Record: true})
			checkRun(t, clean, err, 1)
			var target crashwalk.Target
			for _, hit := range clean.Hits[0] {
				if hit.Point.Tag.Tx == transaction && hit.Point.Step == fsx.StepRename {
					target = crashwalk.Target{Gen: 1, Point: hit.Point}
					break
				}
			}
			if target.Gen == 0 {
				t.Fatalf("startup did not perform %s", transaction)
			}
			r, err := b.Run(ctx, s, crashwalk.RunPlan{Targets: []crashwalk.Target{target}})
			checkRun(t, r, err, 2)
			if err := crashwalk.CompareRuns(clean.Hits[0], r.Hits[0], target); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBinaryWalkTerminalExitAndRefusal(t *testing.T) {
	for _, kind := range []recipes.TerminalKind{recipes.Stop, recipes.Retire} {
		t.Run(fmt.Sprint(kind), func(t *testing.T) {
			b, ctx := binaryBackend(t)
			s := &TerminalWalkScenario{Recipe: recipes.TerminalRecipe{Name: "terminal", Kind: kind, RequestID: admin.OperationID{77}, MaxPoints: 20}}
			if len(s.Steps()) != 0 {
				t.Fatal("terminal declared a boundary after exit")
			}
			clean, err := b.Run(ctx, s, crashwalk.RunPlan{Record: true})
			checkRun(t, clean, err, 1)
			var target crashwalk.Target
			for _, hit := range clean.Hits[0] {
				if !hit.Setup && hit.Point.Step == fsx.StepRename {
					target = crashwalk.Target{Gen: 1, Point: hit.Point}
					break
				}
			}
			if target.Gen == 0 {
				t.Fatal("terminal had no real rename")
			}
			r, err := b.Run(ctx, s, crashwalk.RunPlan{Targets: []crashwalk.Target{target}})
			checkRun(t, r, err, 2)
			if err := crashwalk.CompareRuns(clean.Hits[0], r.Hits[0], target); err != nil {
				t.Fatal(err)
			}
		})
	}
}
