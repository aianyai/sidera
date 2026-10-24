package fsxtest_test

import (
	"errors"
	"fmt"
	"strings"
	"syscall"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
)

// recorder stands in for *testing.T so that what Faulty reports at the end of
// a test can be inspected instead of failing this one.
type recorder struct {
	testing.TB
	cleanups []func()
	errors   []string
}

func (r *recorder) Cleanup(f func()) { r.cleanups = append(r.cleanups, f) }
func (r *recorder) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}
func (r *recorder) Helper() {}
func (r *recorder) Fatalf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
	panic(r)
}
func (r *recorder) finish() {
	for i := len(r.cleanups) - 1; i >= 0; i-- {
		r.cleanups[i]()
	}
}

func TestFaultyPassesEverythingThroughWithoutRules(t *testing.T) {
	mem := fsxtest.NewMem()
	faulty := fsxtest.NewFaulty(t, mem)
	durable(t, faulty, "f", "x")
	if got, err := faulty.ReadFile("f"); err != nil || string(got) != "x" {
		t.Errorf("ReadFile = %q, %v", got, err)
	}
	if got, _ := mem.ReadFile("f"); string(got) != "x" {
		t.Errorf("the write did not reach the wrapped FS: %q", got)
	}
	unlock, err := faulty.Lock("daemon.lock")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Lock("daemon.lock"); !errors.Is(err, fsx.ErrLocked) {
		t.Errorf("a lock taken through Faulty is not held on the wrapped FS: %v", err)
	}
	unlock()
}

func TestFaultyMatchSelectors(t *testing.T) {
	a := fsx.Tag{Tx: "intake", Role: "body"}
	b := fsx.Tag{Tx: "intake", Role: "out"}
	c := fsx.Tag{Tx: "archive", Role: "events"}
	type write struct {
		tag  fsx.Tag
		path string
	}
	// Write i creates the temporary file .tmp-<i+1 as 16 hex digits> in Mem.
	writes := []write{{a, "body"}, {b, "out"}, {a, "body2"}, {c, "events"}}
	for _, tc := range []struct {
		name  string
		match fsxtest.Match
		want  []int // indices of the writes whose rename fails
	}{
		{"by-transaction", fsxtest.Match{Tx: "archive", Step: fsx.StepRename}, []int{3}},
		{"by-role", fsxtest.Match{Role: "out", Step: fsx.StepRename}, []int{1}},
		{"by-transaction-and-role", fsxtest.Match{Tx: "intake", Role: "body", Step: fsx.StepRename}, []int{0, 2}},
		{"by-ordinal-of-the-key", fsxtest.Match{Tx: "intake", Role: "body", Step: fsx.StepRename, Hit: 2}, []int{2}},
		{"by-path-of-the-target", fsxtest.Match{Step: fsx.StepRename, Path: "out"}, []int{1}},
		{"by-path-of-the-source-temporary", fsxtest.Match{Step: fsx.StepRename, Path: ".tmp-0000000000000003"}, []int{2}},
		{"any-transaction-any-role", fsxtest.Match{Step: fsx.StepRename}, []int{0, 1, 2, 3}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			faulty := fsxtest.NewFaulty(t, fsxtest.NewMem())
			rule := faulty.Fail(tc.match, syscall.EIO)
			var failed []int
			for i, w := range writes {
				if err := fsx.Replace(faulty, w.tag, w.path, []byte("x"), fsx.ModePrivate); err != nil {
					if !errors.Is(err, syscall.EIO) {
						t.Fatalf("write %d failed with %v, want EIO", i, err)
					}
					failed = append(failed, i)
				}
			}
			if fmt.Sprint(failed) != fmt.Sprint(tc.want) {
				t.Errorf("failed writes %v, want %v", failed, tc.want)
			}
			if rule.Hits() != len(tc.want) {
				t.Errorf("rule hits = %d, want %d", rule.Hits(), len(tc.want))
			}
		})
	}
}

func TestFaultyFirstMatchingRuleFires(t *testing.T) {
	faulty := fsxtest.NewFaulty(t, fsxtest.NewMem())
	tag := fsx.Tag{Tx: "t", Role: "r"}
	first := faulty.Fail(fsxtest.Match{Tx: "t", Step: fsx.StepMkdir, Hit: 1}, syscall.EIO)
	second := faulty.Fail(fsxtest.Match{Tx: "t", Step: fsx.StepMkdir}, syscall.ENOSPC)

	if err := faulty.Mkdir(tag, "d"); !errors.Is(err, syscall.EIO) {
		t.Errorf("first call = %v, want EIO from the first rule", err)
	}
	if first.Hits() != 1 || second.Hits() != 0 {
		t.Errorf("after the first call hits = %d and %d, want 1 and 0", first.Hits(), second.Hits())
	}
	if err := faulty.Mkdir(tag, "d"); !errors.Is(err, syscall.ENOSPC) {
		t.Errorf("second call = %v, want ENOSPC from the second rule", err)
	}
	if first.Hits() != 1 || second.Hits() != 1 {
		t.Errorf("after the second call hits = %d and %d, want 1 and 1", first.Hits(), second.Hits())
	}
}

