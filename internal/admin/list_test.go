package admin

import (
	"reflect"
	"testing"
	"time"
)

func TestNewList(t *testing.T) {
	a, b := person(t, 1), person(t, 2)
	tests := []struct {
		name    string
		entries []Entry
		want    []ListProblem
	}{
		{"empty list is valid", nil, nil},
		{"view, manage and full control are valid levels", []Entry{
			{Person: a, Level: LevelView}, {Person: b, Level: LevelFullControl, ExpiresAt: t0},
		}, nil},
		{"the same person twice is rejected at the later entry", []Entry{
			{Person: a, Level: LevelView}, {Person: b, Level: LevelView}, {Person: a, Level: LevelManage},
		}, []ListProblem{{Index: 2, Reason: ProblemDuplicatePerson}}},
		{"level none is not a valid entry level", []Entry{
			{Person: a, Level: LevelNone},
		}, []ListProblem{{Index: 0, Reason: ProblemUnknownLevel}}},
		{"a level above full control is unknown", []Entry{
			{Person: a, Level: LevelFullControl + 1},
		}, []ListProblem{{Index: 0, Reason: ProblemUnknownLevel}}},
		{"a negative level is unknown", []Entry{
			{Person: a, Level: -1},
		}, []ListProblem{{Index: 0, Reason: ProblemUnknownLevel}}},
		{"the zero person ID is rejected", []Entry{
			{Level: LevelView},
		}, []ListProblem{{Index: 0, Reason: ProblemZeroPerson}}},
		{"every problem is reported, in input order", []Entry{
			{Person: a, Level: 9}, {Person: a, Level: LevelView}, {Level: LevelNone},
		}, []ListProblem{
			{Index: 0, Reason: ProblemUnknownLevel},
			{Index: 1, Reason: ProblemDuplicatePerson},
			{Index: 2, Reason: ProblemZeroPerson},
			{Index: 2, Reason: ProblemUnknownLevel},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l, got := NewList(tc.entries)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("problems = %v, want %v", got, tc.want)
			}
			if len(tc.want) > 0 && len(l.Entries()) != 0 {
				t.Fatalf("an invalid list must come back empty, got %v", l.Entries())
			}
			if len(tc.want) == 0 && len(l.Entries()) != len(tc.entries) {
				t.Fatalf("entries = %v, want %v", l.Entries(), tc.entries)
			}
		})
	}
}

func TestListIsIsolatedFromCallerSlices(t *testing.T) {
	a := person(t, 1)
	in := []Entry{{Person: a, Level: LevelView}}
	l := mustList(t, in...)
	in[0].Level = LevelFullControl
	l.Entries()[0].Level = LevelFullControl
	if e, _ := l.Lookup(a); e.Level != LevelView {
		t.Fatalf("list changed through a caller's slice: %v", e.Level)
	}
}

func TestEntryExpiresAtTheInstant(t *testing.T) {
	e := Entry{Person: person(t, 1), Level: LevelView, ExpiresAt: t0}
	tests := []struct {
		name string
		at   time.Time
		want bool
	}{
		{"before the expiry the entry grants rights", t0.Add(-time.Nanosecond), false},
		{"at the expiry instant the entry is expired", t0, true},
		{"after the expiry the entry is expired", t0.Add(time.Second), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := e.expiredAt(tc.at); got != tc.want {
				t.Fatalf("expiredAt = %v, want %v", got, tc.want)
			}
		})
	}
	if (Entry{}).expiredAt(t0.Add(1000 * time.Hour)) {
		t.Fatal("an entry without an expiry must never expire")
	}
}
