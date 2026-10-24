package txn_test

import (
	"sync"
	"syscall"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/txn"
)

// closeLog wraps a file system so that the tests can see when the handles of
// appended files are closed.
type closeLog struct {
	fsx.FS
	mu     sync.Mutex
	opened int
	closed int
}

func (c *closeLog) OpenAppend(tag fsx.Tag, p string) (fsx.File, error) {
	f, err := c.FS.OpenAppend(tag, p)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.opened++
	c.mu.Unlock()
	return &loggedFile{File: f, log: c}, nil
}

func (c *closeLog) counts() (opened, closed int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.opened, c.closed
}

type loggedFile struct {
	fsx.File
	log *closeLog
}

func (f *loggedFile) Close() error {
	f.log.mu.Lock()
	f.log.closed++
	f.log.mu.Unlock()
	return f.File.Close()
}

// A failed append drops the handle: the file is closed at once, no byte is
// written to it by the transaction afterwards, and the next transaction opens
// the file again rather than reusing the handle.
func TestFailedAppendDropsTheHandle(t *testing.T) {
	var faulty *fsxtest.Faulty
	var log *closeLog
	r := newRig(t, func(m *fsxtest.Mem) fsx.FS {
		faulty = fsxtest.NewFaulty(t, m)
		log = &closeLog{FS: faulty}
		return log
	}, eventsOnly)
	faulty.FailShortWrite(fsxtest.Match{Tx: "events", Step: fsx.StepWrite, Hit: 2}, 3, syscall.ENOSPC)

	tx := r.w.Begin(eventsOnly)
	defer tx.Close()
	if err := tx.Append("events", "a/events", []byte("first\n")); err != nil {
		t.Fatal(err)
	}
	if opened, closed := log.counts(); opened != 1 || closed != 0 {
		t.Fatalf("after the first append: %d opened, %d closed; want 1 and 0", opened, closed)
	}
	failure := tx.Append("events", "a/events", []byte("second\n"))
	if failure == nil {
		t.Fatal("injected failure did not surface")
	}
	if opened, closed := log.counts(); opened != 1 || closed != 1 {
		t.Errorf("after the failed append: %d opened, %d closed; want the handle closed at once", opened, closed)
	}

	// The Tx is failed: later appends return the error and write nothing.
	before := r.rec.count()
	if err := tx.Append("events", "a/events", []byte("third\n")); err != failure {
		t.Errorf("a later append returned %v, want the first error", err)
	}
	if err := tx.Commit(); err != failure {
		t.Errorf("Commit returned %v, want the first error", err)
	}
	if after := r.rec.count(); after != before {
		t.Errorf("%d points were reached after the failed append: %v", after-before, r.rec.names()[before:])
	}
	if opened, closed := log.counts(); opened != 1 || closed != 1 {
		t.Errorf("after Commit: %d opened, %d closed; a handle is closed once", opened, closed)
	}
	tx.Close()
	if opened, closed := log.counts(); opened != 1 || closed != 1 {
		t.Errorf("after Close: %d opened, %d closed; a handle is closed once", opened, closed)
	}

	// The next transaction opens the file again, and its first bytes follow
	// whatever the failed one left: the partial record is the reader's to skip.
	next := r.w.Begin(eventsOnly)
	defer next.Close()
	if err := next.Append("events", "a/events", []byte("\nfourth\n")); err != nil {
		t.Fatal(err)
	}
	if err := next.Commit(); err != nil {
		t.Fatal(err)
	}
	if opened, closed := log.counts(); opened != 2 || closed != 2 {
		t.Errorf("after the next transaction: %d opened, %d closed; want a new open", opened, closed)
	}
	var opens []string
	for _, n := range r.rec.names() {
		if n == "events/events.open#1" || n == "events/events.open#2" {
			opens = append(opens, n)
		}
	}
	if len(opens) != 2 {
		t.Errorf("open points = %v, want one per transaction", opens)
	}
	if got := snapshot(t, r.mem)["a/events"]; got != "first\nsec\nfourth\n" {
		t.Errorf("file = %q, want the first record, the partial one the failed append left, and the next transaction's", got)
	}
}

