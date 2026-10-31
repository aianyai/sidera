package acceptance

// This file belongs only to the disposable native-runner diagnostic branch.
// Export schemas are deliberately separate from fixture, ledger and trace types.
import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
)

var diagnosticMu sync.Mutex
var diagnosticFailed bool

func diagnosticDir() string { return os.Getenv("SIDERA_CI_DIAGNOSTIC_EXPORT") }

// Errors are classified, never serialized: process errors can contain argv or
// response bodies, and PathError can contain absolute working directories.
func diagnosticClass(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, os.ErrNotExist):
		return "unavailable"
	case errors.Is(err, os.ErrPermission):
		return "permission"
	default:
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return "process_exit"
		}
		return "error"
	}
}

func diagnosticWrite(name string, value any) {
	if diagnosticDir() == "" {
		return
	}
	diagnosticMu.Lock()
	defer diagnosticMu.Unlock()
	f, err := os.OpenFile(filepath.Join(diagnosticDir(), name), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err == nil {
		err = json.NewEncoder(f).Encode(value)
		err = errors.Join(err, f.Close())
	}
	if err != nil {
		diagnosticFailed = true
		fmt.Fprintln(os.Stderr, "diagnostic export failed")
	}
}

type diagnosticManifest struct {
	Candidate    string    `json:"candidate"`
	Diagnostic   string    `json:"diagnostic"`
	Run          string    `json:"run"`
	Attempt      string    `json:"attempt"`
	Job          string    `json:"job"`
	OS           string    `json:"os"`
	Arch         string    `json:"arch"`
	Image        string    `json:"image"`
	ImageVersion string    `json:"image_version"`
	Go           string    `json:"go"`
	Command      string    `json:"command"`
	Start        time.Time `json:"start"`
	End          time.Time `json:"end"`
	ExitCode     int       `json:"exit_code"`
}

func diagnosticInit() {
	if diagnosticDir() == "" {
		return
	}
	if err := os.MkdirAll(diagnosticDir(), 0o700); err != nil {
		panic("diagnostic export directory unavailable")
	}
	for _, name := range []string{"manifest.json", "attempts.jsonl", "boundary.jsonl", "ledger-projection.jsonl", "trace-projection.jsonl"} {
		f, err := os.OpenFile(filepath.Join(diagnosticDir(), name), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			panic("diagnostic export file unavailable")
		}
		if err := f.Close(); err != nil {
			panic("diagnostic export file close failed")
		}
	}
}

func diagnosticMetadata(key string) string {
	value := os.Getenv(key)
	if value == "" {
		return "unavailable"
	}
	return value
}

func diagnosticFinish(start time.Time, code int) int {
	if diagnosticDir() == "" {
		return code
	}
	// manifest.json is one JSON object containing all serial test-process records.
	record := diagnosticManifest{
		Candidate: "b486d9f40fe067c389fed2f6bc0f675cf306901d", Diagnostic: diagnosticMetadata("GITHUB_SHA"),
		Run: diagnosticMetadata("GITHUB_RUN_ID"), Attempt: diagnosticMetadata("GITHUB_RUN_ATTEMPT"), Job: diagnosticMetadata("GITHUB_JOB"),
		OS: runtime.GOOS, Arch: runtime.GOARCH, Image: diagnosticMetadata("ImageOS"), ImageVersion: diagnosticMetadata("ImageVersion"),
		Go: runtime.Version(), Command: diagnosticMetadata("SIDERA_CI_DIAGNOSTIC_COMMAND"), Start: start, End: time.Now().UTC(), ExitCode: code,
	}
	diagnosticMu.Lock()
	var manifest struct {
		Processes []diagnosticManifest `json:"processes"`
	}
	path := filepath.Join(diagnosticDir(), "manifest.json")
	raw, err := os.ReadFile(path)
	if err == nil && len(raw) > 0 {
		err = json.Unmarshal(raw, &manifest)
	}
	if err == nil {
		manifest.Processes = append(manifest.Processes, record)
		raw, err = json.MarshalIndent(manifest, "", "  ")
		if err == nil {
			err = os.WriteFile(path, append(raw, '\n'), 0o600)
		}
	}
	if err != nil {
		diagnosticFailed = true
		fmt.Fprintln(os.Stderr, "diagnostic manifest failed")
	}
	failed := diagnosticFailed
	diagnosticMu.Unlock()
	if failed {
		return 1
	}
	return code
}

type diagnosticTarget struct {
	Generation int    `json:"generation"`
	Point      string `json:"point,omitempty"`
	AfterStep  string `json:"after_step,omitempty"`
}
type diagnosticAttempt struct {
	Parent         string             `json:"parent"`
	Fixture        string             `json:"fixture"`
	Process        int                `json:"process"`
	Scenario       string             `json:"scenario"`
	Seed           string             `json:"seed"`
	Budget         int                `json:"budget"`
	Record         bool               `json:"record"`
	Targets        []diagnosticTarget `json:"targets"`
	Trigger        string             `json:"trigger"`
	Reached        []bool             `json:"reached"`
	Generations    int                `json:"generations"`
	Violations     int                `json:"violations"`
	Classification string             `json:"classification"`
	Comparison     string             `json:"comparison"`
	Start          time.Time          `json:"start"`
	End            time.Time          `json:"end"`
}

func diagnosticRun(root string, s crashwalk.Scenario, seed string, plan crashwalk.RunPlan, result crashwalk.RunResult, err error, start time.Time) {
	if diagnosticDir() == "" {
		return
	}
	if seed == "" {
		seed = "acceptance"
	}
	a := diagnosticAttempt{Parent: filepath.Base(filepath.Dir(root)), Fixture: filepath.Base(root), Process: os.Getpid(), Scenario: s.Name(), Seed: seed, Budget: s.MaxPoints(), Record: plan.Record, Trigger: "none", Reached: result.Reached, Generations: len(result.Hits), Violations: len(result.Violations), Classification: diagnosticClass(err), Comparison: "not_assessed", Start: start, End: time.Now().UTC()}
	for _, t := range plan.Targets {
		a.Targets = append(a.Targets, diagnosticTarget{t.Gen, t.Point.String(), t.AfterStep})
	}
	if plan.Trigger != nil {
		switch plan.Trigger.Kind {
		case crashwalk.TriggerCrashAt:
			a.Trigger = "crash_at:" + plan.Trigger.CrashAt.String()
		case crashwalk.TriggerCorrupt:
			a.Trigger = "corrupt:" + plan.Trigger.Pattern + ":" + plan.Trigger.Damage
		default:
			a.Trigger = "unknown"
		}
	}
	if err == nil && len(result.Violations) > 0 {
		a.Classification = "oracle_violation"
	}
	diagnosticWrite("attempts.jsonl", a)
}
func diagnosticComparison(root, scenario string, result crashwalk.RunResult, runErr, comparison error) {
	diagnosticWrite("attempts.jsonl", struct {
		Parent         string `json:"parent"`
		Process        int    `json:"process"`
		Scenario       string `json:"scenario"`
		Reached        []bool `json:"reached"`
		Generations    int    `json:"generations"`
		Violations     int    `json:"violations"`
		Classification string `json:"classification"`
		Comparison     string `json:"comparison"`
	}{filepath.Base(root), os.Getpid(), scenario, result.Reached, len(result.Hits), len(result.Violations), diagnosticClass(runErr), diagnosticClass(comparison)})
}

type diagnosticGeneration struct {
	Generation    int    `json:"generation"`
	PID           int    `json:"pid"`
	WaitCompleted bool   `json:"wait_completed"`
	Exit          string `json:"exit"`
}
type diagnosticBoundary struct {
	Fixture      string                 `json:"fixture"`
	Process      int                    `json:"process"`
	Owner        string                 `json:"owner"`
	Close        string                 `json:"close"`
	Remove       string                 `json:"remove"`
	RelativePath string                 `json:"relative_path,omitempty"`
	FileType     string                 `json:"file_type"`
	HandleOwner  string                 `json:"handle_owner"`
	Generations  []diagnosticGeneration `json:"generations,omitempty"`
}

func diagnosticCleanup(root, owner string, f *Fixture, closeErr, removeErr error) {
	if diagnosticDir() == "" {
		return
	}
	b := diagnosticBoundary{Fixture: filepath.Base(root), Process: os.Getpid(), Owner: owner, Close: diagnosticClass(closeErr), Remove: diagnosticClass(removeErr), FileType: "unknown", HandleOwner: "unknown"}
	if f == nil {
		b.Close = "unknown"
	}
	var pe *os.PathError
	if errors.As(removeErr, &pe) {
		rel, err := filepath.Rel(root, pe.Path)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			b.RelativePath = filepath.ToSlash(rel)
			switch {
			case strings.HasPrefix(filepath.Base(rel), "adapter_"):
				b.FileType = "adapter"
			case strings.HasPrefix(filepath.Base(rel), "runtime_"):
				b.FileType = "runtime"
			}
		}
	}
	if f != nil {
		for n := 1; n <= len(f.gens); n++ {
			g := f.gens[n]
			if g == nil {
				continue
			}
			state := diagnosticGeneration{Generation: n, PID: g.p.cmd.Process.Pid, WaitCompleted: g.p.exited(), Exit: "unknown"}
			if state.WaitCompleted {
				state.Exit = diagnosticClass(g.p.err)
			}
			b.Generations = append(b.Generations, state)
		}
	}
	diagnosticWrite("boundary.jsonl", b)
}

