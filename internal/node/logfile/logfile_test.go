package logfile_test

import (
	"fmt"
	"io/fs"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/logfile"
)

var setupTag = fsx.Tag{Tx: "setup", Role: "dir"}

// disk is a file system with a logs/ directory and a record of the write steps
// that are crash points: a log write must never be one.
type disk struct {
	mem    *fsxtest.Mem
	fs     fsx.FS
	points int
}

func newDisk(t testing.TB) *disk {
	t.Helper()
	d := &disk{mem: fsxtest.NewMem()}
	for _, dir := range []string{"logs", "state", "config"} {
		if err := fsx.MakeDir(d.mem, setupTag, dir); err != nil {
			t.Fatal(err)
		}
	}
	d.fs = fsx.Observe(d.mem, &fsx.Counter{}, func(fsx.Point) error { d.points++; return nil })
	return d
}

func (d *disk) read(t testing.TB, p string) string {
	t.Helper()
	b, err := d.mem.ReadFile(p)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", p, err)
	}
	return string(b)
}

func (d *disk) exists(p string) bool {
	_, err := d.mem.Stat(p)
	return err == nil
}

// threshold is a budget under which every log is rotated at the size limit() says,
// whenever it is asked: one daemon log (no instance), no floor.
func threshold(limit func() int64) logfile.Budget {
	return logfile.Budget{Total: func() int64 { return 2 * limit() }, Floor: 1}
}

func fixed(n int64) logfile.Budget { return threshold(func() int64 { return n }) }

// The handle can write a log and nothing else: a name that would lead out of
// logs/, or is not the name of a log, is refused before any step runs, and what a
// valid name creates is under logs/.
func TestTheHandleWritesOnlyLogsUnderTheLogsDirectory(t *testing.T) {
	d := newDisk(t)
	h := logfile.New(d.fs, fixed(1<<20))
	for _, tc := range []struct {
		name  string
		valid bool
	}{
		{"daemon.log", true},
		{"adapter-chat.log", true},
		{"adapter-chat-2.log", true},
		{"../state/daemon.log", false},
		{"state/x.log", false},
		{"/etc/passwd.log", false},
		{`..\x.log`, false},
		{"..", false},
		{".", false},
		{"", false},
		{"daemon", false},
		{"daemon.log.1", false},
		{".hidden.log", false},
		{"a b.log", false},
		{"daemon.log\x00", false},
	} {
		t.Run(fmt.Sprintf("%q", tc.name), func(t *testing.T) {
			f, err := h.Open(tc.name)
			if tc.valid != (err == nil) {
				t.Fatalf("Open(%q) = %v, valid %v", tc.name, err, tc.valid)
			}
			if err != nil {
				return
			}
			defer f.Close()
			if _, err := f.Write([]byte("line\n")); err != nil {
				t.Fatal(err)
			}
			if got := d.read(t, path.Join(logfile.Dir, tc.name)); got != "line\n" {
				t.Errorf("the log holds %q", got)
			}
		})
	}
	for _, p := range []string{"state", "config", "."} {
		entries, err := d.mem.ReadDir(p)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 && p != "." {
			t.Errorf("%s holds %v after the handle's writes", p, entries)
		}
	}
}

// A log is diagnostic: its writes, its rotation and its removal are never crash
// points. They carry the reserved tag, which the crash-point numbering skips; a
// log write that was numbered would make the numbering of the real writes depend
// on how many lines were logged.
func TestLogWritesAreNeverCrashPoints(t *testing.T) {
	d := newDisk(t)
	h := logfile.New(d.fs, fixed(64))
	f, err := h.Open("daemon.log")
	if err != nil {
		t.Fatal(err)
	}
	for i := range 20 {
		if _, err := f.Write([]byte(fmt.Sprintf("line %02d of the log\n", i))); err != nil {
			t.Fatal(err)
		}
	}
	if !d.exists("logs/daemon.log.1") {
		t.Fatal("the log was not rotated: the test did not reach the rename")
	}
	f.Close()
	if err := h.Remove("daemon.log"); err != nil {
		t.Fatal(err)
	}
	if d.points != 0 {
		t.Errorf("%d steps of the log writes were numbered as crash points, want none", d.points)
	}
}

