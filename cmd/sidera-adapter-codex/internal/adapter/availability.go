package adapter

import (
	"context"
	"encoding/json"
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
	return conds
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
		conds[i].SinceMS = a.availSince
	}
	a.mu.Unlock()
	a.send("adapter.availability", func(ctx context.Context) error {
		return a.conn.Availability(ctx, adapterproto.AvailabilityParams{Conditions: conds})
	})
}
