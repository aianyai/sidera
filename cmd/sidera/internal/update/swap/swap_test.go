package swap_test

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/aianyai/sidera/cmd/sidera/internal/update/swap"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
)

const (
	program = "bin/sidera"
	console = "bin/sidera-adapter-console"
)

var setup = fsx.Tag{Tx: "setup", Role: "f"}

func put(t *testing.T, fsys fsx.FS, p, content string) {
	t.Helper()
	if err := fsx.Replace(fsys, setup, p, []byte(content), fsx.ModePrivate); err != nil {
		t.Fatalf("setup %s: %v", p, err)
	}
}

// installation is the directory of the programs, with the two programs of a
// release installed, and the directory the new ones were unpacked into.
func installation(t *testing.T) (dst *fsxtest.Mem, src *fsxtest.Mem, items []swap.Item) {
	t.Helper()
	dst, src = fsxtest.NewMem(), fsxtest.NewMem()
	for _, fsys := range []fsx.FS{dst, src} {
		if err := fsx.MakeDir(fsys, setup, "bin"); err != nil {
			t.Fatal(err)
		}
	}
	if err := fsx.MakeDir(src, setup, "cache"); err != nil {
		t.Fatal(err)
	}
	put(t, dst, program, "old sidera")
	put(t, dst, console, "old console")
	put(t, src, "cache/sidera", "new sidera")
	put(t, src, "cache/console", "new console")
	return dst, src, []swap.Item{{Target: program, Source: "cache/sidera"}, {Target: console, Source: "cache/console"}}
}

// contents lists the files of bin with their contents.
func contents(t *testing.T, fsys fsx.Reader) map[string]string {
	t.Helper()
	entries, err := fsys.ReadDir("bin")
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]string)
	for _, e := range entries {
		b, err := fsys.ReadFile("bin/" + e.Name)
		if err != nil {
			t.Fatal(err)
		}
		got[e.Name] = string(b)
	}
	return got
}

// tempName matches the random part of the temporary name a new file is written under.
var tempName = regexp.MustCompile(`\.tmp-[0-9a-f]+`)

var (
	allOld = map[string]string{"sidera": "old sidera", "sidera-adapter-console": "old console"}
	allNew = map[string]string{
		"sidera": "new sidera", "sidera.old": "old sidera",
		"sidera-adapter-console": "new console", "sidera-adapter-console.old": "old console",
	}
)

func assertContents(t *testing.T, fsys fsx.Reader, want map[string]string) {
	t.Helper()
	if got := contents(t, fsys); !maps.Equal(got, want) {
		t.Errorf("bin = %v\nwant  %v", got, want)
	}
}

func commit(t *testing.T, dst fsx.FS, src fsx.Reader, items []swap.Item) error {
	t.Helper()
	s, err := swap.Prepare(dst, src, items)
	if err != nil {
		return err
	}
	return s.Commit()
}

// The programs are set aside before the new ones are renamed in, one target at
// a time, and the new files are complete and durable before the first rename.
func TestCommitRunsItsStepsInOrder(t *testing.T) {
	dst, src, items := installation(t)
	// Only the steps this package decides are pinned: the create, write and sync
	// of each new file belong to fsx.ReplaceDeferred.
	owned := []string{"swap/new.rename", "swap/new.sync-dir", "swap/retire.rename", "swap/install.rename", "swap/install.sync-dir"}
	var steps []string
	observed := fsx.Observe(dst, new(fsx.Counter), func(o fsx.Observation) error {
		if key, _, _ := strings.Cut(o.Point.String(), "#"); slices.Contains(owned, key) {
			step := fmt.Sprintf("%v %s<-%s", o.Point, o.Path, o.From)
			steps = append(steps, tempName.ReplaceAllString(step, ".tmp-N"))
		}
		return nil
	})
	if err := commit(t, observed, src, items); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"swap/new.rename#1 bin/sidera.new<-bin/.tmp-N",
		"swap/new.rename#2 bin/sidera-adapter-console.new<-bin/.tmp-N",
		"swap/new.sync-dir#1 bin<-",
		"swap/retire.rename#1 bin/sidera.old<-bin/sidera", "swap/install.rename#1 bin/sidera<-bin/sidera.new",
		"swap/retire.rename#2 bin/sidera-adapter-console.old<-bin/sidera-adapter-console", "swap/install.rename#2 bin/sidera-adapter-console<-bin/sidera-adapter-console.new",
		"swap/install.sync-dir#1 bin<-",
	}
	if !slices.Equal(steps, want) {
		t.Errorf("steps:\n%s\nwant:\n%s", strings.Join(steps, "\n"), strings.Join(want, "\n"))
	}
	assertContents(t, dst, allNew)
}

