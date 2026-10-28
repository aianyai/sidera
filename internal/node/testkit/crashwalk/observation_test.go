package crashwalk_test

import (
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
)

func TestNewHitConvertsObservationOperandsToStableFingerprints(t *testing.T) {
	for _, test := range []struct {
		name                           string
		step                           fsx.Step
		path, from, wantPath, wantFrom string
	}{
		{"temporary creation names its future file", fsx.StepCreate, "state/out", "", "state/out/.tmp-*", ""},
		{"root temporary creation", fsx.StepCreate, ".", "", ".tmp-*", ""},
		{"temporary write", fsx.StepWrite, "state/out/.tmp-1234", "", "state/out/.tmp-*", ""},
		{"file rename", fsx.StepRename, "state/out/record", "state/out/.tmp-1234", "state/out/record", "state/out/.tmp-*"},
		{"directory rename", fsx.StepRenameDir, "config/.trash-1234", "config/old", "config/.trash-*", "config/old"},
		{"directory within discarded parent", fsx.StepRemoveAll, "config/.trash-1234/old", "", "config/.trash-*/old", ""},
		{"root directory step", fsx.StepSyncDir, ".", "", ".", ""},
		{"logical commit", fsx.StepCommit, "", "", "", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			tag := fsx.Tag{Tx: "observed", Role: "file"}
			if test.step == fsx.StepCommit {
				tag.Role = ""
			}
			p := fsx.Point{Key: fsx.Key{Tag: tag, Step: test.step}, Hit: 7}
			o := fsx.Observation{Point: p, Path: test.path, From: test.from}
			got := crashwalk.NewHit(3, o, true)
			want := crashwalk.Hit{Gen: 3, Point: p, Path: test.wantPath, From: test.wantFrom, Setup: true}
			if got != want {
				t.Fatalf("hit = %+v, want %+v", got, want)
			}
		})
	}
}

func TestCompareRunsRejectsAChangedRenameSource(t *testing.T) {
	for _, step := range []fsx.Step{fsx.StepRename, fsx.StepRenameDir} {
		t.Run(step.String(), func(t *testing.T) {
			p := fsx.Point{Key: fsx.Key{Tag: fsx.Tag{Tx: "observed", Role: "file"}, Step: step}, Hit: 1}
			recorded := []crashwalk.Hit{{Gen: 1, Point: p, Path: "to", From: "first-source"}}
			rerun := append([]crashwalk.Hit(nil), recorded...)
			target := crashwalk.Target{Point: p}
			if err := crashwalk.CompareRuns(recorded, rerun, target); err != nil {
				t.Fatalf("same operands diverged: %v", err)
			}
			rerun[0].From = "second-source"
			err := crashwalk.CompareRuns(recorded, rerun, target)
			if err == nil || !strings.Contains(err.Error(), "first-source") || !strings.Contains(err.Error(), "second-source") {
				t.Fatalf("changed rename source not explained: %v", err)
			}
		})
	}
}
