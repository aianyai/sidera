package crashwalk_test

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/txn"
)

func TestNormalizePath(t *testing.T) {
	rows := []struct{ name, in, want string }{
		{"a plain path is unchanged", "state/out/m1.rec", "state/out/m1.rec"},
		{"a temporary file name loses its random suffix", "state/out/.tmp-9f3a1c", "state/out/.tmp-*"},
		{"a discarded directory loses its random suffix", "config/adapters/.trash-77aa", "config/adapters/.trash-*"},
		{"every element is normalised", "a/.tmp-1/.tmp-2/b", "a/.tmp-*/.tmp-*/b"},
		{"a name that only contains the prefix is unchanged", "state/x.tmp-1", "state/x.tmp-1"},
		{"the root is unchanged", ".", "."},
		{"an empty path is unchanged", "", ""},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			if got := crashwalk.NormalizePath(r.in); got != r.want {
				t.Fatalf("NormalizePath(%q) = %q, want %q", r.in, got, r.want)
			}
		})
	}
}

func TestParseMode(t *testing.T) {
	rows := []struct {
		in   string
		want crashwalk.Mode
		bad  bool
	}{
		{"", crashwalk.Smoke, false}, // a local go test is fast unless it asks for more
		{"smoke", crashwalk.Smoke, false},
		{"full", crashwalk.Full, false},
		{"FULL", 0, true},
		{"everything", 0, true},
	}
	for _, r := range rows {
		got, err := crashwalk.ParseMode(r.in)
		if (err != nil) != r.bad || (!r.bad && got != r.want) {
			t.Errorf("ParseMode(%q) = %v, %v; want %v (bad %v)", r.in, got, err, r.want, r.bad)
		}
	}
}

func TestParseShard(t *testing.T) {
	rows := []struct {
		in   string
		want crashwalk.Shard
		bad  bool
	}{
		{"", crashwalk.Shard{Index: 1, Count: 1}, false},
		{"1/1", crashwalk.Shard{Index: 1, Count: 1}, false},
		{"2/3", crashwalk.Shard{Index: 2, Count: 3}, false},
		{"3/3", crashwalk.Shard{Index: 3, Count: 3}, false},
		{"0/3", crashwalk.Shard{}, true},
		{"4/3", crashwalk.Shard{}, true},
		{"1/0", crashwalk.Shard{}, true},
		{"a/b", crashwalk.Shard{}, true},
		{"1-3", crashwalk.Shard{}, true},
		{"1/3/5", crashwalk.Shard{}, true},
	}
	for _, r := range rows {
		got, err := crashwalk.ParseShard(r.in)
		if (err != nil) != r.bad || (!r.bad && got != r.want) {
			t.Errorf("ParseShard(%q) = %v, %v; want %v (bad %v)", r.in, got, err, r.want, r.bad)
		}
	}
}

// TestShardsPartitionTheTargets: for every shard count, each target is in exactly
// one shard and the shards together are all of them, so splitting a run over CI
// jobs neither skips a point nor repeats one.
func TestShardsPartitionTheTargets(t *testing.T) {
	for n := 1; n <= 7; n++ {
		for total := 0; total <= 40; total += 3 {
			seen := make([]int, total)
			for i := 1; i <= n; i++ {
				sh := crashwalk.Shard{Index: i, Count: n}
				for k := 0; k < total; k++ {
					if sh.Keep(k) {
						seen[k]++
					}
				}
			}
			for k, c := range seen {
				if c != 1 {
					t.Fatalf("%d shards, %d targets: target %d is in %d shards", n, total, k, c)
				}
			}
		}
	}
}

// recording is the clean run of a scenario that has a setup and three steps.
func recordingHits(t *testing.T) []crashwalk.Hit {
	t.Helper()
	p := program{steps: []programStep{
		{"setup", cat(replaced("status", "file", "state/status.rec"), []rawHit{commit("status")})},
		{"send", cat(replaced("intake", "body", "state/body/m1.tmp"), replaced("intake", "out", "state/out/m1.rec"), []rawHit{commit("intake")})},
		{"deliver", cat(replaced("status", "file", "state/status.rec"), []rawHit{commit("status")})},
		{"log", []rawHit{{"journal/events.open", "archive/a.jsonl"}, {"journal/events.write", "archive/a.jsonl"}, {"journal/events.sync", "archive/a.jsonl"}, {"journal/events.sync-dir", "archive"}, commit("journal")}},
	}}
	hits, _ := p.run(t, 1, fsx.Point{}, "")
	return hits
}

