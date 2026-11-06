package adaptersetup_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/aianyai/sidera/sdk/go/adaptersetup"
)

// One classification for every adapter: what is intact is left alone, what is only out
// of date is stale (a refresh rewrites it), what was edited or removed is drift that
// refresh leaves, and what no record holds needs setup.
func TestReconcile(t *testing.T) {
	d := func(s string) string { return adaptersetup.Digest([]byte(s)) }
	rec := &adaptersetup.Record{}
	for _, id := range []string{"current", "stale", "edited", "gone"} {
		rec.Set(adaptersetup.RecordItem{ID: id, Kind: "file", Target: "/t/" + id, Digest: d(id + "-v1")})
	}
	now := map[string]string{"/t/current": d("current-v1"), "/t/stale": d("stale-v1"), "/t/edited": d("mine")} // /t/gone is not there
	probe := func(r adaptersetup.RecordItem) (string, bool, error) {
		digest, ok := now[r.Target]
		return digest, ok, nil
	}
	wanted := []adaptersetup.Wanted{
		{ID: "current", Kind: "file", Target: "/t/current", Digest: d("current-v1")},
		{ID: "stale", Kind: "file", Target: "/t/stale", Digest: d("stale-v2")},
		{ID: "edited", Kind: "file", Target: "/t/edited", Digest: d("edited-v2")},
		{ID: "gone", Kind: "file", Target: "/t/gone", Digest: d("gone-v1")},
		{ID: "new", Kind: "file", Target: "/t/new", Digest: d("new-v1")},
	}
	got, err := adaptersetup.Reconcile(rec, wanted, probe)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Stale) != 1 || got.Stale[0].ID != "stale" {
		t.Errorf("Stale = %+v", got.Stale)
	}
	problems := map[string]adaptersetup.Problem{}
	for _, dr := range got.Drift {
		problems[dr.ID] = dr.Problem
	}
	want := map[string]adaptersetup.Problem{"edited": adaptersetup.ProblemChanged, "gone": adaptersetup.ProblemMissing, "new": adaptersetup.ProblemNeedsSetup}
	if fmt.Sprint(problems) != fmt.Sprint(want) {
		t.Errorf("Drift = %v, want %v", problems, want)
	}
	if all := got.CheckDrift(); len(all) != 4 || all[3].ID != "stale" || all[3].Problem != adaptersetup.ProblemStale || all[3].Target != "/t/stale" {
		t.Errorf("CheckDrift = %+v", all)
	}
	if len(got.Drift) != 3 {
		t.Error("CheckDrift changed Drift")
	}

	// Without a record every wanted item needs setup, and nothing is probed.
	none, err := adaptersetup.Reconcile(nil, wanted, func(adaptersetup.RecordItem) (string, bool, error) {
		t.Error("probed an item that no record holds")
		return "", false, nil
	})
	if err != nil || len(none.Stale) != 0 || len(none.Drift) != len(wanted) || none.Drift[0].Problem != adaptersetup.ProblemNeedsSetup {
		t.Errorf("no record: %+v, %v", none, err)
	}

	// A failure to look is an error and not a difference.
	boom := errors.New("cannot read")
	if _, err := adaptersetup.Reconcile(rec, wanted, func(adaptersetup.RecordItem) (string, bool, error) { return "", false, boom }); !errors.Is(err, boom) {
		t.Errorf("probe error = %v", err)
	}
}

// An item the person declined is not drift: a check and a refresh that kept reporting
// it would show every update of a person who chose not to have it a "run setup" hint.
// It is still listed in Declined, for the plan a person asks for by hand. A recorded
// item is never treated as declined, and an item no one declined is still reported.
func TestReconcileLeavesWhatWasDeclinedOutOfDrift(t *testing.T) {
	d := adaptersetup.Digest([]byte("v1"))
	rec := &adaptersetup.Record{Declined: []string{"no", "kept"}}
	rec.Set(adaptersetup.RecordItem{ID: "kept", Kind: "file", Target: "/t/kept", Digest: d})
	wanted := []adaptersetup.Wanted{
		{ID: "no", Kind: "file", Target: "/t/no", Digest: d},
		{ID: "kept", Kind: "file", Target: "/t/kept", Digest: d},
		{ID: "new", Kind: "file", Target: "/t/new", Digest: d},
	}
	got, err := adaptersetup.Reconcile(rec, wanted, func(adaptersetup.RecordItem) (string, bool, error) { return d, true, nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Declined) != 1 || got.Declined[0].ID != "no" {
		t.Errorf("Declined = %+v", got.Declined)
	}
	if all := got.CheckDrift(); len(all) != 1 || all[0].ID != "new" || all[0].Problem != adaptersetup.ProblemNeedsSetup {
		t.Errorf("CheckDrift = %+v, want only the item no one declined", all)
	}
}
