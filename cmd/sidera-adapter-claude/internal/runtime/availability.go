package runtime

import (
	"context"
	"sort"
	"sync"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// availability is the conditions the adapter reports to the daemon. The daemon
// takes each report as the whole truth, so the conditions of the last probe and
// those of the attached sessions are always sent together.
type availability struct {
	mu     sync.Mutex
	probe  []adapterproto.Availability
	native map[adapterproto.EndpointRef]nativeDown
}

type nativeDown struct {
	sinceMS int64
	detail  string
}

// setProbe replaces the conditions of the last probe and reports.
func (a *App) setProbe(ctx context.Context, conditions []adapterproto.Availability) {
	a.avail.mu.Lock()
	defer a.avail.mu.Unlock()
	a.avail.probe = conditions
	a.sendAvailability(ctx)
}

// setNativeUnavailable reports, or with an empty detail stops reporting, that the
// native session of an attached endpoint cannot be reached. A report that
// changes nothing sends nothing.
func (a *App) setNativeUnavailable(endpoint adapterproto.EndpointRef, detail string) {
	a.avail.mu.Lock()
	defer a.avail.mu.Unlock()
	cur, down := a.avail.native[endpoint]
	switch {
	case detail == "" && !down:
		return
	case detail == "":
		delete(a.avail.native, endpoint)
	case down && cur.detail == detail:
		return
	default:
		if a.avail.native == nil {
			a.avail.native = map[adapterproto.EndpointRef]nativeDown{}
		}
		since := a.nowMS()
		if down {
			since = cur.sinceMS
		}
		a.avail.native[endpoint] = nativeDown{sinceMS: since, detail: detail}
	}
	a.sendAvailability(a.life)
}

// sendAvailability sends the snapshot. The caller holds a.avail.mu, which also
// keeps the snapshots in order.
func (a *App) sendAvailability(ctx context.Context) {
	snap := append([]adapterproto.Availability{}, a.avail.probe...)
	endpoints := make([]adapterproto.EndpointRef, 0, len(a.avail.native))
	for e := range a.avail.native {
		endpoints = append(endpoints, e)
	}
	sort.Slice(endpoints, func(i, j int) bool { return endpoints[i] < endpoints[j] })
	for _, e := range endpoints {
		d := a.avail.native[e]
		snap = append(snap, adapterproto.Availability{
			Condition: adapterproto.ConditionNativeUnavailable, Endpoints: []adapterproto.EndpointRef{e},
			SinceMS: d.sinceMS, Detail: clip(d.detail),
		})
	}
	if err := a.conn().Availability(ctx, adapterproto.AvailabilityParams{Conditions: snap}); err != nil {
		a.log.Warn("the availability was not sent", "error", err)
	}
}
