package fsx_test

import (
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
)

func TestObservationCarriesThePrimitiveOperandsBeforeItRuns(t *testing.T) {
	for _, test := range []struct {
		step       fsx.Step
		path, from string
		run        func(fsx.FS) error
	}{
		{fsx.StepCreate, "d", "", func(fs fsx.FS) error { _, err := fs.CreateTemp(tx, "d", fsx.ModePrivate); return err }},
		{fsx.StepOpen, "d/log", "", func(fs fsx.FS) error { _, err := fs.OpenAppend(tx, "d/log"); return err }},
		{fsx.StepRename, "d/to", "d/from", func(fs fsx.FS) error { return fs.Rename(tx, "d/from", "d/to") }},
		{fsx.StepRenameDir, "to", "from", func(fs fsx.FS) error { return fs.RenameDir(tx, "from", "to") }},
		{fsx.StepSyncDir, ".", "", func(fs fsx.FS) error { return fs.SyncDir(tx, ".") }},
		{fsx.StepRemove, "d/file", "", func(fs fsx.FS) error { return fs.Remove(tx, "d/file") }},
		{fsx.StepRemoveAll, "d", "", func(fs fsx.FS) error { return fs.RemoveAll(tx, "d") }},
		{fsx.StepMkdir, "d", "", func(fs fsx.FS) error { return fs.Mkdir(tx, "d") }},
	} {
		t.Run(test.step.String(), func(t *testing.T) {
			stop := errors.New("stop before primitive")
			var got []fsx.Observation
			// A delegated primitive would panic: the hook must prevent every effect.
			observed := fsx.Observe(nil, new(fsx.Counter), func(o fsx.Observation) error {
				got = append(got, o)
				return stop
			})
			err := test.run(observed)
			if !errors.Is(err, stop) {
				t.Fatalf("error = %v, want hook error", err)
			}
			want := []fsx.Observation{{Point: fsx.Point{Key: fsx.Key{Tag: tx, Step: test.step}, Hit: 1}, Path: test.path, From: test.from}}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("observations = %+v, want %+v", got, want)
			}
			point, ok := fsx.PointOf(err)
			if !ok || point != want[0].Point {
				t.Fatalf("error point = %+v, %v", point, ok)
			}
		})
	}
}

func TestObservationOfFileStepsKeepsTheOpenedName(t *testing.T) {
	for _, create := range []bool{false, true} {
		t.Run(map[bool]string{false: "append", true: "temporary"}[create], func(t *testing.T) {
			mem := fsxtest.NewMem()
			var got []fsx.Observation
			stop := errors.New("stop file step")
			blocked := false
			observed := fsx.Observe(mem, new(fsx.Counter), func(o fsx.Observation) error {
				got = append(got, o)
				if blocked {
					return stop
				}
				return nil
			})
			var f fsx.File
			var err error
			if create {
				f, err = observed.CreateTemp(tx, ".", fsx.ModePrivate)
			} else {
				f, err = observed.OpenAppend(tx, "original")
			}
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			opened := f.Name()
			if err := mem.Rename(tx, opened, "moved"); err != nil {
				t.Fatal(err)
			}
			if _, err := f.Write([]byte("kept")); err != nil {
				t.Fatal(err)
			}
			if err := f.Sync(); err != nil {
				t.Fatal(err)
			}
			if err := mem.SyncDir(tx, "."); err != nil {
				t.Fatal(err)
			}
			if _, err := f.Write([]byte("pending")); err != nil {
				t.Fatal(err)
			}
			blocked = true
			if n, err := f.Write([]byte("blocked")); n != 0 || !errors.Is(err, stop) {
				t.Fatalf("blocked write = %d, %v", n, err)
			}
			if err := f.Sync(); !errors.Is(err, stop) {
				t.Fatalf("blocked sync = %v", err)
			}
			if data, err := mem.ReadFile("moved"); err != nil || string(data) != "keptpending" {
				t.Fatalf("file = %q, %v", data, err)
			}
			if data, err := mem.CrashCloneWorst().ReadFile("moved"); err != nil || string(data) != "kept" {
				t.Fatalf("blocked sync made pending data durable: %q, %v", data, err)
			}
			for _, o := range got[1:] {
				if o.Path != opened || o.From != "" {
					t.Fatalf("file observation = %+v, want opened path %q", o, opened)
				}
			}
			if create && got[0].Path != "." {
				t.Fatalf("create path = %q, want parent directory", got[0].Path)
			}
		})
	}
}

func TestConcurrentObservationsKeepEachPointWithItsOwnOperands(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	var got []fsx.Observation
	observed := fsx.Observe(fsxtest.NewMem(), new(fsx.Counter), func(o fsx.Observation) error {
		if o.Point.Hit == 1 {
			close(entered)
			<-release
		}
		mu.Lock()
		got = append(got, o)
		mu.Unlock()
		return nil
	})
	done := make(chan error, 1)
	go func() { done <- observed.Mkdir(tx, "first") }()
	<-entered
	if err := observed.Mkdir(tx, "second"); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Point.Hit != 2 || got[0].Path != "second" || got[1].Point.Hit != 1 || got[1].Path != "first" {
		t.Fatalf("interleaved observations = %+v", got)
	}
}
