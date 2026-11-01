package proc

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTableSeesThisProcess(t *testing.T) {
	table := NewTable()
	self, err := table.Lookup(os.Getpid())
	if err != nil || !self.Alive || self.StartUnix == 0 {
		t.Fatalf("Lookup(self) = %+v, %v", self, err)
	}
	gone, err := table.Lookup(1 << 22)
	if err != nil || gone.Alive || gone.StartUnix != 0 {
		t.Fatalf("Lookup(missing) = %+v, %v", gone, err)
	}
	snap, err := table.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	var entry *Entry
	for i := range snap {
		if snap[i].PID == os.Getpid() {
			entry = &snap[i]
		}
	}
	if entry == nil {
		t.Fatal("the snapshot lacks this process")
	}
	if entry.StartUnix != self.StartUnix || entry.StartMicro != self.StartMicro {
		t.Fatalf("snapshot %+v and lookup %+v disagree on the start", entry.Info, self)
	}
	if len(entry.Args) != len(os.Args) || entry.Args[0] != os.Args[0] {
		t.Fatalf("arguments %q, want %q", entry.Args, os.Args)
	}
}

func TestLauncherStartsADetachedProcessThatCanBeFoundAndInterrupted(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in")
	if err := os.WriteFile(in, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	table := NewTable()
	started, err := NewLauncher(table).Start(Launch{
		Argv: []string{"/bin/sleep", "30"}, Env: []string{"A=1"}, Dir: dir,
		Stdin: in, Stdout: filepath.Join(dir, "out"), Stderr: filepath.Join(dir, "err"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !started.Alive || started.PGID != started.PID {
		t.Fatalf("started = %+v: a detached process leads its own group", started)
	}
	snap, err := table.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	found := snap.Lookup(started.PID)
	if !found.Alive || found.StartUnix != started.StartUnix || found.StartMicro != started.StartMicro {
		t.Fatalf("found %+v, started %+v", found, started.Info)
	}
	if err := NewSignaller().Interrupt(started.PID); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-started.Done:
		if err == nil {
			t.Fatal("an interrupted sleep exits with an error")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the process did not end after SIGINT")
	}
	if after, _ := table.Lookup(started.PID); after.Alive {
		t.Fatalf("still alive: %+v", after)
	}
}

func TestLauncherFailsForAMissingExecutable(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in")
	_ = os.WriteFile(in, nil, 0o600)
	_, err := NewLauncher(NewTable()).Start(Launch{Argv: []string{filepath.Join(dir, "nope")}, Dir: dir,
		Stdin: in, Stdout: filepath.Join(dir, "out"), Stderr: filepath.Join(dir, "err")})
	if err == nil {
		t.Fatal("want an error")
	}
}

// failingTable cannot read the identity of what the launcher started.
type failingTable struct{ Table }

func (failingTable) Lookup(int) (Info, error) { return Info{}, errors.New("no such table") }

// A process that started but whose identity cannot be read is not a failed
// start: the error says it started, so the caller does not take it for nothing.
func TestLauncherReportsAStartedProcessItCannotIdentify(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in")
	if err := os.WriteFile(in, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	started, err := NewLauncher(failingTable{NewTable()}).Start(Launch{
		Argv: []string{"/usr/bin/true"}, Dir: dir,
		Stdin: in, Stdout: filepath.Join(dir, "out"), Stderr: filepath.Join(dir, "err"),
	})
	if !errors.Is(err, ErrStartedUnidentified) || started.PID != 0 {
		t.Fatalf("Start = %+v, %v: want ErrStartedUnidentified", started, err)
	}
	// Every other failure started nothing.
	_, err = NewLauncher(NewTable()).Start(Launch{Argv: []string{"/usr/bin/true"}, Stdin: filepath.Join(dir, "missing")})
	if err == nil || errors.Is(err, ErrStartedUnidentified) {
		t.Fatalf("a launch that cannot be made: %v", err)
	}
}
