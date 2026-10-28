package txn_test

import (
	"reflect"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/txn"
)

type observationFunc func(fsx.Observation, txn.Kind)

func (f observationFunc) Reached(o fsx.Observation, k txn.Kind) { f(o, k) }

func TestOnlyLogicalCommitObservationsHaveNoPath(t *testing.T) {
	decl := &txn.Decl{Name: "observed", Kind: txn.Recovery, Writes: []txn.Write{{Role: "file", Op: txn.OpReplace}}}
	var observations []fsx.Observation
	var kinds []txn.Kind
	w := txn.New(fsxtest.NewMem(), table(t, decl), observationFunc(func(o fsx.Observation, k txn.Kind) {
		observations = append(observations, o)
		kinds = append(kinds, k)
	}))
	if err := w.Recover(); err != nil {
		t.Fatal(err)
	}
	tx := w.Begin(decl)
	defer tx.Close()
	if err := tx.Replace("file", "record", []byte("body"), fsx.ModePrivate); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var replacement []fsx.Observation
	for i, o := range observations {
		if o.Point.Step == fsx.StepCommit {
			if o.Path != "" || o.From != "" {
				t.Errorf("logical commit has operands: %+v", o)
			}
		} else if o.Path == "" {
			t.Errorf("file operation has no path: %+v", o)
		}
		if o.Point.Tag.Tx == decl.Name {
			if kinds[i] != txn.Recovery {
				t.Errorf("kind = %v, want Recovery", kinds[i])
			}
			replacement = append(replacement, o)
		}
	}
	if len(replacement) != 6 {
		t.Fatalf("replacement observations = %+v", replacement)
	}
	temporary := replacement[1].Path
	wantPaths := []string{".", temporary, temporary, "record", ".", ""}
	var paths []string
	for _, o := range replacement {
		paths = append(paths, o.Path)
	}
	if !reflect.DeepEqual(paths, wantPaths) || replacement[3].From != temporary {
		t.Fatalf("replacement observations = %+v, want paths %v and rename from %q", replacement, wantPaths, temporary)
	}
}
