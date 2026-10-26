package store_test

import (
	"reflect"
	"syscall"
	"testing"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/store"
)

// A write of target records returns them as written: one revision on. The expectation is the disk read back by a new scan,
// never the function's own computation.

// writtenCase is one write function, the records it is given and what it returns.
type writtenCase struct {
	name string
	// setup seeds the disk before the call.
	setup func(t *testing.T, mem *fsxtest.Mem)
	// ins are the records the call is given; the call returns them as written, in
	// the same order.
	ins func() []store.InRecord
	// call makes the write.
	call func(r *rig, ins []store.InRecord) ([]store.InRecord, error)
}

// deliveredWithBody is a delivered target record that still refers to its body,
// which a record that no longer needs it drops when the body is damaged.
func deliveredWithBody(i int) store.InRecord {
	in := inRev(i, 1)
	in.Record = deliveredRecord(in.Record)
	return in
}

func writtenCases() []writtenCase {
	one := func(i int) func() []store.InRecord {
		return func() []store.InRecord { return []store.InRecord{inRev(i, 1), withRev(inRev(i+1, 4), 4)} }
	}
	mixed := func() []store.InRecord {
		return []store.InRecord{inRev(1, 1), inRev(2, 1), inRev(3, 7)}
	}
	seedIns := func(ins ...int) func(t *testing.T, mem *fsxtest.Mem) {
		return func(t *testing.T, mem *fsxtest.Mem) {
			for _, i := range ins {
				seed(t, mem, inRev(i, 1))
			}
		}
	}
	// copiesOfBody1 makes every record refer to the one body a call gives: each gets
	// its own file with a copy of it.
	copiesOfBody1 := func(ins func() []store.InRecord) func() []store.InRecord {
		return func() []store.InRecord {
			recs := ins()
			for i := range recs {
				recs[i].Body = bodyRef(1)
			}
			return recs
		}
	}
	closed8 := directEndpoint(8, "chat")
	closed8.Status, closed8.Token = store.EndpointClosed, nil
	return []writtenCase{
		{name: "Deliver", ins: copiesOfBody1(mixed), call: func(r *rig, ins []store.InRecord) ([]store.InRecord, error) {
			return r.st.Deliver(bodyBytes(1), ins)
		}},
		{name: "DeliverReply", ins: copiesOfBody1(one(1)), call: func(r *rig, ins []store.InRecord) ([]store.InRecord, error) {
			return r.st.DeliverReply(bodyBytes(1), nil, ins)
		}},
		{name: "PromptEnd", ins: func() []store.InRecord { return []store.InRecord{inRev(1, 2), inRev(2, 5)} },
			call: func(r *rig, ins []store.InRecord) ([]store.InRecord, error) {
				head := store.AttemptsRecord{Endpoint: ep(2), Pending: pendingOf(store.PendingNotified, delivery.CauseNone, ins...)}
				return r.st.PromptEnd(head, ins)
			}},
		{name: "Blocked", ins: func() []store.InRecord { return []store.InRecord{inRev(1, 3), inRev(2, 9)} },
			call: func(r *rig, ins []store.InRecord) ([]store.InRecord, error) {
				head := store.AttemptsRecord{Endpoint: ep(2), Pending: pendingOf(store.PendingBlocked, delivery.CauseAdapterOffline, ins...)}
				return r.st.Blocked(head, ins)
			}},
		{name: "Lease", ins: one(1), call: func(r *rig, ins []store.InRecord) ([]store.InRecord, error) {
			return r.st.Lease(ins)
		}},
		{name: "Ack", ins: one(1), call: func(r *rig, ins []store.InRecord) ([]store.InRecord, error) {
			return r.st.Ack(ins)
		}},
		{name: "Tick", ins: mixed, call: func(r *rig, ins []store.InRecord) ([]store.InRecord, error) {
			return r.st.Tick(store.TickBatch{In: ins})
		}},
		{name: "EndpointDrain", ins: mixed, call: func(r *rig, ins []store.InRecord) ([]store.InRecord, error) {
			return r.st.EndpointDrain(store.DrainBatch{In: ins})
		}},
		{name: "InstanceDrain", ins: mixed, call: func(r *rig, ins []store.InRecord) ([]store.InRecord, error) {
			return r.st.InstanceDrain([]store.EndpointRecord{closed8}, store.DrainBatch{In: ins})
		}},
		{name: "EndpointDrainReplay", ins: mixed, call: func(r *rig, ins []store.InRecord) ([]store.InRecord, error) {
			return r.st.EndpointDrainReplay(store.DrainBatch{In: ins})
		}},
		{name: "InstanceDrainReplay", ins: mixed, call: func(r *rig, ins []store.InRecord) ([]store.InRecord, error) {
			return r.st.InstanceDrainReplay([]store.EndpointRecord{closed8}, store.DrainBatch{In: ins})
		}},
		{name: "PromptReplay", setup: seedIns(1, 2), ins: one(1), call: func(r *rig, ins []store.InRecord) ([]store.InRecord, error) {
			return r.st.PromptReplay(ins)
		}},
		{name: "BlockedReplay", setup: seedIns(1, 2), ins: one(1), call: func(r *rig, ins []store.InRecord) ([]store.InRecord, error) {
			return r.st.BlockedReplay(ins)
		}},
		{name: "QuarantineBody", setup: func(t *testing.T, mem *fsxtest.Mem) { seed(t, mem, deliveredWithBody(1)) },
			ins: func() []store.InRecord { return []store.InRecord{deliveredWithBody(1)} },
			call: func(r *rig, ins []store.InRecord) ([]store.InRecord, error) {
				alert := store.QuarantineAlert{Kind: "corrupt_body", At: base, Detail: "d"}
				got, err := r.st.QuarantineBody(store.BodyQuarantine{Record: ins[0], Alert: alert})
				if got == nil {
					return nil, err
				}
				return []store.InRecord{*got}, err
			}},
		{name: "FeedbackForm", ins: func() []store.InRecord { return []store.InRecord{noticeFor(srcOut(40, false))} },
			call: func(r *rig, ins []store.InRecord) ([]store.InRecord, error) {
				got, err := r.st.FeedbackForm([]byte("feedback wording"), ins[0])
				if err != nil && reflect.DeepEqual(got, store.InRecord{}) {
					return nil, err // the zero record is how this one returns nothing
				}
				return []store.InRecord{got}, err
			}},
	}
}

