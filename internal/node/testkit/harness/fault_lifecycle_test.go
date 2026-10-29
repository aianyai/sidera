package harness_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
	"github.com/aianyai/sidera/internal/node/txn"
)

// Run real testing cleanups in a child: unused rules must fail that test,
// without replacing the fixture's reporter or filtering any cleanup errors.
func TestFaultExpectationsEndOnlyAtRealCrashes(t *testing.T) {
	for _, tc := range []struct {
		name string
		pass bool
	}{
		{"runtime-crash", true},
		{"startup-crash", true},
		{"runtime-next-unused", false},
		{"startup-next-unused", false},
		{"stop", false},
		{"restart", false},
		{"cleanup", false},
		{"startup-error", false},
		{"startup-cancel", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(t.Context(), binary, "-test.run=^TestFaultLifecycleChild$", "-test.v")
			cmd.Env = append(os.Environ(), "SIDERA_FAULT_LIFECYCLE="+tc.name)
			out, err := cmd.CombinedOutput()
			if (err == nil) != tc.pass {
				t.Fatalf("child passed = %v, want %v:\n%s", err == nil, tc.pass, out)
			}
			if !strings.Contains(string(out), "lifecycle completed") {
				t.Fatalf("child did not finish its lifecycle checks:\n%s", out)
			}
			if !tc.pass && strings.Count(string(out), "never fired: the fault was not injected") != 1 {
				t.Fatalf("want exactly one actual unused-rule failure:\n%s", out)
			}
		})
	}
}

func TestFaultLifecycleChild(t *testing.T) {
	mode := os.Getenv("SIDERA_FAULT_LIFECYCLE")
	if mode == "" {
		return
	}
	unused := func(f *fsxtest.Faulty) *fsxtest.Rule {
		return f.Fail(fsxtest.Match{Tx: "direct-register", Step: fsx.StepWrite}, fsxtest.DiskFull)
	}
	next := strings.HasSuffix(mode, "next-unused")
	switch {
	case strings.HasPrefix(mode, "runtime-"):
		var old *fsxtest.Rule
		s := &harness.WalkScenario{Script: scenario.Script{
			Name: "armed-crash", Crash: scenario.CrashPlan{MaxPoints: 30},
			Steps: []scenario.Step{{Name: "armed", Do: func(e scenario.Env) error {
				old = unused(e.(*harness.H).Faults())
				return nil
			}}},
		}, Drain: func(h *harness.H) error {
			if old.Hits() != 0 {
				return errors.New("an interrupted future fault was counted as injected")
			}
			// This real write would fail if the old rule leaked into this generation.
			if _, err := h.Direct("recovered"); err != nil {
				return err
			}
			if next {
				unused(h.Faults())
			}
			return nil
		}, Check: func(h *harness.H) error { return oneFaultScopeCrash(h) }}
		result, err := harness.NewWalkBackend(t).Run(t.Context(), s, crashwalk.RunPlan{
			Targets: []crashwalk.Target{{Gen: 1, AfterStep: "armed"}},
		})
		if err != nil || len(result.Hits) != 2 || len(result.Reached) != 1 || !result.Reached[0] || len(result.Violations) != 0 {
			t.Fatalf("armed runtime cutoff = %+v, %v", result, err)
		}
	case strings.HasPrefix(mode, "startup-"):
		h := harness.New(t, harness.Options{})
		leaveStartupDebris(t, h)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		var old *fsxtest.Rule
		o := harness.StartOptions{Arm: func(f *fsxtest.Faulty) {
			old = unused(f)
			if mode == "startup-error" {
				f.Fail(fsxtest.Match{Tx: "txn-cleanup", Step: fsx.StepRemove}, fsxtest.DiskFull)
			}
		}, CrashAt: func(p fsx.Point, _ txn.Kind) (harness.CrashMode, bool) {
			if p.Tag.Tx != "txn-cleanup" || p.Step != fsx.StepRemove {
				return harness.CrashMode{}, false
			}
			if mode == "startup-cancel" {
				cancel()
				return harness.CrashMode{}, false
			}
			return harness.ProcessCrash.At(p.String()), mode != "startup-error"
		}}
		crashed, err := h.TryStart(ctx, o)
		wantCrash := mode == "startup-crash" || next
		if crashed != wantCrash || (err == nil) != wantCrash || old.Hits() != 0 {
			t.Fatalf("armed startup = (%v, %v), old hits %d", crashed, err, old.Hits())
		}
		if mode == "startup-cancel" && !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled startup = %v", err)
		}
		h.Start()
		h.MustDirect("recovered")
		if wantCrash {
			if err := oneFaultScopeCrash(h); err != nil {
				t.Fatal(err)
			}
		} else {
			log, err := h.Ledger()
			if err != nil || len(log.Kind(ledger.KindFixtureCrash)) != 0 {
				t.Fatalf("ordinary startup interruption produced a crash: %v, %v", log, err)
			}
		}
		if next {
			unused(h.Faults())
		}
	default:
		h := harness.New(t, harness.Options{})
		unused(h.Faults())
		switch mode {
		case "stop":
			h.Stop()
		case "restart":
			h.Restart()
		case "cleanup":
		default:
			t.Fatalf("unknown child mode %q", mode)
		}
	}
	t.Log("lifecycle completed")
}

func oneFaultScopeCrash(h *harness.H) error {
	log, err := h.Ledger()
	if err != nil {
		return err
	}
	if len(log.Kind(ledger.KindFixtureCrash)) != 1 {
		return errors.New("the interrupted fault scope needs one real fixture crash")
	}
	return h.Quiesce()
}