func TestFaultyFailedCallsDoNotRunAndFileOperationsAreAttributedToTheOpeningTag(t *testing.T) {
	mem := fsxtest.NewMem()
	faulty := fsxtest.NewFaulty(t, mem)
	tag := fsx.Tag{Tx: "archive", Role: "events"}
	faulty.Fail(fsxtest.Match{Tx: "archive", Role: "events", Step: fsx.StepOpen, Path: "refused"}, syscall.EIO)
	faulty.Fail(fsxtest.Match{Tx: "archive", Role: "events", Step: fsx.StepSync, Hit: 1}, syscall.EIO)

	if _, err := faulty.OpenAppend(tag, "refused"); !errors.Is(err, syscall.EIO) {
		t.Fatalf("OpenAppend = %v, want EIO", err)
	}
	if _, err := mem.Stat("refused"); err == nil {
		t.Error("a failed open created the file")
	}
	f, err := faulty.OpenAppend(tag, "log")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Sync(); !errors.Is(err, syscall.EIO) {
		t.Errorf("Sync = %v, want EIO (attributed to the tag the file was opened with)", err)
	}
	if err := f.Sync(); err != nil {
		t.Errorf("second Sync = %v, want it to pass: the rule was for the first", err)
	}
}

func TestFaultyShortWriteWritesPartThenFails(t *testing.T) {
	mem := fsxtest.NewMem()
	faulty := fsxtest.NewFaulty(t, mem)
	tag := fsx.Tag{Tx: "t", Role: "r"}
	faulty.FailShortWrite(fsxtest.Match{Tx: "t", Step: fsx.StepWrite, Hit: 1}, 4, syscall.ENOSPC)
	faulty.FailShortWrite(fsxtest.Match{Tx: "t", Step: fsx.StepWrite, Hit: 2}, 100, syscall.EIO)
	f, err := faulty.OpenAppend(tag, "log")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	n, err := f.Write([]byte("0123456789"))
	if n != 4 || !errors.Is(err, syscall.ENOSPC) {
		t.Errorf("first Write = %d, %v; want 4 and ENOSPC", n, err)
	}
	n, err = f.Write([]byte("abc"))
	if n != 3 || !errors.Is(err, syscall.EIO) {
		t.Errorf("second Write = %d, %v; want all 3 bytes written and EIO", n, err)
	}
	if got, _ := mem.ReadFile("log"); string(got) != "0123abc" {
		t.Errorf("file = %q, want 0123abc", got)
	}
}

func TestFaultyShortWriteNeedsTheWriteStep(t *testing.T) {
	rec := &recorder{TB: t}
	faulty := fsxtest.NewFaulty(rec, fsxtest.NewMem())
	func() {
		defer func() {
			if recover() != rec {
				t.Error("FailShortWrite with a step other than StepWrite did not stop the test")
			}
		}()
		faulty.FailShortWrite(fsxtest.Match{Step: fsx.StepSync}, 1, syscall.EIO)
	}()
	if len(rec.errors) != 1 || !strings.Contains(rec.errors[0], "StepWrite") {
		t.Errorf("reported %v, want a message naming StepWrite", rec.errors)
	}
}

// A fault that was never injected proves nothing about how the code handles
// it, so the test that registered it fails.
func TestFaultyReportsEveryRuleThatNeverFired(t *testing.T) {
	rec := &recorder{TB: t}
	faulty := fsxtest.NewFaulty(rec, fsxtest.NewMem())
	fired := faulty.Fail(fsxtest.Match{Tx: "t", Step: fsx.StepMkdir}, syscall.EIO)
	faulty.Fail(fsxtest.Match{Tx: "t", Step: fsx.StepRemoveAll}, syscall.EIO)
	faulty.FailShortWrite(fsxtest.Match{Tx: "t", Step: fsx.StepWrite}, 1, syscall.EIO)
	faulty.Mkdir(fsx.Tag{Tx: "t", Role: "r"}, "d")
	if fired.Hits() != 1 {
		t.Fatal("setup: the first rule did not fire")
	}

	rec.finish()

	if len(rec.errors) != 2 {
		t.Fatalf("reported %d problems %v, want exactly the two rules that never fired", len(rec.errors), rec.errors)
	}
	for _, msg := range rec.errors {
		if !strings.Contains(msg, "never fired") {
			t.Errorf("message %q does not say the rule never fired", msg)
		}
	}
	if !strings.Contains(rec.errors[0], "remove-all") || !strings.Contains(rec.errors[1], "write") {
		t.Errorf("messages %v do not name the rules", rec.errors)
	}
}

func TestFaultyWithAllRulesFiredReportsNothing(t *testing.T) {
	rec := &recorder{TB: t}
	faulty := fsxtest.NewFaulty(rec, fsxtest.NewMem())
	faulty.Fail(fsxtest.Match{Step: fsx.StepMkdir}, syscall.EIO)
	faulty.Mkdir(fsx.Tag{Tx: "t", Role: "r"}, "d")
	rec.finish()
	if len(rec.errors) != 0 {
		t.Errorf("reported %v, want nothing", rec.errors)
	}
}

