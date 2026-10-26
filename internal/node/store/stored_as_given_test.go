package store_test

import (
	"reflect"
	"syscall"
	"testing"

	"github.com/aianyai/sidera/internal/handoff"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/store"
)

// A write of target records stores them as given. The engine keeps its view by
// the records it passed, so what a new scan reads back is the expectation here,
// never the function's own computation.

// writtenCase is one write function and the records it is given.
type writtenCase struct {
	name string
	// ins are the records the call is given; the disk holds them, in this form,
	// after the call.
	ins func() []store.InRecord
	// call makes the write.
	call func(r *rig, ins []store.InRecord) error
}

func writtenCases() []writtenCase {
	pair := func(i int) func() []store.InRecord {
		return func() []store.InRecord { return []store.InRecord{tgtIn(i), tgtIn(i + 1)} }
	}
	mixed := func() []store.InRecord {
		return []store.InRecord{tgtIn(1), tgtIn(2), tgtIn(3)}
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
		{name: "Deliver", ins: copiesOfBody1(mixed), call: func(r *rig, ins []store.InRecord) error {
			return r.st.Deliver(bodyBytes(1), ins)
		}},
		{name: "DeliverReply", ins: copiesOfBody1(pair(1)), call: func(r *rig, ins []store.InRecord) error {
			return r.st.DeliverReply(bodyBytes(1), nil, ins)
		}},
		{name: "PromptEnd", ins: pair(1),
			call: func(r *rig, ins []store.InRecord) error {
				head := store.AttemptsRecord{Endpoint: ep(2)}
				return r.st.PromptEnd(head, ins)
			}},
		{name: "Blocked", ins: pair(1),
			call: func(r *rig, ins []store.InRecord) error {
				head := store.AttemptsRecord{Endpoint: ep(2), Blocked: handoff.BlockedReport{Cause: handoff.CauseAdapterOffline, Since: base}}
				return r.st.Blocked(head, ins)
			}},
		{name: "Lease", ins: pair(1), call: func(r *rig, ins []store.InRecord) error {
			return r.st.Lease(ins)
		}},
		{name: "Ack", ins: pair(1), call: func(r *rig, ins []store.InRecord) error {
			return r.st.Ack(ins)
		}},
		{name: "Tick", ins: mixed, call: func(r *rig, ins []store.InRecord) error {
			return r.st.Tick(store.TickBatch{In: ins})
		}},
		{name: "EndpointDrain", ins: mixed, call: func(r *rig, ins []store.InRecord) error {
			return r.st.EndpointDrain(store.DrainBatch{In: ins})
		}},
		{name: "InstanceDrain", ins: mixed, call: func(r *rig, ins []store.InRecord) error {
			return r.st.InstanceDrain([]store.EndpointRecord{closed8}, store.DrainBatch{In: ins})
		}},
		{name: "EndpointDrainReplay", ins: mixed, call: func(r *rig, ins []store.InRecord) error {
			return r.st.EndpointDrainReplay(store.DrainBatch{In: ins})
		}},
		{name: "InstanceDrainReplay", ins: mixed, call: func(r *rig, ins []store.InRecord) error {
			return r.st.InstanceDrainReplay([]store.EndpointRecord{closed8}, store.DrainBatch{In: ins})
		}},
		{name: "FeedbackForm", ins: func() []store.InRecord { return []store.InRecord{noticeFor(srcOut(40, false))} },
			call: func(r *rig, ins []store.InRecord) error {
				return r.st.FeedbackForm([]byte("feedback wording"), ins[0])
			}},
	}
}

// TestWritesStoreTheTargetRecordsAsGiven covers every write function that changes
// the stored form of a target record: a new scan reads back exactly the records
// given, so that a caller keeping a view stores what it passed and works nothing
// out.
func TestWritesStoreTheTargetRecordsAsGiven(t *testing.T) {
	for _, c := range writtenCases() {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			ins := c.ins()
			given := append([]store.InRecord(nil), ins...)
			if err := c.call(r, ins); err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			sc := r.scan() // a new scan: the disk, not the call
			if len(sc.In) != len(given) {
				t.Fatalf("the disk holds %d target records, %d were given", len(sc.In), len(given))
			}
			for i, g := range given {
				var read *store.InRecord
				for j := range sc.In {
					if sc.In[j].Key == g.Key {
						read = &sc.In[j]
					}
				}
				if read == nil {
					t.Fatalf("record %d: %s is not on the disk", i, g.Key)
				}
				if !reflect.DeepEqual(*read, g) {
					t.Errorf("record %d: given %#v, the scan reads %#v", i, g, *read)
				}
			}
			if !reflect.DeepEqual(ins, given) {
				t.Errorf("the records given were changed")
			}
		})
	}
}

// TestAFailedWriteOfTargetRecordsIsAnError injects a failure into every write
// function of target records, before and after the point where its transaction
// commits: the error comes back, so a caller that updates its view only after nil
// never records a write that did not happen (or whose outcome is unknown, which
// the fatal class tells the caller to rescan for).
func TestAFailedWriteOfTargetRecordsIsAnError(t *testing.T) {
	for _, step := range []struct {
		name string
		at   fsx.Step
	}{
		{"a file cannot be created", fsx.StepCreate},
		{"a directory cannot be made durable after the files are in place", fsx.StepSyncDir},
	} {
		for _, c := range writtenCases() {
			t.Run(step.name+"/"+c.name, func(t *testing.T) {
				r := newFaultyRig(t, (&scenario{}).fresh(t))
				rule := r.faulty.Fail(fsxtest.Match{Step: step.at, Hit: 1}, syscall.EIO)
				ins := c.ins()
				given := append([]store.InRecord(nil), ins...)
				err := c.call(r.rig, ins)
				if rule.Hits() == 0 {
					t.Fatal("the injected failure was never reached")
				}
				if err == nil {
					t.Fatalf("%s succeeded although a step of it failed", c.name)
				}
				if !reflect.DeepEqual(ins, given) {
					t.Errorf("the records given were changed")
				}
			})
		}
	}
}
