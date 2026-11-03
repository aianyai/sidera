package update

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/aianyai/sidera/cmd/sidera/internal/update/swap"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/release"
)

const newVersion = "0.12.0"

func TestRunConvergesEverything(t *testing.T) {
	r := newRig(t)
	r.release(newVersion)

	rep, err := Run(context.Background(), r.cfg, "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The programs are the release's, and the old ones are kept to go back to.
	for name, want := range map[string]string{
		"sidera": "new sidera " + newVersion, "sidera-adapter-console": "new console " + newVersion,
		"sidera.old": "old sidera", "sidera-adapter-console.old": "old console",
	} {
		if got := r.read(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(filepath.Join(r.dir, "sidera"))
		if fi.Mode().Perm() != 0o700 {
			t.Errorf("the new program has mode %v, want 0700", fi.Mode().Perm())
		}
	}
	// The daemon was restarted into the release, then the adapters it has were installed from the unpacked package.
	if !reflect.DeepEqual(r.node.restarts, []string{newVersion}) {
		t.Errorf("restarts = %v", r.node.restarts)
	}
	var installed []string
	for _, dir := range r.node.installs {
		installed = append(installed, filepath.Base(dir))
	}
	if !reflect.DeepEqual(installed, []string{"claude", "console"}) {
		t.Errorf("installed adapters = %v, want claude and console", installed)
	}
	want := Report{
		Release: Release{Current: oldVersion, Target: newVersion, Admitted: true, Floor: "0.8.0"},
		Program: ProgramReport{Updated: true, Replaced: []string{filepath.Join(r.dir, "sidera-adapter-console"), filepath.Join(r.dir, "sidera")}},
		Daemon:  DaemonReport{Running: true, Previous: oldVersion, Restarted: true, Version: newVersion},
		Adapters: []AdapterReport{
			{ID: "claude", Previous: oldVersion, Version: newVersion, Replaced: true},
			{ID: "console", Previous: oldVersion, Version: newVersion, Replaced: true},
			{ID: "codex", Previous: oldVersion, Skipped: "the release has no package of this adapter"},
		},
	}
	want.ReleaseURL = rep.ReleaseURL
	if !reflect.DeepEqual(sorted(rep), sorted(want)) {
		t.Errorf("report = %+v\nwant     %+v", rep, want)
	}
	if !strings.HasSuffix(rep.ReleaseURL, "/releases/tag/v"+newVersion) {
		t.Errorf("release URL = %q", rep.ReleaseURL)
	}
	// The plan shown for the confirmation named what was about to happen.
	if len(r.plans) != 1 || !r.plans[0].Restart || !reflect.DeepEqual(r.plans[0].Adapters, []string{"claude", "console"}) || !reflect.DeepEqual(r.plans[0].Missing, []string{"codex"}) || len(r.plans[0].Replace) != 2 {
		t.Errorf("plans = %+v", r.plans)
	}
	// What was downloaded is gone, and the lock does not stay held.
	if r.exists("sidera.new") {
		t.Error("a .new file remains")
	}
	if _, err := os.Stat(filepath.Join(r.cache, newVersion)); !os.IsNotExist(err) {
		t.Errorf("the cache of the release remains: %v", err)
	}
	if _, err := Run(context.Background(), r.cfg, ""); err != nil {
		t.Errorf("a second run after the first: %v", err)
	}
}

// sorted makes adapter order irrelevant: the order is the daemon's.
func sorted(r Report) Report {
	out := r
	out.Adapters = append([]AdapterReport(nil), r.Adapters...)
	for i := range out.Adapters {
		for j := i + 1; j < len(out.Adapters); j++ {
			if out.Adapters[j].ID < out.Adapters[i].ID {
				out.Adapters[i], out.Adapters[j] = out.Adapters[j], out.Adapters[i]
			}
		}
	}
	return out
}

// A run starts from wherever an earlier one stopped and does only what is missing.
func TestRunDoesOnlyWhatIsMissing(t *testing.T) {
	tests := []struct {
		name string
		// setup moves the installation to the starting point of the case.
		setup        func(r *rig)
		wantSwapped  bool
		wantRestarts int
		wantInstalls []string
		wantArchive  int
		wantCurrent  bool
		// wantQuiet is true when the run has nothing to do and so asks the network for nothing.
		wantQuiet bool
	}{
		{name: "nothing is at the target",
			setup: func(*rig) {}, wantSwapped: true, wantRestarts: 1, wantInstalls: []string{"claude", "console", "codex"}, wantArchive: 1},
		{name: "everything is at the target",
			setup: func(r *rig) {
				r.cfg.Installed.Version = newVersion
				r.node.version = newVersion
				for i := range r.node.adapters {
					r.node.adapters[i].Version = newVersion
				}
			}, wantCurrent: true, wantQuiet: true},
		{name: "the programs are new, the daemon and adapters are not",
			setup: func(r *rig) { r.cfg.Installed.Version = newVersion }, wantRestarts: 1, wantInstalls: []string{"claude", "console", "codex"}, wantArchive: 1},
		{name: "only the adapters are old",
			setup: func(r *rig) {
				r.cfg.Installed.Version = newVersion
				r.node.version = newVersion
			}, wantInstalls: []string{"claude", "console", "codex"}, wantArchive: 1},
		{name: "some adapters are new",
			setup: func(r *rig) {
				r.cfg.Installed.Version = newVersion
				r.node.version = newVersion
				r.node.adapters[0].Version = newVersion
				r.node.adapters[2].Version = newVersion
			}, wantInstalls: []string{"console"}, wantArchive: 1},
		{name: "only the daemon is old",
			setup: func(r *rig) {
				r.cfg.Installed.Version = newVersion
				for i := range r.node.adapters {
					r.node.adapters[i].Version = newVersion
				}
			}, wantRestarts: 1},
		{name: "no daemon runs",
			setup: func(r *rig) { r.node.running = false }, wantSwapped: true, wantArchive: 1},
		// The adapters cannot be looked at, so nothing is reported current.
		{name: "no daemon runs and the programs are new",
			setup: func(r *rig) { r.cfg.Installed.Version = newVersion; r.node.running = false }, wantQuiet: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			r.release(newVersion)
			tt.setup(r)
			installs := func() (ids []string) {
				for _, d := range r.node.installs {
					ids = append(ids, filepath.Base(d))
				}
				return ids
			}
			// The packaged claude and console are installed; codex is not in the package.
			rep, err := Run(context.Background(), r.cfg, newVersion)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if rep.Program.Updated != tt.wantSwapped || rep.AlreadyCurrent != tt.wantCurrent {
				t.Errorf("updated=%v current=%v, want %v %v", rep.Program.Updated, rep.AlreadyCurrent, tt.wantSwapped, tt.wantCurrent)
			}
			if len(r.node.restarts) != tt.wantRestarts {
				t.Errorf("restarts = %v", r.node.restarts)
			}
			var wantInstalled []string
			for _, id := range tt.wantInstalls {
				if id != "codex" {
					wantInstalled = append(wantInstalled, id)
				}
			}
			if got := installs(); !reflect.DeepEqual(got, wantInstalled) {
				t.Errorf("installs = %v, want %v", got, wantInstalled)
			}
			if got := r.repo.hit("archive"); got != tt.wantArchive {
				t.Errorf("archive downloads = %d, want %d", got, tt.wantArchive)
			}
			if tt.wantQuiet && r.repo.hit("checksums") != 0 {
				t.Error("a run with nothing to do downloaded the checksums")
			}
			if !tt.wantSwapped && r.read("sidera") != "old sidera" {
				t.Error("the program was replaced")
			}
		})
	}
}

// An adapter newer than the target, or whose version is not a release version, is not
// the release's to replace: it is left as it is and reported, and it does not keep a
// node from being current or make a run download the archive.
func TestRunLeavesAdaptersThatAreNotTheReleases(t *testing.T) {
	foreign := func(r *rig) {
		r.cfg.Installed.Version = newVersion
		r.node.version = newVersion
		r.node.adapters = []Adapter{{"claude", newVersion}, {"codex", "1.0.0"}, {"mine", "local-build"}}
	}
	t.Run("nothing else to do", func(t *testing.T) {
		r := newRig(t)
		r.release(newVersion)
		foreign(r)
		rep, err := Run(context.Background(), r.cfg, newVersion)
		if err != nil {
			t.Fatal(err)
		}
		if !rep.AlreadyCurrent || len(r.node.installs) != 0 || r.repo.hit("checksums") != 0 {
			t.Errorf("current %v, installs %v, checksum downloads %d", rep.AlreadyCurrent, r.node.installs, r.repo.hit("checksums"))
		}
		var skipped []string
		for _, a := range rep.Adapters {
			if a.Skipped != "" {
				skipped = append(skipped, a.ID)
			}
		}
		if !reflect.DeepEqual(skipped, []string{"codex", "mine"}) {
			t.Errorf("skipped = %v, report %+v", skipped, rep.Adapters)
		}
	})
	t.Run("an older one is installed beside them", func(t *testing.T) {
		r := newRig(t)
		r.release(newVersion)
		foreign(r)
		r.node.adapters[0].Version = oldVersion
		rep, err := Run(context.Background(), r.cfg, newVersion)
		if err != nil {
			t.Fatal(err)
		}
		if rep.AlreadyCurrent || len(r.node.installs) != 1 || filepath.Base(r.node.installs[0]) != "claude" || len(rep.Adapters) != 3 {
			t.Errorf("current %v, installs %v, report %+v", rep.AlreadyCurrent, r.node.installs, rep.Adapters)
		}
	})
}

func TestRunStopsBeforeChangingAnything(t *testing.T) {
	otherSigner := func(r *rig) *signer { return newSigner(r.t) }
	tests := []struct {
		name    string
		publish func(r *rig) // publishes the release asked for (the default is an honest 0.12.0)
		cfg     func(r *rig)
		want    string
		// archive is how many archives may have been downloaded before the refusal.
		archive int
	}{
		{name: "signed by another key", publish: func(r *rig) {
			r.repo.add(newVersion, publish(r.t, r.signer, newVersion, releaseOptions{signer: otherSigner(r)}))
		}, want: "cannot be trusted"},
		{name: "the signature of another release", publish: func(r *rig) {
			r.repo.add(newVersion, publish(r.t, r.signer, newVersion, releaseOptions{signedFor: "0.11.5"}))
		}, want: "cannot be trusted"},
		{name: "an archive that is not the one signed", publish: func(r *rig) {
			r.repo.add(newVersion, publish(r.t, r.signer, newVersion, releaseOptions{listedSum: []byte("something else")}))
		}, want: "does not match the signed checksum", archive: 1},
		{name: "an archive that does not unpack", publish: func(r *rig) {
			r.repo.add(newVersion, publish(r.t, r.signer, newVersion, releaseOptions{files: map[string]string{"../sidera": "x"}}))
		}, want: "refused", archive: 1},
		{name: "an unpacked program that is another version", publish: func(r *rig) { r.release(newVersion) },
			cfg: func(r *rig) {
				r.smokeAs = func(string) release.Info {
					return release.Info{Version: "0.12.1", Distribution: release.DistributionArchive}
				}
			},
			want: "reports version", archive: 1},
		{name: "an unpacked program that is not from an archive", publish: func(r *rig) { r.release(newVersion) },
			cfg:  func(r *rig) { r.smokeAs = func(v string) release.Info { return release.Info{Version: v} } },
			want: "distribution", archive: 1},
		{name: "a floor above the installed version", publish: func(r *rig) {
			r.repo.add(newVersion, publish(r.t, r.signer, newVersion, releaseOptions{floor: "0.11.5"}))
		}, want: "0.11.5 or later"},
		{name: "a downgrade", publish: func(r *rig) { r.release(newVersion) },
			cfg: func(r *rig) { r.cfg.Installed.Version = "0.13.0" }, want: "does not go back"},
		{name: "a release that does not exist", publish: func(r *rig) { r.release("0.11.9") }, want: "no such release"},
		{name: "a development build", publish: func(r *rig) { r.release(newVersion) },
			cfg: func(r *rig) { r.cfg.Installed = release.Info{Version: release.DevVersion} }, want: "development build"},
		{name: "a build with no distribution", publish: func(r *rig) { r.release(newVersion) },
			cfg: func(r *rig) { r.cfg.Installed.Distribution = "" }, want: "not installed from a release archive"},
		{name: "a modified release build", publish: func(r *rig) { r.release(newVersion) },
			cfg: func(r *rig) { r.cfg.Installed.Modified = true }, want: "modified checkout"},
		{name: "a declined confirmation", publish: func(r *rig) { r.release(newVersion) },
			cfg: func(r *rig) { r.answer = false }, want: "not confirmed", archive: 1},
		{name: "a daemon newer than the target, programs to replace", publish: func(r *rig) { r.release(newVersion) },
			cfg: func(r *rig) { r.node.version = "0.13.0" }, want: "the daemon runs 0.13.0, newer than " + newVersion},
		{name: "a daemon newer than the target, programs current", publish: func(r *rig) { r.release(newVersion) },
			cfg: func(r *rig) { r.cfg.Installed.Version = newVersion; r.node.version = "0.12.1" }, want: "the daemon runs 0.12.1, newer than " + newVersion},
		{name: "a daemon whose version is not a release version", publish: func(r *rig) { r.release(newVersion) },
			cfg: func(r *rig) { r.node.version = "custom" }, want: "not a release version"},
		{name: "a node that cannot be read", publish: func(r *rig) { r.release(newVersion) },
			cfg: func(r *rig) { r.cfg.Node = brokenNode{} }, want: "read the daemon"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			tt.publish(r)
			if tt.cfg != nil {
				tt.cfg(r)
			}
			_, err := Run(context.Background(), r.cfg, newVersion)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Run = %v, want an error containing %q", err, tt.want)
			}
			r.unchanged()
			if len(r.node.restarts) != 0 || len(r.node.installs) != 0 {
				t.Errorf("the node was changed: restarts %v installs %v", r.node.restarts, r.node.installs)
			}
			if got := r.repo.hit("archive"); got > tt.archive {
				t.Errorf("archive downloads = %d, at most %d expected", got, tt.archive)
			}
			if _, err := os.Stat(filepath.Join(r.cache, newVersion)); !os.IsNotExist(err) {
				t.Errorf("the cache of the release remains after a refusal: %v", err)
			}
		})
	}
}

