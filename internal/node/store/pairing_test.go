package store_test

import (
	"bytes"
	"encoding/hex"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/recfile"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/pairing"
)

// Each write of the pairing records puts its file where a scan finds it, as it
// was given; a removal takes the file away, and removing what is not there is not
// an error and writes nothing. With no file a scan reports no counters and no
// expectations.
func TestPairingRecordsComeBackFromTheDisk(t *testing.T) {
	r := newRig(t)
	empty := r.scan()
	if len(empty.PairingTargets)+len(empty.PairingInitiators)+len(empty.PairingExpectations) != 0 || !equalRecords(empty.PairingLimits, pairing.Limits{}) {
		t.Fatalf("a scan of nothing found %#v", empty)
	}

	target, initiator, limits, expectations := pairingTargetOf(t), pairingInitiatorOf(t), pairingLimitsOf(t), pairingExpectationsOf(t)
	mustDo(t, r.st.PairingTargetPut(target))
	mustDo(t, r.st.PairingTargetPut(target)) // replaces the file of the same request
	mustDo(t, r.st.PairingInitiatorPut(initiator))
	mustDo(t, r.st.PairingLimitsPut(limits))
	mustDo(t, r.st.PairingExpectationsPut(expectations))

	sc := r.scan()
	if len(sc.Damaged) != 0 || len(sc.Unknown) != 0 {
		t.Fatalf("damaged %v, unknown %v", sc.Damaged, sc.Unknown)
	}
	if len(sc.PairingTargets) != 1 || !equalRecords(sc.PairingTargets[0], target) {
		t.Errorf("targets: %#v", sc.PairingTargets)
	}
	if len(sc.PairingInitiators) != 1 || !equalRecords(sc.PairingInitiators[0], initiator) {
		t.Errorf("initiators: %#v", sc.PairingInitiators)
	}
	if !equalRecords(sc.PairingLimits, limits) {
		t.Errorf("limits: %#v", sc.PairingLimits)
	}
	if !equalRecords(sc.PairingExpectations, expectations) {
		t.Errorf("expectations: %#v", sc.PairingExpectations)
	}

	mustDo(t, r.st.PairingTargetDelete(target.ID()))
	mustDo(t, r.st.PairingInitiatorDelete(initiator.ID()))
	sc = r.scan()
	if len(sc.PairingTargets)+len(sc.PairingInitiators) != 0 {
		t.Errorf("after the removals: %#v, %#v", sc.PairingTargets, sc.PairingInitiators)
	}
	before := r.rec.count()
	mustDo(t, r.st.PairingTargetDelete(target.ID()))
	mustDo(t, r.st.PairingInitiatorDelete(initiator.ID()))
	if r.rec.count() != before {
		t.Error("removing a request with no record wrote something")
	}
}

// The expectations are also written inside a transaction another package opens.
func TestPairingExpectationsWriteInsideAnotherTransaction(t *testing.T) {
	r := newRig(t)
	list := pairingExpectationsOf(t)
	p, err := store.PreparePairingExpectations(list)
	mustDo(t, err)
	res, err := store.PrepareAdminResult(adminResult(1))
	mustDo(t, err)
	tx := r.w.Begin(&declXExpect)
	mustDo(t, store.PutPairingExpectations(tx, store.RolePairingExpectations, p))
	mustDo(t, store.PutAdminResult(tx, store.RoleResult, res))
	mustDo(t, tx.Commit())
	if got := r.scan().PairingExpectations; !equalRecords(got, list) {
		t.Errorf("expectations: %#v", got)
	}
}