// Reader calls have no tag or step; a rule names a path to select them.
func TestFaultyReaderCallsAreSelectedByPathAlone(t *testing.T) {
	mem := fsxtest.NewMem()
	durable(t, mem, "f", "x")
	durable(t, mem, "g", "y")
	faulty := fsxtest.NewFaulty(t, mem)
	readFile := faulty.Fail(fsxtest.Match{Path: "f"}, syscall.EIO)

	if _, err := faulty.ReadFile("f"); !errors.Is(err, syscall.EIO) {
		t.Errorf("ReadFile(f) = %v, want EIO", err)
	}
	if _, err := faulty.Stat("f"); !errors.Is(err, syscall.EIO) {
		t.Errorf("Stat(f) = %v, want EIO", err)
	}
	if _, err := faulty.ReadDir("f"); !errors.Is(err, syscall.EIO) {
		t.Errorf("ReadDir(f) = %v, want EIO", err)
	}
	if _, err := faulty.Available("f"); !errors.Is(err, syscall.EIO) {
		t.Errorf("Available(f) = %v, want EIO", err)
	}
	if got, err := faulty.ReadFile("g"); err != nil || string(got) != "y" {
		t.Errorf("ReadFile(g) = %q, %v; want it untouched", got, err)
	}
	if readFile.Hits() != 4 {
		t.Errorf("rule hits = %d, want 4", readFile.Hits())
	}

	// A rule that also names a step does not select reads.
	stepRule := faulty.Fail(fsxtest.Match{Step: fsx.StepSync, Path: "g"}, syscall.EIO)
	if _, err := faulty.ReadFile("g"); err != nil {
		t.Errorf("ReadFile(g) = %v: a rule with a step selected a read", err)
	}
	f, err := faulty.OpenAppend(fsx.Tag{Tx: "t", Role: "r"}, "g")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Sync(); !errors.Is(err, syscall.EIO) || stepRule.Hits() != 1 {
		t.Errorf("Sync = %v with %d hits, want EIO and 1", err, stepRule.Hits())
	}
}

// Failure-path cleanup carries a reserved tag: a rule for "any remove" must not
// fire on it, and only a rule naming that transaction does.
func TestFaultyReservedTagsAreSelectedOnlyByName(t *testing.T) {
	rec := &recorder{TB: t}
	faulty := fsxtest.NewFaulty(rec, fsxtest.NewMem())
	tag := fsx.Tag{Tx: "t", Role: "r"}
	failSync := faulty.Fail(fsxtest.Match{Tx: "t", Step: fsx.StepSync}, syscall.EIO)
	anyRemove := faulty.Fail(fsxtest.Match{Step: fsx.StepRemove}, syscall.EIO)
	cleanup := faulty.Fail(fsxtest.Match{Tx: fsx.TxCleanup, Step: fsx.StepRemove}, syscall.EIO)

	err := fsx.Replace(faulty, tag, "rec", []byte("x"), fsx.ModePrivate)

	if !errors.Is(err, syscall.EIO) || failSync.Hits() != 1 {
		t.Fatalf("setup: Replace = %v, sync rule hits %d", err, failSync.Hits())
	}
	if anyRemove.Hits() != 0 {
		t.Error("a rule for any remove fired on cleanup")
	}
	if cleanup.Hits() != 1 {
		t.Errorf("the rule naming the cleanup transaction fired %d times, want 1", cleanup.Hits())
	}
}

// Steps of a reserved transaction are not numbered, as fsx.Observe does not
// number them: a rule that names an ordinal never selects one, and a rule
// without an ordinal still does.
func TestFaultyDoesNotNumberStepsOfReservedTransactions(t *testing.T) {
	rec := &recorder{TB: t}
	faulty := fsxtest.NewFaulty(rec, fsxtest.NewMem())
	first := faulty.Fail(fsxtest.Match{Tx: fsx.TxCleanup, Step: fsx.StepRemove, Hit: 1}, syscall.EIO)
	unordered := faulty.Fail(fsxtest.Match{Tx: fsx.TxCleanup, Step: fsx.StepRemove}, syscall.EIO)

	if err := faulty.Remove(fsx.Tag{Tx: fsx.TxCleanup}, "x"); !errors.Is(err, syscall.EIO) {
		t.Fatalf("Remove under the cleanup tag = %v, want the injected error", err)
	}
	if first.Hits() != 0 || unordered.Hits() != 1 {
		t.Errorf("rule with an ordinal fired %d times, rule without one %d; want 0 and 1", first.Hits(), unordered.Hits())
	}
	rec.finish()
	if len(rec.errors) != 1 || !strings.Contains(rec.errors[0], "hit=1") {
		t.Errorf("end-of-test report = %q, want only the rule with the ordinal reported as never fired", rec.errors)
	}
}
