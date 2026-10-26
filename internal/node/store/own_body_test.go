package store_test

import (
	"bytes"
	"errors"
	"maps"
	"path"
	"slices"
	"testing"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/store"
)

// bodyFilesOnDisk are the names of the files of bodies/, sorted.
func bodyFilesOnDisk(t *testing.T, r *rig) []string {
	t.Helper()
	got := slices.Clone(r.scan().Bodies)
	slices.Sort(got)
	return got
}

// wantBodyFiles are the body file names of recs, sorted.
func wantBodyFiles(recs ...store.Record) []string {
	var names []string
	for _, rec := range recs {
		names = append(names, store.BodyFileName(rec.Name()))
	}
	slices.Sort(names)
	return names
}

// An accepted record has a body file of its own with a copy of the bytes, and a
// record that holds no reference has none: records never share a body.
func TestEveryRecordHasABodyOfItsOwn(t *testing.T) {
	t.Run("Deliver", func(t *testing.T) {
		r := newRig(t)
		accepted := []store.InRecord{inFor(1, 2), inFor(1, 3), inFor(1, 4)}
		refused := rejected(inFor(1, 5))
		if err := r.st.Deliver(bodyBytes(1), append(slices.Clone(accepted), refused)); err != nil {
			t.Fatal(err)
		}
		var owners []store.Record
		for _, in := range accepted {
			owners = append(owners, in)
			got, err := r.mem.ReadFile(bodyPath(in))
			if err != nil || !bytes.Equal(got, bodyBytes(1)) {
				t.Errorf("the body of %s: %q, %v", in.Name(), got, err)
			}
		}
		if got, want := bodyFilesOnDisk(t, r), wantBodyFiles(owners...); !slices.Equal(got, want) {
			t.Errorf("bodies = %v, want exactly one for each accepted record: %v", got, want)
		}
		if _, err := r.mem.Stat(bodyPath(refused)); err == nil {
			t.Errorf("the rejected record %s has a body", refused.Name())
		}
	})
	t.Run("Intake", func(t *testing.T) {
		r := newRig(t)
		out := srcOut(1, true)
		mustDo(t, r.st.Intake(bodyBytes(1), out))
		got, err := r.mem.ReadFile(bodyPath(out))
		if err != nil || !bytes.Equal(got, bodyBytes(1)) {
			t.Errorf("the body of %s: %q, %v", out.Name(), got, err)
		}
		if got, want := bodyFilesOnDisk(t, r), wantBodyFiles(out); !slices.Equal(got, want) {
			t.Errorf("bodies = %v, want %v", got, want)
		}
	})
}