// Every failing step, of every file, leaves the set as it was: all old, no
// .new, no .old, no temporary file.
func TestAnyFailedStepLeavesEverythingOld(t *testing.T) {
	var points []fsx.Observation
	{
		dst, src, items := installation(t)
		observed := fsx.Observe(dst, new(fsx.Counter), func(o fsx.Observation) error {
			points = append(points, o)
			return nil
		})
		if err := commit(t, observed, src, items); err != nil {
			t.Fatal(err)
		}
	}
	if len(points) != 14 {
		t.Fatalf("recorded %d steps, want 14", len(points))
	}
	for _, o := range points {
		t.Run(o.Point.String(), func(t *testing.T) {
			dst, src, items := installation(t)
			faulty := fsxtest.NewFaulty(t, dst)
			injected := errors.New("injected")
			faulty.Fail(fsxtest.Match{Tx: o.Point.Tag.Tx, Role: o.Point.Tag.Role, Step: o.Point.Step, Hit: o.Point.Hit}, injected)
			err := commit(t, faulty, src, items)
			if !errors.Is(err, injected) {
				t.Fatalf("error = %v, want the injected one", err)
			}
			var rollback *swap.RollbackError
			if errors.As(err, &rollback) {
				t.Errorf("rollback failed without a fault in it: %v", err)
			}
			assertContents(t, dst, allOld)
		})
	}
}

func TestMissingSourceLeavesEverythingOld(t *testing.T) {
	dst, src, items := installation(t)
	items[1].Source = "cache/absent"
	err := commit(t, dst, src, items)
	if err == nil || !strings.Contains(err.Error(), "cache/absent") {
		t.Fatalf("error = %v, want one naming cache/absent", err)
	}
	assertContents(t, dst, allOld)
}

// When a program cannot be put back, its previous version is still in its .old
// file and the error says so.
func TestFailedRollbackNamesWhereThePreviousProgramIs(t *testing.T) {
	injected := errors.New("injected")
	restore := func(t string) fsxtest.Match {
		return fsxtest.Match{Tx: "swap", Role: "restore", Step: fsx.StepRename, Path: t}
	}
	for _, c := range []struct {
		name string
		// The first fault makes the swap fail; the others fail the restoring.
		faults []fsxtest.Match
		held   []string
		want   map[string]string
	}{
		{
			name: "a later target fails to install and an installed one is not put back",
			faults: []fsxtest.Match{
				{Tx: "swap", Role: "install", Step: fsx.StepRename, Hit: 2},
				restore(program),
			},
			held: []string{program + ".old"},
			want: map[string]string{"sidera": "new sidera", "sidera.old": "old sidera", "sidera-adapter-console": "old console"},
		},
		{
			name: "the target that was only set aside is not put back",
			faults: []fsxtest.Match{
				{Tx: "swap", Role: "install", Step: fsx.StepRename, Hit: 1},
				restore(program),
			},
			held: []string{program + ".old"},
			want: map[string]string{"sidera.old": "old sidera", "sidera-adapter-console": "old console"},
		},
		{
			name: "no target is put back",
			faults: []fsxtest.Match{
				{Tx: "swap", Role: "install", Step: fsx.StepRename, Hit: 2},
				restore(program), restore(console),
			},
			held: []string{console + ".old", program + ".old"},
			want: map[string]string{"sidera": "new sidera", "sidera.old": "old sidera", "sidera-adapter-console.old": "old console"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			dst, src, items := installation(t)
			faulty := fsxtest.NewFaulty(t, dst)
			for _, m := range c.faults {
				faulty.Fail(m, injected)
			}
			err := commit(t, faulty, src, items)
			var rollback *swap.RollbackError
			if !errors.As(err, &rollback) {
				t.Fatalf("error = %v, want a *swap.RollbackError", err)
			}
			if !slices.Equal(rollback.Held, c.held) {
				t.Errorf("Held = %v, want %v", rollback.Held, c.held)
			}
			for _, old := range c.held {
				text := fmt.Sprintf("rename it to %s to restore it", strings.TrimSuffix(old, ".old"))
				if !strings.Contains(err.Error(), old) || !strings.Contains(err.Error(), text) {
					t.Errorf("error does not tell how to restore %s: %v", old, err)
				}
			}
			assertContents(t, dst, c.want)
		})
	}
}

