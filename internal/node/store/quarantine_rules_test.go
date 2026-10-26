package store_test

import (
	"errors"
	"path"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/store"
)

// A damaged file is settled by one transaction that writes every effect first and
// removes (or overwrites) the damaged file last. While the file is still there a
// restart simply settles it again, so nothing remembers a half-done settlement.
// Each test below is one row of that rule: the effects, the file gone or
// overwritten, one alert, and a second start that has nothing more to do.

// settled runs the recovery rules over the disk of r and then checks that a second
// start does nothing: no rule has work and nothing on the disk changes.
func settled(t *testing.T, r *rig) *store.Scan {
	t.Helper()
	newSim(t, r.st).recoverAll()
	after := snapshot(t, r.mem, ".")
	second := openRig(t, r.mem.Clone())
	s := newSim(t, second.st)
	s.recoverAll()
	if len(s.did) != 0 {
		t.Errorf("a second start had work: %v", s.did)
	}
	if diff := equalMaps(snapshot(t, second.mem, "."), after, nil); diff != "" {
		t.Errorf("a second start changed the disk: %s", diff)
	}
	for p := range after {
		if strings.HasSuffix(p, ".corrupt") {
			t.Errorf("the bad bytes were kept under %s", p)
		}
	}
	return r.scan()
}

// worldRig is a rig over the disk of a scenario: the endpoints every world has,
// so that no record is queued for an endpoint that does not exist.
func worldRig(t *testing.T) *rig { return openRig(t, (&scenario{}).fresh(t)) }

func present(t *testing.T, r *rig, dir, name string) bool {
	t.Helper()
	_, ok := snapshot(t, r.mem, dir)[path.Join(dir, name)]
	return ok
}

func oneAlert(t *testing.T, sc *store.Scan, dir, name, kind string) {
	t.Helper()
	if len(sc.Alerts) != 1 || !hasAlert(sc, dir, name, kind) {
		t.Errorf("want exactly one alert %q for %s/%s, got %v", kind, dir, name, sc.Alerts)
	}
}

var damages = []struct {
	name string
	do   func(testing.TB, *fsxtest.Mem, string)
}{
	{"body", damageBody},
	{"first line", damageHeader},
}

func TestADamagedTargetRecordIsAlertedAndDeleted(t *testing.T) {
	for _, d := range damages {
		t.Run(d.name, func(t *testing.T) {
			r := worldRig(t)
			seed(t, r.mem, inRev(1, 1), inRev(2, 1))
			seedBodies(t, r.mem, bodyBytes(1), bodyBytes(2))
			d.do(t, r.mem, path.Join(store.InDir, inRev(1, 1).Name()))
			healthy := snapshot(t, r.mem, store.InDir)[path.Join(store.InDir, inRev(2, 1).Name())]
			sc := settled(t, r)
			if present(t, r, store.InDir, inRev(1, 1).Name()) {
				t.Error("the damaged file is still there")
			}
			if got := snapshot(t, r.mem, store.InDir)[path.Join(store.InDir, inRev(2, 1).Name())]; got != healthy {
				t.Error("the healthy record was touched")
			}
			oneAlert(t, sc, store.InDir, inRev(1, 1).Name(), "corrupt_record")
			if len(sc.Seq) != 0 {
				t.Errorf("a target record raises no floor: %v", sc.Seq)
			}
			if len(sc.Bodies) != 1 || sc.Bodies[0] != bodyRef(2).Name() {
				t.Errorf("the body only the deleted record referred to must go as an orphan: %v", sc.Bodies)
			}
		})
	}
}

