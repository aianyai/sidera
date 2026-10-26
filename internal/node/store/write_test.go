package store_test

import (
	"errors"
	"path"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/handoff"
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
			[]string{path.Join(store.OutDir, srcOut(1, true).Name()), bodyPath(srcOut(1, true))}},
		{"deliver", nil, func(r *rig) error { return r.st.Deliver(bodyBytes(1), []store.InRecord{tgtIn(1)}) },
			[]string{path.Join(store.InDir, tgtIn(1).Name()), bodyPath(tgtIn(1))}},
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
		{"orphan-gc", func(t *testing.T, mem *fsxtest.Mem) { seedBodyOf(t, mem, tgtIn(1), bodyBytes(1)) },
			func(r *rig) error { return r.st.OrphanGC([]string{store.BodyFileName(tgtIn(1).Name())}) }, nil},
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

// DeliverReply settles bodies the way the other writes do: the records of the
// reply get a body file of their own before they are written, and the source
// record of the message answered, when the evidence ended its last delivery,
// loses its own body file in the same transaction, after the records. An update
// that keeps the reference keeps the file.
func TestDeliverReplyRemovesTheBodyOfTheSourceRecordThatDroppedItsRef(t *testing.T) {
	for _, c := range []struct {
		rule string
		out  store.OutRecord
		want bool // the body of the source record is still there
	}{
		{"the body of the source record is removed when the update drops its ref", finishedOut(1, true), false},
		{"the body of the source record stays when the update keeps its ref", srcOut(1, true), true},
	} {
		t.Run(c.rule, func(t *testing.T) {
			r := newRig(t)
			source := srcOut(1, true)
			seed(t, r.mem, source)
			seedBodies(t, r.mem, source)
			reply := tgtIn(2)
			out := c.out
			if err := r.st.DeliverReply(bodyBytes(2), &out, []store.InRecord{reply}); err != nil {
				t.Fatal(err)
			}
			_, err := r.mem.Stat(bodyPath(source))
			if (err == nil) != c.want {
				t.Errorf("the body of the source record is present = %v, want %v", err == nil, c.want)
			}
			if got, err := r.mem.ReadFile(bodyPath(reply)); err != nil || string(got) != string(bodyBytes(2)) {
				t.Errorf("the reply's record has its own body: %q, %v", got, err)
			}
			if got := r.scan().In; len(got) != 1 || got[0].Key != reply.Key {
				t.Errorf("target records after the call: %+v", got)
			}
			// The removal is in the transaction of the reply: its points are the
			// removal and the directory sync of that transaction, after the records.
			var steps []string
			for _, p := range r.rec.points {
				steps = append(steps, p.Tag.Tx+"/"+p.Tag.Role)
			}
			removed := false
			for _, s := range steps {
				removed = removed || s == "deliver-reply/body-dir"
			}
			if removed == c.want {
				t.Errorf("the directory of the bodies was synced = %v with the source body kept = %v (points %v)", removed, c.want, steps)
			}
		})
	}
}