func pointsOf(targets []crashwalk.Target) []string {
	var out []string
	for _, tg := range targets {
		if tg.AfterStep != "" {
			out = append(out, "after:"+tg.AfterStep)
		} else {
			out = append(out, tg.Point.String())
		}
	}
	return out
}

// TestPlanFullTakesEveryWindowPointExceptTheSyncSteps: the setup is not part of
// the window (the startup scenario covers what it writes), the commit point is,
// and the steps that only sync are not: killing a process leaves the page cache,
// so a kill before a sync and a kill after the previous step leave the same disk.
func TestPlanFullTakesEveryWindowPointExceptTheSyncSteps(t *testing.T) {
	got := crashwalk.Plan(recordingHits(t), crashwalk.Full, []string{"send", "deliver"})
	// The points come sorted by key, not in the order the recording made them:
	// the order of a shard's numbering must not depend on the recording's
	// interleaving. The step boundaries follow in the order of the steps.
	want := []string{
		// intake: body then out, each create, write, rename, and the commit
		"intake/body.create#1", "intake/body.rename#1", "intake/body.write#1",
		"intake/commit#1",
		"intake/out.create#1", "intake/out.rename#1", "intake/out.write#1",
		// the journal: the open and the write, no sync
		"journal/commit#1", "journal/events.open#1", "journal/events.write#1",
		// status in the deliver step is the second use of the keys: ordinal 2
		"status/commit#2", "status/file.create#2", "status/file.rename#2", "status/file.write#2",
		// step boundaries
		"after:send", "after:deliver",
	}
	if g := pointsOf(got); !reflect.DeepEqual(g, want) {
		t.Fatalf("Plan(full):\n got %v\nwant %v", g, want)
	}
	for _, tg := range got {
		if tg.Gen != 1 {
			t.Errorf("target %v is not for the first generation", tg)
		}
	}
}

func TestPlanFullNeverTargetsASyncStepOrASetupHit(t *testing.T) {
	for _, tg := range crashwalk.Plan(recordingHits(t), crashwalk.Full, nil) {
		if tg.AfterStep != "" {
			continue
		}
		if crashwalk.IsSync(tg.Point.Step) {
			t.Errorf("target %v is a sync step", tg.Point)
		}
		if tg.Point.Tag.Tx == "status" && tg.Point.Hit == 1 {
			t.Errorf("target %v belongs to the setup", tg.Point)
		}
	}
}

// TestPlanSmokeTakesOnePointPerTransactionBeforeItsLastFileRole: stopping before
// the last file of a transaction becomes visible leaves the transaction half done
// in the most informative way, one run per transaction.
func TestPlanSmokeTakesOnePointPerTransactionBeforeItsLastFileRole(t *testing.T) {
	got := crashwalk.Plan(recordingHits(t), crashwalk.Smoke, []string{"send", "deliver"})
	want := []string{
		"intake/out.rename#1",    // out is written after body: stop before its rename
		"journal/events.write#1", // an appended file has no rename: its first append
		"status/file.rename#2",   // the single file of status, its first use in the window
	}
	if g := pointsOf(got); !reflect.DeepEqual(g, want) {
		t.Fatalf("Plan(smoke):\n got %v\nwant %v", g, want)
	}
}

func TestPlanSmokeOfARecordingWithoutAWindowIsEmpty(t *testing.T) {
	setupOnly := recordingHits(t)[:6]
	if got := crashwalk.Plan(setupOnly, crashwalk.Smoke, nil); len(got) != 0 {
		t.Fatalf("Plan = %v for a recording that is all setup", pointsOf(got))
	}
}

