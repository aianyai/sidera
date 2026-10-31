//go:build diagnostic

package acceptance

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
)

// Each external data boundary gets a canary. Failures name the boundary only;
// neither test output nor the diagnostic artifact may print a canary value.
func TestDiagnosticExportAllowlist(t *testing.T) {
	export := t.TempDir()
	t.Setenv("SIDERA_CI_DIAGNOSTIC_EXPORT", export)
	secret := "synthetic-private-canary-41"
	t.Setenv("SIDERA_TOKEN", secret)
	diagnosticInit()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "l"), 0o700); err != nil {
		t.Fatal(err)
	}
	e := ledger.Entry{V: 1, Writer: "fixture", Seq: 1, Kind: "fixture.step", Attrs: ledger.Attrs{Name: "safe-step", Detail: secret, CWD: secret, LockPath: secret}}
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw[:len(raw)-1], []byte(`,"token":"`+secret+`","unknown":{"body":"`+secret+`"}}`)...)
	if err := os.WriteFile(filepath.Join(root, "l", "fixture-0.jsonl"), append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	trace := `{"gen":1,"point":"clock-high-water/clock.rename#2","path":"` + secret + `","from":"` + secret + `","kind":"normal","crash":true,"token":"` + secret + `","detail":"` + secret + `","unknown":"` + secret + `"}` + "\n"
	if err := os.WriteFile(filepath.Join(root, "g1.jsonl"), []byte(trace), 0o600); err != nil {
		t.Fatal(err)
	}
	diagnosticCapture(root)
	unsafeErr := errors.New(secret)
	diagnosticCleanup(root, "test-root", nil, unsafeErr, &os.PathError{Op: "remove", Path: filepath.Join(root, "l", "adapter_fake-2.jsonl"), Err: unsafeErr})
	diagnosticRun(root, diagnosticCanaryScenario{}, "safe-seed", crashwalk.RunPlan{}, crashwalk.RunResult{Violations: []string{secret}}, unsafeErr, time.Now())
	diagnosticComparison(root, "safe-scenario", crashwalk.RunResult{Violations: []string{secret}}, unsafeErr, unsafeErr)
	if code := diagnosticFinish(time.Now(), 1); code != 1 {
		t.Fatal("original exit status changed")
	}
	for _, name := range []string{"manifest.json", "attempts.jsonl", "boundary.jsonl", "ledger-projection.jsonl", "trace-projection.jsonl"} {
		content, err := os.ReadFile(filepath.Join(export, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(content), secret) {
			t.Fatalf("private data crossed %s boundary", name)
		}
		if len(content) == 0 {
			t.Fatalf("boundary %s was not exercised", name)
		}
	}
	content, _ := os.ReadFile(filepath.Join(export, "ledger-projection.jsonl"))
	if !strings.Contains(string(content), `"writer":"fixture"`) {
		t.Fatal("ledger evidence was not projected")
	}
	content, _ = os.ReadFile(filepath.Join(export, "trace-projection.jsonl"))
	if !strings.Contains(string(content), `"point":"clock-high-water/clock.rename#2"`) {
		t.Fatal("trace evidence was not projected")
	}
}

func TestDiagnosticTruncatedMaterialIsUnavailable(t *testing.T) {
	export := t.TempDir()
	t.Setenv("SIDERA_CI_DIAGNOSTIC_EXPORT", export)
	diagnosticInit()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "g1.jsonl"), []byte(`{"gen":1,"point":"clock-high-water/clock.rename#2"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	diagnosticCapture(root)
	content, err := os.ReadFile(filepath.Join(export, "trace-projection.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), `"point"`) || !strings.Contains(string(content), `"unavailable"`) {
		t.Fatal("truncated trace was claimed as evidence")
	}
}

type diagnosticCanaryScenario struct{}

func (diagnosticCanaryScenario) Name() string                       { return "safe-scenario" }
func (diagnosticCanaryScenario) Steps() []string                    { return nil }
func (diagnosticCanaryScenario) MaxPoints() int                     { return 1 }
func (diagnosticCanaryScenario) Recovery() []crashwalk.RecoveryCase { return nil }
