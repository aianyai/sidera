package fsx_test

import (
	"errors"
	"io/fs"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
)

func TestObserveNumbersEveryWriteStepPerKey(t *testing.T) {
	r := newRecorded()
	a := fsx.Tag{Tx: "intake", Role: "body"}
	b := fsx.Tag{Tx: "intake", Role: "out"}
	for _, tag := range []fsx.Tag{a, b, a} {
		if err := fsx.Replace(r.fs, tag, "rec", []byte("x"), fsx.ModePrivate); err != nil {
			t.Fatal(err)
		}
	}
	assertSteps(t, r.take(), []string{
		"intake/body.create#1", "intake/body.write#1", "intake/body.sync#1", "intake/body.rename#1", "intake/body.sync-dir#1",
		"intake/out.create#1", "intake/out.write#1", "intake/out.sync#1", "intake/out.rename#1", "intake/out.sync-dir#1",
		"intake/body.create#2", "intake/body.write#2", "intake/body.sync#2", "intake/body.rename#2", "intake/body.sync-dir#2",
	})
}

func TestObserveAttributesFileStepsToTheTagTheFileWasOpenedWith(t *testing.T) {
	r := newRecorded()
	opened := fsx.Tag{Tx: "archive", Role: "events"}
	f, err := r.fs.OpenAppend(opened, "log")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("a")); err != nil {
		t.Fatal(err)
	}
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil { // not a step
		t.Fatal(err)
	}
	assertSteps(t, r.take(), []string{"archive/events.open#1", "archive/events.write#1", "archive/events.sync#1"})
}

func TestObserveDoesNotCountReadsAndLocks(t *testing.T) {
	r := newRecorded()
	mustWrite(t, r.mem, "f", "x")
	if _, err := r.fs.ReadFile("f"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.fs.ReadDir("."); err != nil {
		t.Fatal(err)
	}
	if _, err := r.fs.Stat("f"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.fs.Available("."); err != nil {
		t.Fatal(err)
	}
	unlock, err := r.fs.Lock("daemon.lock")
	if err != nil {
		t.Fatal(err)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	assertSteps(t, r.take(), nil)
}

func TestObserveCallsTheHookBeforeTheStepAndASkipsItOnError(t *testing.T) {
	mem := fsxtest.NewMem()
	var seenBefore []bool
	hookErr := errors.New("stop here")
	observed := fsx.Observe(mem, new(fsx.Counter), func(p fsx.Point) error {
		_, err := mem.Stat("rec")
		seenBefore = append(seenBefore, err == nil)
		if p.Step == fsx.StepRename {
			return hookErr
		}
		return nil
	})

	err := fsx.Replace(observed, tx, "rec", []byte("x"), fsx.ModePrivate)

	if !errors.Is(err, hookErr) {
		t.Fatalf("error = %v, want the hook's error", err)
	}
	var fe *fsx.Error
	if !errors.As(err, &fe) || fe.Point.Step != fsx.StepRename || fe.Point.Hit != 1 {
		t.Fatalf("error point = %+v, want the first rename", fe)
	}
	if _, err := mem.Stat("rec"); !errors.Is(err, fs.ErrNotExist) {
		t.Error("the rename ran although the hook refused it")
	}
	for i, existed := range seenBefore {
		if existed {
			t.Errorf("hook call %d saw the target already present: it ran after the step", i+1)
		}
	}
}

func TestObservedErrorsCarryTheirPoint(t *testing.T) {
	mem := fsxtest.NewMem()
	observed := fsx.Observe(mem, new(fsx.Counter), nil)
	if err := observed.Mkdir(tx, "d"); err != nil {
		t.Fatal(err)
	}
	err := observed.Mkdir(tx, "d") // exists: the second mkdir of this key fails
	p, ok := fsx.PointOf(err)
	if !ok || p.String() != "tx/f.mkdir#2" {
		t.Fatalf("PointOf = %v, %v; want tx/f.mkdir#2", p, ok)
	}
	if !errors.Is(err, fs.ErrExist) {
		t.Errorf("error %v does not wrap fs.ErrExist", err)
	}
	if _, ok := fsx.PointOf(errors.New("plain")); ok {
		t.Error("PointOf found a point in a plain error")
	}
	// The composite error reports the ordinal the observer gave.
	var fe *fsx.Error
	if !errors.As(fsx.MakeDir(observed, tx, "d"), &fe) || fe.Point.Hit != 3 {
		t.Errorf("composite error point = %+v, want ordinal 3", fe)
	}
}

func TestObserveSkipsReservedTags(t *testing.T) {
	r := newRecorded()
	for _, tag := range []fsx.Tag{{Tx: fsx.TxLog}, fsx.Tag{Tx: fsx.TxCleanup}} {
		f, err := r.fs.OpenAppend(tag, "x")
		if err != nil {
			t.Fatal(err)
		}
		f.Write([]byte("a"))
		f.Sync()
		f.Close()
		r.fs.Remove(tag, "x")
	}
	assertSteps(t, r.take(), nil)
	if !(fsx.Tag{Tx: "log"}).Reserved() || !(fsx.Tag{Tx: fsx.TxCleanup}).Reserved() || (fsx.Tag{Tx: "intake", Role: "log"}).Reserved() {
		t.Error("Reserved is wrong")
	}
}