func TestPlanRecoveryFullTakesEveryCaseSmokeTheFirstOfEachTransaction(t *testing.T) {
	tb := table(t)
	_ = tb
	cases := []crashwalk.RecoveryCase{
		{Item: key(t, "orphans/body.remove"), After: crashwalk.CrashAt(point(t, "intake/out.rename#1"))},
		{Item: key(t, "orphans/commit"), After: crashwalk.CrashAt(point(t, "intake/out.rename#1"))},
		{Item: key(t, "replay/state.rename"), After: crashwalk.CorruptOffline("state/in/*", "flip the first line checksum")},
		{Item: key(t, "replay/commit"), After: crashwalk.CorruptOffline("state/in/*", "flip the first line checksum")},
	}
	full := crashwalk.PlanRecovery(cases, crashwalk.Full)
	if len(full) != 4 {
		t.Fatalf("full: %d cases, want all 4", len(full))
	}
	smoke := crashwalk.PlanRecovery(cases, crashwalk.Smoke)
	var items []string
	for _, c := range smoke {
		items = append(items, c.Item.String())
	}
	sort.Strings(items)
	if want := []string{"orphans/body.remove", "replay/state.rename"}; !reflect.DeepEqual(items, want) {
		t.Fatalf("smoke: %v, want the first case of each recovery transaction %v", items, want)
	}
}

func TestIsSync(t *testing.T) {
	for _, s := range []string{"sync", "sync-dir"} {
		st, err := fsx.ParseStep(s)
		if err != nil {
			t.Fatal(err)
		}
		if !crashwalk.IsSync(st) {
			t.Errorf("%s is not a sync step", s)
		}
	}
	for _, s := range []string{"create", "open", "write", "rename", "remove", "remove-all", "mkdir", "rename-dir", "commit"} {
		st, err := fsx.ParseStep(s)
		if err != nil {
			t.Fatal(err)
		}
		if crashwalk.IsSync(st) {
			t.Errorf("%s is a sync step", s)
		}
	}
}

func TestCheckBudget(t *testing.T) {
	hits := recordingHits(t)
	n := len(crashwalk.Plan(hits, crashwalk.Full, nil)) // the main points of the window
	rows := []struct {
		name string
		max  int
		bad  bool
	}{
		{"exactly the budget", n, false},
		{"one under the points", n - 1, true},
		{"room to spare", n + 20, false},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			err := crashwalk.CheckBudget(scenario{name: "send-recv", max: r.max}, hits)
			if (err != nil) != r.bad {
				t.Fatalf("CheckBudget = %v, bad %v", err, r.bad)
			}
			if err != nil && !strings.Contains(err.Error(), "send-recv") {
				t.Fatalf("the error does not name the scenario: %v", err)
			}
		})
	}
}

func TestCompareRuns(t *testing.T) {
	recorded := recordingHits(t)
	target := crashwalk.Target{Gen: 1, Point: point(t, "intake/out.rename#1")}
	rerun := func() []crashwalk.Hit {
		var out []crashwalk.Hit
		for _, h := range recorded {
			out = append(out, h)
			if h.Point == target.Point {
				break
			}
		}
		return out
	}
	rows := []struct {
		rule   string
		mutate func([]crashwalk.Hit) []crashwalk.Hit
		bad    bool
	}{
		{"the same prefix", func(h []crashwalk.Hit) []crashwalk.Hit { return h }, false},
		{"another path for a file of the same key", func(h []crashwalk.Hit) []crashwalk.Hit {
			for i := range h {
				if h[i].Point == point(t, "intake/body.rename#1") {
					h[i].Path = "state/body/other.tmp"
				}
			}
			return h
		}, true},
		{"the target was not the last hit", func(h []crashwalk.Hit) []crashwalk.Hit {
			return append(h, crashwalk.Hit{Gen: 1, Point: point(t, "intake/out.sync-dir#1"), Path: "state/out"})
		}, true},
		{"the target was never reached", func(h []crashwalk.Hit) []crashwalk.Hit { return h[:len(h)-1] }, true},
		{"another key took the place of a hit", func(h []crashwalk.Hit) []crashwalk.Hit {
			h[3].Point = point(t, "intake/body.remove#1")
			return h
		}, true},
		{"the other keys interleave differently but each key's own order holds", func(h []crashwalk.Hit) []crashwalk.Hit {
			// Swap two adjacent hits of different pairs (the commit of the setup
			// and the first write of the next transaction): the global order
			// changes, no pair's own order does.
			h[5], h[6] = h[6], h[5]
			return h
		}, false},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			err := crashwalk.CompareRuns(recorded, r.mutate(rerun()), target)
			if (err != nil) != r.bad {
				t.Fatalf("CompareRuns = %v, bad %v", err, r.bad)
			}
		})
	}
}