// A log is rotated once its size would pass the threshold: the file that is full
// becomes the one rotation, which replaces the older one, so the two files hold
// the newest lines and never more than twice the threshold.
func TestRotationKeepsOneRotatedFileAndBoundsTheSize(t *testing.T) {
	d := newDisk(t)
	const limit = 100
	h := logfile.New(d.fs, fixed(limit))
	f, err := h.Open("daemon.log")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var all []string
	for i := range 40 {
		line := fmt.Sprintf("entry %03d xxxxxxxxxxxxxxx\n", i) // 26 bytes
		all = append(all, line)
		if _, err := f.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{"logs/daemon.log", "logs/daemon.log.1"} {
			if d.exists(p) && int64(len(d.read(t, p))) > limit {
				t.Fatalf("after line %d, %s holds %d bytes, over the threshold %d", i, p, len(d.read(t, p)), limit)
			}
		}
	}
	entries, err := d.mem.ReadDir("logs")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name)
	}
	if strings.Join(names, ",") != "daemon.log,daemon.log.1" {
		t.Fatalf("logs/ holds %v, want the log and one rotation", names)
	}
	// the two files are the newest lines, the rotated one before the current one
	got := d.read(t, "logs/daemon.log.1") + d.read(t, "logs/daemon.log")
	want := strings.Join(all[len(all)-strings.Count(got, "\n"):], "")
	if got != want {
		t.Errorf("the files hold\n%s\nwant the newest lines in order\n%s", got, want)
	}
	if strings.Count(got, "\n") < 4 { // a full file of three lines and the start of the next
		t.Errorf("only %d lines are kept", strings.Count(got, "\n"))
	}
}

