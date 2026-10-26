package store_test

import (
	"path"
	"reflect"
	"testing"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/store"
)

// The double fault of a session creation: the first acceptance of a control
// record fails after its endpoint was created, and the signal accepted again has
// the same record name and a new endpoint, so two session endpoints still being
// created name the record as their creator. When that record is damaged in turn,
// its quarantine closes both: an endpoint left behind would hold one of the
// node's endpoint slots for good, because every rule that removes an endpoint
// leaves a creating one alone while its record is lost or damaged.

// doubleFaultEndpoints are the two endpoints of the double fault: the one of the
// failed first acceptance and the one the record holds.
var doubleFaultEndpoints = []ident.EndpointID{ep(5), ep(6)}

// seedDoubleFault seeds the endpoints of the double fault, and a delivery derived
// from the record.
func seedDoubleFault(t *testing.T, mem *fsxtest.Mem) {
	seed(t, mem,
		session(5, store.EndpointCreating, ctlName(1)),
		session(6, store.EndpointCreating, ctlName(1)),
		derivedIn(1, 6))
}

// verifyDoubleFault states what the quarantine of the record must have produced.
func verifyDoubleFault(t *testing.T, r *rig) {
	sc := r.scan()
	name := sessionCtl(1, 6).Name()
	if !onlyAlert(sc, store.CtlDir, name, "control_unknown") {
		t.Errorf("the alert must say a native session may remain: %v", sc.Alerts)
	}
	if _, _, c := find(t, sc, name); c != nil {
		t.Errorf("a control record has no replacement, yet one is there: %#v", c)
	}
	for _, id := range doubleFaultEndpoints {
		if e := endpointOf(sc, id); e == nil || e.Status != store.EndpointClosed || e.Token != nil {
			t.Errorf("the session endpoint %v of the damaged control record must be closed: %#v", id, e)
		}
	}
}

func doubleFaultScenarios() []scenario {
	return []scenario{
		{
			name: "quarantine of a control record that created two session endpoints",
			setup: func(t *testing.T, mem *fsxtest.Mem) {
				seedDoubleFault(t, mem)
				seed(t, mem, sessionCtl(1, 6))
				damageBody(t, mem, path.Join(store.CtlDir, sessionCtl(1, 6).Name()))
			},
			mode: atomic, mustComplete: true, run: recoverRun,
			verify: verifyDoubleFault,
		},
	}
}

// TestQuarantineClosesEveryCreatingSessionEndpointOfTheDamagedControlRecord walks
// every crash point, of every kind of crash, of the quarantine of a control
// record that two session endpoints still being created name as their creator:
// each ends with both endpoints closed.
func TestQuarantineClosesEveryCreatingSessionEndpointOfTheDamagedControlRecord(t *testing.T) {
	sink := newSink()
	for i, sc := range doubleFaultScenarios() {
		t.Run(sc.name, func(t *testing.T) {
			st := runScenario(t, &sc, sink, uint64(900+i))
			t.Logf("%d points, %d crash states, %d recoveries", st.points, st.clones, st.recoveries)
		})
	}
	t.Logf("%d recoveries after a crash during recovery", runNestedWalks(t, sink))
}

// The session endpoints a quarantine closes are decided by the store from the
// scan it is given, not by the caller: a list the caller built could miss an
// endpoint (a slot leaked for good) or name an open one or another record's (a
// live session closed and its deliveries drained, which cannot be undone).