type brokenNode struct{ *fakeNode }

func (brokenNode) Status(context.Context) (NodeStatus, error) {
	return NodeStatus{}, errors.New("boom")
}

func TestRunRefusesWhereTheProgramsAre(t *testing.T) {
	t.Run("a symbolic link", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symbolic links need a privilege on Windows")
		}
		r := newRig(t)
		r.release(newVersion)
		link := filepath.Join(t.TempDir(), "sidera")
		if err := os.Symlink(filepath.Join(r.dir, "sidera"), link); err != nil {
			t.Fatal(err)
		}
		r.cfg.Program = link
		if _, err := Run(context.Background(), r.cfg, ""); err == nil || !strings.Contains(err.Error(), "symbolic link") {
			t.Fatalf("Run = %v, want a refusal for the link", err)
		}
		r.unchanged()
	})
	t.Run("a directory that is a link", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symbolic links need a privilege on Windows")
		}
		r := newRig(t)
		r.release(newVersion)
		link := filepath.Join(t.TempDir(), "bin")
		if err := os.Symlink(r.dir, link); err != nil {
			t.Fatal(err)
		}
		r.cfg.Program = filepath.Join(link, "sidera")
		if _, err := Run(context.Background(), r.cfg, ""); err == nil || !strings.Contains(err.Error(), "symbolic link") {
			t.Fatalf("Run = %v, want a refusal for the link", err)
		}
		r.unchanged()
	})
	t.Run("a directory that cannot be written", func(t *testing.T) {
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("a read-only directory stops a normal user on Unix")
		}
		r := newRig(t)
		r.release(newVersion)
		if err := os.Chmod(r.dir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(r.dir, 0o700) })
		_, err := Run(context.Background(), r.cfg, "")
		if err == nil || !strings.Contains(err.Error(), "cannot write to") {
			t.Fatalf("Run = %v, want a refusal for the directory", err)
		}
		if r.repo.hit("checksums") != 0 {
			t.Error("a read-only installation was refused after the download began")
		}
	})
	t.Run("a console program is replaced only where it is", func(t *testing.T) {
		r := newRig(t)
		r.release(newVersion)
		if err := os.Remove(filepath.Join(r.dir, "sidera-adapter-console")); err != nil {
			t.Fatal(err)
		}
		rep, err := Run(context.Background(), r.cfg, "")
		if err != nil {
			t.Fatal(err)
		}
		if !rep.Program.ConsoleMissing || r.exists("sidera-adapter-console") || r.read("sidera") != "new sidera "+newVersion {
			t.Errorf("report %+v, console exists %v", rep.Program, r.exists("sidera-adapter-console"))
		}
	})
}