func TestCommitTwiceIsRefused(t *testing.T) {
	dst, src, items := installation(t)
	s, err := swap.Prepare(dst, src, items)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(); err == nil {
		t.Error("a second Commit succeeded")
	}
	assertContents(t, dst, allNew)
}

func TestPrepareRefusesWhatCannotBeSwapped(t *testing.T) {
	dst, src, items := installation(t)
	if _, err := swap.Prepare(dst, src, nil); err == nil {
		t.Error("Prepare of nothing succeeded")
	}
	if _, err := swap.Prepare(dst, src, append(items, items[0])); err == nil {
		t.Error("Prepare with a target listed twice succeeded")
	}
	assertContents(t, dst, allOld)
}

// The states a crash can leave, as Commit's steps are the only ones that move
// programs: after Clean every program is there, old or new, whether the process
// crashed or the power was lost, and a program that a crash had taken from its
// name is reported as restored.
func TestCleanAfterACrashAtAnyStep(t *testing.T) {
	programs := []struct{ path, name, old, new string }{
		{program, "sidera", "old sidera", "new sidera"},
		{console, "sidera-adapter-console", "old console", "new console"},
	}
	for _, loss := range []struct {
		name  string
		crash func(*fsxtest.Mem) *fsxtest.Mem
	}{
		{"process crash", (*fsxtest.Mem).Clone},
		{"power loss", (*fsxtest.Mem).CrashCloneWorst},
	} {
		t.Run(loss.name, func(t *testing.T) {
			var snapshots []*fsxtest.Mem // the state a crash before each step leaves, and the end state
			var names []string
			{
				dst, src, items := installation(t)
				observed := fsx.Observe(dst, new(fsx.Counter), func(o fsx.Observation) error {
					snapshots = append(snapshots, loss.crash(dst))
					names = append(names, o.Point.String())
					return nil
				})
				if err := commit(t, observed, src, items); err != nil {
					t.Fatal(err)
				}
				snapshots = append(snapshots, loss.crash(dst))
				names = append(names, "completed")
			}
			for i, crashed := range snapshots {
				t.Run("before "+names[i], func(t *testing.T) {
					before := contents(t, crashed)
					var wantRestored []string
					for _, p := range programs {
						if _, there := before[p.name]; !there {
							wantRestored = append(wantRestored, p.path)
						}
					}
					cleaned, err := swap.Clean(crashed, []string{program, console})
					if err != nil {
						t.Errorf("Clean = %v", err)
					}
					if len(cleaned.Left) != 0 {
						t.Errorf("Left = %v, want none: nothing is in use", cleaned.Left)
					}
					if !slices.Equal(cleaned.Restored, wantRestored) {
						t.Errorf("Restored = %v, want %v", cleaned.Restored, wantRestored)
					}
					after := contents(t, crashed)
					for _, p := range programs {
						if got := after[p.name]; got != p.old && got != p.new {
							t.Errorf("%s holds %q after Clean; want a program", p.name, got)
						}
						for _, suffix := range []string{".old", ".new"} {
							if _, stray := after[p.name+suffix]; stray {
								t.Errorf("%s%s was left", p.name, suffix)
							}
						}
					}
				})
			}
		})
	}
}

