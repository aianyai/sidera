package ledger_test

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/testkit/ledger"
)

var start = time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)

func fixedNow() time.Time { return start }

// mustOpen opens a writer that is closed when the test ends, before its temporary
// directory is removed: Windows removes no file that is still open.
func mustOpen(t *testing.T, f ledger.Factory, writer string, pid int, now func() time.Time) *ledger.Writer {
	t.Helper()
	w, err := f.Open(writer, pid, now)
	if err != nil {
		t.Fatalf("Open(%s): %v", writer, err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w
}

func mustAppend(t *testing.T, w *ledger.Writer, kind string, a ledger.Attrs) {
	t.Helper()
	if err := w.Append(kind, a); err != nil {
		t.Fatalf("Append(%s): %v", kind, err)
	}
}

// TestWriterStampsEveryEntry: the writer, not the caller, numbers entries from 1,
// reads the time from the clock it was given and records the step the fixture
// announced.
func TestWriterStampsEveryEntry(t *testing.T) {
	now := start
	mem := ledger.NewMemory()
	w := mustOpen(t, mem, "adapter/fake", 0, func() time.Time { return now })
	mustAppend(t, w, ledger.KindAdapterStarted, ledger.Attrs{Instance: "fake"})
	now = now.Add(2 * time.Minute)
	mem.SetStep("second step")
	mustAppend(t, w, ledger.KindKnobHit, ledger.Attrs{Knob: "k"})

	log, err := mem.Log()
	if err != nil {
		t.Fatal(err)
	}
	got := log.Entries()
	if len(got) != 2 {
		t.Fatalf("%d entries, want 2", len(got))
	}
	want := []struct {
		seq  int64
		at   int64
		step string
	}{{1, start.UnixMilli(), ""}, {2, start.Add(2 * time.Minute).UnixMilli(), "second step"}}
	for i, e := range got {
		if e.Seq != want[i].seq || e.AtMS != want[i].at || e.Step != want[i].step || e.V != 1 || e.Writer != "adapter/fake" {
			t.Errorf("entry %d = %+v, want seq %d at %d step %q", i, e, want[i].seq, want[i].at, want[i].step)
		}
	}
}

// TestWriterRefusesAnInvalidEntryWithoutConsumingASequenceNumber: a refused entry
// leaves no gap, so a reader's continuity check keeps meaning "nothing is lost".
func TestWriterRefusesAnInvalidEntryWithoutConsumingASequenceNumber(t *testing.T) {
	mem := ledger.NewMemory()
	w := mustOpen(t, mem, "fixture", 0, fixedNow)
	if err := w.Append("no.such.kind", ledger.Attrs{}); err == nil {
		t.Fatal("an unknown kind was written")
	}
	if err := w.Append(ledger.KindFixtureStep, ledger.Attrs{}); err == nil {
		t.Fatal("an entry without its required attribute was written")
	}
	mustAppend(t, w, ledger.KindFixtureStep, ledger.Attrs{Name: "a"})
	log, err := mem.Log()
	if err != nil {
		t.Fatal(err)
	}
	if e := log.Entries(); len(e) != 1 || e[0].Seq != 1 {
		t.Fatalf("entries %+v, want exactly one with seq 1", e)
	}
}

func TestOpenRefusesAnInvalidWriterName(t *testing.T) {
	for _, name := range []string{"", "daemon", "adapter/", "adapter/a b", "runtime/a/b", "agent/x_y"} {
		t.Run(name, func(t *testing.T) {
			if _, err := ledger.NewMemory().Open(name, 0, fixedNow); err == nil {
				t.Fatalf("Open(%q) succeeded", name)
			}
		})
	}
	if _, err := ledger.NewMemory().Open("fixture", 0, nil); err == nil {
		t.Fatal("Open without a clock succeeded")
	}
	if _, err := ledger.NewMemory().Open("fixture", -1, fixedNow); err == nil {
		t.Fatal("Open with a negative pid succeeded")
	}
}

// TestMemoryReopenContinuesTheSequence: the in-process stand-in for a restarted
// adapter writes to the same stream, as one process's writer would.
func TestMemoryReopenContinuesTheSequence(t *testing.T) {
	mem := ledger.NewMemory()
	w1 := mustOpen(t, mem, "adapter/fake", 0, fixedNow)
	mustAppend(t, w1, ledger.KindAdapterStarted, ledger.Attrs{Instance: "fake"})
	w2 := mustOpen(t, mem, "adapter/fake", 0, fixedNow)
	mustAppend(t, w2, ledger.KindAdapterStarted, ledger.Attrs{Instance: "fake"})
	log, err := mem.Log()
	if err != nil {
		t.Fatalf("a restarted writer broke the sequence: %v", err)
	}
	if e := log.Entries(); len(e) != 2 || e[0].Seq != 1 || e[1].Seq != 2 {
		t.Fatalf("entries %+v", e)
	}
}

// TestConcurrentWritersKeepEachSequence: many writers and many goroutines per
// writer; every writer's entries come out as 1..n (run with -race).
func TestConcurrentWritersKeepEachSequence(t *testing.T) {
	mem := ledger.NewMemory()
	const writers, goroutines, each = 4, 8, 50
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		lw := mustOpen(t, mem, fmt.Sprintf("agent/e%d", w), 0, fixedNow)
		for g := 0; g < goroutines; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < each; i++ {
					if err := lw.Append(ledger.KindClientCall, ledger.Attrs{Action: "send"}); err != nil {
						t.Error(err)
						return
					}
				}
			}()
		}
	}
	wg.Wait()
	log, err := mem.Log()
	if err != nil {
		t.Fatal(err)
	}
	for w := 0; w < writers; w++ {
		es := log.Writer(fmt.Sprintf("agent/e%d", w))
		if len(es) != goroutines*each {
			t.Fatalf("writer %d has %d entries, want %d", w, len(es), goroutines*each)
		}
		for i, e := range es {
			if e.Seq != int64(i+1) {
				t.Fatalf("writer %d entry %d has seq %d", w, i, e.Seq)
			}
		}
	}
}