func TestADamagedControlRecordClosesItsCreatingSessionsAlertsAndIsDeleted(t *testing.T) {
	for _, d := range damages {
		t.Run(d.name, func(t *testing.T) {
			r := worldRig(t)
			c := sessionCtl(1, 5)
			seed(t, r.mem, c, derivedIn(1, 5, 1),
				session(5, store.EndpointCreating, ctlName(1)),
				session(7, store.EndpointOpen, ctlName(1)))
			d.do(t, r.mem, path.Join(store.CtlDir, c.Name()))
			sc := settled(t, r)
			if present(t, r, store.CtlDir, c.Name()) {
				t.Error("the damaged control record is still there")
			}
			if e := endpointOf(sc, ep(5)); e == nil || e.Status != store.EndpointClosed || e.Token != nil {
				t.Errorf("the session being created must be closed: %#v", e)
			}
			if e := endpointOf(sc, ep(7)); e == nil || e.Status != store.EndpointOpen {
				t.Errorf("an open session must stay open: %#v", e)
			}
			if _, in, _ := find(t, sc, derivedIn(1, 5, 1).Name()); in == nil {
				t.Error("the delivery derived from the control record is not deleted with it")
			}
			oneAlert(t, sc, store.CtlDir, c.Name(), "control_unknown")
		})
	}
}

func TestADamagedSourceRecordWithATrustedFirstLineIsOverwrittenAsLost(t *testing.T) {
	pair := addr(2, 2)
	other := addr(2, 3)
	two := srcOut(3, false)
	two.Key.Recipients = []ident.Address{pair, other}
	two.Outbound.Recipients = append(two.Outbound.Recipients, two.Outbound.Recipients[0])
	two.Outbound.Recipients[1].Address = other
	two.Envelope.To = two.Key.Recipients
	two.Envelope.Seq = map[ident.EndpointID]uint64{pair.Endpoint: 3, other.Endpoint: 3}
	for _, c := range []struct {
		name string
		rec  store.OutRecord
		want map[ident.Address]uint64
	}{
		{"a message", srcOut(3, false), map[ident.Address]uint64{pair: 5}},
		{"a control signal", outCtlRecord(3), map[ident.Address]uint64{pair: 5}},
		{"a message to two recipients raises both pairs", two, map[ident.Address]uint64{pair: 5, other: 1}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			seed(t, r.mem, srcOut(1, false), srcOut(4, false), store.SeqRecord{Sender: ep(1), Floors: map[ident.Address]uint64{pair: 2}}, c.rec)
			seedBodies(t, r.mem, bodyBytes(1), bodyBytes(3), bodyBytes(4))
			damageBody(t, r.mem, path.Join(store.OutDir, c.rec.Name()))
			sc := settled(t, r)
			out, _, _ := find(t, sc, c.rec.Name())
			if out == nil || out.Lost == nil || len(out.Lost.Deliveries) != len(c.rec.Key.Recipients) || len(out.Feedback) != len(c.rec.Key.Recipients) {
				t.Fatalf("the file was not overwritten with the sender's lost record and its feedback: %#v", out)
			}
			// m is the highest of the floor and the numbers still there (4 for the first
			// pair, none for the second); the lost record may have used the next one, so
			// the floor is m+1.
			for to, want := range c.want {
				if got := floorOf(t, sc, 1, to); got != want {
					t.Errorf("floor for %v = %d, want %d", to, got, want)
				}
			}
			oneAlert(t, sc, store.OutDir, c.rec.Name(), "corrupt_record")
		})
	}
}