// A failure to open leaves no handle to close, and a failed sync closes the
// handle as well.
func TestFailedOpenAndFailedSyncFileLeaveNoOpenHandle(t *testing.T) {
	for _, c := range []struct {
		name  string
		match fsxtest.Match
		run   func(tx *txn.Tx) error
	}{
		{"open", fsxtest.Match{Tx: "sweep", Role: "digest", Step: fsx.StepOpen},
			func(tx *txn.Tx) error { return tx.Append("digest", "a/digest", []byte("d")) }},
		{"sync-file", fsxtest.Match{Tx: "sweep", Role: "digest", Step: fsx.StepSync},
			func(tx *txn.Tx) error {
				if err := tx.Append("digest", "a/digest", []byte("d")); err != nil {
					return err
				}
				return tx.SyncFile("digest", "a/digest")
			}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var faulty *fsxtest.Faulty
			var log *closeLog
			r := newRig(t, func(m *fsxtest.Mem) fsx.FS {
				faulty = fsxtest.NewFaulty(t, m)
				log = &closeLog{FS: faulty}
				return log
			}, sweepLike)
			faulty.Fail(c.match, syscall.EIO)
			tx := r.w.Begin(sweepLike)
			if err := c.run(tx); err == nil {
				t.Fatal("injected failure did not surface")
			}
			if opened, closed := log.counts(); opened != closed {
				t.Errorf("right after the failure: %d handles opened, %d closed", opened, closed)
			}
			tx.Close()
			if opened, closed := log.counts(); opened != closed {
				t.Errorf("after Close: %d handles opened, %d closed", opened, closed)
			}
		})
	}
}

// Handles of a transaction that ends are closed, by Commit and by Close, and
// a file appended under one role is one handle however many appends.
func TestHandlesAreClosedWhenTheTransactionEnds(t *testing.T) {
	for _, ending := range []string{"commit", "close"} {
		t.Run(ending, func(t *testing.T) {
			var log *closeLog
			r := newRig(t, func(m *fsxtest.Mem) fsx.FS { log = &closeLog{FS: m}; return log }, eventsOnly)
			tx := r.w.Begin(eventsOnly)
			for i := 0; i < 3; i++ {
				if err := tx.Append("events", "a/events", []byte("e\n")); err != nil {
					t.Fatal(err)
				}
			}
			if err := tx.Append("events", "a/other", []byte("e\n")); err != nil {
				t.Fatal(err)
			}
			if opened, closed := log.counts(); opened != 2 || closed != 0 {
				t.Fatalf("while open: %d opened, %d closed; want 2 and 0", opened, closed)
			}
			if ending == "commit" {
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
			} else {
				tx.Close()
			}
			if opened, closed := log.counts(); opened != 2 || closed != 2 {
				t.Errorf("after %s: %d opened, %d closed; want both closed", ending, opened, closed)
			}
		})
	}
}

// The sync of an appended file goes through the handle that appended it: the
// content appended before the sync is durable after it, and the steps of the
// sync are attributed to the appending role.
func TestSyncFileMakesWhatWasAppendedDurableUnderTheAppendingRole(t *testing.T) {
	r := newRig(t, nil, sweepLike)
	tx := r.w.Begin(sweepLike)
	defer tx.Close()
	for _, err := range []error{
		tx.Append("digest", "a/digest", []byte("d1\n")),
		tx.Append("digest", "a/digest", []byte("d2\n")),
		tx.SyncFile("digest", "a/digest"),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	want := []string{
		"sweep/digest.open#1", "sweep/digest.write#1", "sweep/digest.write#2",
		"sweep/digest.sync#1", "sweep/digest.sync-dir#1",
	}
	got := r.rec.names()
	crashed := r.mem.CrashCloneWorst()
	// The rest of the declaration: the sync is the commit point, so the Tx is
	// ended the way every owner ends one.
	put(t, r.mem, map[string]string{"a/old": "x"})
	for _, err := range []error{tx.Remove("old", "a/old"), tx.SyncDir("old-dir", "a"), tx.Commit()} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("points = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("point %d = %s, want %s", i, got[i], want[i])
		}
	}
	if content := snapshot(t, crashed)["a/digest"]; content != "d1\nd2\n" {
		t.Errorf("after a power loss the digest is %q", content)
	}
}
