package store_test

import (
	"errors"
	"path"
	"syscall"
	"testing"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/txn"
)

// A write that removes files, the bodies of the records it writes without a
// reference included, asks the disk which of them exist before it opens its
// transaction. A read that fails after the commit point would be an
// ordinary error on a disk that already changed, and the caller would go on
// with a view of memory that is no longer the disk. So a failed existence check
// ends the call before any write: an ordinary failure, nothing on the disk
// changed, no crash point reached.
func TestAFailedExistenceCheckEndsTheCallBeforeAnyWrite(t *testing.T) {
	finished := func() store.OutRecord {
		out := srcOut(1, true)
		d, _, err := out.Outbound.Recipients[0].Delivery.Apply(base, delivery.Event{Type: delivery.EventDelivered})
		if err != nil {
			t.Fatal(err)
		}
		out.Outbound.Recipients[0].Delivery, out.Body = d, nil
		return out
	}
	delivered := func() store.InRecord {
		in := tgtIn(1)
		in.Record, in.Body = deliveredRecord(in.Record), nil
		return in
	}
	source, target := srcOut(1, true), tgtIn(1)
	ctl := sessionCtl(2, 5)
	ctl.Body = bodyRef(2)
	endpoint := directEndpoint(7, "chat")
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, r *rig)
		fails string // the path whose existence check fails
		run   func(st *store.Store) error
	}{
		{"SourceUpdate dropping a body", func(t *testing.T, r *rig) { mustDo(t, r.st.Intake(bodyBytes(1), source)) }, bodyPath(source),
			func(st *store.Store) error { return st.SourceUpdate(finished()) }},
		{"Ack dropping a body", func(t *testing.T, r *rig) { seedBodies(t, r.mem, target) }, bodyPath(target),
			func(st *store.Store) error { return dropWritten(st.Ack([]store.InRecord{delivered()})) }},
		{"Tick dropping a body", func(t *testing.T, r *rig) { mustDo(t, r.st.Intake(bodyBytes(1), source)) }, bodyPath(source),
			func(st *store.Store) error {
				return dropWritten(st.Tick(store.TickBatch{Out: []store.OutRecord{finished()}}))
			}},
		{"ControlResult dropping a body", func(t *testing.T, r *rig) { seedBodyOf(t, r.mem, created(t, 20, 5), bodyBytes(20)) }, bodyPath(created(t, 20, 5)),
			func(st *store.Store) error {
				cr := created(t, 20, 5)
				return st.ControlResult(cr, ptr(openedBy(cr)))
			}},
		{"EndpointDrain dropping a body", func(t *testing.T, r *rig) { seedBodies(t, r.mem, target) }, bodyPath(target),
			func(st *store.Store) error {
				d := rejected(tgtIn(1))
				return dropWritten(st.EndpointDrain(store.DrainBatch{In: []store.InRecord{d}}))
			}},
		{"ArchiveSweep removing the body of a target record", func(t *testing.T, r *rig) { seedBodies(t, r.mem, target) }, bodyPath(target),
			func(st *store.Store) error {
				return st.ArchiveSweep(store.SweepChunk{Now: base, Records: []store.FileRef{{Dir: store.InDir, Name: target.Name()}}})
			}},
		{"ArchiveSweep removing the body of a control record", func(t *testing.T, r *rig) { seedBodies(t, r.mem, ctl) }, bodyPath(ctl),
			func(st *store.Store) error {
				return st.ArchiveSweep(store.SweepChunk{Now: base, Ctl: []store.FileRef{{Dir: store.CtlDir, Name: ctl.Name()}}})
			}},
		{"ArchiveSweep removing the body of a source record after it wrote a floor", func(t *testing.T, r *rig) { mustDo(t, r.st.Intake(bodyBytes(1), source)) }, bodyPath(source),
			func(st *store.Store) error {
				return st.ArchiveSweep(store.SweepChunk{Now: base, Outs: []store.OutRecord{source}})
			}},
		{"ArchiveSweep removing a record", func(t *testing.T, r *rig) { seed(t, r.mem, inRev(1, 1)) }, path.Join(store.InDir, inRev(1, 1).Name()),
			func(st *store.Store) error {
				return st.ArchiveSweep(store.SweepChunk{Now: base, Records: []store.FileRef{{Dir: store.InDir, Name: inRev(1, 1).Name()}}})
			}},
		{"ArchiveSweep removing an endpoint", func(t *testing.T, r *rig) { seed(t, r.mem, endpoint) }, path.Join(store.EndpointsDir, endpoint.Name()),
			func(st *store.Store) error {
				return st.ArchiveSweep(store.SweepChunk{Now: base, Endpoints: []store.FileRef{{Dir: store.EndpointsDir, Name: endpoint.Name()}}})
			}},
		{"OrphanGC", func(t *testing.T, r *rig) { seedBodyOf(t, r.mem, target, bodyBytes(1)) }, bodyPath(target),
			func(st *store.Store) error { return st.OrphanGC([]string{store.BodyFileName(target.Name())}) }},
		{"OrphanGCEndpoint", func(t *testing.T, r *rig) { seed(t, r.mem, endpoint) }, path.Join(store.EndpointsDir, endpoint.Name()),
			func(st *store.Store) error { return st.OrphanGCEndpoint([]store.EndpointRecord{endpoint}) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			c.setup(t, r)
			before := snapshot(t, r.mem, ".")
			f := newFaultyRig(t, r.mem)
			rule := f.faulty.Fail(fsxtest.Match{Path: c.fails}, syscall.EIO)
			err := c.run(f.st)
			if !errors.Is(err, syscall.EIO) {
				t.Fatalf("err = %v, want the failure of the existence check", err)
			}
			if rule.Hits() == 0 {
				t.Fatal("the existence check was not made")
			}
			if txn.IsFatal(err) {
				t.Errorf("a call that wrote nothing returned a fatal error: %v", err)
			}
			if n := f.rec.count(); n != 0 {
				t.Errorf("%d crash points were reached before the existence check failed", n)
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