func TestADamagedSourceRecordWithAnUnreadableFirstLineIsAlertedAndDeleted(t *testing.T) {
	t.Run("a damaged first line", func(t *testing.T) {
		r := newRig(t)
		seed(t, r.mem, srcOut(1, false), srcOut(2, false))
		damageHeader(t, r.mem, path.Join(store.OutDir, srcOut(2, false).Name()))
		sc := settled(t, r)
		if present(t, r, store.OutDir, srcOut(2, false).Name()) {
			t.Error("the damaged file is still there")
		}
		if len(sc.Out) != 1 || len(sc.Seq) != 0 {
			t.Errorf("nothing is known of the file: no replacement and no floor, got %d source records and floors %v", len(sc.Out), sc.Seq)
		}
		oneAlert(t, sc, store.OutDir, srcOut(2, false).Name(), "corrupt_record")
	})
	t.Run("all zero", func(t *testing.T) {
		r := newRig(t)
		seed(t, r.mem, srcOut(1, false))
		seedRaw(t, r.mem, store.OutDir, zeroRec, make([]byte, 300))
		sc := settled(t, r)
		if present(t, r, store.OutDir, zeroRec) {
			t.Error("the all-zero file is still there")
		}
		if len(sc.Out) != 1 || len(sc.Seq) != 0 {
			t.Errorf("no replacement and no floor, got %d source records and floors %v", len(sc.Out), sc.Seq)
		}
		oneAlert(t, sc, store.OutDir, zeroRec, "corrupt_record")
	})
}

func TestADamagedRecordOfAnotherDirectoryIsAlertedAndDeleted(t *testing.T) {
	r := newRig(t)
	seed(t, r.mem, directEndpoint(7, "chat"), store.CheckpointRecord{Key: store.CheckpointKey{Scope: "session", ID: ep(5).String()}, State: []byte(`{}`)})
	name := directEndpoint(7, "chat").Name()
	damageBody(t, r.mem, path.Join(store.EndpointsDir, name))
	sc := settled(t, r)
	if present(t, r, store.EndpointsDir, name) || hasEndpoint(sc, ep(7)) || len(sc.Checkpoints) != 1 {
		t.Errorf("the damaged endpoint record must be gone and the checkpoint untouched: %v %v", sc.Endpoints, sc.Checkpoints)
	}
	oneAlert(t, sc, store.EndpointsDir, name, "corrupt_record")
}

