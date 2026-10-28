package harness

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/txn"
)

func TestStartupAttemptCutsOffBeforeSnapshotAndKeepsOldCallbacksInTheirGeneration(t *testing.T) {
	disk := fsxtest.NewMem()
	if err := fsx.Replace(disk, setupTag, "kept", []byte("before cutoff"), fsx.ModePrivate); err != nil {
		t.Fatal(err)
	}
	p, err := fsx.ParsePoint("init/commit#1")
	if err != nil {
		t.Fatal(err)
	}
	r, err := newWalkRun(context.Background(), crashwalk.RunPlan{Targets: []crashwalk.Target{{Gen: 1, Point: p}}}, false)
	if err != nil {
		t.Fatal(err)
	}
	old := r.begin(nil)
	old.openWindow()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := &startupAttempt{generation: old, disk: disk, cut: newCutoff(disk), cancel: cancel}
	a.Reached(fsx.Observation{Point: p}, txn.Normal)
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("startup waits were not canceled")
	}
	if _, err := a.cut.ReadFile("kept"); !errors.Is(err, errCutOff) {
		t.Fatalf("old disk still accessible: %v", err)
	}
	snapshot := a.snapshot()
	if snapshot == nil {
		t.Fatal("the target made no crash snapshot")
	}
	r.finishGeneration()
	next := r.begin(nil)
	next.openWindow()
	before := old.recording()
	a.Reached(fsx.Observation{Point: fsx.Point{Key: fsx.Key{Tag: fsx.Tag{Tx: "init", Role: "late"}, Step: fsx.StepWrite}, Hit: 1}, Path: "late"}, txn.Normal)
	if !reflect.DeepEqual(old.recording(), before) || len(next.recording()) != 0 {
		t.Fatal("an old callback entered a closed or new generation")
	}
	if err := fsx.Replace(a.cut, setupTag, "kept", []byte("late write"), fsx.ModePrivate); err == nil {
		t.Fatal("the old process wrote after its snapshot")
	}
	if got, err := snapshot.ReadFile("kept"); err != nil || string(got) != "before cutoff" {
		t.Fatalf("crash snapshot changed: %q, %v", got, err)
	}
}
