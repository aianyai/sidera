package store_test

import (
	"fmt"
	"path"
	"testing"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/handoff"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/store"
)

// The committed control result is the authority for all creation effects. Every
// intermediate disk must keep notification before the head and the head before
// open, and recovery must finish exactly one first prompt per creation at the
// ctl's time. A replay of several creations is one transaction, whose declared
// order holds across them: every delivery, then every head, then every endpoint.
func initialPromptScenario(replay bool, creations int) scenario {
	name := "control-result with an initial prompt"
	if replay {
		name = "initial prompt of a committed creation not yet applied"
		if creations > 1 {
			name = fmt.Sprintf("initial prompts of %d committed creations not yet applied", creations)
		}
	}
	// Creation i is control record i, for session endpoint 4+i and message body i.
	creation := func(t *testing.T, i int) (store.CtlRecord, store.InRecord) {
		return withInitial(created(t, i, 4+i), i)
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
			for i := 1; i <= creations; i++ {
				c, in := withInitial(sessionCtl(i, 4+i), i)
				if replay {
					c, in = creation(t, i)
				}
				seedWithBodies(t, mem, session(4+i, store.EndpointCreating, ctlName(i)), c, in)
			}
		},
		run: func(t *testing.T, r *rig, s *sim) error {
			if replay {
				s.recoverAll()
				return nil
			}
			c, in := creation(t, 1)
			effect := store.CreationEffect(c, openedBy(c), &in)
			return r.st.ControlResult(c, &effect)
		},
		crashed: func(t *testing.T, where string, snap map[string]string) {
			read := func(dir string, rec store.Record) string { return snap[path.Join(dir, rec.Name())] }
			for i := 1; i <= creations; i++ {
				c, in := creation(t, i)
				ep := openedBy(c)
				head := store.AttemptsRecord{Endpoint: ep.ID, Attempts: []handoff.Attempt{{At: c.Record.DeliveredAt(), Outcome: handoff.AttemptDelivered, Kind: handoff.PromptKindFirst}}}
				headWritten := read(store.AttemptsDir, head) == string(mustEncode(t, head))
				notified := in
				notified.Record = applied(t, in.Record, delivery.Event{Type: delivery.EventNotified})
				if headWritten && read(store.InDir, notified) != string(mustEncode(t, notified)) {
					t.Errorf("%s: the attempts head of creation %d is complete before its delivery is notified", where, i)
				}
				if read(store.EndpointsDir, ep) == string(mustEncode(t, ep)) && !headWritten {
					t.Errorf("%s: the endpoint of creation %d is open before its first prompt is settled", where, i)
				}
			}
		},
		verify: func(t *testing.T, r *rig) {
			sc := r.scan()
			if len(sc.Attempts) != creations || len(sc.In) != creations {
				t.Fatalf("initial prompt attempts: %+v and deliveries %d, want one of each per creation (%d)", sc.Attempts, len(sc.In), creations)
			}
			for _, h := range sc.Attempts {
				if len(h.Attempts) != 1 {
					t.Fatalf("initial prompt attempts: %+v, want exactly one", h)
				}
				a := h.Attempts[0]
				if a.Kind != handoff.PromptKindFirst || a.Outcome != handoff.AttemptDelivered {
					t.Errorf("initial attempt %+v is not a delivered first prompt", a)
				}
			}
			for _, c := range sc.Ctl {
				for _, h := range sc.Attempts {
					if h.Endpoint == c.SessionEndpoint && !h.Attempts[0].At.Equal(c.Record.DeliveredAt()) {
						t.Errorf("initial attempt %+v does not reflect the committed creation", h.Attempts[0])
					}
				}
			}
			for _, in := range sc.In {
				if in.Record.Sub != delivery.SubNotified {
					t.Errorf("initial delivery: %+v, want notified", in)
				}
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
