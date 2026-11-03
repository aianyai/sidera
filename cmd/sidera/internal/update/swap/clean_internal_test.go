package swap

import (
	"errors"
	"maps"
	"slices"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
)

// errHeld stands for the failure of a removal that a process running from the
// file causes. Which error that is depends on the platform, so these tests give
// clean their own judgment of it and cover, on every platform, what Clean does
// with a file it was told is held.
var errHeld = errors.New("held by a running process")

func isTestHeld(err error) bool { return errors.Is(err, errHeld) }

func TestCleanSetsAHeldOldProgramAside(t *testing.T) {
	const program, console = "bin/sidera", "bin/sidera-adapter-console"
	remove := func(path string) fsxtest.Match {
		return fsxtest.Match{Tx: "swap", Role: "clean", Step: fsx.StepRemove, Path: path}
	}
	for _, c := range []struct {
		name   string
		files  map[string]string
		held   []string // paths whose removal fails as held
		rename *fsxtest.Match
		left   []string
		fails  bool
		want   map[string]string
	}{
		{
			name:  "a held old program is renamed so that its name is free",
			files: map[string]string{"sidera": "s", "sidera.old": "o"},
			held:  []string{program + ".old"},
			left:  []string{program + ".old-1"},
			want:  map[string]string{"sidera": "s", "sidera.old-1": "o"},
		},
		{
			name:  "the name is the first free one",
			files: map[string]string{"sidera": "s", "sidera.old": "o", "sidera.old-1": "p", "sidera.old-3": "q"},
			held:  []string{program + ".old", program + ".old-1", program + ".old-3"},
			left:  []string{program + ".old-1", program + ".old-3", program + ".old-2"},
			want:  map[string]string{"sidera": "s", "sidera.old-1": "p", "sidera.old-2": "o", "sidera.old-3": "q"},
		},
		{
			name:  "set-aside files that are free are removed, and only those of the program",
			files: map[string]string{"sidera": "s", "sidera.old-1": "p", "sidera.old-12": "q", "sidera.old-x": "x", "sidera.older": "y", "sidera-adapter-console": "c", "sidera-adapter-console.old-2": "r"},
			want:  map[string]string{"sidera": "s", "sidera.old-x": "x", "sidera.older": "y", "sidera-adapter-console": "c"},
		},
		{
			name:  "a set-aside file does not stop the next old program from being removed",
			files: map[string]string{"sidera": "s", "sidera.old": "o", "sidera.old-1": "p"},
			held:  []string{program + ".old-1"},
			left:  []string{program + ".old-1"},
			want:  map[string]string{"sidera": "s", "sidera.old-1": "p"},
		},
		{
			name:   "an old program that cannot be set aside is a failure and stays",
			files:  map[string]string{"sidera": "s", "sidera.old": "o"},
			held:   []string{program + ".old"},
			rename: &fsxtest.Match{Tx: "swap", Role: "hold", Step: fsx.StepRename, Path: program + ".old-1"},
			fails:  true,
			want:   map[string]string{"sidera": "s", "sidera.old": "o"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			mem := fsxtest.NewMem()
			tag := fsx.Tag{Tx: "setup", Role: "f"}
			if err := fsx.MakeDir(mem, tag, "bin"); err != nil {
				t.Fatal(err)
			}
			for name, content := range c.files {
				if err := fsx.Replace(mem, tag, "bin/"+name, []byte(content), fsx.ModePrivate); err != nil {
					t.Fatal(err)
				}
			}
			faulty := fsxtest.NewFaulty(t, mem)
			for _, p := range c.held {
				faulty.Fail(remove(p), errHeld)
			}
			if c.rename != nil {
				faulty.Fail(*c.rename, errors.New("injected"))
			}
			cleaned, err := clean(faulty, []string{program, console}, isTestHeld)
			if (err != nil) != c.fails {
				t.Errorf("error = %v, want failure: %v", err, c.fails)
			}
			if !slices.Equal(cleaned.Left, c.left) {
				t.Errorf("Left = %v, want %v", cleaned.Left, c.left)
			}
			got := make(map[string]string)
			entries, err := mem.ReadDir("bin")
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range entries {
				b, err := mem.ReadFile("bin/" + e.Name)
				if err != nil {
					t.Fatal(err)
				}
				got[e.Name] = string(b)
			}
			if !maps.Equal(got, c.want) {
				t.Errorf("bin = %v\nwant  %v", got, c.want)
			}
		})
	}
}
