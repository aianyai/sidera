package store_test

import (
	"syscall"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/txn"
)

// endpointStatuses is the status of every session endpoint on the disk, by id.
func endpointStatuses(sc *store.Scan) map[string]store.EndpointStatus {
	out := map[string]store.EndpointStatus{}
	for _, e := range sc.Endpoints {
		out[e.ID.String()] = e.Status
	}
	return out
}

// A control intake that fails before its commit point leaves the session
// endpoint it prepared, being created and naming the control record as its
// creator. The same signal accepted again has the same record name and a new
// endpoint. Once the record is there the first endpoint is not the one it
// creates, and no one else would ever remove it: it holds a token and counts
// against the limit of open endpoints.
func TestEndpointLeftByAFailedControlIntakeIsReclaimedAfterARetry(t *testing.T) {
	sc := scenario{name: "failed control intake"}
	mem := sc.fresh(t)
	f := newFaultyRig(t, mem)
	f.faulty.Fail(fsxtest.Match{Tx: "control-intake", Role: "ctl", Step: fsx.StepCreate, Hit: 1}, syscall.ENOSPC)

	first := sessionCtl(1, 5)
	first.Body = bodyRef(1)
	err := f.st.ControlIntake(bodyBytes(1), ptr(session(5, store.EndpointCreating, ctlName(1))), first)
	if err == nil || txn.IsFatal(err) {
		t.Fatalf("the first attempt must fail without being fatal: %v", err)
	}

	retry := sessionCtl(1, 6)
	retry.Body = bodyRef(1)
	mustDo(t, f.st.ControlIntake(bodyBytes(1), ptr(session(6, store.EndpointCreating, ctlName(1))), retry))
	done := created(t, 1, 6)
	mustDo(t, f.st.ControlResult(done, ptr(openedBy(done)), []store.BodyRef{*bodyRef(1)}))

	r := openRig(t, mem)
	if got := endpointStatuses(r.scan())[ep(5).String()]; got != store.EndpointCreating {
		t.Fatalf("setup: the endpoint of the failed attempt is %v, want creating", got)
	}
	s := newSim(t, r.st)
	s.recoverAll()
	if s.did["orphan-gc-endpoint"] != 1 {
		t.Errorf("orphan-gc-endpoint ran %d times, want once: %v", s.did["orphan-gc-endpoint"], s)
	}
	got := endpointStatuses(r.scan())
	if _, there := got[ep(5).String()]; there {
		t.Errorf("the endpoint of the failed attempt is still on the disk: %v", got)
	}
	if got[ep(6).String()] != store.EndpointOpen {
		t.Errorf("the endpoint of the accepted signal is %v, want open", got[ep(6).String()])
	}
	noWork(t, "after recovery", r.scan(), s.enabled)
}

// A creating endpoint whose control record does not hold the creation of that
// endpoint is not part of any control record: with no intent at all, or with an
// intent that names another endpoint.
func TestOrphanEndpointsNamesAnEndpointItsControlRecordDoesNotCreate(t *testing.T) {
	noIntent := sessionCtl(1, 5)
	noIntent.Intent = nil
	for _, c := range []struct {
		name   string
		ctl    store.CtlRecord
		orphan bool
	}{
		{"a control record without an intent", noIntent, true},
		{"a control record whose intent names another endpoint", sessionCtl(1, 6), true},
		{"a control record whose intent names the endpoint", sessionCtl(1, 5), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			mem := (&scenario{name: "x"}).fresh(t)
			seed(t, mem, session(5, store.EndpointCreating, ctlName(1)), c.ctl)
			r := openRig(t, mem)
			got := store.OrphanEndpoints(r.scan())
			if c.orphan != (len(got) == 1) || (len(got) == 1 && got[0].ID != ep(5)) {
				t.Errorf("OrphanEndpoints = %v, want the endpoint named: %v", got, c.orphan)
			}
			if c.orphan {
				s := newSim(t, r.st)
				s.recoverAll()
				if _, there := endpointStatuses(r.scan())[ep(5).String()]; there {
					t.Errorf("the orphan endpoint was not removed")
				}
			}
		})
	}
}
