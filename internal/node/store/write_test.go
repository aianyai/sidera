package store_test

import (
	"errors"
	"path"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/txn"
)

// Each method opens the transaction of its own name, and writes only the
// files the table says; a write that the declaration does not name panics.
func TestEveryMethodRunsItsOwnTransaction(t *testing.T) {
	closed7 := directEndpoint(7, "chat")
	closed7.Status, closed7.Token = store.EndpointClosed, nil
	for _, c := range []struct {
		tx    string
		setup func(t *testing.T, mem *fsxtest.Mem)
		run   func(r *rig) error
		files []string // paths that exist afterwards
	}{
		{"intake", nil, func(r *rig) error { return r.st.Intake(bodyBytes(1), srcOut(1, true)) },
			[]string{path.Join(store.OutDir, srcOut(1, true).Name()), path.Join(store.BodiesDir, bodyRef(1).Name())}},
		{"deliver", nil, func(r *rig) error { return r.st.Deliver(bodyBytes(1), []store.InRecord{tgtIn(1)}) },
			[]string{path.Join(store.InDir, tgtIn(1).Name()), path.Join(store.BodiesDir, bodyRef(1).Name())}},
		{"control-reply", nil, func(r *rig) error { return r.st.ControlReply(outCtlRecord(1)) },
			[]string{path.Join(store.OutDir, outCtlRecord(1).Name())}},
		{"prompt-begin", nil, func(r *rig) error { return r.st.PromptBegin(store.AttemptsRecord{Endpoint: ep(2)}) },
			[]string{path.Join(store.AttemptsDir, store.AttemptsRecord{Endpoint: ep(2)}.Name())}},
		{"endpoint-update", nil, func(r *rig) error { return r.st.EndpointUpdate(directEndpoint(7, "chat")) },
			[]string{path.Join(store.EndpointsDir, directEndpoint(7, "chat").Name())}},
		{"endpoint-close", nil, func(r *rig) error { res := adminResult(1); return r.st.EndpointClose(closed7, &res) },
			[]string{path.Join(store.EndpointsDir, closed7.Name()), path.Join(store.AdminDir, adminResult(1).Name())}},
		{"checkpoint-put", nil, func(r *rig) error {
			return r.st.CheckpointPut(store.CheckpointRecord{Key: store.CheckpointKey{Scope: "session", ID: ep(5).String()}, State: []byte(`{}`)})
		}, nil},
		{"alert-raise", nil, func(r *rig) error {
			return r.st.AlertRaise(store.AlertRecord{ID: store.AlertID{1}, Kind: "k", At: base, Detail: "d"})
		}, nil},
		{"daemon-stop", nil, func(r *rig) error { return r.st.DaemonStop(adminResult(2)) },
			[]string{path.Join(store.AdminDir, adminResult(2).Name())}},
		{"archive-events", nil, func(r *rig) error { return r.st.ArchiveEvents(base, []store.ArchiveEvent{eventAccepted()}) },
			[]string{dayFile}},
		{"feedback-form", nil, func(r *rig) error {
			o := srcOut(1, false)
			o.Feedback = []delivery.Item{feedbackItem(1)}
			return r.st.FeedbackForm([]byte("feedback wording"), noticeFor(o))
		}, nil},
		{"orphan-gc", func(t *testing.T, mem *fsxtest.Mem) { seedBodies(t, mem, bodyBytes(1)) },
			func(r *rig) error { return r.st.OrphanGC([]string{bodyRef(1).Name()}) }, nil},
		{"archive-expire", func(t *testing.T, mem *fsxtest.Mem) {
			seedRaw(t, mem, store.ArchiveDir, "2026-01-01.jsonl", []byte("\n"))
		},
			func(r *rig) error { return r.st.ArchiveExpire([]string{"2026-01-01.jsonl"}) }, nil},
	} {
		t.Run(c.tx, func(t *testing.T) {
			r := newRig(t)
			if c.setup != nil {
				c.setup(t, r.mem)
			}
			if err := c.run(r); err != nil {
				t.Fatal(err)
			}
			if r.rec.count() == 0 {
				t.Fatal("no crash point was reached")
			}
			for _, p := range r.rec.points {
				if p.Tag.Tx != c.tx {
					t.Errorf("the point %v belongs to another transaction than %s", p, c.tx)
				}
			}
			for _, p := range c.files {
				if _, err := r.mem.Stat(p); err != nil {
					t.Errorf("%s: %v", p, err)
				}
			}
		})
	}
}