// A body that does not hash to its name affects its owner records only.
func TestABodyThatDoesNotMatchItsNameSettlesEachRecordThatRefersToIt(t *testing.T) {
	pair := addr(2, 2)
	settleBody := func(t *testing.T, r *rig, q store.BodyQuarantine) ([]store.InRecord, error) {
		t.Helper()
		sc := r.scan()
		q.Scan = sc
		return r.st.QuarantineBody(q)
	}
	t.Run("a target record that needs the body is deleted", func(t *testing.T) {
		r := worldRig(t)
		in := inRev(1, 1)
		seed(t, r.mem, in, inRev(2, 1))
		seedBodies(t, r.mem, bodyBytes(1), bodyBytes(2))
		_, err := settleBody(t, r, store.BodyQuarantine{Record: in, Alert: newSim(t, r.st).alert("corrupt_body", store.InDir, in.Name())})
		mustDo(t, err)
		sc := settled(t, r)
		if present(t, r, store.InDir, in.Name()) {
			t.Error("the record is still there")
		}
		if len(sc.Alerts) != 1 || len(sc.Bodies) != 1 {
			t.Errorf("one alert, and the body is an orphan to remove: %v %v", sc.Alerts, sc.Bodies)
		}
	})
	t.Run("a target record that no longer needs the body drops the reference", func(t *testing.T) {
		r := worldRig(t)
		in := inRev(1, 1)
		in.Record = deliveredRecord(in.Record)
		seed(t, r.mem, in)
		seedBodies(t, r.mem, bodyBytes(1))
		written, err := settleBody(t, r, store.BodyQuarantine{Record: in, Alert: newSim(t, r.st).alert("corrupt_body", store.InDir, in.Name())})
		mustDo(t, err)
		if len(written) != 1 || written[0].Rev != in.Rev+1 || written[0].Body != nil {
			t.Fatalf("written = %+v", written)
		}
		sc := settled(t, r)
		_, got, _ := find(t, sc, in.Name())
		if got == nil || got.Body != nil || got.Record.State != delivery.StateDelivered || got.Rev != in.Rev+1 || len(sc.Alerts) != 1 || len(sc.Bodies) != 0 {
			t.Errorf("the record keeps its state and drops the reference; the body goes: %#v %v %v", got, sc.Alerts, sc.Bodies)
		}
	})
	t.Run("a source record that needs the body is overwritten as lost", func(t *testing.T) {
		r := worldRig(t)
		out := srcOut(1, false)
		seed(t, r.mem, out, srcOut(2, false), store.SeqRecord{Sender: ep(1), Floors: map[ident.Address]uint64{pair: 1}})
		seedBodies(t, r.mem, bodyBytes(1), bodyBytes(2))
		_, err := settleBody(t, r, store.BodyQuarantine{Record: out, Replacement: lostOutAt(out.Key, base), Alert: newSim(t, r.st).alert("corrupt_body", store.OutDir, out.Name())})
		mustDo(t, err)
		sc := settled(t, r)
		got, _, _ := find(t, sc, out.Name())
		if got == nil || got.Lost == nil || len(got.Feedback) != 1 || got.Body != nil {
			t.Fatalf("not overwritten as lost: %#v", got)
		}
		// m = max(floor 1, the other record's number 2); the lost record's number is lost.
		if f := floorOf(t, sc, 1, pair); f != 3 {
			t.Errorf("floor = %d, want 3", f)
		}
		if len(sc.Alerts) != 1 || len(sc.Bodies) != 1 {
			t.Errorf("one alert, and the lost record's body is an orphan to remove: %v %v", sc.Alerts, sc.Bodies)
		}
	})
	t.Run("a source record that no longer needs the body drops the reference", func(t *testing.T) {
		r := worldRig(t)
		out := finishedOut(1, false)
		body := bodyRef(1)
		out.Body = body
		seed(t, r.mem, out)
		seedBodies(t, r.mem, bodyBytes(1))
		_, err := settleBody(t, r, store.BodyQuarantine{Record: out, Alert: newSim(t, r.st).alert("corrupt_body", store.OutDir, out.Name())})
		mustDo(t, err)
		sc := settled(t, r)
		got, _, _ := find(t, sc, out.Name())
		want := finishedOut(1, false)
		if got == nil || got.Lost != nil || got.Body != nil || !equalRecords(*got, want) || len(sc.Seq) != 0 || len(sc.Alerts) != 1 {
			t.Errorf("only the reference goes, no floor is raised: %#v %v %v", got, sc.Seq, sc.Alerts)
		}
	})
	t.Run("a control record drops the reference", func(t *testing.T) {
		r := worldRig(t)
		c := sessionCtl(1, 5)
		c.Body = bodyRef(1)
		seed(t, r.mem, c, session(5, store.EndpointOpen, ctlName(1)))
		seedBodies(t, r.mem, bodyBytes(1))
		_, err := settleBody(t, r, store.BodyQuarantine{Record: c, Alert: newSim(t, r.st).alert("corrupt_body", store.CtlDir, c.Name())})
		mustDo(t, err)
		sc := settled(t, r)
		if _, _, got := find(t, sc, c.Name()); got == nil || got.Body != nil || len(sc.Bodies) != 0 || len(sc.Alerts) != 1 {
			t.Errorf("only the reference goes: %#v %v %v", got, sc.Bodies, sc.Alerts)
		}
	})
	t.Run("a wrong call fails loudly and writes nothing", func(t *testing.T) {
		r := worldRig(t)
		needing := srcOut(1, false)
		finished := finishedOut(2, false)
		finished.Body = bodyRef(2)
		seed(t, r.mem, needing, finished)
		seedBodies(t, r.mem, bodyBytes(1), bodyBytes(2))
		before := snapshot(t, r.mem, ".")
		alert := func(o store.OutRecord) store.QuarantineAlert {
			return newSim(t, r.st).alert("corrupt_body", store.OutDir, o.Name())
		}
		for name, q := range map[string]store.BodyQuarantine{
			"a source record that needs the body, with no replacement": {Record: needing, Alert: alert(needing)},
			"a source record that does not need the body, with one":    {Record: finished, Replacement: lostOutAt(finished.Key, base), Alert: alert(finished)},
			"a replacement that is not the record's own":               {Record: needing, Replacement: lostOutAt(finished.Key, base), Alert: alert(needing)},
			"a record that is not on the disk":                         {Record: srcOut(9, false), Replacement: lostOutAt(srcOut(9, false).Key, base), Alert: alert(srcOut(9, false))},
			"a record that does not refer to a body":                   {Record: func() store.OutRecord { o := srcOut(1, false); o.Body = nil; return o }(), Replacement: lostOutAt(needing.Key, base), Alert: alert(needing)},
		} {
			q.Scan = r.scan()
			if _, err := r.st.QuarantineBody(q); err == nil {
				t.Errorf("%s: no error", name)
			}
		}
		if diff := equalMaps(snapshot(t, r.mem, "."), before, nil); diff != "" {
			t.Errorf("a refused call changed the disk: %s", diff)
		}
	})
}