// A run that is refused for where the programs are creates nothing: not the cache
// directory, not the lock. Under sudo (which keeps the user's home) they would belong to
// root and stop every later run of the user.
func TestRunCreatesNothingBeforeItIsAllowed(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a program directory of another owner and a user who is not root")
	}
	r := newRig(t)
	r.release(newVersion)
	r.cfg.Program = "/sidera" // its directory, "/", is owned by root
	if _, err := Run(context.Background(), r.cfg, ""); err == nil || !strings.Contains(err.Error(), "owned by") {
		t.Fatalf("Run = %v, want a refusal for the owner", err)
	}
	if _, err := os.Lstat(r.cache); !os.IsNotExist(err) {
		t.Errorf("the refused run left the cache directory behind: %v", err)
	}
}

// A cache directory that others can enter is not trusted with a download that is checked
// once and used later.
func TestRunRefusesACacheOthersCanEnter(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("who may enter a directory is its access list on Windows")
	}
	r := newRig(t)
	r.release(newVersion)
	if err := os.MkdirAll(r.cache, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(r.cache, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Run(context.Background(), r.cfg, "")
	if err == nil || !strings.Contains(err.Error(), "open to other users") {
		t.Fatalf("Run = %v, want a refusal for the cache directory", err)
	}
	r.unchanged()
	if r.repo.hit("archive") != 0 {
		t.Error("the archive was downloaded into a directory others can enter")
	}
}

// A program owned by another user is refused; "/" is owned by root, which is not the
// user running the tests.
func TestCheckPlaceRefusesAnotherOwner(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a path of another owner and a user who is not root")
	}
	err := checkPlace("/", true)
	if err == nil || !strings.Contains(err.Error(), "owned by") || !strings.Contains(err.Error(), "sudo") {
		t.Fatalf("checkPlace(/) = %v, want a refusal that names the owner and sudo", err)
	}
}

