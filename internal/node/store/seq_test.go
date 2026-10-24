package store_test

import (
	"syscall"
	"testing"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/store"
)

func scanWith(t *testing.T, floors map[ident.Address]uint64, outs ...store.OutRecord) *store.Scan {
	t.Helper()
	r := newRig(t)
	for _, o := range outs {
		seed(t, r.mem, o)
	}
	if floors != nil {
		seed(t, r.mem, store.SeqRecord{Sender: ep(1), Floors: floors})
	}
	return r.scan()
}

func withSeq(i int, seq uint64, toLocal bool) store.OutRecord {
	o := srcOut(i, toLocal)
	o.Envelope.Seq[o.Key.Recipients[0].Endpoint] = seq
	return o
}

func TestSeqBookStartsAboveFloorAndRecords(t *testing.T) {
	pair := addr(2, 2)
	for _, c := range []struct {
		name   string
		floors map[ident.Address]uint64
		outs   []store.OutRecord
		want   uint64
	}{
		{"nothing at all starts at 1", nil, nil, 1},
		{"records only: the highest plus one", nil, []store.OutRecord{withSeq(1, 4, false), withSeq(2, 2, false), withSeq(3, 3, false)}, 5},
		{"a floor above every record", map[ident.Address]uint64{pair: 9}, []store.OutRecord{withSeq(1, 5, false)}, 10},
		{"a floor below a record", map[ident.Address]uint64{pair: 3}, []store.OutRecord{withSeq(1, 5, false)}, 6},
		{"a floor and no records", map[ident.Address]uint64{pair: 7}, nil, 8},
		{"a floor for another pair counts for nothing", map[ident.Address]uint64{addr(3, 3): 50}, []store.OutRecord{withSeq(1, 2, false)}, 3},
		{"a control signal counts like a message", nil, []store.OutRecord{func() store.OutRecord { o := outCtlRecord(1); o.Envelope.Seq[ep(2)] = 6; return o }()}, 7},
		{"a lost record has no number and counts for nothing", nil, []store.OutRecord{lostOutAt(outKey(1, false), base)}, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := store.NewSeqBook(scanWith(t, c.floors, c.outs...))
			if got := b.Next(ep(1), pair); got != c.want {
				t.Errorf("Next = %d, want %d", got, c.want)
			}
		})
	}
}

func TestSeqBookCountsEveryPairOnItsOwn(t *testing.T) {
	b := store.NewSeqBook(scanWith(t, nil, withSeq(1, 3, false), withSeq(2, 8, true)))
	if got := b.Next(ep(1), addr(2, 2)); got != 4 {
		t.Errorf("to node 2: %d, want 4", got)
	}
	if got := b.Next(ep(1), addr(local, 2)); got != 9 {
		t.Errorf("to a local endpoint: %d, want 9", got)
	}
	if got := b.Next(ep(1), addr(2, 5)); got != 1 {
		t.Errorf("to another endpoint: %d, want 1", got)
	}
	if got := b.Next(ep(3), addr(2, 2)); got != 1 {
		t.Errorf("from another endpoint: %d, want 1", got)
	}
}

// A number is given twice until its write is acknowledged: the book moves only
// when told, after the commit, so a failed or crashed write gives the same
// number again and no gap is made.
func TestSeqBookMovesOnlyWhenNoted(t *testing.T) {
	b := store.NewSeqBook(scanWith(t, nil, withSeq(1, 2, false)))
	pair := addr(2, 2)
	n := b.Next(ep(1), pair)
	if again := b.Next(ep(1), pair); again != n || n != 3 {
		t.Fatalf("Next twice without Note = %d then %d, want 3 both times", n, again)
	}
	b.Note(ep(1), pair, n)
	if got := b.Next(ep(1), pair); got != 4 || b.High(ep(1), pair) != 3 {
		t.Errorf("after Note(3): Next %d High %d, want 4 and 3", got, b.High(ep(1), pair))
	}
	b.Note(ep(1), pair, 2)
	if b.High(ep(1), pair) != 3 {
		t.Errorf("Note of a lower number moved High back to %d", b.High(ep(1), pair))
	}
}

func TestFailedIntakeGivesTheNumberAgain(t *testing.T) {
	r := newRig(t)
	f := newFaultyRig(t, r.mem)
	book := store.NewSeqBook(f.scan())
	pair := addr(local, 2)
	n := book.Next(ep(1), pair)
	f.faulty.Fail(fsxtest.Match{Tx: "intake", Role: "out", Step: fsx.StepRename, Hit: 1}, syscall.EIO)
	out := srcOut(1, true)
	out.Envelope.Seq[ep(2)] = n
	if err := f.st.Intake(bodyBytes(1), out); err == nil {
		t.Fatal("the injected failure did not surface")
	}
	// The write failed: the book was not told, and the disk has no record.
	if got := book.Next(ep(1), pair); got != n {
		t.Errorf("after a failed intake Next = %d, want %d again", got, n)
	}
	restarted := openRig(t, r.mem.Clone())
	if got := store.NewSeqBook(restarted.scan()).Next(ep(1), pair); got != n {
		t.Errorf("a restart gives %d, want %d: a failed intake leaves no number used", got, n)
	}
}