// A bad floor file is treated as missing.
func TestADamagedFloorFileIsOverwrittenWithWhatTheRecordsSayAndAlerted(t *testing.T) {
	pair := addr(2, 2)
	name := store.SeqRecord{Sender: ep(1)}.Name()
	t.Run("a damaged body", func(t *testing.T) {
		r := newRig(t)
		seed(t, r.mem, withSeq(1, 3, false), withSeq(2, 2, false), store.SeqRecord{Sender: ep(1), Floors: map[ident.Address]uint64{pair: 40}})
		damageBody(t, r.mem, path.Join(store.SeqDir, name))
		sc := settled(t, r)
		if len(sc.Damaged) != 0 || len(sc.Seq) != 1 || sc.Seq[0].Floors[pair] != 3 {
			t.Errorf("the file must be overwritten with a valid floor file of the highest number the records hold: damaged %v, floors %v", sc.Damaged, sc.Seq)
		}
		if got := store.NewSeqBook(sc).Next(ep(1), pair); got != 4 {
			t.Errorf("Next = %d, want 4", got)
		}
		oneAlert(t, sc, store.SeqDir, name, "corrupt_record")
	})
	t.Run("an unreadable first line", func(t *testing.T) {
		r := newRig(t)
		seed(t, r.mem, withSeq(1, 3, false), store.SeqRecord{Sender: ep(1), Floors: map[ident.Address]uint64{pair: 40}})
		damageHeader(t, r.mem, path.Join(store.SeqDir, name))
		sc := settled(t, r)
		if present(t, r, store.SeqDir, name) || len(sc.Seq) != 0 {
			t.Errorf("nothing says whose floors these were: the file goes, got %v", sc.Seq)
		}
		if got := store.NewSeqBook(sc).Next(ep(1), pair); got != 4 {
			t.Errorf("Next = %d, want 4", got)
		}
		oneAlert(t, sc, store.SeqDir, name, "corrupt_record")
	})
}