// A run cut short between the renames of two programs, or of one, is finished by
// running again: that is the convergence the replacement relies on.
func TestRunFinishesAnInterruptedSwap(t *testing.T) {
	t.Run("between two programs", func(t *testing.T) {
		r := newRig(t)
		r.release(newVersion)
		// The console program was replaced; the running one was not.
		r.write("sidera-adapter-console", "new console "+newVersion)
		r.write("sidera-adapter-console.old", "old console")
		r.write("sidera.new", "half of a new program")
		rep, err := Run(context.Background(), r.cfg, "")
		if err != nil {
			t.Fatal(err)
		}
		if r.read("sidera") != "new sidera "+newVersion || r.read("sidera-adapter-console") != "new console "+newVersion || r.exists("sidera.new") {
			t.Errorf("sidera %q, console %q, .new %v", r.read("sidera"), r.read("sidera-adapter-console"), r.exists("sidera.new"))
		}
		if !rep.Program.Updated {
			t.Error("the programs were not reported as replaced")
		}
	})
	t.Run("between the renames of one program", func(t *testing.T) {
		r := newRig(t)
		r.release(newVersion)
		// The console program was set aside and its replacement was not put in place.
		if err := os.Rename(filepath.Join(r.dir, "sidera-adapter-console"), filepath.Join(r.dir, "sidera-adapter-console.old")); err != nil {
			t.Fatal(err)
		}
		r.write("sidera-adapter-console.new", "new console "+newVersion)
		rep, err := Run(context.Background(), r.cfg, "")
		if err != nil {
			t.Fatal(err)
		}
		if got := r.read("sidera-adapter-console"); got != "new console "+newVersion {
			t.Errorf("console = %q", got)
		}
		if want := []string{filepath.Join(r.dir, "sidera-adapter-console")}; !reflect.DeepEqual(rep.Program.Restored, want) {
			t.Errorf("restored = %v, want %v", rep.Program.Restored, want)
		}
	})
}