// What a sweep removes, it remembers: the floor of each pair is written before
// the records go, so a restart does not give a number again.
func TestSweepKeepsNumbersFromBeingGivenAgain(t *testing.T) {
	r := newRig(t)
	for i := 1; i <= 3; i++ {
		o := srcOutFinished(i)
		o.Envelope.Seq[ep(2)] = uint64(10 * i)
		seed(t, r.mem, o)
	}
	sc := r.scan()
	plan := store.PlanSweep(sc, base.Add(farAway))
	if len(plan.Records) != 3 {
		t.Fatalf("the sweep takes %d records, want 3", len(plan.Records))
	}
	book := store.NewSeqBook(sc)
	mustDo(t, r.st.ArchiveSweep(store.SweepChunk{Now: base.Add(farAway), Summaries: plan.Summaries, Records: plan.Records, Bodies: plan.Bodies, Floors: book.FloorWrites(plan.Outs)}))
	after := openRig(t, r.mem.CrashCloneWorst())
	sc = after.scan()
	if len(sc.Out) != 0 {
		t.Fatalf("%d source records are left", len(sc.Out))
	}
	if got := store.NewSeqBook(sc).Next(ep(1), addr(2, 2)); got != 31 {
		t.Errorf("Next after the sweep = %d, want 31: the highest number used was 30", got)
	}
}

func TestFloorWritesPruneTargetsThatCannotReceiveAndNeverLowerAFloor(t *testing.T) {
	r := newRig(t)
	gone, kept := addr(2, 7), addr(2, 8)
	seed(t, r.mem, store.SeqRecord{Sender: ep(1), Floors: map[ident.Address]uint64{gone: 5, kept: 9}})
	mustDo(t, r.st.ArchiveSweep(store.SweepChunk{
		Now:        base,
		Floors:     []store.FloorWrite{{Sender: ep(1), Floors: map[ident.Address]uint64{kept: 4, addr(2, 9): 2}}},
		KeepTarget: func(a ident.Address) bool { return a != gone },
	}))
	sc := r.scan()
	if len(sc.Seq) != 1 {
		t.Fatalf("%d floor files", len(sc.Seq))
	}
	f := sc.Seq[0].Floors
	if _, ok := f[gone]; ok {
		t.Errorf("a target that cannot receive any more kept its floor")
	}
	if f[kept] != 9 {
		t.Errorf("a floor was lowered from 9 to %d", f[kept])
	}
	if f[addr(2, 9)] != 2 {
		t.Errorf("a new floor was not written: %v", f)
	}
}

// Quarantining source records raises the floors by one per record whose number
// is lost, above everything that is still known; two records of one pair give
// two gaps, not a number that grows with the square of their count.
func TestQuarantineRaisesTheFloorByTheNumberOfLostRecords(t *testing.T) {
	pair := addr(2, 2)
	r := newRig(t)
	for i := 1; i <= 6; i++ {
		seed(t, r.mem, srcOut(i, false))
	}
	damageBody(t, r.mem, "state/out/"+srcOut(3, false).Name())
	damageBody(t, r.mem, "state/out/"+srcOut(5, false).Name())
	s := newSim(t, r.st)
	s.recoverAll()
	sc := r.scan()
	if got := floorOf(t, sc, 1, pair); got != 8 {
		t.Errorf("floor = %d, want 8: the others go up to 6 and two numbers are lost", got)
	}
	if got := store.NewSeqBook(sc).Next(ep(1), pair); got != 9 {
		t.Errorf("Next = %d, want 9", got)
	}
}

// A floor file that is damaged is quarantined like any other file of its
// kind, and what it said is lost: the next number follows the records that are
// left. That is the risk the design accepts for a damaged floor file, and the
// owner has an alert to see it by.
func TestDamagedFloorFileIsQuarantinedAndTheNextNumberFollowsTheRecords(t *testing.T) {
	r := newRig(t)
	pair := addr(2, 2)
	seed(t, r.mem, withSeq(1, 3, false), store.SeqRecord{Sender: ep(1), Floors: map[ident.Address]uint64{pair: 40}})
	name := store.SeqRecord{Sender: ep(1)}.Name()
	damageBody(t, r.mem, "state/seq/"+name)
	newSim(t, r.st).recoverAll()
	sc := r.scan()
	if len(sc.Seq) != 0 || !hasCorrupt(sc, store.SeqDir, name, alertFor(store.SeqDir, name)) {
		t.Fatalf("the damaged floor file was not quarantined: %v %v", sc.Seq, sc.Corrupt)
	}
	if got := store.NewSeqBook(sc).Next(ep(1), pair); got != 4 {
		t.Errorf("Next = %d, want 4", got)
	}
}