// Target records carry a revision that the store maintains: the first write is
// revision 1 and each write after a read adds one.
func TestTargetRecordsAreWrittenWithTheNextRevision(t *testing.T) {
	r := newRig(t)
	mustDo(t, r.st.Deliver(nil, []store.InRecord{tgtIn(1)}))
	sc := r.scan()
	if len(sc.In) != 1 || sc.In[0].Rev != 1 {
		t.Fatalf("after Deliver: %+v", sc.In)
	}
	in := sc.In[0]
	in.Record.Lease.Handouts = 1
	mustDo(t, r.st.Lease([]store.InRecord{in}))
	in = r.scan().In[0]
	if in.Rev != 2 || in.Record.Lease.Handouts != 1 {
		t.Errorf("after Lease: rev %d, handouts %d, want 2 and 1", in.Rev, in.Record.Lease.Handouts)
	}
	in.Record.State = delivery.StateDelivered
	mustDo(t, r.st.Ack([]store.InRecord{in}, nil))
	if got := r.scan().In[0].Rev; got != 3 {
		t.Errorf("after Ack: rev %d, want 3", got)
	}
	// A record written again from an older read (revision 2) gets that revision
	// plus one: the revision tells that a write happened, it does not order writers.
	mustDo(t, r.st.Lease([]store.InRecord{in}))
	if got := r.scan().In[0].Rev; got != 3 {
		t.Errorf("after a write of an older read: rev %d, want 3", got)
	}
}

func TestReleaseOfABodyThatIsAlreadyGoneIsQuiet(t *testing.T) {
	r := newRig(t)
	seed(t, r.mem, srcOut(1, false))
	if err := r.st.SourceUpdate(srcOutFinished(1), []store.BodyRef{*bodyRef(1)}); err != nil {
		t.Errorf("the body was already removed by an earlier, interrupted release: %v", err)
	}
}

// What cannot be encoded is refused before a transaction is opened: nothing is
// written and no crash point is reached.
func TestRecordsThatCannotBeEncodedAreRefusedBeforeAnyWrite(t *testing.T) {
	bad := map[string]func(r *rig) error{
		"an out record that is lost and regular at once": func(r *rig) error {
			o := srcOut(1, false)
			o.Lost = &store.LostOut{DiscoveredAt: base, Deliveries: nil}
			return r.st.Intake(bodyBytes(1), o)
		},
		"an out record whose message is not the key's": func(r *rig) error {
			o := srcOut(1, false)
			o.Outbound.Message = "other"
			return r.st.Intake(bodyBytes(1), o)
		},
		"an out record whose envelope recipients are not the key's": func(r *rig) error {
			o := srcOut(1, false)
			o.Envelope.To = []ident.Address{addr(3, 3)}
			return r.st.Intake(bodyBytes(1), o)
		},
		"an out record with a body reference over the limit": func(r *rig) error {
			o := srcOut(1, false)
			o.Body = &store.BodyRef{Len: 1 << 30}
			return r.st.Intake(bodyBytes(1), o)
		},
		"a target record with an empty message id": func(r *rig) error {
			in := tgtIn(1)
			in.Key.Message = ""
			return r.st.Deliver(nil, []store.InRecord{in})
		},
		"a target record that is lost and has a body": func(r *rig) error {
			in := tgtIn(1)
			in.Lost = true
			return r.st.Deliver(nil, []store.InRecord{in})
		},
		"an open endpoint without a token": func(r *rig) error {
			e := directEndpoint(7, "chat")
			e.Token = nil
			return r.st.EndpointUpdate(e)
		},
		"a closed endpoint with a token": func(r *rig) error {
			e := directEndpoint(7, "chat")
			e.Status = store.EndpointClosed
			return r.st.EndpointUpdate(e)
		},
		"a checkpoint state that is not an object": func(r *rig) error {
			return r.st.CheckpointPut(store.CheckpointRecord{Key: store.CheckpointKey{Scope: "session", ID: "x"}, State: []byte(`[1]`)})
		},
		"a checkpoint state over 16 KiB": func(r *rig) error {
			return r.st.CheckpointPut(store.CheckpointRecord{Key: store.CheckpointKey{Scope: "session", ID: "x"}, State: []byte(`{"a":"` + strings.Repeat("x", 17<<10) + `"}`)})
		},
		"an alert kind in capitals": func(r *rig) error {
			return r.st.AlertRaise(store.AlertRecord{ID: store.AlertID{1}, Kind: "Corrupt", At: base, Detail: "d"})
		},
		"an alert with the zero id": func(r *rig) error {
			return r.st.AlertRaise(store.AlertRecord{Kind: "corrupt", At: base, Detail: "d"})
		},
		"a floor of zero": func(r *rig) error {
			return r.st.ArchiveSweep(store.SweepChunk{Now: base, Floors: []store.FloorWrite{{Sender: ep(1), Floors: map[ident.Address]uint64{addr(2, 2): 0}}}})
		},
		"an archive event without a file": func(r *rig) error {
			return r.st.ArchiveEvents(base, []store.ArchiveEvent{{At: base, Event: "x"}})
		},
		"an archive file name outside the archive": func(r *rig) error {
			return r.st.ArchiveExpire([]string{"../keys/identity.key"})
		},
		"an orphan body name that is not a digest": func(r *rig) error {
			return r.st.OrphanGC([]string{"../config/node.json"})
		},
	}
	for name, run := range bad {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			before := snapshot(t, r.mem, ".")
			if err := run(r); err == nil {
				t.Fatal("accepted")
			}
			if r.rec.count() != 0 {
				t.Errorf("%d crash points were reached by a call that was refused", r.rec.count())
			}
			if after := snapshot(t, r.mem, "."); len(after) != len(before) {
				t.Errorf("the disk changed: %v", keysOf(after))
			}
		})
	}
}