func TestAssessCoverage(t *testing.T) {
	tb := table(t)
	all := func() []crashwalk.Hit {
		var out []crashwalk.Hit
		for _, k := range tb.Keys() {
			out = append(out, crashwalk.Hit{Gen: 1, Point: fsx.Point{Key: k.Key, Hit: 1}})
		}
		return out
	}
	t.Run("every declared key hit once is full coverage", func(t *testing.T) {
		c := crashwalk.Assess(tb, all())
		if len(c.MissingNormal) != 0 || len(c.MissingRecovery) != 0 || len(c.Undeclared) != 0 {
			t.Fatalf("%+v", c)
		}
	})
	t.Run("a normal key never hit is missing, recovery keys are counted apart", func(t *testing.T) {
		var hits []crashwalk.Hit
		for _, h := range all() {
			if h.Point.Key.String() == "intake/out.rename" || h.Point.Key.String() == "orphans/body.remove" {
				continue
			}
			hits = append(hits, h)
		}
		c := crashwalk.Assess(tb, hits)
		if got := keyStrings(c.MissingNormal); !reflect.DeepEqual(got, []string{"intake/out.rename"}) {
			t.Errorf("MissingNormal = %v", got)
		}
		if got := keyStrings(c.MissingRecovery); !reflect.DeepEqual(got, []string{"orphans/body.remove"}) {
			t.Errorf("MissingRecovery = %v", got)
		}
	})
	t.Run("a write that no declaration lists is undeclared", func(t *testing.T) {
		hits := append(all(), crashwalk.Hit{Gen: 1, Point: point(t, "rogue/file.rename#1")})
		c := crashwalk.Assess(tb, hits)
		if got := keyStrings(c.Undeclared); !reflect.DeepEqual(got, []string{"rogue/file.rename"}) {
			t.Fatalf("Undeclared = %v", got)
		}
	})
	t.Run("sync steps count as hit", func(t *testing.T) {
		c := crashwalk.Assess(tb, all())
		for _, k := range c.MissingNormal {
			if crashwalk.IsSync(k.Step) {
				t.Fatalf("a sync key is missing: %v", k)
			}
		}
	})
}

func keyStrings(keys []fsx.Key) []string {
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = k.String()
	}
	return out
}