func TestRunWhenTheDaemonCannotRestart(t *testing.T) {
	r := newRig(t)
	r.release(newVersion)
	r.node.restartErr = errors.New("the program is not the one expected")
	rep, err := Run(context.Background(), r.cfg, "")
	if err == nil || !strings.Contains(err.Error(), "restart the daemon") || !strings.Contains(err.Error(), "run sidera update again") {
		t.Fatalf("Run = %v", err)
	}
	if !rep.Program.Updated || rep.Daemon.Restarted || len(r.node.installs) != 0 {
		t.Errorf("report %+v, installs %v: the programs stay replaced and the adapters wait for the daemon", rep, r.node.installs)
	}
}

func TestRunWhenTheDaemonRestartsAsAnotherVersion(t *testing.T) {
	r := newRig(t)
	r.release(newVersion)
	r.node.restartTo = "0.11.0"
	_, err := Run(context.Background(), r.cfg, "")
	if err == nil || !strings.Contains(err.Error(), "not "+newVersion) {
		t.Fatalf("Run = %v", err)
	}
	if len(r.node.installs) != 0 {
		t.Error("adapters were installed into a daemon of another version")
	}
}

func TestRunInstallsTheOtherAdaptersWhenOneFails(t *testing.T) {
	r := newRig(t)
	r.release(newVersion)
	r.node.installErr = map[string]error{"claude": errors.New("in use")}
	rep, err := Run(context.Background(), r.cfg, "")
	if err == nil || !strings.Contains(err.Error(), "install adapter claude") {
		t.Fatalf("Run = %v", err)
	}
	failed, installed := 0, 0
	for _, a := range rep.Adapters {
		switch {
		case a.Error != "":
			failed++
		case a.Version == newVersion:
			installed++
		}
	}
	if failed != 1 || installed != 1 {
		t.Errorf("adapters = %+v", rep.Adapters)
	}
}