// TestDirWritesEachEntryBeforeAppendReturns: a ledger entry is the proof that an
// effect happened, so it must be on stable storage before the writer goes on:
// one sync per entry, and the line is in the file when Append returns.
func TestDirWritesEachEntryBeforeAppendReturns(t *testing.T) {
	dir := t.TempDir()
	d := ledger.NewDir(dir)
	syncs := 0
	d.SetSync(func(f *os.File) error { syncs++; return f.Sync() })
	w := mustOpen(t, d, "adapter/fake", 4242, fixedNow)
	for i := 1; i <= 3; i++ {
		mustAppend(t, w, ledger.KindKnobHit, ledger.Attrs{Knob: "k"})
		if syncs != i {
			t.Fatalf("after %d appends there were %d syncs", i, syncs)
		}
		raw, err := os.ReadFile(filepath.Join(dir, "adapter_fake-4242.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if n := strings.Count(string(raw), "\n"); n != i {
			t.Fatalf("after %d appends the file has %d lines", i, n)
		}
	}
}

func TestDirSyncFailureFailsTheAppend(t *testing.T) {
	d := ledger.NewDir(t.TempDir())
	d.SetSync(func(*os.File) error { return os.ErrInvalid })
	w := mustOpen(t, d, "fixture", 1, fixedNow)
	if err := w.Append(ledger.KindFixtureStep, ledger.Attrs{Name: "a"}); err == nil {
		t.Fatal("an entry whose sync failed was reported as written")
	}
}

// TestDirRefusesASecondWriterOnTheSameFile: two processes with one pid cannot
// exist, so a second open of a file means two writers share a sequence.
func TestDirRefusesASecondWriterOnTheSameFile(t *testing.T) {
	d := ledger.NewDir(t.TempDir())
	mustOpen(t, d, "fixture", 7, fixedNow)
	if _, err := d.Open("fixture", 7, fixedNow); err == nil {
		t.Fatal("a second writer on the same file was accepted")
	}
	other, err := d.Open("fixture", 8, fixedNow)
	if err != nil {
		t.Fatalf("a writer with another pid was refused: %v", err)
	}
	_ = other.Close()
}

func TestReadDirMergesWritersAndRestartsOfOneWriter(t *testing.T) {
	dir := t.TempDir()
	d := ledger.NewDir(dir)
	for _, pid := range []int{100, 200} { // an adapter and its restart: two pids, one writer
		w := mustOpen(t, d, "adapter/fake", pid, fixedNow)
		mustAppend(t, w, ledger.KindAdapterStarted, ledger.Attrs{Instance: "fake"})
		mustAppend(t, w, ledger.KindKnobHit, ledger.Attrs{Knob: "k"})
	}
	f := mustOpen(t, d, "fixture", 0, fixedNow)
	mustAppend(t, f, ledger.KindFixtureStep, ledger.Attrs{Name: "s"})

	log, err := ledger.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(log.Entries()); n != 5 {
		t.Fatalf("%d entries, want 5", n)
	}
	if n := len(log.Writer("adapter/fake")); n != 4 {
		t.Fatalf("adapter has %d entries, want 4", n)
	}
	if n := len(log.Kind(ledger.KindAdapterStarted)); n != 2 {
		t.Fatalf("%d adapter.started entries, want 2", n)
	}
	if got := log.Steps(); !reflect.DeepEqual(got, []string{"s"}) {
		t.Fatalf("Steps() = %v", got)
	}
}

func writeStream(t *testing.T, dir, name string, lines ...string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Join(lines, "")), 0o600); err != nil {
		t.Fatal(err)
	}
}