// A failure keeps the class the transaction layer gave it: before the commit
// point a failed write is an ordinary failure, a failed directory sync is
// fatal, and the method does not wrap it into anything else.
func TestWriteErrorsKeepTheirClass(t *testing.T) {
	for _, c := range []struct {
		name  string
		match fsxtest.Match
		fatal bool
	}{
		{"a failed sync of the temporary file", fsxtest.Match{Tx: "intake", Role: "out", Step: fsx.StepSync, Hit: 1}, false},
		{"a failed write of the body", fsxtest.Match{Tx: "intake", Role: "body", Step: fsx.StepWrite, Hit: 1}, false},
		{"a failed rename before the commit point", fsxtest.Match{Tx: "intake", Role: "out", Step: fsx.StepRename, Hit: 1}, false},
		{"a failed sync of the directory", fsxtest.Match{Tx: "intake", Role: "out", Step: fsx.StepSyncDir, Hit: 1}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			f := newFaultyRig(t, r.mem)
			f.faulty.Fail(c.match, syscall.EIO)
			err := f.st.Intake(bodyBytes(1), srcOut(1, true))
			var te *txn.Error
			if !errors.As(err, &te) {
				t.Fatalf("error %v is not a *txn.Error", err)
			}
			if txn.IsFatal(err) != c.fatal {
				t.Errorf("IsFatal = %v, want %v", txn.IsFatal(err), c.fatal)
			}
			if !errors.Is(err, syscall.EIO) {
				t.Errorf("the system error is lost: %v", err)
			}
		})
	}
}

func TestBodiesAreContentAddressed(t *testing.T) {
	r := newRig(t)
	mustDo(t, r.st.Intake(bodyBytes(1), srcOut(1, true)))
	out2 := srcOut(2, true)
	out2.Body = bodyRef(1)
	mustDo(t, r.st.Intake(bodyBytes(1), out2))
	sc := r.scan()
	if len(sc.Bodies) != 1 || sc.Bodies[0] != bodyRef(1).Name() {
		t.Fatalf("bodies = %v, want the one file of the digest", sc.Bodies)
	}
	if got, _ := r.mem.ReadFile(path.Join(store.BodiesDir, bodyRef(1).Name())); string(got) != string(bodyBytes(1)) {
		t.Errorf("a body file is the bare content, got %q", got)
	}
	if want := "ea8c004183bd3bd33e27907ed5cb35a24808cb99ab2bb41a727d0c675f5bcdfc"; bodyRef(1).Name() != want {
		t.Errorf("the name of a body is the lowercase hex SHA-256 of its content: %q, want %q", bodyRef(1).Name(), want)
	}
}

func TestEveryFileIsOwnerOnly(t *testing.T) {
	r := newRig(t)
	mustDo(t, r.st.Intake(bodyBytes(1), srcOut(1, true)))
	mustDo(t, r.st.ArchiveEvents(base, []store.ArchiveEvent{eventAccepted()}))
	for p := range snapshot(t, r.mem, store.StateDir) {
		info, err := r.mem.Stat(p)
		if err != nil || !info.OwnerOnly {
			t.Errorf("%s is not owner-only: %+v %v", p, info, err)
		}
	}
}

// A head record lists its followers as they were read, and the followers written
// with it must be those: a revision or an acceptance time that is not the listed
// one would never be replayed, and a follower that is not listed would be lost.
func TestHeadRecordRefusesAFollowerItDoesNotListAsItWasRead(t *testing.T) {
	a, b := inRev(1, 1), inRev(2, 1)
	event := delivery.Event{Type: delivery.EventNotified}
	notify := func(in store.InRecord) store.InRecord {
		in.Record, _, _ = in.Record.Apply(base, event)
		return in
	}
	head := store.AttemptsRecord{Endpoint: ep(2), Pending: pendingOf(store.PendingNotified, delivery.CauseNone, a)}
	for name, run := range map[string]func(r *rig) error{
		"a follower that is not listed": func(r *rig) error { return r.st.PromptEnd(head, []store.InRecord{notify(a), notify(b)}) },
		"a follower with another revision than the one listed": func(r *rig) error {
			stale := notify(a)
			stale.Rev = 5
			return r.st.PromptEnd(head, []store.InRecord{stale})
		},
		"a follower accepted at another time than the one listed": func(r *rig) error {
			other := notify(a)
			other.AcceptedAt = other.AcceptedAt.Add(time.Second)
			return r.st.PromptEnd(head, []store.InRecord{other})
		},
		"an event the transaction does not carry": func(r *rig) error {
			blocked := head
			blocked.Pending.Event = store.PendingBlocked
			return r.st.PromptEnd(blocked, []store.InRecord{notify(a)})
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			if err := run(r); err == nil {
				t.Error("accepted")
			}
			if r.rec.count() != 0 {
				t.Errorf("%d crash points were reached by a call that was refused", r.rec.count())
			}
		})
	}
	r := newRig(t)
	mustDo(t, r.st.PromptEnd(head, []store.InRecord{notify(a)}))
}
