package crashwalk_test

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/txn"
)

// This layer reaches status only during recovery, and never reaches journal or
// replay. The declaration table remains the same as the complete program's.
func partitionedSystem(t *testing.T) (*fakeBackend, scenario, *crashwalk.CoveragePartition) {
	t.Helper()
	b, s := recoveryOnlySystem(t)
	p := b.progs[s.name]
	p.steps = p.steps[:1]
	s.steps = s.steps[:1]
	b.progs[s.name] = p
	b.recoveryRecord = slices.DeleteFunc(b.recoveryRecord, func(h rawHit) bool { return strings.HasPrefix(h.key, "replay/") })
	s.recovery = slices.DeleteFunc(s.recovery, func(c crashwalk.RecoveryCase) bool { return c.Item.Tag.Tx == "replay" })
	partition := &crashwalk.CoveragePartition{}
	for _, k := range table(t).Keys() {
		if k.Tag.Tx == "journal" || k.Tag.Tx == "replay" {
			partition.CoveredElsewhere = append(partition.CoveredElsewhere, k.Key)
		} else {
			partition.Required = append(partition.Required, k.Key)
		}
	}
	return b, s, partition
}

func TestWalkPartitionRequiresOnlyItsOwnCoverageAndKeepsActualCounts(t *testing.T) {
	for _, mode := range []crashwalk.Mode{crashwalk.Full, crashwalk.Smoke} {
		b, s, partition := partitionedSystem(t)
		rep, err := crashwalk.Walk(context.Background(), b, table(t), []crashwalk.Scenario{s}, crashwalk.Options{Mode: mode, Coverage: partition})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(rep.Coverage, partition) {
			t.Fatalf("partition %+v, want %+v", rep.Coverage, partition)
		}
		var declaredNormal, declaredRecovery, requiredNormal, requiredRecovery int
		for _, k := range table(t).Keys() {
			if k.Kind == txn.Normal {
				declaredNormal++
				if slices.Contains(partition.Required, k.Key) {
					requiredNormal++
				}
			} else {
				declaredRecovery++
				if slices.Contains(partition.Required, k.Key) {
					requiredRecovery++
				}
			}
		}
		if rep.Normal.Declared != declaredNormal || rep.Normal.Reached != requiredNormal || rep.Recovery.Declared != declaredRecovery || rep.Recovery.Reached != requiredRecovery {
			t.Fatalf("counts no longer represent actual hits against the whole table: %+v", rep)
		}
		if rep.RecoveryNormalPlanned != 4 || len(rep.Sources.NormalOnlyInRecovery) != 6 {
			t.Fatalf("missing actual dynamic sources: %+v", rep)
		}
		wantDynamic, wantStatic := 4, 2
		if mode == crashwalk.Smoke {
			wantDynamic, wantStatic = 0, 1
		}
		if rep.RecoveryNormalRuns != wantDynamic || rep.RecoveryRuns != wantStatic || rep.RecoveryNormalSkipped != (mode == crashwalk.Smoke) {
			t.Fatalf("partition changed mode selection: %+v", rep)
		}
	}
}

func TestWalkCopiesCoverageBeforeRunning(t *testing.T) {
	b, s, partition := partitionedSystem(t)
	want := &crashwalk.CoveragePartition{Required: slices.Clone(partition.Required), CoveredElsewhere: slices.Clone(partition.CoveredElsewhere)}
	opts := crashwalk.Options{Mode: crashwalk.Full, Coverage: partition, Observe: func(e crashwalk.Event) {
		if e.Phase == "record" && !e.Done {
			partition.Required[0] = fsx.Key{}
			partition.CoveredElsewhere[0] = fsx.Key{}
		}
	}}
	rep, err := crashwalk.Walk(context.Background(), b, table(t), []crashwalk.Scenario{s}, opts)
	if err != nil || !reflect.DeepEqual(rep.Coverage, want) {
		t.Fatalf("caller mutation changed the walk's partition: %+v %v", rep.Coverage, err)
	}
}

func TestWalkRejectsInvalidPartitionsBeforeRunning(t *testing.T) {
	for _, fault := range []string{"unclassified", "overlap", "duplicate required", "duplicate elsewhere", "unknown", "empty required", "elsewhere recovery assignment"} {
		t.Run(fault, func(t *testing.T) {
			b, s, p := partitionedSystem(t)
			want := ""
			switch fault {
			case "unclassified":
				p.Required = p.Required[1:]
				want = "unclassified"
			case "overlap":
				p.CoveredElsewhere = append(p.CoveredElsewhere, p.Required[0])
				want = "exactly once"
			case "duplicate required":
				p.Required = append(p.Required, p.Required[0])
				want = "exactly once"
			case "duplicate elsewhere":
				p.CoveredElsewhere = append(p.CoveredElsewhere, p.CoveredElsewhere[0])
				want = "exactly once"
			case "unknown":
				p.Required = append(p.Required, key(t, "unknown/file.write"))
				want = "undeclared"
			case "empty required":
				p.CoveredElsewhere = append(p.CoveredElsewhere, p.Required...)
				p.Required = nil
				want = "Required is empty"
			case "elsewhere recovery assignment":
				s.recovery = append(s.recovery, crashwalk.RecoveryCase{Item: key(t, "replay/commit"), After: s.recovery[0].After})
				want = "assigns CoveredElsewhere"
			}
			_, err := crashwalk.Walk(context.Background(), b, table(t), []crashwalk.Scenario{s}, crashwalk.Options{Mode: crashwalk.Full, Coverage: p})
			if err == nil || !strings.Contains(err.Error(), want) || len(b.runs) != 0 {
				t.Fatalf("invalid partition ran backend or failed for another reason: runs=%d error=%v", len(b.runs), err)
			}
		})
	}
}

