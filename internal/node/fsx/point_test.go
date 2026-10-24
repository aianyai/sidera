package fsx_test

import (
	"math"
	"strings"
	"sync"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
)

// Golden crash point identifiers, written out by hand: the text is what a
// crash target is named by, so it must not drift.
var goldenPoints = []struct {
	text  string
	point fsx.Point
}{
	{"intake/out.rename#2", fsx.Point{Key: fsx.Key{Tag: fsx.Tag{Tx: "intake", Role: "out"}, Step: fsx.StepRename}, Hit: 2}},
	{"intake/commit#1", fsx.Point{Key: fsx.Key{Tag: fsx.Tag{Tx: "intake"}, Step: fsx.StepCommit}, Hit: 1}},
	{"archive/segment.sync-dir#17", fsx.Point{Key: fsx.Key{Tag: fsx.Tag{Tx: "archive", Role: "segment"}, Step: fsx.StepSyncDir}, Hit: 17}},
	{"a/b.create#1", fsx.Point{Key: fsx.Key{Tag: fsx.Tag{Tx: "a", Role: "b"}, Step: fsx.StepCreate}, Hit: 1}},
	{"adapter-upgrade/pkg-1.rename-dir#3", fsx.Point{Key: fsx.Key{Tag: fsx.Tag{Tx: "adapter-upgrade", Role: "pkg-1"}, Step: fsx.StepRenameDir}, Hit: 3}},
	{"t/r.remove-all#1", fsx.Point{Key: fsx.Key{Tag: fsx.Tag{Tx: "t", Role: "r"}, Step: fsx.StepRemoveAll}, Hit: 1}},
	{"t/commit.sync#9", fsx.Point{Key: fsx.Key{Tag: fsx.Tag{Tx: "t", Role: "commit"}, Step: fsx.StepSync}, Hit: 9}},
	{"t/r.open#" + "2147483647", fsx.Point{Key: fsx.Key{Tag: fsx.Tag{Tx: "t", Role: "r"}, Step: fsx.StepOpen}, Hit: math.MaxInt32}},
}

func TestPointStringAndParseAreInverse(t *testing.T) {
	for _, g := range goldenPoints {
		t.Run(g.text, func(t *testing.T) {
			if got := g.point.String(); got != g.text {
				t.Errorf("String() = %q, want %q", got, g.text)
			}
			got, err := fsx.ParsePoint(g.text)
			if err != nil {
				t.Fatalf("ParsePoint(%q): %v", g.text, err)
			}
			if got != g.point {
				t.Errorf("ParsePoint(%q) = %+v, want %+v", g.text, got, g.point)
			}
			key := g.point.Key
			parsed, err := fsx.ParseKey(key.String())
			if err != nil || parsed != key {
				t.Errorf("ParseKey(%q) = %+v, %v; want %+v", key.String(), parsed, err, key)
			}
		})
	}
}

func TestStepNamesAreTheFixedVocabulary(t *testing.T) {
	want := map[fsx.Step]string{
		fsx.StepCreate: "create", fsx.StepOpen: "open", fsx.StepWrite: "write", fsx.StepSync: "sync",
		fsx.StepRename: "rename", fsx.StepSyncDir: "sync-dir", fsx.StepRemove: "remove",
		fsx.StepRemoveAll: "remove-all", fsx.StepMkdir: "mkdir", fsx.StepRenameDir: "rename-dir",
		fsx.StepCommit: "commit",
	}
	for step, name := range want {
		if got := step.String(); got != name {
			t.Errorf("Step(%d).String() = %q, want %q", step, got, name)
		}
		if parsed, err := fsx.ParseStep(name); err != nil || parsed != step {
			t.Errorf("ParseStep(%q) = %v, %v; want %v", name, parsed, err, step)
		}
	}
	if len(want) != 11 {
		t.Fatalf("vocabulary has %d steps, want 11", len(want))
	}
}

func TestParsePointRejectsWhatIsNotCanonical(t *testing.T) {
	bad := []struct{ name, text string }{
		{"empty", ""},
		{"no-ordinal", "intake/out.rename"},
		{"no-slash", "intake.rename#1"},
		{"no-step-separator", "intake/outrename#1"},
		{"unknown-step", "intake/out.fsync#1"},
		{"zero-ordinal", "intake/out.rename#0"},
		{"leading-zero", "intake/out.rename#01"},
		{"plus-sign", "intake/out.rename#+1"},
		{"minus-sign", "intake/out.rename#-1"},
		{"empty-ordinal", "intake/out.rename#"},
		{"ordinal-overflow", "intake/out.rename#99999999999999999999999"},
		{"space-in-ordinal", "intake/out.rename#1 "},
		{"upper-case-tx", "Intake/out.rename#1"},
		{"digit-first-tx", "1ntake/out.rename#1"},
		{"underscore-role", "intake/o_ut.rename#1"},
		{"empty-tx", "/out.rename#1"},
		{"empty-role", "intake/.rename#1"},
		{"tx-too-long", strings.Repeat("a", 33) + "/out.rename#1"},
		{"role-too-long", "intake/" + strings.Repeat("a", 33) + ".rename#1"},
		{"commit-with-role", "intake/out.commit#1"},
		{"role-without-step-name", "intake/out.#1"},
		{"commit-with-empty-role", "a/.commit#1"},
		{"two-ordinals", "intake/out.rename#1#2"},
		{"trailing-newline", "intake/commit#1\n"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if p, err := fsx.ParsePoint(tc.text); err == nil {
				t.Errorf("ParsePoint(%q) = %+v, want an error", tc.text, p)
			}
		})
	}
	// A key has no ordinal.
	if _, err := fsx.ParseKey("intake/out.rename#1"); err == nil {
		t.Error("ParseKey accepted an ordinal")
	}
	if _, err := fsx.ParsePoint("intake/out.rename"); err == nil {
		t.Error("ParsePoint accepted a key")
	}
}