// A write of a record that holds no reference succeeds when the file of its body
// is already gone: an earlier, interrupted transaction of the same write removed it.
func TestTheWriteOfARecordWithNoRefIsQuietWhenItsBodyIsAlreadyGone(t *testing.T) {
	r := newRig(t)
	seed(t, r.mem, srcOut(1, false))
	if err := r.st.SourceUpdate(srcOutFinished(1)); err != nil {
		t.Errorf("the body was already removed by an earlier, interrupted write: %v", err)
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
		"a session endpoint that the intent of the control record does not name": func(r *rig) error {
			c := sessionCtl(1, 5)
			c.Body = bodyRef(1)
			return r.st.ControlIntake(bodyBytes(1), ptr(session(6, store.EndpointCreating, ctlName(1))), c)
		},
		"a session endpoint with a control record that has no intent": func(r *rig) error {
			c := sessionCtl(1, 5)
			c.Intent, c.Body = nil, bodyRef(1)
			return r.st.ControlIntake(bodyBytes(1), ptr(session(5, store.EndpointCreating, ctlName(1))), c)
		},
		"a target record with an empty message id": func(r *rig) error {
			in := tgtIn(1)
			in.Key.Message = ""
			return r.st.Deliver(nil, []store.InRecord{in})
		},
		"a target record without the fields of a handout": func(r *rig) error {
			in := tgtIn(1)
			in.Handout = store.HandoutFields{}
			return r.st.Deliver(nil, []store.InRecord{in})
		},
		"a prompt whose target record is another endpoint's than its head's": func(r *rig) error {
			return r.st.PromptEnd(store.AttemptsRecord{Endpoint: ep(4)}, []store.InRecord{tgtIn(1)})
		},
		"a block whose target record is another endpoint's than its head's": func(r *rig) error {
			return r.st.Blocked(store.AttemptsRecord{Endpoint: ep(4)}, []store.InRecord{tgtIn(1)})
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
		"a source record of a sweep that has no sending endpoint": func(r *rig) error {
			o := srcOutFinished(1)
			o.Key.Sender = ident.EndpointID{}
			return r.st.ArchiveSweep(store.SweepChunk{Now: base, Outs: []store.OutRecord{o}})
		},
		"a source record among the records of a sweep": func(r *rig) error {
			return r.st.ArchiveSweep(store.SweepChunk{Now: base, Records: []store.FileRef{{Dir: store.OutDir, Name: srcOutFinished(1).Name()}}})
		},
		"an endpoint among the records of a sweep": func(r *rig) error {
			return r.st.ArchiveSweep(store.SweepChunk{Now: base, Records: []store.FileRef{{Dir: store.EndpointsDir, Name: session(5, store.EndpointClosed, "").Name()}}})
		},
		"an archive event without a file": func(r *rig) error {
			return r.st.ArchiveEvents(base, []store.ArchiveEvent{{At: base, Event: "x"}})
		},
		"an archive file name outside the archive": func(r *rig) error {
			return r.st.ArchiveExpire([]string{"../keys/identity.key"})
		},
		"an orphan body name that is not the name of a body file": func(r *rig) error {
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

// A body file is named after the record it belongs to, and no two records share
// one: two records that said the same bytes each have a file with a copy.
func TestBodyFilesAreNamedAfterTheirRecords(t *testing.T) {
	r := newRig(t)
	first, second := srcOut(1, true), srcOut(2, true)
	second.Body = bodyRef(1)
	mustDo(t, r.st.Intake(bodyBytes(1), first))
	mustDo(t, r.st.Intake(bodyBytes(1), second))
	want := []string{store.BodyFileName(first.Name()), store.BodyFileName(second.Name())}
	slices.Sort(want)
	got := slices.Clone(r.scan().Bodies)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("bodies = %v, want one file for each of the two records: %v", got, want)
	}
	for _, rec := range []store.Record{first, second} {
		if b, _ := r.mem.ReadFile(bodyPath(rec)); string(b) != string(bodyBytes(1)) {
			t.Errorf("the body file of %s is the bare content, got %q", rec.Name(), b)
		}
	}
}

// The name of a body file is the name of its record without the extension, then
// ".body": the hash of the key for an in/ or ctl/ record, and the sender then the
// hash of the key for an out/ record. The names are part of the layout on disk.
func TestTheNameOfABodyFileIsTheNameOfItsRecord(t *testing.T) {
	for _, c := range []struct {
		record store.Record
		want   string
	}{
		{tgtIn(1), "9217a562696c0e2029e1e14cca852cea2de0be95435a39f5921d9b797081f128.body"},
		{srcOut(1, true), "ep1zyg3zyg3zyg3zyg3zyg3zyg3zyj0kcgq.c911b2aa6f9940e8d3baa7c5ac0be241da57642b7a7a0a5b0b3de59a723ff1bc.body"},
	} {
		if got := store.BodyFileName(c.record.Name()); got != c.want {
			t.Errorf("BodyFileName(%s) = %q, want %q", c.record.Name(), got, c.want)
		}
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

// A prompt and a block write their recipients, made durable together, and the
// head record last: the head says the work is done, so it cannot be there while a
// recipient is not. With no recipient, the head is written alone.
func TestAPromptAndABlockWriteTheirRecipientsBeforeTheirHead(t *testing.T) {
	event := func(t *testing.T, ev delivery.Event, ins ...store.InRecord) []store.InRecord {
		for i := range ins {
			ins[i].Record = applied(t, ins[i].Record, ev)
		}
		return ins
	}
	notified := delivery.Event{Type: delivery.EventNotified}
	blocked := delivery.Event{Type: delivery.EventBlocked, Cause: delivery.CauseAdapterOffline}
	report := handoff.BlockedReport{Cause: handoff.CauseAdapterOffline, Since: base}
	for _, c := range []struct {
		name string
		run  func(t *testing.T, st *store.Store) error
		want []string // the roles written, a run of one role counted once
	}{
		{"a prompt", func(t *testing.T, st *store.Store) error {
			return st.PromptEnd(store.AttemptsRecord{Endpoint: ep(2)}, event(t, notified, tgtIn(1), tgtIn(2)))
		}, []string{"in", "in-dir", "attempts"}},
		{"a block", func(t *testing.T, st *store.Store) error {
			return st.Blocked(store.AttemptsRecord{Endpoint: ep(2), Blocked: report}, event(t, blocked, tgtIn(1), tgtIn(2)))
		}, []string{"in", "in-dir", "attempts"}},
		{"a prompt that reached nobody", func(t *testing.T, st *store.Store) error {
			return st.PromptEnd(store.AttemptsRecord{Endpoint: ep(2)}, nil)
		}, []string{"attempts"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			seedWithBodies(t, r.mem, tgtIn(1), tgtIn(2))
			var got []string
			r.rec.on = func(p fsx.Point, _ txn.Kind) {
				if role := p.Tag.Role; role != "" && (len(got) == 0 || got[len(got)-1] != role) {
					got = append(got, role)
				}
			}
			mustDo(t, c.run(t, r.st))
			if !slices.Equal(got, c.want) {
				t.Errorf("the roles written, in order, are %v, want %v", got, c.want)
			}
		})
	}
}