func line(writer string, pid int, seq int, kind, attrs string) string {
	return fmt.Sprintf(`{"v":1,"writer":%q,"pid":%d,"seq":%d,"at_ms":1,"step":"","kind":%q,"attrs":%s}`+"\n", writer, pid, seq, kind, attrs)
}

// TestReadDirRejects: one row per way a ledger can be damaged that a test must
// notice instead of reading around.
func TestReadDirRejects(t *testing.T) {
	step := func(n int) string { return line("fixture", 0, n, "fixture.step", `{"name":"s"}`) }
	rows := []struct {
		rule  string
		files map[string][]string
		want  string
	}{
		{"a gap in the sequence", map[string][]string{"fixture-0.jsonl": {step(1), step(3)}}, "seq"},
		{"a repeated sequence number", map[string][]string{"fixture-0.jsonl": {step(1), step(1)}}, "seq"},
		{"a stream that does not start at 1", map[string][]string{"fixture-0.jsonl": {step(2)}}, "seq"},
		{"garbage between lines", map[string][]string{"fixture-0.jsonl": {step(1), "not json\n", step(2)}}, "fixture-0.jsonl:2"},
		{"a blank line", map[string][]string{"fixture-0.jsonl": {step(1), "\n", step(2)}}, "fixture-0.jsonl:2"},
		{"a line that is not canonical", map[string][]string{"fixture-0.jsonl": {strings.Replace(step(1), `"v":1,`, `"v": 1,`, 1)}}, "fixture-0.jsonl:1"},
		{"a pid that is not the file's", map[string][]string{"fixture-5.jsonl": {step(1)}}, "pid"},
		{"a writer that is not the file's", map[string][]string{"adapter_fake-0.jsonl": {step(1)}}, "writer"},
		{"a file name that is not a stream name", map[string][]string{"stray.jsonl": {step(1)}}, "stray.jsonl"},
		{"a last line without its newline when no process was killed", map[string][]string{"fixture-0.jsonl": {step(1), strings.TrimSuffix(step(2), "\n")}}, "newline"},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			dir := t.TempDir()
			for name, lines := range r.files {
				writeStream(t, dir, name, lines...)
			}
			_, err := ledger.ReadDir(dir)
			if err == nil {
				t.Fatal("ReadDir accepted the damaged ledger")
			}
			if !strings.Contains(err.Error(), r.want) {
				t.Fatalf("error %q does not say %q", err, r.want)
			}
		})
	}
}

// TestReadDirToleratesTheLastLineOfAKilledProcess: a process that was killed
// while it wrote leaves a line without its end; that is the one damage a reader
// forgives, and only for a process a fixture entry says it killed.
func TestReadDirToleratesTheLastLineOfAKilledProcess(t *testing.T) {
	adapter := func(n int) string { return line("adapter/fake", 4242, n, "knob.hit", `{"knob":"k"}`) }
	kill := line("fixture", 0, 1, "fixture.kill", `{"pids":[4242]}`)
	rows := []struct {
		name  string
		lines []string
		kill  bool
		want  int // entries of the adapter
		fail  bool
	}{
		{"a cut line of a killed process is dropped", []string{adapter(1), adapter(2)[:30]}, true, 1, false},
		{"a complete line that lost only its newline is kept", []string{adapter(1), strings.TrimSuffix(adapter(2), "\n")}, true, 2, false},
		{"a cut line of a process nobody killed is an error", []string{adapter(1), adapter(2)[:30]}, false, 0, true},
		{"a cut line in the middle is an error even for a killed process", []string{adapter(2)[:30] + "\n", adapter(1)}, true, 0, true},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			dir := t.TempDir()
			writeStream(t, dir, "adapter_fake-4242.jsonl", r.lines...)
			if r.kill {
				writeStream(t, dir, "fixture-0.jsonl", kill)
			}
			log, err := ledger.ReadDir(dir)
			if r.fail {
				if err == nil {
					t.Fatal("ReadDir accepted the damaged ledger")
				}
				return
			}
			if err != nil {
				t.Fatalf("ReadDir: %v", err)
			}
			if n := len(log.Writer("adapter/fake")); n != r.want {
				t.Fatalf("adapter has %d entries, want %d", n, r.want)
			}
		})
	}
}

