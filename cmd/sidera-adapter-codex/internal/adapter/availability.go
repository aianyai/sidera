package adapter

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// conditionsLocked is what currently keeps the adapter from working, as the
// protocol states it. The caller holds a.mu.
func (a *App) conditionsLocked() []adapterproto.Availability {
	conds := []adapterproto.Availability{}
	if len(a.missing) > 0 {
		conds = append(conds, adapterproto.Availability{
			Condition: adapterproto.ConditionDependencyMissing,
			Detail:    cut(a.missing.String(), adapterproto.MaxDetailLen),
		})
	}
	if a.liveClientLocked() == nil {
		conds = append(conds, adapterproto.Availability{
			Condition: adapterproto.ConditionNativeUnavailable,
			Detail:    "the App Server cannot be reached",
		})
	} else if !a.signedIn {
		conds = append(conds, adapterproto.Availability{
			Condition: adapterproto.ConditionRuntimeAuth,
			Detail:    "Codex has no signed-in account",
		})
	}
	return append(conds, a.threadConditionsLocked()...)
}

// threadConditionsLocked is the condition of each attached session whose thread
// takes no prompt, limited to that session's endpoint: one such thread must not
// block the sessions the adapter created. The caller holds a.mu.
func (a *App) threadConditionsLocked() []adapterproto.Availability {
	var conds []adapterproto.Availability
	for ep, s := range a.sessions {
		if s.refusal == "" {
			continue
		}
		conds = append(conds, adapterproto.Availability{
			Condition: adapterproto.ConditionNativeUnavailable,
			Endpoints: []adapterproto.EndpointRef{ep},
			SinceMS:   s.refusalSince,
			Detail:    s.refusal,
		})
	}
	sort.Slice(conds, func(i, j int) bool { return conds[i].Endpoints[0] < conds[j].Endpoints[0] })
	return conds
}

// notePromptRefusal records what the server answered to the queueing of a prompt for
// an attached session: err nil, or an answer that says the thread takes no prompt,
// ends or begins the session's condition. It reports the snapshot when that changes
// it. The caller does not hold a.mu.
func (a *App) notePromptRefusal(s *session, err error) {
	detail := ""
	if err != nil {
		detail = cut("the thread takes no prompt: "+err.Error(), adapterproto.MaxDetailLen)
	}
	a.setRefusal(s, detail)
}

// setRefusal sets the condition of an attached session's endpoint, empty for none,
// and reports the snapshot when that changes it. The caller does not hold a.mu.
func (a *App) setRefusal(s *session, detail string) {
	a.mu.Lock()
	if s.refusal == detail {
		a.mu.Unlock()
		return
	}
	if s.refusal == "" || detail == "" {
		s.refusalSince = time.Now().UnixMilli()
	}
	s.refusal = detail
	a.mu.Unlock()
	a.publishAvailability(false)
}

// publishAvailability sends the full snapshot of what keeps the adapter from
// working. Unless force is set it sends only when the snapshot changed; every
// snapshot replaces the one before, and the time a condition began is the time its
// snapshot changed.
func (a *App) publishAvailability(force bool) {
	a.mu.Lock()
	if !a.started || a.conn == nil {
		a.mu.Unlock()
		return
	}
	conds := a.conditionsLocked()
	key, _ := json.Marshal(conds)
	if !force && string(key) == a.lastAvail {
		a.mu.Unlock()
		return
	}
	if string(key) != a.lastAvail || a.availSince == 0 {
		a.availSince = time.Now().UnixMilli()
	}
	a.lastAvail = string(key)
	for i := range conds {
		if conds[i].SinceMS == 0 {
			conds[i].SinceMS = a.availSince
		}
	}
	a.mu.Unlock()
	a.send("adapter.availability", func(ctx context.Context) error {
		return a.conn.Availability(ctx, adapterproto.AvailabilityParams{Conditions: conds})
	})
}