// A bad source record always raises the floor to m+1.
func TestADamagedSourceRecordRaisesTheFloorAboveEverythingKnown(t *testing.T) {
	pair := addr(2, 2)
	t.Run("above the highest number of the records left", func(t *testing.T) {
		r := newRig(t)
		for i := 1; i <= 6; i++ {
			seed(t, r.mem, srcOut(i, false))
		}
		damageBody(t, r.mem, path.Join(store.OutDir, srcOut(3, false).Name()))
		damageBody(t, r.mem, path.Join(store.OutDir, srcOut(5, false).Name()))
		sc := settled(t, r)
		// Each settlement reads the floor the one before wrote: 7, then 8.
		if got := floorOf(t, sc, 1, pair); got != 8 {
			t.Errorf("floor = %d, want 8", got)
		}
		if got := store.NewSeqBook(sc).Next(ep(1), pair); got != 9 {
			t.Errorf("Next = %d, want 9", got)
		}
	})
	t.Run("a floor above every record is never lowered", func(t *testing.T) {
		r := newRig(t)
		seed(t, r.mem, srcOut(1, false), srcOut(2, false), store.SeqRecord{Sender: ep(1), Floors: map[ident.Address]uint64{pair: 40}})
		damageBody(t, r.mem, path.Join(store.OutDir, srcOut(2, false).Name()))
		sc := settled(t, r)
		if got := floorOf(t, sc, 1, pair); got != 41 {
			t.Errorf("floor = %d, want 41", got)
		}
	})
	t.Run("a floor file that is damaged too is settled first", func(t *testing.T) {
		r := newRig(t)
		seed(t, r.mem, srcOut(1, false), srcOut(2, false), store.SeqRecord{Sender: ep(1), Floors: map[ident.Address]uint64{pair: 40}})
		damageBody(t, r.mem, path.Join(store.OutDir, srcOut(2, false).Name()))
		damageBody(t, r.mem, path.Join(store.SeqDir, store.SeqRecord{Sender: ep(1)}.Name()))
		sc := settled(t, r)
		if len(sc.Damaged) != 0 || len(sc.Alerts) != 2 {
			t.Errorf("both files are settled, each with one alert: damaged %v alerts %v", sc.Damaged, sc.Alerts)
		}
		if got := store.NewSeqBook(sc).Next(ep(1), pair); got != 3 {
			t.Errorf("Next = %d, want 3: the floor file was lost, the lost record raises above the one record left", got)
		}
	})
}

// A write that meets a damaged floor file does nothing and names the file; the
// caller settles it like any other damaged file and tries again.
func TestAWriteThatMeetsADamagedFloorFileLeavesItToTheSettlement(t *testing.T) {
	pair := addr(2, 2)
	name := store.SeqRecord{Sender: ep(1)}.Name()
	r := newRig(t)
	seed(t, r.mem, srcOutFinished(1), store.SeqRecord{Sender: ep(1), Floors: map[ident.Address]uint64{pair: 40}})
	damageBody(t, r.mem, path.Join(store.SeqDir, name))
	plan := store.PlanSweep(r.scan(), base.Add(farAway))
	err := r.st.ArchiveSweep(plan.Chunk(base.Add(farAway)))
	var de *store.DamagedFileError
	if !errors.As(err, &de) {
		t.Fatalf("error = %v, want a *DamagedFileError", err)
	}
	sc := r.scan()
	mustDo(t, r.st.Quarantine(store.Quarantine{File: de.File, Scan: sc, Alert: newSim(t, r.st).alert("corrupt_record", de.File.Dir, de.File.Name)}))
	plan = store.PlanSweep(r.scan(), base.Add(farAway))
	mustDo(t, r.st.ArchiveSweep(plan.Chunk(base.Add(farAway))))
	if sc := r.scan(); len(sc.Out) != 0 || len(sc.Damaged) != 0 || len(sc.Alerts) != 1 {
		t.Errorf("the sweep must go on after the floor file was settled: %d records, damaged %v, alerts %v", len(sc.Out), sc.Damaged, sc.Alerts)
	}
}