// A body that the records written with it do not say is refused before anything
// is written: the one a record refers to is the one given, byte for byte.
func TestABodyIsRefusedThatItsRecordsDoNotSay(t *testing.T) {
	over := bytes.Repeat([]byte{'x'}, envelope.MaxBody+1)
	for _, m := range []struct {
		name string
		run  func(st *store.Store, body []byte, ref *store.BodyRef) error
	}{
		{"Intake", func(st *store.Store, body []byte, ref *store.BodyRef) error {
			o := srcOut(1, true)
			o.Body = ref
			return st.Intake(body, o)
		}},
		{"Deliver", func(st *store.Store, body []byte, ref *store.BodyRef) error {
			in := tgtIn(1)
			in.Body = ref
			return st.Deliver(body, []store.InRecord{in})
		}},
		{"DeliverReply", func(st *store.Store, body []byte, ref *store.BodyRef) error {
			in := tgtIn(1)
			in.Body = ref
			return st.DeliverReply(body, nil, []store.InRecord{in})
		}},
		{"ControlIntake", func(st *store.Store, body []byte, ref *store.BodyRef) error {
			c := sessionCtl(1, 5)
			c.Body = ref
			return st.ControlIntake(body, ptr(session(5, store.EndpointCreating, ctlName(1))), c)
		}},
		{"FeedbackForm", func(st *store.Store, body []byte, ref *store.BodyRef) error {
			o := srcOut(1, false)
			o.Feedback = []delivery.Item{feedbackItem(1)}
			notice := noticeFor(o)
			notice.Body = ref
			return st.FeedbackForm(body, notice)
		}},
	} {
		for _, c := range []struct {
			name string
			body []byte
			ref  *store.BodyRef
		}{
			{"a body that does not match the ref", bodyBytes(2), bodyRef(1)},
			{"a record that holds a ref with no body given", nil, bodyRef(1)},
			{"a body that no record refers to", bodyBytes(1), nil},
			{"a body over the limit", over, ptr(store.BodyRefOf(over))},
		} {
			t.Run(m.name+"/"+c.name, func(t *testing.T) {
				r := newRig(t)
				before := snapshot(t, r.mem, ".")
				if err := m.run(r.st, c.body, c.ref); err == nil {
					t.Fatal("accepted")
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
}

// ownBodyWrite is a write that can drop a body: the records in gone are written
// by it, with their Body reference dropped when drop is set and kept otherwise;
// the records in rest are on disk with their bodies and not touched by it.
type ownBodyWrite struct {
	name  string
	gone  []store.Record
	rest  []store.Record
	write func(st *store.Store, drop bool) error
}

func withCtlBody(c store.CtlRecord, i int) store.CtlRecord {
	c.Body = bodyRef(i)
	return c
}

func ownBodyWrites() []ownBodyWrite {
	outOf := func(i int, drop bool) store.OutRecord {
		if drop {
			return finishedOut(i, true)
		}
		return srcOut(i, true)
	}
	inOf := func(i int, drop bool) store.InRecord {
		in := tgtIn(i)
		if drop {
			in.Record, in.Body = deliveredRecord(in.Record), nil
		}
		return in
	}
	ctlOf := func(i, session int, drop bool) store.CtlRecord {
		c := withCtlBody(sessionCtl(i, session), i)
		if drop {
			c.Body = nil
		}
		return c
	}
	closed := directEndpoint(7, "chat")
	closed.Status, closed.Token = store.EndpointClosed, nil
	return []ownBodyWrite{
		{"SourceUpdate", []store.Record{srcOut(1, true)}, []store.Record{srcOut(2, true)},
			func(st *store.Store, drop bool) error { return st.SourceUpdate(outOf(1, drop)) }},
		{"DeliverReply with an updated out", []store.Record{srcOut(1, true)}, []store.Record{srcOut(2, true)},
			func(st *store.Store, drop bool) error {
				out := outOf(1, drop)
				return st.DeliverReply(bodyBytes(3), &out, []store.InRecord{tgtIn(3)})
			}},
		{"ControlResult", []store.Record{withCtlBody(sessionCtl(1, 5), 1)}, []store.Record{withCtlBody(sessionCtl(2, 6), 2)},
			func(st *store.Store, drop bool) error { return st.ControlResult(ctlOf(1, 5, drop), nil) }},
		{"ControlReply", []store.Record{srcOut(1, true)}, []store.Record{srcOut(2, true)},
			func(st *store.Store, drop bool) error { return st.ControlReply(outOf(1, drop)) }},
		{"FeedbackClear", []store.Record{srcOut(1, true)}, []store.Record{srcOut(2, true)},
			func(st *store.Store, drop bool) error { return st.FeedbackClear([]store.OutRecord{outOf(1, drop)}) }},
		{"FeedbackReplay", []store.Record{srcOut(1, true)}, []store.Record{srcOut(2, true)},
			func(st *store.Store, drop bool) error { return st.FeedbackReplay([]store.OutRecord{outOf(1, drop)}) }},
		{"Ack", []store.Record{tgtIn(1)}, []store.Record{tgtIn(2)},
			func(st *store.Store, drop bool) error { return st.Ack([]store.InRecord{inOf(1, drop)}) }},
		{"Tick with out, in and ctl",
			[]store.Record{srcOut(1, true), tgtIn(1), withCtlBody(sessionCtl(1, 5), 1)},
			[]store.Record{srcOut(2, true), tgtIn(2), withCtlBody(sessionCtl(2, 6), 2)},
			func(st *store.Store, drop bool) error {
				return st.Tick(store.TickBatch{
					Out: []store.OutRecord{outOf(1, drop)},
					In:  []store.InRecord{inOf(1, drop)},
					Ctl: []store.CtlRecord{ctlOf(1, 5, drop)},
				})
			}},
		{"EndpointDrain", []store.Record{tgtIn(1), withCtlBody(sessionCtl(1, 5), 1)}, []store.Record{tgtIn(2), withCtlBody(sessionCtl(2, 6), 2)},
			func(st *store.Store, drop bool) error {
				return st.EndpointDrain(store.DrainBatch{
					In: []store.InRecord{inOf(1, drop)}, Ctl: []store.CtlRecord{ctlOf(1, 5, drop)},
				})
			}},
		{"InstanceDrain", []store.Record{tgtIn(1), withCtlBody(sessionCtl(1, 5), 1)}, []store.Record{tgtIn(2), withCtlBody(sessionCtl(2, 6), 2)},
			func(st *store.Store, drop bool) error {
				return st.InstanceDrain([]store.EndpointRecord{closed}, store.DrainBatch{
					In: []store.InRecord{inOf(1, drop)}, Ctl: []store.CtlRecord{ctlOf(1, 5, drop)},
				})
			}},
	}
}

// seedOn writes the records of c and the body file of each.
func (c ownBodyWrite) seedOn(t *testing.T, mem *fsxtest.Mem) {
	t.Helper()
	all := slices.Concat(c.gone, c.rest)
	seed(t, mem, all...)
	seedBodies(t, mem, all...)
}

// assertBodyIntact fails the test unless the body file of rec is there with the
// bytes its reference says.
func assertBodyIntact(t *testing.T, mem *fsxtest.Mem, rec store.Record) {
	t.Helper()
	got, err := mem.ReadFile(bodyPath(rec))
	if err != nil {
		t.Errorf("the body of %s is gone: %v", rec.Name(), err)
		return
	}
	if want := bodyHeld(rec); want == nil || store.BodyRefOf(got) != *want {
		t.Errorf("the body of %s is not the one its record refers to: %q", rec.Name(), got)
	}
}

func assertBodyGone(t *testing.T, mem *fsxtest.Mem, rec store.Record) {
	t.Helper()
	if _, err := mem.Stat(bodyPath(rec)); err == nil {
		t.Errorf("the body of %s is still there", rec.Name())
	}
}

// A record that no longer holds a reference loses the body file that is named
// after it, and no other body: the writes that drop a body never ask who else
// refers to it, because nobody does.
func TestARecordThatStopsNeedingItsBodyRemovesItsOwnBodyAndNoOther(t *testing.T) {
	for _, c := range ownBodyWrites() {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			c.seedOn(t, r.mem)
			if err := c.write(r.st, true); err != nil {
				t.Fatal(err)
			}
			for _, rec := range c.gone {
				assertBodyGone(t, r.mem, rec)
			}
			for _, rec := range c.rest {
				assertBodyIntact(t, r.mem, rec)
			}
			// Writing it again is quiet: the file is already gone, and nothing of
			// the bodies is touched.
			r.rec.forget()
			if err := c.write(r.st, true); err != nil {
				t.Errorf("the second write: %v", err)
			}
			for _, p := range r.rec.points {
				if p.Tag.Role == "body" || p.Tag.Role == "body-dir" {
					t.Errorf("the second write reached %v although no body was left to remove", p)
				}
			}
			for _, rec := range c.rest {
				assertBodyIntact(t, r.mem, rec)
			}
		})
		t.Run(c.name+" keeping its body", func(t *testing.T) {
			r := newRig(t)
			c.seedOn(t, r.mem)
			if err := c.write(r.st, false); err != nil {
				t.Fatal(err)
			}
			for _, rec := range slices.Concat(c.gone, c.rest) {
				assertBodyIntact(t, r.mem, rec)
			}
		})
	}
}

// The body goes after the record that dropped its reference is written: a crash
// between the two leaves a body no record refers to, which the orphan collection
// removes, and never a record that points at a body that is gone.
func TestABodyIsRemovedAfterTheRecordThatDroppedIt(t *testing.T) {
	for _, c := range ownBodyWrites() {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			c.seedOn(t, r.mem)
			r.rec.forget()
			if err := c.write(r.st, true); err != nil {
				t.Fatal(err)
			}
			lastReplace, firstRemove := -1, -1
			for i, p := range r.rec.points {
				switch {
				case p.Tag.Role == "body" && p.Step == fsx.StepRemove && firstRemove < 0:
					firstRemove = i
				case p.Tag.Role != "body" && p.Step == fsx.StepRename:
					lastReplace = i
				}
			}
			if firstRemove < 0 {
				t.Fatalf("no body was removed (points %v)", r.rec.points)
			}
			if lastReplace < 0 || lastReplace > firstRemove {
				t.Errorf("a record was replaced at point %d, after the first removal of a body at %d (points %v)", lastReplace, firstRemove, r.rec.points)
			}
		})
	}
}

// A sweep that removes a record removes the body file named after it too, if one
// is still there, after the records, and no other body.
func TestASweepRemovesTheBodiesOfTheRecordsItRemoves(t *testing.T) {
	r := newRig(t)
	swept := []store.Record{tgtIn(1), withCtlBody(sessionCtl(1, 5), 1), srcOut(1, true)}
	kept := []store.Record{tgtIn(2), withCtlBody(sessionCtl(2, 6), 2), srcOut(2, true)}
	all := slices.Concat(swept, kept)
	seed(t, r.mem, all...)
	seedBodies(t, r.mem, all...)
	r.rec.forget()
	err := r.st.ArchiveSweep(store.SweepChunk{
		Now:     base,
		Records: []store.FileRef{{Dir: store.InDir, Name: swept[0].Name()}},
		Ctl:     []store.FileRef{{Dir: store.CtlDir, Name: swept[1].Name()}},
		Outs:    []store.OutRecord{swept[2].(store.OutRecord)},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range swept {
		if _, err := r.mem.Stat(path.Join(dirOf(rec), rec.Name())); err == nil {
			t.Errorf("%s was not removed", rec.Name())
		}
		assertBodyGone(t, r.mem, rec)
	}
	for _, rec := range kept {
		assertBodyIntact(t, r.mem, rec)
	}
	lastRecord, firstBody := -1, -1
	for i, p := range r.rec.points {
		switch {
		case p.Tag.Role == "body" && p.Step == fsx.StepRemove && firstBody < 0:
			firstBody = i
		case (p.Tag.Role == "ctl" || p.Tag.Role == "record") && p.Step == fsx.StepRemove:
			lastRecord = i
		}
	}
	if firstBody < 0 || lastRecord < 0 || lastRecord > firstBody {
		t.Errorf("the last record was removed at point %d, the first body at %d: the bodies go after the records (points %v)", lastRecord, firstBody, r.rec.points)
	}
}

// A record that still needs its body is never written without a reference to it:
// the write would remove the body, and the record would be left with nothing to
// hand out or send again. The call is refused, typed, before anything is written
// and with the body untouched. The same holds for every write of records, whether
// or not it can drop a body.
func TestARecordThatStillNeedsItsBodyIsRefusedWithoutAReferenceToIt(t *testing.T) {
	queued := func() store.OutRecord {
		o := srcOut(1, true)
		o.Body = nil
		return o
	}
	accepted := func() store.InRecord {
		in := tgtIn(1)
		in.Body = nil
		return in
	}
	head := store.AttemptsRecord{Endpoint: ep(2)}
	for _, c := range []struct {
		name string
		run  func(st *store.Store) error
	}{
		{"Intake", func(st *store.Store) error { return st.Intake(nil, queued()) }},
		{"Deliver", func(st *store.Store) error { return st.Deliver(nil, []store.InRecord{accepted()}) }},
		{"DeliverReply with an updated out", func(st *store.Store) error {
			o := queued()
			return st.DeliverReply(bodyBytes(3), &o, []store.InRecord{tgtIn(3)})
		}},
		{"DeliverReply with a target record", func(st *store.Store) error {
			return st.DeliverReply(nil, nil, []store.InRecord{accepted()})
		}},
		{"SourceUpdate", func(st *store.Store) error { return st.SourceUpdate(queued()) }},
		{"ControlReply", func(st *store.Store) error { return st.ControlReply(queued()) }},
		{"PromptEnd", func(st *store.Store) error {
			return st.PromptEnd(head, []store.InRecord{accepted()})
		}},
		{"Lease", func(st *store.Store) error { return st.Lease([]store.InRecord{accepted()}) }},
		{"Ack", func(st *store.Store) error { return st.Ack([]store.InRecord{accepted()}) }},
		{"Tick with a source record", func(st *store.Store) error {
			return st.Tick(store.TickBatch{Out: []store.OutRecord{queued()}})
		}},
		{"Tick with a target record", func(st *store.Store) error {
			return st.Tick(store.TickBatch{In: []store.InRecord{accepted()}})
		}},
		{"EndpointDrain", func(st *store.Store) error {
			return st.EndpointDrain(store.DrainBatch{In: []store.InRecord{accepted()}})
		}},
		{"FeedbackClear", func(st *store.Store) error { return st.FeedbackClear([]store.OutRecord{queued()}) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			seed(t, r.mem, srcOut(1, true), tgtIn(1))
			seedBodies(t, r.mem, srcOut(1, true), tgtIn(1))
			before := snapshot(t, r.mem, ".")
			r.rec.forget()
			err := c.run(r.st)
			if !errors.Is(err, store.ErrBodyDropped) {
				t.Fatalf("err = %v, want ErrBodyDropped", err)
			}
			if n := r.rec.count(); n != 0 {
				t.Errorf("%d crash points were reached by a call that was refused", n)
			}
			after := snapshot(t, r.mem, ".")
			if !maps.Equal(before, after) {
				t.Errorf("the disk changed: %v -> %v", keysOf(before), keysOf(after))
			}
		})
	}

	// A source record that never had a body (a control signal sent with none) is
	// queued without a reference and is written as it is.
	t.Run("a source record that never had a body", func(t *testing.T) {
		r := newRig(t)
		o := srcOut(1, false)
		o.Body, o.Outbound.BodyNonEmpty, o.Envelope.ContentType = nil, false, ""
		mustDo(t, r.st.Intake(nil, o))
		mustDo(t, r.st.SourceUpdate(o))
	})
}
