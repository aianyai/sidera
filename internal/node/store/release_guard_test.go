package store_test

import (
	"errors"
	"testing"

	"github.com/aianyai/sidera/internal/node/store"
)

// Bodies are addressed by content, so a record a transaction writes can refer to
// the very file the same transaction was asked to remove: a reply whose bytes equal
// the message it answers, say. Every write that takes a release refuses, before it
// writes anything, a release that names a body one of its own records refers to.
func TestAWriteRefusesToReleaseABodyARecordItWritesRefersTo(t *testing.T) {
	held := []store.BodyRef{*bodyRef(1)}
	ctl := func() store.CtlRecord {
		c := sessionCtl(1, 5)
		c.Body = bodyRef(1)
		return c
	}
	closed := []store.EndpointRecord{directEndpoint(7, "chat")}
	closed[0].Status = store.EndpointClosed
	for _, c := range []struct {
		name string
		run  func(st *store.Store) error
	}{
		{"DeliverReply: the reply refers to it", func(st *store.Store) error {
			out := finishedOut(2, true)
			return dropWritten(st.DeliverReply(nil, &out, []store.InRecord{tgtIn(1)}, held))
		}},
		{"DeliverReply: the source record still refers to it", func(st *store.Store) error {
			out := srcOut(1, true)
			return dropWritten(st.DeliverReply(nil, &out, []store.InRecord{inRev(2, 1)}, held))
		}},
		{"DeliverReply: the reply carries the body itself", func(st *store.Store) error {
			out := finishedOut(2, true)
			return dropWritten(st.DeliverReply(bodyBytes(1), &out, []store.InRecord{tgtIn(1)}, held))
		}},
		{"SourceUpdate", func(st *store.Store) error { return st.SourceUpdate(srcOut(1, true), held) }},
		{"ControlResult", func(st *store.Store) error { return st.ControlResult(ctl(), nil, held) }},
		{"Ack", func(st *store.Store) error { return dropWritten(st.Ack([]store.InRecord{inRev(1, 1)}, held)) }},
		{"Tick: a source record refers to it", func(st *store.Store) error {
			return dropWritten(st.Tick(store.TickBatch{Out: []store.OutRecord{srcOut(1, true)}, Release: held}))
		}},
		{"Tick: a target record refers to it", func(st *store.Store) error {
			return dropWritten(st.Tick(store.TickBatch{Out: []store.OutRecord{finishedOut(2, true)}, In: []store.InRecord{inRev(1, 1)}, Release: held}))
		}},
		{"Tick: a control record refers to it", func(st *store.Store) error {
			return dropWritten(st.Tick(store.TickBatch{Ctl: []store.CtlRecord{ctl()}, Release: held}))
		}},
		{"EndpointDrain: a target record refers to it", func(st *store.Store) error {
			return dropWritten(st.EndpointDrain(store.DrainBatch{In: []store.InRecord{inRev(1, 1)}, Release: held}))
		}},
		{"EndpointDrain: a control record refers to it", func(st *store.Store) error {
			return dropWritten(st.EndpointDrain(store.DrainBatch{Ctl: []store.CtlRecord{ctl()}, Release: held}))
		}},
		{"InstanceDrain", func(st *store.Store) error {
			return dropWritten(st.InstanceDrain(closed, store.DrainBatch{In: []store.InRecord{inRev(1, 1)}, Release: held}))
		}},
		{"EndpointDrainReplay", func(st *store.Store) error {
			return dropWritten(st.EndpointDrainReplay(store.DrainBatch{In: []store.InRecord{inRev(1, 1)}, Release: held}))
		}},
		{"InstanceDrainReplay", func(st *store.Store) error {
			return dropWritten(st.InstanceDrainReplay(closed, store.DrainBatch{In: []store.InRecord{inRev(1, 1)}, Release: held}))
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			seedBodies(t, r.mem, bodyBytes(1))
			before := snapshot(t, r.mem, ".")
			r.rec.forget()
			err := c.run(r.st)
			if !errors.Is(err, store.ErrReleasedBodyInUse) {
				t.Fatalf("err = %v, want ErrReleasedBodyInUse", err)
			}
			if n := r.rec.count(); n != 0 {
				t.Errorf("%d crash points were reached by a call that was refused", n)
			}
			after := snapshot(t, r.mem, ".")
			if len(after) != len(before) {
				t.Errorf("the disk changed: %v -> %v", keysOf(before), keysOf(after))
			}
			for k, v := range before {
				if after[k] != v {
					t.Errorf("%s changed on disk", k)
				}
			}
		})
	}
}
