package fsx_test

import (
	"reflect"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
)

func TestLeftoversFindsTemporaryFilesAndDiscardedDirectories(t *testing.T) {
	mem := fsxtest.NewMem()
	mustMkdir(t, mem, "state")
	mustMkdir(t, mem, "state/in")
	mustMkdir(t, mem, ".trash-1")
	mustMkdir(t, mem, "state/.trash-a")
	mustWrite(t, mem, "state/in/keep", "x")
	mustWrite(t, mem, ".tmp-0001", "x")
	mustWrite(t, mem, "state/.tmp-0002", "x")
	mustWrite(t, mem, "state/in/.tmp-0003", "x")
	mustWrite(t, mem, ".trash-1/inner", "x")   // inside a leftover: removed with it
	mustWrite(t, mem, "state/.tmpnot", "x")    // not a leftover: no dash
	mustWrite(t, mem, "state/in/trash-2", "x") // not a leftover: no dot
	mustWrite(t, mem, "state/in/.tmp", "x")    // not a leftover: no dash

	got, err := fsx.Leftovers(mem, ".")
	if err != nil {
		t.Fatal(err)
	}

	want := []fsx.Leftover{
		{Path: ".tmp-0001"},
		{Path: ".trash-1", IsDir: true},
		{Path: "state/.tmp-0002"},
		{Path: "state/.trash-a", IsDir: true},
		{Path: "state/in/.tmp-0003"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Leftovers = %v\nwant       %v", got, want)
	}
}

func TestLeftoversOfAMissingDirectoryIsAnError(t *testing.T) {
	if _, err := fsx.Leftovers(fsxtest.NewMem(), "nope"); err == nil {
		t.Error("Leftovers of a missing directory succeeded")
	}
}

func TestIsLeftover(t *testing.T) {
	for name, want := range map[string]bool{
		".tmp-abc": true, ".trash-1": true, ".tmp-": true,
		"tmp-abc": false, ".tmp": false, ".trash": false, "x.tmp-1": false, "": false,
	} {
		if got := fsx.IsLeftover(name); got != want {
			t.Errorf("IsLeftover(%q) = %v, want %v", name, got, want)
		}
	}
}