func TestCounterNumbersEachKeyFromOne(t *testing.T) {
	var c fsx.Counter
	a := fsx.Key{Tag: fsx.Tag{Tx: "t", Role: "a"}, Step: fsx.StepWrite}
	b := fsx.Key{Tag: fsx.Tag{Tx: "t", Role: "b"}, Step: fsx.StepWrite}
	other := fsx.Key{Tag: fsx.Tag{Tx: "t", Role: "a"}, Step: fsx.StepSync}
	for _, tc := range []struct {
		key  fsx.Key
		want int
	}{{a, 1}, {a, 2}, {b, 1}, {other, 1}, {a, 3}, {b, 2}} {
		if got := c.Next(tc.key); got.Hit != tc.want || got.Key != tc.key {
			t.Errorf("Next(%v) = %v, want ordinal %d", tc.key, got, tc.want)
		}
	}
}

func TestCounterIsSafeForConcurrentUse(t *testing.T) {
	var c fsx.Counter
	k := fsx.Key{Tag: fsx.Tag{Tx: "t", Role: "r"}, Step: fsx.StepWrite}
	const goroutines, each = 8, 500
	seen := make([][]int, goroutines)
	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range each {
				seen[g] = append(seen[g], c.Next(k).Hit)
			}
		}()
	}
	wg.Wait()
	counts := make(map[int]int)
	for _, hits := range seen {
		for _, h := range hits {
			counts[h]++
		}
	}
	for h := 1; h <= goroutines*each; h++ {
		if counts[h] != 1 {
			t.Fatalf("ordinal %d was handed out %d times, want exactly once", h, counts[h])
		}
	}
}

func TestPointValidateNamesTheProblem(t *testing.T) {
	ok := fsx.Point{Key: fsx.Key{Tag: fsx.Tag{Tx: "t", Role: "r"}, Step: fsx.StepWrite}, Hit: 1}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid point rejected: %v", err)
	}
	for name, mutate := range map[string]func(p *fsx.Point){
		"bad-tx":          func(p *fsx.Point) { p.Tag.Tx = "T" },
		"bad-role":        func(p *fsx.Point) { p.Tag.Role = "" },
		"unknown-step":    func(p *fsx.Point) { p.Step = 0 },
		"step-too-large":  func(p *fsx.Point) { p.Step = fsx.StepCommit + 1 },
		"zero-ordinal":    func(p *fsx.Point) { p.Hit = 0 },
		"commit-has-role": func(p *fsx.Point) { p.Step = fsx.StepCommit },
	} {
		p := ok
		mutate(&p)
		if err := p.Validate(); err == nil {
			t.Errorf("%s: Validate accepted %+v", name, p)
		}
	}
}

// FuzzParsePoint: decoding never panics, and whatever decodes re-encodes to
// the identical text.
func FuzzParsePoint(f *testing.F) {
	for _, g := range goldenPoints {
		f.Add(g.text)
	}
	f.Add("intake/out.rename#01")
	f.Add("intake/commit#1\n")
	f.Add("a/b.c#d")
	f.Fuzz(func(t *testing.T, s string) {
		if p, err := fsx.ParsePoint(s); err == nil {
			if got := p.String(); got != s {
				t.Fatalf("ParsePoint(%q) re-encodes as %q", s, got)
			}
			if err := p.Validate(); err != nil {
				t.Fatalf("ParsePoint(%q) returned a point that does not validate: %v", s, err)
			}
		}
		if k, err := fsx.ParseKey(s); err == nil {
			if got := k.String(); got != s {
				t.Fatalf("ParseKey(%q) re-encodes as %q", s, got)
			}
		}
	})
}

// FuzzPointRoundTrip: every valid point parses back to itself.
func FuzzPointRoundTrip(f *testing.F) {
	for _, g := range goldenPoints {
		f.Add(g.point.Tag.Tx, g.point.Tag.Role, uint8(g.point.Step), g.point.Hit)
	}
	f.Fuzz(func(t *testing.T, tx, role string, step uint8, hit int) {
		p := fsx.Point{Key: fsx.Key{Tag: fsx.Tag{Tx: tx, Role: role}, Step: fsx.Step(step)}, Hit: hit}
		if p.Validate() != nil {
			return
		}
		got, err := fsx.ParsePoint(p.String())
		if err != nil {
			t.Fatalf("ParsePoint(%q): %v", p.String(), err)
		}
		if got != p {
			t.Fatalf("round trip of %+v gave %+v", p, got)
		}
	})
}