func TestClean(t *testing.T) {
	injected := errors.New("injected")
	for _, c := range []struct {
		name     string
		files    map[string]string // in bin
		fault    *fsxtest.Match
		restored []string
		fails    bool
		want     map[string]string
	}{
		{
			name:  "leftovers of earlier runs",
			files: map[string]string{"sidera": "s", "sidera.old": "o", "sidera.new": "n", "sidera-adapter-console": "c"},
			want:  map[string]string{"sidera": "s", "sidera-adapter-console": "c"},
		},
		{
			name:  "nothing to clean",
			files: map[string]string{"sidera": "s", "sidera-adapter-console": "c"},
			want:  map[string]string{"sidera": "s", "sidera-adapter-console": "c"},
		},
		{
			name:  "an old program that cannot be removed for any reason but being in use is a failure",
			files: map[string]string{"sidera": "s", "sidera.old": "o", "sidera-adapter-console": "c", "sidera-adapter-console.old": "o"},
			fault: &fsxtest.Match{Tx: "swap", Role: "clean", Step: fsx.StepRemove, Path: program + ".old"},
			fails: true,
			want:  map[string]string{"sidera": "s", "sidera.old": "o", "sidera-adapter-console": "c"},
		},
		{
			name:  "a new file that cannot be removed is a failure and the rest is cleaned",
			files: map[string]string{"sidera": "s", "sidera.new": "n", "sidera-adapter-console": "c", "sidera-adapter-console.old": "o"},
			fault: &fsxtest.Match{Tx: "swap", Role: "clean", Step: fsx.StepRemove, Path: program + ".new"},
			fails: true,
			want:  map[string]string{"sidera": "s", "sidera.new": "n", "sidera-adapter-console": "c"},
		},
		{
			name:     "a program taken from its name by a crash between the two renames is put back",
			files:    map[string]string{"sidera": "s", "sidera-adapter-console.old": "o", "sidera-adapter-console.new": "n"},
			restored: []string{console},
			want:     map[string]string{"sidera": "s", "sidera-adapter-console": "o"},
		},
		{
			name:     "an old program alone is put back",
			files:    map[string]string{"sidera.old": "o", "sidera-adapter-console": "c"},
			restored: []string{program},
			want:     map[string]string{"sidera": "o", "sidera-adapter-console": "c"},
		},
		{
			name:  "a program that cannot be put back stays in its old file and is a failure",
			files: map[string]string{"sidera.old": "o", "sidera.new": "n", "sidera-adapter-console": "c"},
			fault: &fsxtest.Match{Tx: "swap", Role: "recover", Step: fsx.StepRename, Path: program},
			fails: true,
			want:  map[string]string{"sidera.old": "o", "sidera-adapter-console": "c"},
		},
		{
			name:  "a program missing without a copy is not Clean's to invent",
			files: map[string]string{"sidera-adapter-console": "c"},
			want:  map[string]string{"sidera-adapter-console": "c"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			mem := fsxtest.NewMem()
			if err := fsx.MakeDir(mem, setup, "bin"); err != nil {
				t.Fatal(err)
			}
			for name, content := range c.files {
				put(t, mem, "bin/"+name, content)
			}
			var fsys fsx.FS = mem
			if c.fault != nil {
				faulty := fsxtest.NewFaulty(t, mem)
				faulty.Fail(*c.fault, injected)
				fsys = faulty
			}
			cleaned, err := swap.Clean(fsys, []string{program, console})
			if len(cleaned.Left) != 0 {
				t.Errorf("Left = %v, want none", cleaned.Left)
			}
			if !slices.Equal(cleaned.Restored, c.restored) {
				t.Errorf("Restored = %v, want %v", cleaned.Restored, c.restored)
			}
			if (err != nil) != c.fails {
				t.Errorf("error = %v, want failure: %v", err, c.fails)
			}
			if c.fails && !errors.Is(err, injected) {
				t.Errorf("error = %v, want one with the cause", err)
			}
			assertContents(t, mem, c.want)
		})
	}
}