// A second type at the file boundary excludes credentials and arbitrary future
// ledger attributes even if the source decoder accepts additional members.
type diagnosticLedger struct {
	Fixture       string `json:"fixture"`
	Writer        string `json:"writer"`
	PID           int64  `json:"pid"`
	Seq           int64  `json:"seq"`
	AtMS          int64  `json:"at_ms"`
	Step          string `json:"step"`
	Kind          string `json:"kind"`
	Instance      string `json:"instance,omitempty"`
	Attempt       string `json:"attempt,omitempty"`
	Endpoint      string `json:"endpoint,omitempty"`
	NativeRef     string `json:"native_ref,omitempty"`
	RuntimeWriter string `json:"runtime_writer,omitempty"`
	RuntimeSeq    int64  `json:"runtime_seq,omitempty"`
	Outcome       string `json:"outcome,omitempty"`
	MessageID     string `json:"message_id,omitempty"`
	Feedback      string `json:"feedback,omitempty"`
	Action        string `json:"action,omitempty"`
	BodyDigest    string `json:"body_digest,omitempty"`
	TextDigest    string `json:"text_digest,omitempty"`
}

func diagnosticProjectLedger(fixture string, e ledger.Entry) diagnosticLedger {
	a := e.Attrs
	return diagnosticLedger{fixture, e.Writer, e.PID, e.Seq, e.AtMS, e.Step, e.Kind, a.Instance, a.Attempt, a.Endpoint, a.NativeRef, a.RuntimeWriter, a.RuntimeSeq, a.Outcome, a.MessageID, a.Feedback, a.Action, a.BodyDigest, a.TextDigest}
}

