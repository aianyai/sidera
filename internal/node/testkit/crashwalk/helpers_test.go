package crashwalk_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/txn"
)

// table builds the declarations of a small program, written the way an owner of
// files declares them:
//
//	intake  (normal)   body, out: two replaced files, then the commit
//	status  (normal)   one replaced file
//	journal (normal)   an appended file that is synced, and no commit point of its own
//	orphans (recovery) one removal and one directory sync
//	replay  (recovery) one replaced file
func table(t testing.TB) *txn.Table {
	t.Helper()
	tb, err := txn.NewTable(
		&txn.Decl{Name: "intake", Kind: txn.Normal, Writes: []txn.Write{{Role: "body", Op: txn.OpReplace}, {Role: "out", Op: txn.OpReplace}}},
		&txn.Decl{Name: "status", Kind: txn.Normal, Writes: []txn.Write{{Role: "file", Op: txn.OpReplace}}},
		&txn.Decl{Name: "journal", Kind: txn.Normal, Writes: []txn.Write{{Role: "events", Op: txn.OpAppend}, {Role: "events", Op: txn.OpSyncFile}}},
		&txn.Decl{Name: "orphans", Kind: txn.Recovery, Writes: []txn.Write{{Role: "body", Op: txn.OpRemove}, {Role: "dir", Op: txn.OpSyncDir}}},
		&txn.Decl{Name: "replay", Kind: txn.Recovery, Writes: []txn.Write{{Role: "state", Op: txn.OpReplace}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	return tb
}

func key(t testing.TB, s string) fsx.Key {
	t.Helper()
	k, err := fsx.ParseKey(s)
	if err != nil {
		t.Fatalf("ParseKey(%q): %v", s, err)
	}
	return k
}

func point(t testing.TB, s string) fsx.Point {
	t.Helper()
	p, err := fsx.ParsePoint(s)
	if err != nil {
		t.Fatalf("ParsePoint(%q): %v", s, err)
	}
	return p
}

// steps of a replaced file, in the order fsx runs them.
var replaceSteps = []string{"create", "write", "sync", "rename", "sync-dir"}

// program is a script of what a system under test does: each step of a scenario
// is a list of (key, path) hits, and a counter numbers each key from 1 as fsx
// does.
type program struct {
	steps []programStep
}

type programStep struct {
	name string
	hits []rawHit
}

type rawHit struct {
	key  string
	path string
}

// replaced returns the hits of one replaced file written by tx/role.
func replaced(tx, role, path string) []rawHit {
	out := make([]rawHit, len(replaceSteps))
	for i, s := range replaceSteps {
		out[i] = rawHit{tx + "/" + role + "." + s, path}
	}
	return out
}

func commit(tx string) rawHit { return rawHit{tx + "/commit", ""} }

func cat(parts ...[]rawHit) []rawHit {
	var out []rawHit
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// run plays the program from the start with a fresh counter, stopping after the
// hit that equals stop (a zero stop plays everything). Hits before the first step
// are setup.
func (p program) run(t testing.TB, gen int, stop fsx.Point, upToStep string) (hits []crashwalk.Hit, stopped bool) {
	t.Helper()
	counts := map[fsx.Key]int{}
	for _, st := range p.steps {
		for _, h := range st.hits {
			k := key(t, h.key)
			counts[k]++
			pt := fsx.Point{Key: k, Hit: counts[k]}
			hits = append(hits, crashwalk.Hit{Gen: gen, Point: pt, Path: h.path, Setup: st.name == "setup"})
			if stop != (fsx.Point{}) && pt == stop {
				return hits, true
			}
		}
		if upToStep != "" && st.name == upToStep {
			return hits, true
		}
	}
	return hits, false
}

// scenario is a Scenario with the answers written as data.
type scenario struct {
	name     string
	steps    []string
	max      int
	recovery []crashwalk.RecoveryCase
}

func (s scenario) Name() string                       { return s.name }
func (s scenario) Steps() []string                    { return s.steps }
func (s scenario) MaxPoints() int                     { return s.max }
func (s scenario) Recovery() []crashwalk.RecoveryCase { return s.recovery }

// fakeBackend is a Backend over a program: it records, kills at a target, and
// plays the recovery of a trigger, and says what it was asked.
type fakeBackend struct {
	t        testing.TB
	progs    map[string]program
	recovery map[string][]rawHit // by case item key: hits a recovery run makes
	// misbehaviours, switched on by tests
	skipTarget func(crashwalk.Target) bool              // the target is never reached
	alter      func(crashwalk.Target, *[]crashwalk.Hit) // change the rerun's hits
	violate    func(crashwalk.RunPlan) []string         // invariants that failed
	cleanExtra []rawHit                                 // hits a clean recording makes beyond the program
	afterMain  func(crashwalk.Target) []rawHit          // hits the restart after a main crash makes

	runs []crashwalk.RunPlan
}

func (b *fakeBackend) Run(_ context.Context, s crashwalk.Scenario, p crashwalk.RunPlan) (crashwalk.RunResult, error) {
	b.runs = append(b.runs, p)
	prog, ok := b.progs[s.Name()]
	if !ok {
		return crashwalk.RunResult{}, fmt.Errorf("no program for %q", s.Name())
	}
	var res crashwalk.RunResult
	if p.Record {
		hits, _ := prog.run(b.t, 1, fsx.Point{}, "")
		for i, h := range b.cleanExtra {
			k := key(b.t, h.key)
			hits = append(hits, crashwalk.Hit{Gen: 1, Point: fsx.Point{Key: k, Hit: 100 + i}, Path: h.path})
		}
		res.Hits = [][]crashwalk.Hit{hits}
		return res, nil
	}
	if p.Trigger != nil {
		// A main crash, then a recovery run that must reach the target.
		gen1, _ := prog.run(b.t, 1, p.Trigger.CrashAt, "")
		res.Hits = append(res.Hits, gen1)
		var gen2 []crashwalk.Hit
		for _, tg := range p.Targets {
			item := tg.Point.Key
			counts := map[fsx.Key]int{}
			for _, h := range b.recovery[item.String()] {
				k := key(b.t, h.key)
				counts[k]++
				gen2 = append(gen2, crashwalk.Hit{Gen: 2, Point: fsx.Point{Key: k, Hit: counts[k]}, Path: h.path})
			}
			reached := false
			for _, h := range gen2 {
				if h.Point == tg.Point {
					reached = true
				}
			}
			if b.skipTarget != nil && b.skipTarget(tg) {
				reached = false
			}
			res.Reached = append(res.Reached, reached)
		}
		res.Hits = append(res.Hits, gen2)
		if b.violate != nil {
			res.Violations = b.violate(p)
		}
		return res, nil
	}
	for _, tg := range p.Targets {
		hits, stopped := prog.run(b.t, 1, tg.Point, tg.AfterStep)
		if b.alter != nil {
			b.alter(tg, &hits)
		}
		reached := stopped
		if b.skipTarget != nil && b.skipTarget(tg) {
			reached = false
		}
		res.Hits = append(res.Hits, hits)
		res.Reached = append(res.Reached, reached)
		if b.afterMain != nil {
			counts := map[fsx.Key]int{}
			var gen2 []crashwalk.Hit
			for _, h := range b.afterMain(tg) {
				k := key(b.t, h.key)
				counts[k]++
				gen2 = append(gen2, crashwalk.Hit{Gen: 2, Point: fsx.Point{Key: k, Hit: counts[k]}, Path: h.path})
			}
			if len(gen2) > 0 {
				res.Hits = append(res.Hits, gen2)
			}
		}
	}
	if b.violate != nil {
		res.Violations = b.violate(p)
	}
	return res, nil
}

func hitKeys(hits []crashwalk.Hit) string {
	var parts []string
	for _, h := range hits {
		parts = append(parts, h.Point.String())
	}
	return strings.Join(parts, " ")
}
