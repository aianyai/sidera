package harness_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/daemon"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/keys"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
)

func startupWalk(op harness.StartupOperation) *harness.StartupWalkScenario {
	return &harness.StartupWalkScenario{Label: "startup", Operation: op, PointBudget: 300}
}

func startupResult(t testing.TB, b *harness.WalkBackend, s *harness.StartupWalkScenario, p crashwalk.RunPlan) crashwalk.RunResult {
	t.Helper()
	r, err := b.Run(context.Background(), s, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Violations) != 0 {
		t.Fatalf("startup invariants: %v", r.Violations)
	}
	for i, hs := range r.Hits {
		for _, h := range hs {
			if h.Gen != i+1 || h.Setup {
				t.Fatalf("startup observation in wrong window: %+v", h)
			}
		}
	}
	return r
}

func TestStartupWalkInitUsesActualWritesAndSkipsACompletedMarker(t *testing.T) {
	b := harness.NewWalkBackend(t)
	s := startupWalk(harness.StartupInit)
	checked := 0
	s.Check = func(r fsx.Reader) error {
		checked++
		if done, err := keys.Initialized(r); err != nil || !done {
			return fmt.Errorf("init incomplete: %v", err)
		}
		return nil
	}
	rec := startupResult(t, b, s, crashwalk.RunPlan{Record: true})
	cov := crashwalk.Assess(harness.ProgramTable(t), rec.Hits[0])
	t.Logf("startup recording: normal %d/%d, recovery %d/%d; main points %d", cov.Normal.Reached, cov.Normal.Declared, cov.Recovery.Reached, cov.Recovery.Declared, len(crashwalk.Plan(rec.Hits[0], crashwalk.Full, nil)))
	again := startupResult(t, b, s, crashwalk.RunPlan{Record: true})
	if !reflect.DeepEqual(rec.Hits, again.Hits) {
		t.Fatal("init recording changed")
	}
	for _, row := range []struct {
		point, path string
		completed   bool
	}{
		{"init/identity.rename#1", keys.IdentityKeyPath, false},
		{"init/marker.rename#1", keys.InitMarkerPath, false},
		{"init/marker.sync-dir#1", "keys", true},
		{"init/commit#1", "", true},
	} {
		t.Run(row.point, func(t *testing.T) {
			target := crashwalk.Target{Gen: 1, Point: walkPoint(t, row.point)}
			out := startupResult(t, b, s, crashwalk.RunPlan{Targets: []crashwalk.Target{target}})
			if !out.Reached[0] || len(out.Hits) != 2 {
				t.Fatalf("init did not recover: %+v", out)
			}
			if err := crashwalk.CompareRuns(rec.Hits[0], out.Hits[0], target); err != nil {
				t.Fatal(err)
			}
			last := out.Hits[0][len(out.Hits[0])-1]
			if last.Path != row.path {
				t.Fatalf("actual init path: %+v", last)
			}
			if last.Point.Step == fsx.StepRename && !strings.Contains(last.From, ".tmp-*") {
				t.Fatalf("actual init rename source: %+v", last)
			}
			if row.completed && len(out.Hits[1]) != 0 {
				t.Fatal("completed init fabricated a second writer trace")
			}
			if !row.completed && len(out.Hits[1]) == 0 {
				t.Fatal("unfinished init never ran again")
			}
		})
	}
	if checked != 6 {
		t.Fatalf("read-only oracle called %d times", checked)
	}
}

func TestStartupWalkStartObservesOnlyItsOwnWriterAndStopsBeforeRestart(t *testing.T) {
	b := harness.NewWalkBackend(t)
	s := startupWalk(harness.StartupStart)
	s.StartAfter = 21 * 24 * time.Hour
	rec := startupResult(t, b, s, crashwalk.RunPlan{Record: true})
	cov := crashwalk.Assess(harness.ProgramTable(t), rec.Hits[0])
	t.Logf("startup recording: normal %d/%d, recovery %d/%d; main points %d", cov.Normal.Reached, cov.Normal.Declared, cov.Recovery.Reached, cov.Recovery.Declared, len(crashwalk.Plan(rec.Hits[0], crashwalk.Full, nil)))
	again := startupResult(t, b, s, crashwalk.RunPlan{Record: true})
	if !reflect.DeepEqual(rec.Hits, again.Hits) {
		t.Fatal("start recording changed")
	}
	var point fsx.Point
	for _, hit := range rec.Hits[0] {
		if hit.Point.Tag.Tx == "init" {
			t.Fatal("setup init polluted start generation")
		}
		if hit.Point.String() == "cert-renew/cert.rename#1" {
			point = hit.Point
		}
	}
	if point == (fsx.Point{}) {
		t.Fatal("real start did not renew its certificate")
	}
	for _, target := range []crashwalk.Target{{Gen: 1, Point: point}, {Gen: 1, Point: walkPoint(t, "cert-renew/commit#1")}, {Gen: 1, AfterStep: s.Steps()[0]}} {
		out := startupResult(t, b, s, crashwalk.RunPlan{Targets: []crashwalk.Target{target}})
		if !out.Reached[0] || len(out.Hits) != 2 {
			t.Fatalf("start not reclaimed: %+v", out)
		}
		if target.Point == point {
			last := out.Hits[0][len(out.Hits[0])-1]
			if last.Path != keys.CertificatePath || !strings.Contains(last.From, ".tmp-*") {
				t.Fatalf("actual certificate rename: %+v", last)
			}
		}
		if target.AfterStep == "" {
			if err := crashwalk.CompareRuns(rec.Hits[0], out.Hits[0], target); err != nil {
				t.Fatal(err)
			}
		} else if !reflect.DeepEqual(rec.Hits[0], out.Hits[0]) {
			t.Fatal("boundary did not include the completed start and shutdown")
		}
	}
	// At init's current clock the first start has nothing to renew or repair.
	plain := startupWalk(harness.StartupStart)
	out := startupResult(t, b, plain, crashwalk.RunPlan{Targets: []crashwalk.Target{{Gen: 1, AfterStep: plain.Steps()[0]}}})
	if len(out.Hits) != 2 || !out.Reached[0] {
		t.Fatalf("quiet first start boundary: %+v", out)
	}
}