type diagnosticTrace struct {
	Fixture    string `json:"fixture"`
	Generation int    `json:"generation"`
	Point      string `json:"point"`
	Marker     bool   `json:"marker"`
}

func diagnosticProjectTrace(fixture string, raw []byte) (diagnosticTrace, error) {
	var line traceLine
	if err := json.Unmarshal(raw, &line); err != nil {
		return diagnosticTrace{}, err
	}
	if _, err := fsx.ParsePoint(line.Point); err != nil && line.Point != "proc/cli.recv-after-response" {
		return diagnosticTrace{}, err
	}
	return diagnosticTrace{fixture, line.Gen, line.Point, line.Crash}, nil
}

type diagnosticAvailability struct {
	Fixture  string `json:"fixture"`
	Material string `json:"material"`
	Status   string `json:"status"`
}

var diagnosticTraceName = regexp.MustCompile(`^g[1-9][0-9]*\.jsonl$`)

func diagnosticCapture(root string) {
	if diagnosticDir() == "" {
		return
	}
	// Only existing ledger and generation traces are opened. No fixture data,
	// environment, process output, runtime directory or binary is read.
	roots := []string{root}
	children, _ := os.ReadDir(root)
	for _, child := range children {
		if child.IsDir() && strings.HasPrefix(child.Name(), "w") {
			roots = append(roots, filepath.Join(root, child.Name()))
		}
	}
	for _, dir := range roots {
		id := filepath.Base(dir)
		files, err := os.ReadDir(filepath.Join(dir, "l"))
		if err != nil || len(files) == 0 {
			diagnosticWrite("ledger-projection.jsonl", diagnosticAvailability{id, "ledger", "unavailable"})
		}
		for _, file := range files {
			if file.Type()&fs.ModeSymlink != 0 || file.IsDir() || !strings.HasSuffix(file.Name(), ".jsonl") {
				continue
			}
			diagnosticLines(filepath.Join(dir, "l", file.Name()), "ledger-projection.jsonl", id, false)
		}
		traces, err := os.ReadDir(dir)
		count := 0
		if err == nil {
			for _, file := range traces {
				if file.Type()&fs.ModeSymlink == 0 && !file.IsDir() && diagnosticTraceName.MatchString(file.Name()) {
					count++
					diagnosticLines(filepath.Join(dir, file.Name()), "trace-projection.jsonl", id, true)
				}
			}
		}
		if err != nil || count == 0 {
			diagnosticWrite("trace-projection.jsonl", diagnosticAvailability{id, "trace", "unavailable"})
		}
	}
}
func diagnosticLines(path, dest, fixture string, trace bool) {
	f, err := os.Open(path)
	if err != nil {
		diagnosticWrite(dest, diagnosticAvailability{fixture, "file", diagnosticClass(err)})
		return
	}
	defer f.Close()
	reader := bufio.NewReader(f)
	lines := 0
	for {
		raw, readErr := reader.ReadBytes('\n')
		if readErr != nil {
			if lines == 0 || len(raw) != 0 || readErr != io.EOF {
				diagnosticWrite(dest, diagnosticAvailability{fixture, "tail", "unavailable"})
			}
			return
		}
		lines++
		if trace {
			projection, err := diagnosticProjectTrace(fixture, raw)
			if err != nil {
				diagnosticWrite(dest, diagnosticAvailability{fixture, "trace_line", "unavailable"})
				continue
			}
			diagnosticWrite(dest, projection)
		} else {
			var e ledger.Entry
			if err := json.Unmarshal(raw, &e); err != nil {
				diagnosticWrite(dest, diagnosticAvailability{fixture, "ledger_line", "unavailable"})
				continue
			}
			diagnosticWrite(dest, diagnosticProjectLedger(fixture, e))
		}
	}
}
