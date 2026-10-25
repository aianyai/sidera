package store_test

import (
	"path"
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
// leaves a creating one alone while its record is lost, quarantined or damaged.

// doubleFaultEndpoints are the two endpoints of the double fault: the one of the
// failed first acceptance and the one the record holds.
var doubleFaultEndpoints = []ident.EndpointID{ep(5), ep(6)}

// seedDoubleFault seeds the endpoints of the double fault, and a delivery derived
// from the record.
func seedDoubleFault(t *testing.T, mem *fsxtest.Mem) {
	seed(t, mem,
		session(5, store.EndpointCreating, ctlName(1)),
		session(6, store.EndpointCreating, ctlName(1)),
		derivedIn(1, 6, 1))
}

// verifyDoubleFault states what the quarantine of the record must have produced.
func verifyDoubleFault(t *testing.T, r *rig) {
	sc := r.scan()
	name := sessionCtl(1, 6).Name()
	id := alertFor(store.CtlDir, name)
	if !hasAlert(sc, id, "control_unknown") {
		t.Errorf("the alert must say a native session may remain: %v", sc.Alerts)
	}
	if _, _, c := find(t, sc, name); c == nil || !c.Lost {
		t.Errorf("the replacement of the control record is missing: %#v", c)
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
		{
			name: "resumed quarantine of a control record that created two session endpoints",
			setup: func(t *testing.T, mem *fsxtest.Mem) {
				seedDoubleFault(t, mem)
				quarantinedDamaged(t, mem, store.CtlDir, sessionCtl(1, 6))
			},
			mode: atomic, mustComplete: true, run: recoverRun,
			verify: verifyDoubleFault,
		},
	}
}

// TestQuarantineClosesEveryCreatingSessionEndpointOfTheDamagedControlRecord walks
// every crash point, of every kind of crash, of the quarantine of a control
// record that two session endpoints still being created name as their creator,
// and of the resumption of such a quarantine: each ends with both endpoints
// closed.
func TestQuarantineClosesEveryCreatingSessionEndpointOfTheDamagedControlRecord(t *testing.T) {
	sink := newSink()
	for i, sc := range doubleFaultScenarios() {
		t.Run(sc.name, func(t *testing.T) {
			st := runScenario(t, &sc, sink, uint64(900+i))
			t.Logf("%d points, %d crash states, %d recoveries", st.points, st.clones, st.recoveries)
		})
	}
}