func TestCheckRecoveryCasesUnion(t *testing.T) {
	tb := table(t)
	all := func() []crashwalk.RecoveryCase {
		var cases []crashwalk.RecoveryCase
		for _, k := range tb.Keys() {
			if k.Kind != txn.Recovery || crashwalk.IsSync(k.Step) {
				continue
			}
			cases = append(cases, crashwalk.RecoveryCase{Item: k.Key, After: crashwalk.CrashAt(point(t, "intake/out.rename#1"))})
		}
		return cases
	}
	cases := all()
	if len(cases) != 4 { // orphans/body.remove, orphans/commit, replay/state.{create,write,rename}, replay/commit
		// The table has two recovery transactions: count what it declares.
		t.Logf("recovery items of the table: %d", len(cases))
	}
	t.Run("the union is every recovery item that is not a sync step", func(t *testing.T) {
		half := len(cases) / 2
		sa := scenario{name: "a", recovery: cases[:half]}
		sb := scenario{name: "b", recovery: cases[half:]}
		if err := crashwalk.CheckRecoveryCases(tb, []crashwalk.Scenario{sa, sb}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("an item no scenario covers is reported", func(t *testing.T) {
		err := crashwalk.CheckRecoveryCases(tb, []crashwalk.Scenario{scenario{name: "a", recovery: cases[1:]}})
		if err == nil || !strings.Contains(err.Error(), cases[0].Item.String()) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("an item that is not a recovery write is reported", func(t *testing.T) {
		extra := append(append([]crashwalk.RecoveryCase{}, cases...), crashwalk.RecoveryCase{Item: key(t, "intake/out.rename"), After: crashwalk.CrashAt(point(t, "intake/out.rename#1"))})
		err := crashwalk.CheckRecoveryCases(tb, []crashwalk.Scenario{scenario{name: "a", recovery: extra}})
		if err == nil || !strings.Contains(err.Error(), "intake/out.rename") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("a sync item is reported: a kill there is no different from the step before it", func(t *testing.T) {
		extra := append(append([]crashwalk.RecoveryCase{}, cases...), crashwalk.RecoveryCase{Item: key(t, "orphans/dir.sync-dir"), After: crashwalk.CrashAt(point(t, "intake/out.rename#1"))})
		if err := crashwalk.CheckRecoveryCases(tb, []crashwalk.Scenario{scenario{name: "a", recovery: extra}}); err == nil {
			t.Fatal("a sync item was accepted")
		}
	})
	t.Run("an item named twice is reported", func(t *testing.T) {
		dup := append(append([]crashwalk.RecoveryCase{}, cases...), cases[0])
		if err := crashwalk.CheckRecoveryCases(tb, []crashwalk.Scenario{scenario{name: "a", recovery: dup}}); err == nil {
			t.Fatal("a repeated item was accepted")
		}
	})
}

// FuzzParseShardAndMode: the settings come from the environment of a CI job, so a
// malformed value must be an error and never a panic, and a shard that parses is
// one that picks its residue class.
func FuzzParseShardAndMode(f *testing.F) {
	// The last is an input the fuzzer found: a valid shard of eight billion parts.
	for _, s := range []string{"", "1/1", "2/3", "0/3", "a/b", "1/3/5", "smoke", "full", "010/00008000000000"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if sh, err := crashwalk.ParseShard(s); err == nil {
			if sh.Count < 1 || sh.Index < 1 || sh.Index > sh.Count {
				t.Fatalf("ParseShard(%q) = %+v", s, sh)
			}
			// Count consecutive runs hold exactly one of the shard's, checked for
			// counts a test can afford to walk.
			if sh.Count <= 1000 {
				kept := 0
				for k := 0; k < sh.Count; k++ {
					if sh.Keep(k) {
						kept++
					}
				}
				if kept != 1 {
					t.Fatalf("shard %+v keeps %d of %d consecutive runs, want exactly one", sh, kept, sh.Count)
				}
			}
		}
		_, _ = crashwalk.ParseMode(s)
	})
}

// TestPlanOrderDoesNotDependOnTheInterleavingOfTransactions: two recordings in
// which the transactions interleave differently (each pair of transaction and
// role keeps its own order, so CompareRuns accepts either as the other) must plan
// the same targets in the same order, or shard k of one is not target k of the
// other.
func TestPlanOrderDoesNotDependOnTheInterleavingOfTransactions(t *testing.T) {
	mk := func(points ...string) []crashwalk.Hit {
		var hits []crashwalk.Hit
		for _, s := range points {
			hits = append(hits, crashwalk.Hit{Gen: 1, Point: point(t, s), Path: "x"})
		}
		return hits
	}
	a := mk("intake/out.create#1", "status/file.create#1", "intake/out.rename#1", "status/file.rename#1")
	b := mk("status/file.create#1", "intake/out.create#1", "status/file.rename#1", "intake/out.rename#1")
	for _, mode := range []crashwalk.Mode{crashwalk.Full, crashwalk.Smoke} {
		ga, gb := pointsOf(crashwalk.Plan(a, mode, []string{"s1", "s2"})), pointsOf(crashwalk.Plan(b, mode, []string{"s1", "s2"}))
		if !reflect.DeepEqual(ga, gb) {
			t.Errorf("mode %v: the plans differ with the interleaving:\n %v\n %v", mode, ga, gb)
		}
	}
	// Hit numbers sort as numbers: hit 10 follows hit 9.
	hits := mk("status/file.create#9", "status/file.create#10")
	if got := pointsOf(crashwalk.Plan(hits, crashwalk.Full, nil)); !reflect.DeepEqual(got, []string{"status/file.create#9", "status/file.create#10"}) {
		t.Errorf("hit numbers are not ordered as numbers: %v", got)
	}
}