// The alert of a settlement is the store's, derived from where the damage is and
// what shows it. A settlement redone after a crash that wrote the alert and not
// the removal writes the same alert file; a later, different damage of a file of
// the same name, as a resent message taken in again would leave, is an alert of its
// own and never replaces one the owner has acknowledged.
func TestTheAlertOfASettlementFollowsTheDamageAndNotTheCall(t *testing.T) {
	name := inRev(1, 1).Name()
	file := path.Join(store.InDir, name)
	damaged := func(t *testing.T, damage func(testing.TB, *fsxtest.Mem, string)) *rig {
		r := worldRig(t)
		seed(t, r.mem, inRev(1, 1))
		damage(t, r.mem, file)
		return r
	}
	t.Run("a settlement redone after a crash writes the same alert", func(t *testing.T) {
		first := damaged(t, damageBody)
		redo := openRig(t, first.mem.Clone())
		newSim(t, first.st).recoverAll()
		if alerts := first.scan().Alerts; len(alerts) != 1 {
			t.Fatalf("alerts = %v, want one", alerts)
		}
		// The crash: the alert is on the disk, the damaged file is too.
		seed(t, redo.mem, first.scan().Alerts[0])
		sc := settled(t, redo)
		oneAlert(t, sc, store.InDir, name, "corrupt_record")
		if present(t, redo, store.InDir, name) {
			t.Error("the damaged file is still there")
		}
	})
	t.Run("a different damage of the same name is another alert", func(t *testing.T) {
		r := damaged(t, damageBody)
		newSim(t, r.st).recoverAll()
		seed(t, r.mem, inRev(1, 1))
		damageHeader(t, r.mem, file)
		sc := settled(t, r)
		if len(sc.Alerts) != 2 {
			t.Errorf("alerts = %v, want one for each damage", sc.Alerts)
		}
	})
	t.Run("a body that is another file under the same record name is another alert", func(t *testing.T) {
		r := worldRig(t)
		settle := func(body int) {
			in := inRev(1, 1)
			in.Body = bodyRef(body)
			seed(t, r.mem, in)
			_, err := r.st.QuarantineBody(store.BodyQuarantine{Record: in, Alert: newSim(t, r.st).alert("corrupt_body", store.InDir, in.Name())})
			mustDo(t, err)
		}
		settle(1)
		settle(2)
		if sc := r.scan(); len(sc.Alerts) != 2 {
			t.Errorf("alerts = %v, want one for each body", sc.Alerts)
		}
	})
	t.Run("the id is what a test that names the alert file restates", func(t *testing.T) {
		r := damaged(t, damageBody)
		bad, err := r.mem.ReadFile(file)
		mustDo(t, err)
		newSim(t, r.st).recoverAll()
		want := alertOf(newSim(t, r.st).alert("corrupt_record", store.InDir, name), store.InDir, name, bad)
		if alerts := r.scan().Alerts; len(alerts) != 1 || alerts[0].ID != want.ID {
			t.Errorf("alerts = %v, want the alert %s", alerts, want.ID)
		}
	})
}

// The floors of a sender are written only to the floor file of that sender: a
// damaged file whose key names another sender is refused and nothing is written.
func TestAFloorFileIsNotOverwrittenWithTheFloorsOfAnotherSender(t *testing.T) {
	pair := addr(2, 2)
	r := newRig(t)
	seed(t, r.mem, srcOut(1, false),
		store.SeqRecord{Sender: ep(1), Floors: map[ident.Address]uint64{pair: 40}},
		store.SeqRecord{Sender: ep(2), Floors: map[ident.Address]uint64{pair: 7}})
	damageBody(t, r.mem, path.Join(store.SeqDir, store.SeqRecord{Sender: ep(1)}.Name()))
	sc := r.scan()
	if len(sc.Damaged) != 1 || sc.Damaged[0].Dir != store.SeqDir {
		t.Fatalf("damaged = %v, want the floor file", sc.Damaged)
	}
	d := sc.Damaged[0]
	d.Key = ep(2)
	before := snapshot(t, r.mem, ".")
	if err := r.st.Quarantine(store.Quarantine{File: d, Scan: sc, Alert: newSim(t, r.st).alert("corrupt_record", d.Dir, d.Name)}); err == nil {
		t.Fatal("the floor file of one sender was settled with the key of another")
	}
	if diff := equalMaps(snapshot(t, r.mem, "."), before, nil); diff != "" {
		t.Errorf("a refused quarantine changed the disk: %s", diff)
	}
}