func TestWalkPartitionRejectsANewUnclassifiedDeclaration(t *testing.T) {
	original := &txn.Decl{Name: "original", Kind: txn.Normal, Writes: []txn.Write{{Role: "file", Op: txn.OpRemove}}}
	old, err := txn.NewTable(original)
	if err != nil {
		t.Fatal(err)
	}
	p := &crashwalk.CoveragePartition{}
	for _, k := range old.Keys() {
		p.Required = append(p.Required, k.Key)
	}
	current, err := txn.NewTable(original, &txn.Decl{Name: "new", Kind: txn.Normal, Writes: []txn.Write{{Role: "file", Op: txn.OpRemove}}})
	if err != nil {
		t.Fatal(err)
	}
	b, s := system(t)
	_, err = crashwalk.Walk(context.Background(), b, current, []crashwalk.Scenario{s}, crashwalk.Options{Mode: crashwalk.Full, Coverage: p})
	if err == nil || !strings.Contains(err.Error(), "new/file.remove") || !strings.Contains(err.Error(), "unclassified") || len(b.runs) != 0 {
		t.Fatalf("new declaration escaped classification: %v", err)
	}
}

func TestWalkPartitionStillRequiresItsNormalAndRecoveryHits(t *testing.T) {
	for _, name := range []string{"intake/body.write", "orphans/dir.sync-dir"} {
		t.Run(name, func(t *testing.T) {
			b, s, p := partitionedSystem(t)
			backend := backendFunc(func(ctx context.Context, sc crashwalk.Scenario, plan crashwalk.RunPlan) (crashwalk.RunResult, error) {
				r, err := b.Run(ctx, sc, plan)
				for i := range r.Hits {
					r.Hits[i] = slices.DeleteFunc(r.Hits[i], func(h crashwalk.Hit) bool { return h.Point.Key.String() == name })
				}
				return r, err
			})
			_, err := crashwalk.Walk(context.Background(), backend, table(t), []crashwalk.Scenario{s}, crashwalk.Options{Mode: crashwalk.Full, Coverage: p})
			if err == nil || !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), "never reached") {
				t.Fatalf("missing Required hit accepted: %v", err)
			}
		})
	}
}

func TestWalkPartitionRejectsElsewhereHitsInEveryRun(t *testing.T) {
	for _, phase := range []string{"record", "recovery-record", "main", "boundary", "recovery", "recovery-normal", "generation 3"} {
		t.Run(phase, func(t *testing.T) {
			b, s, p := partitionedSystem(t)
			injected := false
			backend := backendFunc(func(ctx context.Context, sc crashwalk.Scenario, plan crashwalk.RunPlan) (crashwalk.RunResult, error) {
				r, err := b.Run(ctx, sc, plan)
				current, gen := "record", 1
				if plan.Trigger != nil {
					current, gen = "recovery-record", 2
					if len(plan.Targets) > 0 {
						current = "recovery"
						if plan.Targets[0].Point.Tag.Tx == "status" {
							current = "recovery-normal"
						}
					}
				} else if !plan.Record {
					current = "main"
					if plan.Targets[0].AfterStep != "" {
						current = "boundary"
					}
				}
				if phase == "generation 3" && current == "recovery" {
					current, gen = phase, 3
				}
				if current == phase {
					r.Hits[gen-1] = append(r.Hits[gen-1], crashwalk.Hit{Gen: gen, Point: point(t, "journal/events.open#1")})
					injected = true
				}
				return r, err
			})
			_, err := crashwalk.Walk(context.Background(), backend, table(t), []crashwalk.Scenario{s}, crashwalk.Options{Mode: crashwalk.Full, Coverage: p})
			if !injected || err == nil || !strings.Contains(err.Error(), "CoveredElsewhere") || !strings.Contains(err.Error(), "journal/events.open") {
				t.Fatalf("actual source disagreement accepted: injected=%v error=%v", injected, err)
			}
		})
	}
}

func TestWalkPartitionPreservesRecoveryEvidenceAndInterruptionChecks(t *testing.T) {
	for _, fault := range []string{"third generation", "observation only"} {
		t.Run(fault, func(t *testing.T) {
			b, s, p := partitionedSystem(t)
			changed := false
			backend := backendFunc(func(ctx context.Context, sc crashwalk.Scenario, plan crashwalk.RunPlan) (crashwalk.RunResult, error) {
				r, err := b.Run(ctx, sc, plan)
				if fault == "third generation" && plan.Trigger != nil && len(plan.Targets) == 0 {
					r.Hits = append(r.Hits, r.Hits[1])
					r.Hits[1] = nil
					for i := range r.Hits[2] {
						r.Hits[2][i].Gen = 3
					}
					changed = true
				}
				if fault == "observation only" && len(plan.Targets) > 0 && plan.Targets[0].Point.Tag.Tx == "status" {
					r.Reached = []bool{false}
					changed = true
				}
				return r, err
			})
			_, err := crashwalk.Walk(context.Background(), backend, table(t), []crashwalk.Scenario{s}, crashwalk.Options{Mode: crashwalk.Full, Coverage: p})
			want := "complete recovery recording must end in generation 2"
			if fault == "observation only" {
				want = "not reached"
			}
			if !changed || err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("invalid evidence accepted: changed=%v error=%v", changed, err)
			}
		})
	}
}
