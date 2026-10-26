package engine_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/store"
)

// A control record that is damaged is quarantined, and the quarantine closes the
// session endpoints it left being created. A control signal that failed to be
// accepted leaves such an endpoint, and the signal accepted again makes another
// under the same record name: when that record is damaged in turn, both are
// the endpoints of the quarantined record, and both are closed by the same
// transaction, not only the first in the order of the scan. A session that is open
// is a live one and stays open.
//
// The store decides which endpoints a control record's quarantine closes
// (store.QuarantineSessions), from the scan the engine hands it, so the engine
// passes no list. The owner is told with the alert of a session nobody controls:
// the creation that would have settled the endpoints went with the record, and a
// native session may exist.
func TestQuarantineClosesEveryCreatingSessionEndpointOfTheDamagedControlRecord(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	hash := strings.Repeat("a1", 32)
	creating := func(i byte) store.EndpointRecord {
		return store.EndpointRecord{
			ID: mustEndpoint(t, i), Kind: sessionKind, Instance: "chat", Status: store.EndpointCreating,
			Token: bytes.Repeat([]byte{i}, store.TokenLen), Session: &store.SessionInfo{Preset: "default"}, CreatedBy: hash,
		}
	}
	first, second := creating(90), creating(91)
	open := creating(92)
	open.Status, open.Session = store.EndpointOpen, &store.SessionInfo{Preset: "default", NativeRef: "native-1"}
	for _, ep := range []store.EndpointRecord{first, second, open} {
		r.seedRecord(ep)
	}
	r.seedFile(store.CtlDir, hash+".rec", []byte("not a record"))

	e, rep, err := r.open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.stop2(e)
	if rep.Rules["quarantine"] != 1 {
		t.Errorf("report %v, want the quarantine once", rep.Rules)
	}
	sc := r.scan()
	for _, ep := range sc.Endpoints {
		switch ep.ID {
		case first.ID, second.ID:
			if ep.Status != store.EndpointClosed || len(ep.Token) != 0 {
				t.Errorf("endpoint %s is %v with a token of %d bytes, want closed and none: it was being created by the damaged record", ep.ID, ep.Status, len(ep.Token))
			}
		case open.ID:
			if ep.Status != store.EndpointOpen {
				t.Errorf("the open session %s is %v, want it left open", ep.ID, ep.Status)
			}
		}
	}
	if len(sc.Ctl) != 0 || len(sc.Damaged) != 0 {
		t.Errorf("%d control records and %d damaged files remain, want none", len(sc.Ctl), len(sc.Damaged))
	}
	var told bool
	for _, a := range sc.Alerts {
		told = told || a.Kind == "control_unknown"
	}
	if !told {
		t.Errorf("the alerts are %+v, want one of kind control_unknown: a session being created may exist", sc.Alerts)
	}
}