// TestQuarantineSessionsAreTheEndpointsCreatedByTheRecordThatAreNotOpen states
// the rule by hand, row by row.
func TestQuarantineSessionsAreTheEndpointsCreatedByTheRecordThatAreNotOpen(t *testing.T) {
	closed := func(e store.EndpointRecord) store.EndpointRecord {
		e.Status, e.Token = store.EndpointClosed, nil
		return e
	}
	creatingOfRecord := session(5, store.EndpointCreating, ctlName(1))
	secondCreating := session(6, store.EndpointCreating, ctlName(1))
	alreadyClosed := session(7, store.EndpointClosed, ctlName(1))
	open := session(8, store.EndpointOpen, ctlName(1))
	ofAnother := session(9, store.EndpointCreating, ctlName(2))
	direct := session(4, store.EndpointCreating, "")
	sc := &store.Scan{Endpoints: []store.EndpointRecord{creatingOfRecord, secondCreating, alreadyClosed, open, ofAnother, direct}}

	rows := []struct {
		name string
		ctl  string
		want []store.EndpointRecord
	}{
		{"every creating endpoint of the record is closed, and one already closed is written again", sessionCtl(1, 6).Name(),
			[]store.EndpointRecord{closed(creatingOfRecord), closed(secondCreating), closed(alreadyClosed)}},
		{"an open endpoint naming the record stays open, and another record's endpoint is not touched", sessionCtl(2, 9).Name(),
			[]store.EndpointRecord{closed(ofAnother)}},
		{"a record that created nothing closes nothing", sessionCtl(3, 3).Name(), nil},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			if got := store.QuarantineSessions(sc, row.ctl); !reflect.DeepEqual(got, row.want) {
				t.Errorf("QuarantineSessions(%s) = %#v, want %#v", row.ctl, got, row.want)
			}
		})
	}
	// The scan it reads is not changed.
	if sc.Endpoints[0].Status != store.EndpointCreating || sc.Endpoints[0].Token == nil {
		t.Errorf("QuarantineSessions changed the endpoint of the scan: %#v", sc.Endpoints[0])
	}
}

// sessionMix seeds the endpoints a quarantine of control record 1 meets: two
// still being created that name it, one that was opened and still names it, one
// still being created for another record, and one runtime endpoint with no
// creator.
func sessionMix(t *testing.T, mem *fsxtest.Mem) {
	seed(t, mem,
		session(5, store.EndpointCreating, ctlName(1)),
		session(6, store.EndpointCreating, ctlName(1)),
		session(7, store.EndpointOpen, ctlName(1)),
		session(8, store.EndpointCreating, ctlName(2)),
		session(4, store.EndpointOpen, ""))
}

// verifySessionMix states what the quarantine of record 1 leaves of sessionMix.
func verifySessionMix(t *testing.T, r *rig) {
	t.Helper()
	sc := r.scan()
	for _, want := range []struct {
		id     ident.EndpointID
		status store.EndpointStatus
	}{
		{ep(5), store.EndpointClosed},
		{ep(6), store.EndpointClosed},
		{ep(7), store.EndpointOpen},
		{ep(8), store.EndpointCreating},
		{ep(4), store.EndpointOpen},
	} {
		e := endpointOf(sc, want.id)
		if e == nil || e.Status != want.status {
			t.Errorf("endpoint %v after the quarantine: %#v, want status %v", want.id, e, want.status)
			continue
		}
		if (want.status == store.EndpointClosed) != (e.Token == nil) {
			t.Errorf("endpoint %v is %v and holds token %v", want.id, e.Status, e.Token)
		}
	}
}

// TestQuarantineClosesOnlyTheEndpointsTheStoreFindsInTheScan runs the quarantine
// of a control record with the caller giving no list.
func TestQuarantineClosesOnlyTheEndpointsTheStoreFindsInTheScan(t *testing.T) {
	rec := sessionCtl(1, 6)
	t.Run("quarantine", func(t *testing.T) {
		r := newRig(t)
		sessionMix(t, r.mem)
		seed(t, r.mem, rec)
		damageBody(t, r.mem, path.Join(store.CtlDir, rec.Name()))
		sc := r.scan()
		if len(sc.Damaged) != 1 {
			t.Fatalf("damaged = %v, want the control record", sc.Damaged)
		}
		mustDo(t, r.st.Quarantine(store.Quarantine{File: sc.Damaged[0], Scan: sc, Alert: newSim(t, r.st).alert("control_unknown", store.CtlDir, rec.Name())}))
		verifySessionMix(t, r)
	})
	t.Run("a control record is not quarantined without the scan that lists its endpoints", func(t *testing.T) {
		r := newRig(t)
		sessionMix(t, r.mem)
		seed(t, r.mem, rec)
		damageBody(t, r.mem, path.Join(store.CtlDir, rec.Name()))
		sc := r.scan()
		before := snapshot(t, r.mem, ".")
		if err := r.st.Quarantine(store.Quarantine{File: sc.Damaged[0], Alert: newSim(t, r.st).alert("control_unknown", store.CtlDir, rec.Name())}); err == nil {
			t.Fatal("Quarantine of a control record without a scan succeeded: its session endpoints would be left open")
		}
		if diff := equalMaps(snapshot(t, r.mem, "."), before, nil); diff != "" {
			t.Errorf("a refused quarantine changed the disk: %s", diff)
		}
	})
}