func TestLogSelectors(t *testing.T) {
	mem := ledger.NewMemory()
	a := mustOpen(t, mem, "adapter/fake", 0, fixedNow)
	r := mustOpen(t, mem, "runtime/n1", 0, fixedNow)
	mustAppend(t, a, ledger.KindKnobHit, ledger.Attrs{Knob: "a"})
	mustAppend(t, r, ledger.KindKnobHit, ledger.Attrs{Knob: "b"})
	mustAppend(t, r, ledger.KindRuntimeClosed, ledger.Attrs{})
	log, err := mem.Log()
	if err != nil {
		t.Fatal(err)
	}
	if n := len(log.Kind(ledger.KindKnobHit)); n != 2 {
		t.Errorf("Kind(knob.hit) = %d, want 2", n)
	}
	if n := len(log.Writer("runtime/n1")); n != 2 {
		t.Errorf("Writer(runtime/n1) = %d, want 2", n)
	}
	if n := len(log.Kind("nothing")); n != 0 {
		t.Errorf("Kind(nothing) = %d, want 0", n)
	}
	if n := log.Count(func(e ledger.Entry) bool { return e.Attrs.Knob == "b" }); n != 1 {
		t.Errorf("Count = %d, want 1", n)
	}
}

// TestLogPIDsListsEveryProcessThatWrote: a fixture ends the processes a test left
// behind by the ids the ledger records, so it needs them without scanning entries.
func TestLogPIDsListsEveryProcessThatWrote(t *testing.T) {
	dir := t.TempDir()
	d := ledger.NewDir(dir)
	for _, w := range []struct {
		name string
		pid  int
	}{{"adapter/fake", 300}, {"adapter/fake", 100}, {"runtime/n1", 200}, {"fixture", 0}} {
		lw := mustOpen(t, d, w.name, w.pid, fixedNow)
		mustAppend(t, lw, ledger.KindKnobHit, ledger.Attrs{Knob: "k"})
	}
	log, err := ledger.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := log.PIDs(); !reflect.DeepEqual(got, []int64{100, 200, 300}) {
		t.Fatalf("PIDs() = %v, want the real processes, ascending, without the test process (0)", got)
	}
}

// TestDirLedgerOrdersEntriesByTimeThenScriptStep: on a test clock that stands
// still the time says nothing, so the step the fixture announced orders entries
// of different writers: an entry of an earlier step comes first.
func TestDirLedgerOrdersEntriesByTimeThenScriptStep(t *testing.T) {
	dir := t.TempDir()
	d := ledger.NewDir(dir)
	fx := mustOpen(t, d, "fixture", 0, fixedNow)
	a := mustOpen(t, d, "adapter/fake", 1, fixedNow)
	r := mustOpen(t, d, "runtime/n1", 2, fixedNow)
	step := func(name string) {
		mustAppend(t, fx, ledger.KindFixtureStep, ledger.Attrs{Name: name})
		d.SetStep(name)
	}
	step("first")
	mustAppend(t, r, ledger.KindKnobHit, ledger.Attrs{Knob: "from the runtime in step one"})
	step("second")
	mustAppend(t, a, ledger.KindKnobHit, ledger.Attrs{Knob: "from the adapter in step two"})
	step("third")
	mustAppend(t, a, ledger.KindKnobHit, ledger.Attrs{Knob: "from the adapter in step three"})
	mustAppend(t, r, ledger.KindKnobHit, ledger.Attrs{Knob: "from the runtime in step three"})

	log, err := ledger.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range log.Kind(ledger.KindKnobHit) {
		got = append(got, e.Attrs.Knob)
	}
	want := []string{
		"from the runtime in step one", "from the adapter in step two",
		"from the adapter in step three", "from the runtime in step three",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order\n got %v\nwant %v", got, want)
	}
}