// A line is written whole to one file: a line that would pass the threshold
// starts a new file, and a line longer than the threshold is written whole to a
// file of its own rather than cut.
func TestALineIsNeverCutByTheRotation(t *testing.T) {
	d := newDisk(t)
	h := logfile.New(d.fs, fixed(30))
	f, err := h.Open("daemon.log")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	long := strings.Repeat("y", 80) + "\n"
	for _, line := range []string{"short one\n", long, "short two\n"} {
		if _, err := f.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	cur, prev := d.read(t, "logs/daemon.log"), d.read(t, "logs/daemon.log.1")
	if cur != "short two\n" || prev != long {
		t.Errorf("current %q, rotated %q; want the long line whole in the rotation", cur, prev)
	}
}

// The threshold is read at each write, so a change of the number of instances
// takes effect at the next rotation without reopening the log; the size the log
// had is not lost by closing and opening it again.
func TestAChangedThresholdAndAReopenedLogAreHonoured(t *testing.T) {
	d := newDisk(t)
	limit := int64(1000)
	h := logfile.New(d.fs, threshold(func() int64 { return limit }))
	f, err := h.Open("daemon.log")
	if err != nil {
		t.Fatal(err)
	}
	write := func(f *logfile.Log, s string) {
		t.Helper()
		if _, err := f.Write([]byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	write(f, strings.Repeat("a", 90)+"\n")
	limit = 100
	write(f, strings.Repeat("b", 90)+"\n")
	if d.read(t, "logs/daemon.log.1") != strings.Repeat("a", 90)+"\n" {
		t.Errorf("the lowered threshold did not rotate: %q", d.read(t, "logs/daemon.log.1"))
	}
	f.Close()
	f, err = h.Open("daemon.log")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	write(f, strings.Repeat("c", 90)+"\n")
	if got := d.read(t, "logs/daemon.log.1"); got != strings.Repeat("b", 90)+"\n" {
		t.Errorf("a reopened log forgot its size: the rotation holds %q", got)
	}
	if got := d.read(t, "logs/daemon.log"); got != strings.Repeat("c", 90)+"\n" {
		t.Errorf("the current log holds %q", got)
	}
}

// Many goroutines log at once, as the daemon's do: every line arrives whole and
// none is lost between the two files while the threshold is large enough to hold
// them all.
func TestConcurrentWritersKeepEveryLineWhole(t *testing.T) {
	d := newDisk(t)
	h := logfile.New(d.fs, fixed(1<<20))
	f, err := h.Open("daemon.log")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	const writers, each = 8, 50
	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range each {
				if _, err := f.Write([]byte(fmt.Sprintf("writer %d line %03d\n", w, i))); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	seen := map[string]int{}
	for _, line := range strings.Split(strings.TrimSuffix(d.read(t, "logs/daemon.log"), "\n"), "\n") {
		seen[line]++
	}
	if len(seen) != writers*each {
		t.Errorf("%d distinct whole lines, want %d", len(seen), writers*each)
	}
	for line, n := range seen {
		if n != 1 || !strings.HasPrefix(line, "writer ") || len(line) != len("writer 0 line 000") {
			t.Errorf("line %q seen %d times", line, n)
		}
	}
}

// Removing a log, when its instance is disabled, removes the rotation with it,
// and a log that is not there is no error.
func TestRemoveDeletesALogAndItsRotation(t *testing.T) {
	d := newDisk(t)
	h := logfile.New(d.fs, fixed(20))
	f, err := h.Open("adapter-chat.log")
	if err != nil {
		t.Fatal(err)
	}
	for i := range 4 {
		f.Write([]byte(fmt.Sprintf("line number %d\n", i)))
	}
	f.Close()
	if !d.exists("logs/adapter-chat.log") || !d.exists("logs/adapter-chat.log.1") {
		t.Fatal("the setting up did not make a log and a rotation")
	}
	if err := h.Remove("adapter-chat.log"); err != nil {
		t.Fatal(err)
	}
	if d.exists("logs/adapter-chat.log") || d.exists("logs/adapter-chat.log.1") {
		t.Error("a file of the removed log is left")
	}
	if err := h.Remove("adapter-chat.log"); err != nil {
		t.Errorf("removing a log that is not there: %v", err)
	}
	if err := h.Remove("../state"); err == nil {
		t.Error("Remove took a name that is not a log")
	}
}

// The threshold of each file is the total divided among two files of every log
// of the daemon and of each enabled instance, with a floor. The expected values
// are worked out by hand: 150 MiB shared by the daemon alone is 75 MiB a file;
// with four instances, five logs of two files each, 15 MiB; the floor is 1 MiB,
// reached at 74 instances and below which nothing goes.
func TestThresholdFollowsTheBudget(t *testing.T) {
	const mib = 1 << 20
	for _, tc := range []struct {
		name      string
		total     int64
		instances int
		want      int64
	}{
		{"the daemon alone", 150 * mib, 0, 75 * mib},
		{"one instance", 150 * mib, 1, 37*mib + mib/2},
		{"four instances", 150 * mib, 4, 15 * mib},
		{"the last count above the floor", 150 * mib, 73, 150 * mib / 148},
		{"exactly the floor", 150 * mib, 74, mib},
		{"below the floor", 150 * mib, 100, mib},
		{"a small total takes the floor", 1 * mib, 0, mib},
		{"a total of nothing takes the floor", 0, 3, mib},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := logfile.Threshold(tc.total, tc.instances); got != tc.want {
				t.Errorf("Threshold(%d, %d) = %d, want %d", tc.total, tc.instances, got, tc.want)
			}
		})
	}
}

// flaky is a file system whose log writes fail while fail is set.
type flaky struct {
	fsx.FS
	fail atomic.Bool
	hits atomic.Int32
}

func (f *flaky) OpenAppend(tag fsx.Tag, p string) (fsx.File, error) {
	file, err := f.FS.OpenAppend(tag, p)
	if err != nil {
		return nil, err
	}
	return &flakyFile{File: file, fs: f}, nil
}

type flakyFile struct {
	fsx.File
	fs *flaky
}

func (f *flakyFile) Write(p []byte) (int, error) {
	if f.fs.fail.Load() {
		f.fs.hits.Add(1)
		return 0, fs.ErrPermission
	}
	return f.File.Write(p)
}

// A write the disk refuses is an error to the caller, which may drop it, and
// nothing else: the next write works, and the refused line is not in the file.
func TestAFailedWriteIsAnErrorAndTheNextWriteWorks(t *testing.T) {
	d := newDisk(t)
	bad := &flaky{FS: d.mem}
	h := logfile.New(bad, fixed(1<<20))
	f, err := h.Open("daemon.log")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write([]byte("before\n")); err != nil {
		t.Fatal(err)
	}
	bad.fail.Store(true)
	if _, err := f.Write([]byte("lost\n")); err == nil {
		t.Fatal("a write that the disk refused was reported as made")
	}
	if bad.hits.Load() == 0 {
		t.Fatal("the injected fault was never hit")
	}
	bad.fail.Store(false)
	if _, err := f.Write([]byte("after\n")); err != nil {
		t.Fatalf("the write after the failure: %v", err)
	}
	if got := d.read(t, "logs/daemon.log"); got != "before\nafter\n" {
		t.Errorf("the log holds %q, want the lines that were written", got)
	}
}

// A rotation that cannot be made leaves the log with the file it had: the line is
// refused, nothing is lost from the file, and once the threshold allows, writing
// goes on to the same file.
func TestAFailedRotationLeavesTheLogWritable(t *testing.T) {
	d := newDisk(t)
	faulty := fsxtest.NewFaulty(t, d.mem)
	rule := faulty.Fail(fsxtest.Match{Tx: fsx.TxLog, Step: fsx.StepRename}, fs.ErrPermission)
	limit := int64(30)
	h := logfile.New(faulty, threshold(func() int64 { return limit }))
	f, err := h.Open("daemon.log")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write([]byte("first line of it\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("second line of it\n")); err == nil {
		t.Fatal("a line was reported written when the rotation it needed failed")
	}
	if rule.Hits() == 0 {
		t.Fatal("the injected fault was never hit")
	}
	limit = 1 << 20
	if _, err := f.Write([]byte("third\n")); err != nil {
		t.Fatalf("writing after the failed rotation: %v", err)
	}
	if got := d.read(t, "logs/daemon.log"); got != "first line of it\nthird\n" {
		t.Errorf("the log holds %q", got)
	}
}

// The rule that shares the budget among the logs lives in the handle: a log opened
// by name is rotated at the size the budget gives for the instances there are when
// it is written, and every log of the handle follows the same budget, so whoever
// opens a log (the daemon, an adapter host) never counts instances itself. The
// budget here is 1200 bytes and no floor: the daemon alone has 600 bytes for each of
// two files, one instance 300, five instances 100.
func TestALogIsRotatedAtTheShareOfTheBudgetForTheInstancesThereAre(t *testing.T) {
	d := newDisk(t)
	instances := 0
	h := logfile.New(d.fs, logfile.Budget{
		Total:     func() int64 { return 1200 },
		Instances: func() int { return instances },
		Floor:     1,
	})
	daemon, err := h.Open("daemon.log")
	if err != nil {
		t.Fatal(err)
	}
	defer daemon.Close()
	adapter, err := h.Open("adapter-chat.log")
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	logs := []*logfile.Log{daemon, adapter}
	rotations := []string{"logs/daemon.log.1", "logs/adapter-chat.log.1"}
	line := []byte(strings.Repeat("x", 59) + "\n")
	write := func(n int) {
		t.Helper()
		for _, l := range logs {
			for i := 0; i < n; i++ {
				if _, err := l.Write(line); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	// No instance: 600 bytes a file, so 120 bytes are far from it.
	write(2)
	for _, p := range rotations {
		if d.exists(p) {
			t.Errorf("%s exists after 120 bytes under a threshold of 600", p)
		}
	}
	// Five instances: 100 bytes a file. The next line takes the file past it.
	instances = 5
	write(1)
	for _, p := range rotations {
		if !d.exists(p) {
			t.Errorf("%s is missing: with 5 instances the threshold is 100 bytes", p)
		}
	}
	// No instance again: the new file takes 600 bytes before it rotates.
	instances = 0
	before := d.read(t, rotations[0])
	write(5)
	if got := d.read(t, rotations[0]); got != before {
		t.Errorf("the log rotated at the old share after the instances were gone: %q", got)
	}
}

// A budget with no instance source counts none, and one with no floor never goes
// below MinThreshold, so a small total does not rotate a log at every line.
func TestTheBudgetDefaultsToNoInstancesAndToTheMinimumThreshold(t *testing.T) {
	d := newDisk(t)
	h := logfile.New(d.fs, logfile.Budget{Total: func() int64 { return 10 }})
	f, err := h.Open("daemon.log")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for i := 0; i < 4; i++ {
		if _, err := f.Write([]byte(strings.Repeat("x", 1000) + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	if d.exists("logs/daemon.log.1") {
		t.Error("a log was rotated below MinThreshold")
	}
	if logfile.MinThreshold != 1<<20 {
		t.Errorf("MinThreshold = %d, want 1 MiB", logfile.MinThreshold)
	}
}

// A log with no total to share has no threshold, which is a mistake of the caller
// that shows at once and not at the first rotation.
func TestAHandleWithoutATotalOpensNoLog(t *testing.T) {
	d := newDisk(t)
	h := logfile.New(d.fs, logfile.Budget{})
	if f, err := h.Open("daemon.log"); err == nil {
		f.Close()
		t.Fatal("a log was opened by a handle that has no budget")
	}
	if d.exists("logs/daemon.log") {
		t.Error("a refused log left a file behind")
	}
}