// A pairing file is read as its kind says and is refused, as a damaged body,
// when what it holds is not what the key and the kind make it: a request under
// the key of another, bytes of another record, or bytes the pairing package does
// not accept. The writer cannot make such a file.
func TestPairingRecordsAreHeldToTheirKeys(t *testing.T) {
	target := pairingTargetOf(t)
	targetBytes, initiatorBytes := pairingBytes(t, pairingTargetHex), pairingBytes(t, pairingInitiatorHex)
	limitsBytes, expectationsBytes := pairingBytes(t, pairingLimitsHex), pairingBytes(t, pairingExpectationsHex)
	id := string(store.PairingKeyBytes(target.ID()))
	other := target.ID()
	other[0] ^= 1
	otherID := string(store.PairingKeyBytes(other))

	for _, c := range []struct {
		name      string
		kind      store.Kind
		key       string
		body      []byte
		wantError bool
	}{
		{"a target", store.KindPairingTarget, id, targetBytes, false},
		{"an initiator", store.KindPairingInitiator, id, initiatorBytes, false},
		{"the limits", store.KindPairingLimits, "singleton", limitsBytes, false},
		{"the expectations", store.KindPairingExpectations, "singleton", expectationsBytes, false},

		{"a target under the key of another request", store.KindPairingTarget, otherID, targetBytes, true},
		{"an initiator under the key of another request", store.KindPairingInitiator, otherID, initiatorBytes, true},
		{"an initiator in the place of a target", store.KindPairingTarget, id, initiatorBytes, true},
		{"limits in the place of the expectations", store.KindPairingExpectations, "singleton", limitsBytes, true},
		{"a target with trailing bytes", store.KindPairingTarget, id, append(append([]byte(nil), targetBytes...), 0), true},
		{"empty limits bytes", store.KindPairingLimits, "singleton", nil, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			file, err := recfile.EncodeFrame(string(c.kind), []byte(c.key), c.body)
			if err != nil {
				t.Fatal(err)
			}
			rec, dmg := store.DecodeRecord(c.kind.Dir(), nameOf(string(c.kind), c.key), file)
			if (dmg != nil) != c.wantError {
				t.Fatalf("damage = %+v, want damage %v", dmg, c.wantError)
			}
			if dmg != nil {
				if dmg.Class != store.BodyDamaged || dmg.Reason != store.ReasonBodyInvalid {
					t.Errorf("damage = class %d reason %d (%s), want a body damage of an invalid body", dmg.Class, dmg.Reason, dmg.Detail)
				}
				return
			}
			again, err := store.EncodeRecord(rec)
			if err != nil || !bytes.Equal(again, file) {
				t.Errorf("decode then encode: %v", err)
			}
		})
	}

	// A key that does not name a request or the one file of a singleton kind is
	// not a key of the kind.
	for name, c := range map[string]struct {
		kind store.Kind
		key  string
	}{
		"a request key of zeros":      {store.KindPairingTarget, hex.EncodeToString(make([]byte, 32))},
		"a request key in capitals":   {store.KindPairingInitiator, "ABCDEF" + id[6:]},
		"a request key too short":     {store.KindPairingTarget, id[2:]},
		"a limits key of a request":   {store.KindPairingLimits, id},
		"an expectations key of none": {store.KindPairingExpectations, "other"},
	} {
		if _, err := store.ParseKey(c.kind, []byte(c.key)); err == nil {
			t.Errorf("%s: ParseKey accepted it", name)
		}
	}
	if _, err := store.EncodeRecord(store.PairingTargetRecord{}); err == nil {
		t.Error("a target record with no identifier was encoded")
	}
}

// The pairing rules decide when a pairing record ends, so no retention of the
// store does: a sweep takes none of them, however late it is.
func TestPairingRecordsAreNeverSwept(t *testing.T) {
	recs := pairingRecords(t)
	for _, rec := range recs {
		if until, finished := store.RetainUntil(rec); finished || !until.IsZero() {
			t.Errorf("%T: RetainUntil = %v, %v, want no end", rec, until, finished)
		}
	}
	r := newRig(t)
	seed(t, r.mem, recs...)
	plan := store.PlanSweep(r.scan(), base.Add(100*365*24*time.Hour))
	if n := len(plan.Records) + len(plan.Ctl) + len(plan.Outs) + len(plan.OutLosts) + len(plan.Endpoints); n != 0 {
		t.Errorf("a sweep far in the future takes %d of them: %+v", n, plan)
	}
}