// TestReadDirRejectsARepeatedStepName: an entry names its step and nothing else,
// so two announcements of one name would put the entries of the second among the
// entries of the first.
func TestReadDirRejectsARepeatedStepName(t *testing.T) {
	dir := t.TempDir()
	fx := mustOpen(t, ledger.NewDir(dir), "fixture", 0, fixedNow)
	mustAppend(t, fx, ledger.KindFixtureStep, ledger.Attrs{Name: "advance"})
	mustAppend(t, fx, ledger.KindFixtureStep, ledger.Attrs{Name: "recv"})
	mustAppend(t, fx, ledger.KindFixtureStep, ledger.Attrs{Name: "advance"})
	_, err := ledger.ReadDir(dir)
	if err == nil || !strings.Contains(err.Error(), `"advance" twice`) {
		t.Fatalf("ReadDir = %v, want a refusal that names the repeated step", err)
	}
}

func TestAppendSeqReturnsTheSequenceNumberOfTheEntry(t *testing.T) {
	w := mustOpen(t, ledger.NewMemory(), "runtime/n-0123", 0, fixedNow)
	for want := int64(1); want <= 3; want++ {
		seq, err := w.AppendSeq(ledger.KindRuntimeClosed, ledger.Attrs{})
		if err != nil || seq != want {
			t.Fatalf("AppendSeq = %d, %v; want %d", seq, err, want)
		}
	}
	if seq, err := w.AppendSeq("no.such.kind", ledger.Attrs{}); err == nil || seq != 0 {
		t.Fatalf("a refused entry returned (%d, %v)", seq, err)
	}
}

// TestCheckConfirmations: a creation is confirmed only by an entry of the
// runtime's that exists, and is that creation.
func TestCheckConfirmations(t *testing.T) {
	const attempt = "00000000-0000-4000-8000-000000000001"
	build := func(returned ledger.Attrs, runtime func(w *ledger.Writer)) *ledger.Log {
		mem := ledger.NewMemory()
		rt := mustOpen(t, mem, "runtime/n-0123", 0, fixedNow)
		runtime(rt)
		ad := mustOpen(t, mem, "adapter/fake", 0, fixedNow)
		mustAppend(t, ad, ledger.KindNativeCreateReturned, returned)
		log, err := mem.Log()
		if err != nil {
			t.Fatal(err)
		}
		return log
	}
	created := func(w *ledger.Writer) {
		mustAppend(t, w, ledger.KindRuntimePromptReceived, ledger.Attrs{Attempt: "p", TextDigest: "ab"})
		mustAppend(t, w, ledger.KindRuntimeSessionCreated, ledger.Attrs{Attempt: attempt, NativeRef: "n-0123"})
	}
	returned := func(seq int64) ledger.Attrs {
		return ledger.Attrs{Attempt: attempt, NativeRef: "n-0123", RuntimeWriter: "runtime/n-0123", RuntimeSeq: seq, Outcome: "created"}
	}
	rows := []struct {
		rule     string
		log      *ledger.Log
		wantFail string
	}{
		{"a confirmation that names the runtime's creation entry", build(returned(2), created), ""},
		{"a confirmation that names an entry the runtime never wrote", build(returned(3), created), "has no entry 3"},
		{"a confirmation that names an entry of another kind", build(returned(1), created), "runtime.prompt.received"},
		{"a confirmation that names the creation of another attempt", build(ledger.Attrs{Attempt: "00000000-0000-4000-8000-000000000002", NativeRef: "n-0123", RuntimeWriter: "runtime/n-0123", RuntimeSeq: 2, Outcome: "created"}, created), "another attempt"},
		{"a creation that made no native session needs no runtime entry", build(ledger.Attrs{Attempt: attempt, Outcome: "failed"}, func(*ledger.Writer) {}), ""},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			err := r.log.CheckConfirmations()
			switch {
			case r.wantFail == "" && err != nil:
				t.Fatalf("CheckConfirmations: %v", err)
			case r.wantFail != "" && (err == nil || !strings.Contains(err.Error(), r.wantFail)):
				t.Fatalf("CheckConfirmations = %v, want a failure that says %q", err, r.wantFail)
			}
		})
	}
}