func TestRunIsExclusive(t *testing.T) {
	r := newRig(t)
	r.release(newVersion)
	if err := os.MkdirAll(r.cache, 0o700); err != nil {
		t.Fatal(err)
	}
	cache := mustOpen(r.cache)
	unlock, err := cache.Lock(lockName)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := Run(context.Background(), r.cfg, ""); err == nil || !strings.Contains(err.Error(), "another sidera update is running") {
		t.Fatalf("Run = %v", err)
	}
	r.unchanged()
}

func mustOpen(dir string) fsx.FS {
	f, err := fsx.OpenOS(dir, rand.Reader)
	if err != nil {
		panic(err)
	}
	return f
}

func TestCheck(t *testing.T) {
	t.Run("newer", func(t *testing.T) {
		r := newRig(t)
		r.release(newVersion)
		rep, err := Check(context.Background(), r.cfg, "")
		if err != nil {
			t.Fatal(err)
		}
		if !rep.UpdateAvailable || !rep.Admitted || rep.Target != newVersion || rep.Floor != "0.8.0" || rep.Refusal != "" {
			t.Errorf("report = %+v", rep)
		}
		if r.repo.hit("archive") != 0 {
			t.Error("a check downloaded the archive")
		}
		if _, err := os.Stat(r.cache); !os.IsNotExist(err) {
			t.Errorf("a check wrote the cache: %v", err)
		}
		r.unchanged()
	})
	t.Run("older is not an update", func(t *testing.T) {
		r := newRig(t)
		r.release("0.10.0")
		rep, err := Check(context.Background(), r.cfg, "")
		if err != nil || rep.UpdateAvailable || !strings.Contains(rep.Refusal, "does not go back") {
			t.Errorf("report = %+v, %v", rep, err)
		}
	})
	t.Run("the same version", func(t *testing.T) {
		r := newRig(t)
		r.release(oldVersion)
		rep, err := Check(context.Background(), r.cfg, "")
		if err != nil || rep.UpdateAvailable || rep.Refusal != "" {
			t.Errorf("report = %+v, %v", rep, err)
		}
	})
	t.Run("not admitted by the floor", func(t *testing.T) {
		r := newRig(t)
		r.repo.add(newVersion, publish(t, r.signer, newVersion, releaseOptions{floor: "0.11.5"}))
		rep, err := Check(context.Background(), r.cfg, newVersion)
		if err != nil || !rep.UpdateAvailable || rep.Admitted || !strings.Contains(rep.Refusal, "0.11.5 or later") {
			t.Errorf("report = %+v, %v", rep, err)
		}
	})
	t.Run("a build update does not replace", func(t *testing.T) {
		r := newRig(t)
		r.release(newVersion)
		r.cfg.Installed = release.Info{Version: release.DevVersion}
		rep, err := Check(context.Background(), r.cfg, "")
		if err != nil || !strings.Contains(rep.Refusal, "development build") {
			t.Errorf("report = %+v, %v", rep, err)
		}
	})
	t.Run("a release that cannot be believed is an error", func(t *testing.T) {
		r := newRig(t)
		r.repo.add(newVersion, publish(t, r.signer, newVersion, releaseOptions{signer: newSigner(t)}))
		if _, err := Check(context.Background(), r.cfg, ""); err == nil {
			t.Error("Check = nil")
		}
	})
}

func TestNormalizeVersion(t *testing.T) {
	for in, want := range map[string]string{"": "", "0.12.0": "0.12.0", "v0.12.0": "0.12.0", "v0.12.0-rc.1": "0.12.0-rc.1"} {
		if got, err := NormalizeVersion(in); err != nil || got != want {
			t.Errorf("NormalizeVersion(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"vv1.0.0", "1.2", "latest", "1.2.3+b", " 1.2.3", "V1.2.3"} {
		if got, err := NormalizeVersion(in); err == nil {
			t.Errorf("NormalizeVersion(%q) = %q, want an error", in, got)
		}
	}
}

func TestExplainRollbackNamesAbsolutePaths(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bin")
	cause := errors.New("rename failed")
	err := explainRollback(dir, &swap.RollbackError{Cause: cause, Held: []string{"sidera.old"}})
	for _, want := range []string{"rename failed", filepath.Join(dir, "sidera.old"), "rename it to " + filepath.Join(dir, "sidera")} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q does not contain %q", err, want)
		}
	}
	if plain := errors.New("plain"); explainRollback(dir, plain) != plain {
		t.Error("an error that is not a rollback failure was changed")
	}
}
