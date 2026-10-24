package txn_test

import (
	"reflect"
	"sync"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/txn"
)

// replayLike is a recovery declaration of the tests, so that coverage has both
// kinds to split by.
var replayLike = &txn.Decl{Name: "replay", Kind: txn.Recovery,
	Writes: []txn.Write{{Role: "rec", Op: txn.OpReplace, Optional: true}}}

// Coverage lists every key of the table, the ones never reached with a zero,
// each with the kind of its transaction.
func TestCoverageListsEveryKeyOfTheTableWithZerosIncluded(t *testing.T) {
	r := newRigUnrecovered(t, nil, multi, replayLike)
	keys := r.tab.Keys()
	cov := r.w.Coverage()

	if len(cov) != len(keys) {
		t.Fatalf("Coverage lists %d keys, the table has %d", len(cov), len(keys))
	}
	for i, c := range cov {
		if c.Key != keys[i] {
			t.Errorf("entry %d is %v, the table's key is %v", i, c.Key, keys[i])
		}
		if c.Hits != 0 {
			t.Errorf("%v has %d hits before anything ran", c.Key.Key, c.Hits)
		}
	}
}

func TestCoverageCountsHitsPerKeyAndSplitsThemByKind(t *testing.T) {
	r := newRig(t, nil, multi, replayLike)
	if _, err := firstError(r.runMulti()); err != nil {
		t.Fatal(err)
	}
	if _, err := firstError(r.runMulti()); err != nil {
		t.Fatal(err)
	}

	// Per key, written out by hand: the deferred group has two files, the log
	// two appends, and the run was twice. The rig's Recover settled the three
	// directories of the disk (the root, a and b) once.
	want := map[string]int{
		"txn-settle/dir.sync-dir": 3, "txn-settle/commit": 1,
		"multi/body.create": 2, "multi/body.write": 2, "multi/body.sync": 2, "multi/body.rename": 2, "multi/body.sync-dir": 2,
		"multi/in.create": 4, "multi/in.write": 4, "multi/in.sync": 4, "multi/in.rename": 4,
		"multi/in-dir.sync-dir": 2,
		"multi/out.create":      2, "multi/out.write": 2, "multi/out.sync": 2, "multi/out.rename": 2, "multi/out.sync-dir": 2,
		"multi/journal.open": 2, "multi/journal.write": 4, "multi/journal.sync": 2, "multi/journal.sync-dir": 2,
		"multi/commit": 2,
	}
	got := map[string]int{}
	byKind := map[txn.Kind]int{}
	for _, c := range r.w.Coverage() {
		byKind[c.Key.Kind]++
		if c.Hits > 0 {
			got[c.Key.Key.String()] = c.Hits
		}
		if c.Key.Tag.Tx == "multi" && c.Key.Kind != txn.Normal {
			t.Errorf("%v has kind %d, want Normal", c.Key.Key, c.Key.Kind)
		}
		if c.Key.Tag.Tx == "replay" && c.Key.Kind != txn.Recovery {
			t.Errorf("%v has kind %d, want Recovery", c.Key.Key, c.Key.Kind)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("hits:\n got %v\nwant %v", got, want)
	}
	// multi has 20 keys, and the settle transaction has its directory sync and
	// its commit.
	if byKind[txn.Normal] != 20+2 {
		t.Errorf("%d Normal keys, want %d", byKind[txn.Normal], 20+2)
	}
	// The recovery declarations are the cleanup transaction (4 steps' worth
	// of keys: remove, remove-all, sync-dir and its commit) and replay (5 and
	// a commit).
	if byKind[txn.Recovery] != 4+6 {
		t.Errorf("%d Recovery keys, want %d", byKind[txn.Recovery], 4+6)
	}
}

// Counts belong to a Writer: another Writer on the same table starts from
// zero, so parallel tests do not disturb each other.
func TestCoverageBelongsToTheWriter(t *testing.T) {
	r1 := newRig(t, nil, multi)
	r2 := newRigUnrecovered(t, nil, multi)
	if _, err := firstError(r1.runMulti()); err != nil {
		t.Fatal(err)
	}
	for _, c := range r2.w.Coverage() {
		if c.Hits != 0 {
			t.Errorf("a second Writer counts %v: %d", c.Key.Key, c.Hits)
		}
	}
}

// Transactions of different declarations may run in parallel, and the counts
// stay exact (run with the race detector).
func TestCoverageIsExactUnderParallelTransactions(t *testing.T) {
	ds := []*txn.Decl{
		{Name: "p1", Kind: txn.Normal, Writes: []txn.Write{{Role: "r", Op: txn.OpReplace, Many: true}}},
		{Name: "p2", Kind: txn.Normal, Writes: []txn.Write{{Role: "r", Op: txn.OpReplace, Many: true}}},
		{Name: "p3", Kind: txn.Normal, Writes: []txn.Write{{Role: "r", Op: txn.OpReplace, Many: true}}},
	}
	r := newRig(t, nil, ds...)
	const rounds = 20
	var wg sync.WaitGroup
	for i, d := range ds {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < rounds; j++ {
				tx := r.w.Begin(d)
				if err := tx.Replace("r", "a/"+d.Name, []byte{byte(i), byte(j)}, mode); err != nil {
					t.Error(err)
				}
				if err := tx.Commit(); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	for _, c := range r.w.Coverage() {
		if c.Key.Tag.Tx[0] != 'p' {
			continue
		}
		if c.Hits != rounds {
			t.Errorf("%v has %d hits, want %d", c.Key.Key, c.Hits, rounds)
		}
	}
}

// The commit point is counted on the same counter as the steps, so its
// ordinal is the number of the run.
func TestCommitPointsAreNumberedPerRun(t *testing.T) {
	r := newRig(t, nil, singleReplace)
	for i := 1; i <= 3; i++ {
		tx := r.w.Begin(singleReplace)
		if err := tx.Replace("rec", "a/rec", []byte{byte(i)}, mode); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	var commits []fsx.Point
	for _, p := range r.rec.points {
		if p.Step == fsx.StepCommit {
			commits = append(commits, p)
		}
	}
	if len(commits) != 3 || commits[0].Hit != 1 || commits[2].Hit != 3 || commits[2].String() != "single/commit#3" {
		t.Errorf("commit points = %v", commits)
	}
}