// TestWritesReturnTheTargetRecordsAsAScanReadsThemBack covers every write
// function that changes the stored form of a target record, so that a caller
// keeping a view never works the revision out itself.
func TestWritesReturnTheTargetRecordsAsAScanReadsThemBack(t *testing.T) {
	for _, c := range writtenCases() {
		t.Run(c.name, func(t *testing.T) {
			mem := (&scenario{setup: c.setup}).fresh(t)
			r := openRig(t, mem)
			ins := c.ins()
			given := append([]store.InRecord(nil), ins...)
			written, err := c.call(r, ins)
			if err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			if len(written) != len(given) {
				t.Fatalf("returned %d records for %d given", len(written), len(given))
			}
			sc := r.scan() // a new scan: the disk, not the call
			for i, w := range written {
				wantRev := given[i].Rev + 1
				if w.Key != given[i].Key || w.Rev != wantRev {
					t.Errorf("record %d: returned %s at revision %d, want %s at revision %d", i, w.Key, w.Rev, given[i].Key, wantRev)
				}
				var read *store.InRecord
				for j := range sc.In {
					if sc.In[j].Key == given[i].Key {
						read = &sc.In[j]
					}
				}
				if read == nil {
					t.Fatalf("record %d: %s is not on the disk", i, given[i].Key)
				}
				if !reflect.DeepEqual(*read, w) {
					t.Errorf("record %d: returned %#v, the scan reads %#v", i, w, *read)
				}
			}
			if !reflect.DeepEqual(ins, given) {
				t.Errorf("the records given were changed")
			}
		})
	}
}

// TestAFailedWriteReturnsNoRecords injects a failure into every write function
// that returns records, before and after the point where its transaction
// commits: the error comes back with nothing, so a caller that stores what a
// write returns cannot update its view of a write that did not happen (or whose
// outcome is unknown, which the fatal class tells the caller to rescan for).
func TestAFailedWriteReturnsNoRecords(t *testing.T) {
	for _, step := range []struct {
		name string
		at   fsx.Step
	}{
		{"a file cannot be created", fsx.StepCreate},
		{"a directory cannot be made durable after the files are in place", fsx.StepSyncDir},
	} {
		for _, c := range writtenCases() {
			t.Run(step.name+"/"+c.name, func(t *testing.T) {
				mem := (&scenario{setup: c.setup}).fresh(t)
				r := newFaultyRig(t, mem)
				rule := r.faulty.Fail(fsxtest.Match{Step: step.at, Hit: 1}, syscall.EIO)
				ins := c.ins()
				given := append([]store.InRecord(nil), ins...)
				written, err := c.call(r.rig, ins)
				if rule.Hits() == 0 {
					t.Fatal("the injected failure was never reached")
				}
				if err == nil {
					t.Fatalf("%s succeeded although a step of it failed", c.name)
				}
				if len(written) != 0 {
					t.Errorf("%s returned %d records with the error %v", c.name, len(written), err)
				}
				if !reflect.DeepEqual(ins, given) {
					t.Errorf("the records given were changed")
				}
			})
		}
	}
}
