package store_test

import (
	"path"
	"testing"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/handoff"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/store"
)

// The committed control result is the authority for all creation effects. Every
// intermediate disk must keep notification before the head and the head before
// open, and recovery must finish exactly one first prompt at the ctl's time.
func initialPromptScenario(replay bool) scenario {
	name := "control-result with an initial prompt"
	if replay {
		name = "initial prompt of a committed creation not yet applied"
	}
	return scenario{
		name: name, mode: atomic, mustComplete: replay, bodiesResolve: true,
		durable: func() string {
			if replay {
				return ""
			}
			return "control-result/ctl.sync-dir#1"
		}(),
		setup: func(t *testing.T, mem *fsxtest.Mem) {
			c, in := withInitial(sessionCtl(1, 5), 1)
			if replay {
				c, in = withInitial(created(t, 1, 5), 1)
			}
			seedWithBodies(t, mem, session(5, store.EndpointCreating, ctlName(1)), c, in)
		},
		run: func(t *testing.T, r *rig, s *sim) error {
			if replay {
				s.recoverAll()
				return nil
			}
			c, in := withInitial(created(t, 1, 5), 1)
			effect := store.CreationEffect(c, openedBy(c), &in)
			return r.st.ControlResult(c, &effect)
		},
		crashed: func(t *testing.T, where string, snap map[string]string) {
			c, in := withInitial(created(t, 1, 5), 1)
			ep := openedBy(c)
			head := store.AttemptsRecord{Endpoint: ep.ID, Attempts: []handoff.Attempt{{At: c.Record.DeliveredAt(), Outcome: handoff.AttemptDelivered, Kind: handoff.PromptKindFirst}}}
			read := func(dir string, rec store.Record) string { return snap[path.Join(dir, rec.Name())] }
			headWritten := read(store.AttemptsDir, head) == string(mustEncode(t, head))
			notified := in
			notified.Record = applied(t, in.Record, delivery.Event{Type: delivery.EventNotified})
			if headWritten && read(store.InDir, notified) != string(mustEncode(t, notified)) {
				t.Errorf("%s: the attempts head is complete before its delivery is notified", where)
			}
			if read(store.EndpointsDir, ep) == string(mustEncode(t, ep)) && !headWritten {
				t.Errorf("%s: the endpoint is open before its first prompt is settled", where)
			}
		},
		verify: func(t *testing.T, r *rig) {
			sc := r.scan()
			if len(sc.Attempts) != 1 || len(sc.Attempts[0].Attempts) != 1 {
				t.Fatalf("initial prompt attempts: %+v, want exactly one", sc.Attempts)
			}
			if a := sc.Attempts[0].Attempts[0]; a.Kind != handoff.PromptKindFirst || a.Outcome != handoff.AttemptDelivered || !a.At.Equal(sc.Ctl[0].Record.DeliveredAt()) {
				t.Errorf("initial attempt %+v does not reflect the committed creation", a)
			}
			if len(sc.In) != 1 || sc.In[0].Record.Sub != delivery.SubNotified {
				t.Errorf("initial delivery: %+v, want notified", sc.In)
			}
		},
	}
}

func TestOldCreationResultDoesNotReplaceLaterPromptState(t *testing.T) {
	r := newRig(t)
	c, in := withInitial(created(t, 1, 5), 1)
	ep := openedBy(c)
	head := store.AttemptsRecord{Endpoint: ep.ID, Attempts: []handoff.Attempt{
		{At: base, Outcome: handoff.AttemptDelivered, Kind: handoff.PromptKindFirst},
		{At: base.Add(handoff.DefaultMinInterval), Outcome: handoff.AttemptDelivered, Kind: handoff.PromptKindReprompt},
	}}
	seedWithBodies(t, r.mem, c, ep, in, head)
	if work := store.EffectReplays(r.scan()); len(work) != 0 {
		t.Fatalf("old creation of an open endpoint has replay work: %+v", work)
	}
	newSim(t, r.st).recoverAll()
	got := r.scan().Attempts
	if len(got) != 1 || !equalRecords(got[0], head) {
		t.Errorf("later attempts were replaced: %+v", got)
	}
}
