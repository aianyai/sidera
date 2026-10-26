package store_test

import (
	"errors"
	"path"
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
	if len(plan.Outs) != 3 {
		t.Fatalf("the sweep takes %d source records, want 3", len(plan.Outs))
	}
	mustDo(t, r.st.ArchiveSweep(plan.Chunk(base.Add(farAway))))
	after := openRig(t, r.mem.CrashCloneWorst())
	sc = after.scan()
	if len(sc.Out) != 0 {
		t.Fatalf("%d source records are left", len(sc.Out))
	}
	if got := store.NewSeqBook(sc).Next(ep(1), addr(2, 2)); got != 31 {
		t.Errorf("Next after the sweep = %d, want 31: the highest number used was 30", got)
	}
}

func TestSweepFloorsPruneTargetsThatCannotReceiveAndNeverLowerAFloor(t *testing.T) {
	r := newRig(t)
	gone, kept := addr(2, 7), addr(2, 8)
	seed(t, r.mem, store.SeqRecord{Sender: ep(1), Floors: map[ident.Address]uint64{gone: 5, kept: 9}})
	toKept, toNew := srcOutFinished(1), srcOutFinished(2)
	toKept.Key.Recipients, toKept.Envelope.Seq = []ident.Address{kept}, map[ident.EndpointID]uint64{kept.Endpoint: 4}
	toNew.Key.Recipients, toNew.Envelope.Seq = []ident.Address{addr(2, 9)}, map[ident.EndpointID]uint64{ep(9): 2}
	mustDo(t, r.st.ArchiveSweep(store.SweepChunk{
		Now:        base,
		Outs:       []store.OutRecord{toKept, toNew},
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

// A floor file that is damaged when a write needs to read it is not read as
// empty and overwritten: that would lose what it held with no alert. The write
// does nothing and returns the damaged file as the scan would have listed it, so
// that the caller settles it by the usual rule and tries again; the scan lists
// the floor files first, so a startup meets none after it began to quarantine
// source records.
func TestDamagedFloorFileMetByAWriteIsReportedAndNothingIsWritten(t *testing.T) {
	pair := addr(2, 2)
	floorName := store.SeqRecord{Sender: ep(1)}.Name()
	build := func(t *testing.T) *rig {
		r := newRig(t)
		for i := 1; i <= 3; i++ {
			seed(t, r.mem, srcOutFinished(i))
		}
		seed(t, r.mem, store.SeqRecord{Sender: ep(1), Floors: map[ident.Address]uint64{pair: 40}})
		damageBody(t, r.mem, path.Join(store.SeqDir, floorName))
		return r
	}
	wantDamaged := func(t *testing.T, what string, err error) {
		t.Helper()
		var de *store.DamagedFileError
		if !errors.As(err, &de) {
			t.Fatalf("%s: error = %v, want a *DamagedFileError", what, err)
		}
		if de.File.Dir != store.SeqDir || de.File.Name != floorName || de.File.Damage.Class != store.BodyDamaged {
			t.Errorf("%s: the damaged file is %+v, want the floor file of endpoint 1 with a damaged body", what, de.File)
		}
	}

	t.Run("a sweep of source records", func(t *testing.T) {
		r := build(t)
		before := snapshot(t, r.mem, ".")
		plan := store.PlanSweep(r.scan(), base.Add(farAway))
		if len(plan.Outs) != 3 {
			t.Fatalf("the plan takes %d source records, want 3", len(plan.Outs))
		}
		wantDamaged(t, "ArchiveSweep", r.st.ArchiveSweep(plan.Chunk(base.Add(farAway))))
		if diff := equalMaps(snapshot(t, r.mem, "."), before, nil); diff != "" {
			t.Errorf("a sweep that could not read a floor file changed the disk: %s", diff)
		}
	})

	t.Run("the quarantine of a source record of the same sender", func(t *testing.T) {
		r := build(t)
		damageBody(t, r.mem, path.Join(store.OutDir, srcOutFinished(2).Name()))
		before := snapshot(t, r.mem, ".")
		sc := r.scan()
		if len(sc.Damaged) != 2 || sc.Damaged[0].Dir != store.SeqDir {
			t.Fatalf("damaged files = %v, want the floor file first, then the source record", sc.Damaged)
		}
		out := sc.Damaged[1]
		id := alertFor(out.Dir, out.Name)
		err := r.st.Quarantine(store.Quarantine{File: out, Scan: sc, Replacement: lostOutAt(out.Key.(store.OutKey), base), Alert: newSim(t, r.st).alert(id, "corrupt_record", out.Dir+"/"+out.Name)})
		wantDamaged(t, "Quarantine", err)
		if diff := equalMaps(snapshot(t, r.mem, "."), before, nil); diff != "" {
			t.Errorf("a quarantine that could not read the floor file wrote something: %s", diff)
		}
	})

	t.Run("the usual rule then lets both go on", func(t *testing.T) {
		r := build(t)
		damageBody(t, r.mem, path.Join(store.OutDir, srcOutFinished(2).Name()))
		newSim(t, r.st).recoverAll()
		sc := r.scan()
		if len(sc.Damaged) != 0 || !hasAlert(sc, alertFor(store.SeqDir, floorName), "corrupt_record") {
			t.Fatalf("the floor file was not settled with an alert: damaged %v, alerts %v", sc.Damaged, sc.Alerts)
		}
		if out, _, _ := find(t, sc, srcOutFinished(2).Name()); out == nil || out.Lost == nil {
			t.Errorf("the damaged source record was not replaced")
		}
		plan := store.PlanSweep(sc, base.Add(farAway))
		mustDo(t, r.st.ArchiveSweep(plan.Chunk(base.Add(farAway))))
	})
}

// The sweep works out the floors from the records it is given to remove, for
// every sender and every target: a chunk has no way to remove a source record
// without them, and a lost record, whose numbers are lost, adds none.
func TestArchiveSweepWritesTheFloorOfEverySenderAndTargetOfTheRecordsItRemoves(t *testing.T) {
	r := newRig(t)
	a, b := addr(2, 2), addr(2, 3)
	multi := srcOutOf(1, 1)
	multi.Key.Recipients = []ident.Address{a, b}
	multi.Outbound.Recipients = append(multi.Outbound.Recipients, multi.Outbound.Recipients[0])
	multi.Outbound.Recipients[1].Address = b
	multi.Envelope.To = multi.Key.Recipients
	multi.Envelope.Seq = map[ident.EndpointID]uint64{a.Endpoint: 7, b.Endpoint: 3}
	other := finishedOutOf(6, 2)
	other.Envelope.Seq = map[ident.EndpointID]uint64{a.Endpoint: 5}
	lost := lostOutAt(finishedOutOf(1, 3).Key, base)
	seed(t, r.mem, multi, other, lost)
	mustDo(t, r.st.ArchiveSweep(store.SweepChunk{Now: base, Outs: []store.OutRecord{multi, other, lost}}))
	sc := r.scan()
	if len(sc.Out) != 0 {
		t.Fatalf("%d source records left", len(sc.Out))
	}
	for _, c := range []struct {
		sender int
		to     ident.Address
		want   uint64
	}{{1, a, 7}, {1, b, 3}, {6, a, 5}} {
		if got := floorOf(t, sc, c.sender, c.to); got != c.want {
			t.Errorf("floor of endpoint %d for %v = %d, want %d", c.sender, c.to, got, c.want)
		}
	}
}