func TestStartupWalkLayoutRepairsTheStoppedDiskAcrossThreeGenerations(t *testing.T) {
	b := harness.NewWalkBackend(t)
	for _, name := range []string{"layout/dir.mkdir#1", "layout/commit#1"} {
		t.Run(name, func(t *testing.T) {
			s := startupWalk(harness.StartupStart)
			trigger := crashwalk.CorruptOffline("state/checkpoints", "remove-empty-directory")
			s.Damage = func(d fsx.FS, got crashwalk.Trigger) error {
				if got != trigger {
					return errors.New("wrong damage trigger")
				}
				// No lock may remain held by the first start.
				unlock, err := d.Lock("daemon.lock")
				if err != nil {
					return err
				}
				if err = unlock(); err != nil {
					return err
				}
				return fsx.RemoveAll(d, fsx.Tag{Tx: "offline", Role: "dir"}, got.Pattern)
			}
			s.Check = func(r fsx.Reader) error {
				missing, err := daemon.LayoutWork(r)
				if err != nil {
					return err
				}
				if len(missing) != 0 {
					return fmt.Errorf("missing layout: %v", missing)
				}
				return nil
			}
			target := crashwalk.Target{Gen: 2, Point: walkPoint(t, name)}
			s.RecoveryCases = []crashwalk.RecoveryCase{{Item: target.Point.Key, After: trigger}}
			out := startupResult(t, b, s, crashwalk.RunPlan{Trigger: &trigger, Targets: []crashwalk.Target{target}})
			if len(out.Hits) != 3 || !out.Reached[0] {
				t.Fatalf("layout not replayed: %+v", out)
			}
			last := out.Hits[1][len(out.Hits[1])-1]
			if last.Point != target.Point {
				t.Fatalf("recovery trace passed its cutoff: %+v", last)
			}
			if target.Point.Step == fsx.StepMkdir && last.Path != "state/checkpoints" {
				t.Fatalf("wrong layout path: %+v", last)
			}
			if target.Point.Step == fsx.StepCommit && (last.Path != "" || last.From != "") {
				t.Fatalf("commit operands: %+v", last)
			}
		})
	}
}

func TestStartupWalkReturnsTheFinalOracleFailure(t *testing.T) {
	s := startupWalk(harness.StartupInit)
	s.Check = func(fsx.Reader) error { return errors.New("startup oracle refused") }
	res, err := harness.NewWalkBackend(t).Run(context.Background(), s, crashwalk.RunPlan{Record: true})
	if err != nil || !reflect.DeepEqual(res.Violations, []string{"startup oracle refused"}) {
		t.Fatalf("startup oracle: %+v, %v", res, err)
	}
}

func TestStartupWalkInitPreservesKeysAndOwnerFilesAcrossItsRetry(t *testing.T) {
	b := harness.NewWalkBackend(t)
	s := startupWalk(harness.StartupInit)
	protected := []string{keys.IdentityKeyPath, keys.NetworkKeyPath, keys.SelfRevocationPath, keys.ClockPath, "config/node.json", "config/policy.json", "config/presets.json", "config/instances.json"}
	want := map[string]string{}
	s.Check = func(r fsx.Reader) error {
		for _, path := range protected {
			data, err := r.ReadFile(path)
			if err != nil {
				return err
			}
			want[path] = string(data)
		}
		return nil
	}
	startupResult(t, b, s, crashwalk.RunPlan{Record: true})
	checks := 0
	s.Check = func(r fsx.Reader) error {
		checks++
		for _, path := range protected {
			got, err := r.ReadFile(path)
			if err != nil {
				return err
			}
			if string(got) != want[path] {
				return fmt.Errorf("retry changed %s", path)
			}
		}
		return nil
	}
	target := crashwalk.Target{Gen: 1, Point: walkPoint(t, "init/marker.rename#1")}
	startupResult(t, b, s, crashwalk.RunPlan{Targets: []crashwalk.Target{target}})
	if checks != 1 {
		t.Fatalf("preservation oracle called %d times", checks)
	}
}

func TestStartupWalkInitCrashesItsActualTransactionRecovery(t *testing.T) {
	s := startupWalk(harness.StartupInit)
	trigger := crashwalk.CrashAt(walkPoint(t, "init/identity.rename#1"))
	target := crashwalk.Target{Gen: 2, Point: walkPoint(t, "txn-cleanup/commit#1")}
	out := startupResult(t, harness.NewWalkBackend(t), s, crashwalk.RunPlan{Trigger: &trigger, Targets: []crashwalk.Target{target}})
	if len(out.Hits) != 3 || !out.Reached[0] {
		t.Fatalf("init recovery not interrupted: %+v", out)
	}
	last := out.Hits[1][len(out.Hits[1])-1]
	if last.Point != target.Point || last.Path != "" || last.From != "" {
		t.Fatalf("recovery passed its logical commit: %+v", last)
	}
	removed := false
	for _, hit := range out.Hits[1] {
		if hit.Point.Tag.Tx == "txn-cleanup" && hit.Point.Step == fsx.StepRemove {
			removed = removed || strings.HasPrefix(hit.Path, "keys/.tmp-*")
		}
	}
	if !removed {
		t.Fatal("actual init recovery did not remove the interrupted key's temporary file")
	}
}
