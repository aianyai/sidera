package adapter

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/config"
	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/lane"
	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/state"
	"github.com/aianyai/sidera/sdk/go/adapterkit"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// InstanceKey is the key of the instance endpoint, the address of Hermes itself.
const InstanceKey = "hermes"

const (
	conversationName = "Hermes conversation"
	orphanName       = "Hermes (unattributed)"
)

// Endpoint is a direct endpoint the adapter holds: its key and the daemon's
// reference to it.
type Endpoint struct {
	Key string
	Ref adapterproto.EndpointRef
}

// Placement is the endpoint a card sends from.
type Placement struct {
	Endpoint
	// Reason, when not empty, says why the card is sent from the shared endpoint
	// instead of its own; it is meant for a comment on the card.
	Reason string
}

// endpoints keeps the direct endpoints: the instance endpoint, which is the address
// of Hermes and is visible in the directory, and one hidden endpoint per Hermes
// conversation or cron job plus the shared orphan, which are the addresses a peer
// replies to. The conversation endpoints are recorded in the state, and a record is
// written before the endpoint is used, so that a restart finds the same set.
type endpoints struct {
	conn  *adapterkit.Conn
	store *state.Store
	log   *slog.Logger
	now   func() time.Time
	tm    timing
	max   int

	// mu serializes registering and retiring, so that one key is never registered
	// twice and a retirement never races a placement. It is held across calls to the
	// daemon, which may wait for it to be running; nothing that the receive loop
	// needs is guarded by it.
	mu sync.Mutex
	// hidden holds the keys whose declaration is known to be in force.
	hidden map[string]bool

	refMu    sync.RWMutex
	instance adapterproto.EndpointRef
}

func newEndpoints(conn *adapterkit.Conn, store *state.Store, log *slog.Logger, now func() time.Time, tm timing, max int) *endpoints {
	return &endpoints{conn: conn, store: store, log: log, now: now, tm: tm, max: max, hidden: map[string]bool{}}
}

// adopt takes the endpoints the daemon lists in adapter.start as the truth about
// which are open: a listed endpoint the state does not know (registered, then the
// process ended before the record was written) is recorded, one with another
// reference is corrected, and a recorded one that is not listed is forgotten. It
// does not call the daemon, which is not running yet.
func (e *endpoints) adopt(listed []adapterproto.DirectAttach) error {
	return e.store.Update(func(d *state.Data) error {
		open := map[string]bool{}
		for _, l := range listed {
			if l.Key == InstanceKey {
				e.setInstance(l.Endpoint)
				continue
			}
			open[l.Key] = true
			if rec, ok := d.Endpoints[l.Key]; ok {
				rec.Ref = string(l.Endpoint)
			} else {
				d.Endpoints[l.Key] = &state.Endpoint{Key: l.Key, Ref: string(l.Endpoint), LastActivity: e.now()}
			}
		}
		for key := range d.Endpoints {
			if !open[key] {
				delete(d.Endpoints, key)
			}
		}
		return nil
	})
}

func (e *endpoints) setInstance(ref adapterproto.EndpointRef) {
	e.refMu.Lock()
	e.instance = ref
	e.refMu.Unlock()
}

// list returns the endpoints the receive loop reads: the instance endpoint once it
// is registered, then the recorded ones.
func (e *endpoints) list() []Endpoint {
	var out []Endpoint
	e.refMu.RLock()
	if e.instance != "" {
		out = append(out, Endpoint{Key: InstanceKey, Ref: e.instance})
	}
	e.refMu.RUnlock()
	d := e.store.View()
	for _, rec := range d.EndpointList() {
		out = append(out, Endpoint{Key: rec.Key, Ref: adapterproto.EndpointRef(rec.Ref)})
	}
	return out
}

// ref returns the daemon's reference to the open endpoint of the key.
func (e *endpoints) ref(key string) (adapterproto.EndpointRef, bool) {
	for _, ep := range e.list() {
		if ep.Key == key {
			return ep.Ref, true
		}
	}
	return "", false
}

// setup registers the instance endpoint and makes sure every recorded endpoint is
// hidden, waiting for the daemon as long as it takes. It runs after adapter.start
// is answered. Hiding is repeated for the recorded endpoints because a process
// that ended between registering and declaring leaves one visible.
func (e *endpoints) setup(ctx context.Context, displayName string) {
	e.retry(ctx, "registering the instance endpoint", func() error {
		reg, err := e.conn.Register(ctx, adapterproto.RegisterParams{
			Key:         InstanceKey,
			DisplayName: displayName,
			Description: "Hermes Agent. A message sent here is a new request for Hermes; it becomes a card on its Kanban board.",
		})
		if err == nil {
			e.setInstance(reg.Endpoint)
		}
		return err
	})
	d := e.store.View()
	for _, rec := range d.EndpointList() {
		e.retry(ctx, "hiding the endpoint "+rec.Key, func() error {
			e.mu.Lock()
			defer e.mu.Unlock()
			return e.hide(ctx, rec.Key, adapterproto.EndpointRef(rec.Ref))
		})
	}
}

// retry runs fn until it succeeds or ctx ends, pausing longer after each failure.
// The daemon holds an endpoint request until it is running, so a failure is rare
// and a long wait is not one.
func (e *endpoints) retry(ctx context.Context, what string, fn func() error) {
	b := newBackoff(e.tm.registerMin, e.tm.registerMax)
	for ctx.Err() == nil {
		err := fn()
		if err == nil {
			return
		}
		if ctx.Err() != nil {
			return
		}
		pause := b.next()
		e.log.Warn(what, "error", err, "retry_in", pause)
		sleep(ctx, pause)
	}
}

// hide declares the endpoint hidden from the directory. The caller holds mu.
func (e *endpoints) hide(ctx context.Context, key string, ref adapterproto.EndpointRef) error {
	if e.hidden[key] {
		return nil
	}
	hidden := true
	if _, err := e.conn.Declare(ctx, adapterproto.DeclareParams{Endpoint: ref, HiddenFromDirectory: &hidden}); err != nil {
		return fmt.Errorf("declaring %s hidden: %w", key, err)
	}
	e.hidden[key] = true
	return nil
}

// Place returns the endpoint a card of the source sends from. A conversation or cron
// job that has none yet gets one, registered, recorded and hidden before it is
// returned; when the cap is reached the endpoint idle longest is retired for it, or,
// with none to retire, the shared orphan endpoint is used and the placement says why.
func (e *endpoints) Place(ctx context.Context, src lane.Source, target config.Target) (Placement, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	d := e.store.View()
	p := lane.PlaceEndpoint(lane.EndpointKey(src), d.EndpointList(), d.SessionList(), d.CardList(), e.max)
	if p.Retire != "" {
		if err := e.retire(ctx, p.Retire); err != nil {
			return Placement{}, err
		}
	}
	ref, err := e.ensure(ctx, p.Key, target)
	if err != nil {
		return Placement{}, err
	}
	return Placement{Endpoint: Endpoint{Key: p.Key, Ref: ref}, Reason: p.Reason}, nil
}

// ensure returns the endpoint of the key, registering it when it is not recorded.
// The same key is the same endpoint: it is what lets a retired conversation come
// back at its old address. The caller holds mu.
func (e *endpoints) ensure(ctx context.Context, key string, target config.Target) (adapterproto.EndpointRef, error) {
	d := e.store.View()
	rec, ok := d.Endpoints[key]
	if !ok {
		name := conversationName
		if key == lane.OrphanKey {
			name = orphanName
		}
		reg, err := e.conn.Register(ctx, adapterproto.RegisterParams{
			Key:         key,
			DisplayName: name,
			Description: "A Hermes conversation or job. Replies to what it sent come back here.",
		})
		if err != nil {
			return "", fmt.Errorf("registering the endpoint %s: %w", key, err)
		}
		if err := e.store.Update(func(d *state.Data) error {
			d.Endpoints[key] = &state.Endpoint{Key: key, Ref: string(reg.Endpoint), LastActivity: e.now(), Target: target}
			return nil
		}); err != nil {
			return "", err
		}
		rec = &state.Endpoint{Ref: string(reg.Endpoint)}
	} else if err := e.touch(key); err != nil {
		return "", err
	}
	ref := adapterproto.EndpointRef(rec.Ref)
	if err := e.hide(ctx, key, ref); err != nil {
		return "", err
	}
	return ref, nil
}

// touch records that the endpoint was used. A key that is not recorded, the
// instance endpoint's, is ignored.
func (e *endpoints) touch(key string) error {
	return e.store.Update(func(d *state.Data) error {
		if rec, ok := d.Endpoints[key]; ok {
			rec.LastActivity = e.now()
		}
		return nil
	})
}

// retire closes the endpoint and forgets it. The record goes first: an endpoint
// that stays open because the close failed is listed by the daemon at the next
// start and recorded again, while a record of a closed endpoint would be used.
// The caller holds mu.
func (e *endpoints) retire(ctx context.Context, key string) error {
	d := e.store.View()
	rec, ok := d.Endpoints[key]
	if !ok {
		return nil
	}
	if err := e.store.Update(func(d *state.Data) error {
		delete(d.Endpoints, key)
		return nil
	}); err != nil {
		return err
	}
	delete(e.hidden, key)
	if err := e.conn.Unregister(ctx, adapterproto.UnregisterParams{Endpoint: adapterproto.EndpointRef(rec.Ref)}); err != nil {
		return fmt.Errorf("closing the endpoint %s: %w", key, err)
	}
	return nil
}

// retireIdle closes the conversation endpoints that have been unused for idle and
// have nothing in flight.
func (e *endpoints) retireIdle(ctx context.Context, idle time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	d := e.store.View()
	keys := lane.IdleEndpoints(d.EndpointList(), d.SessionList(), d.CardList(), idle, e.now())
	slices.Sort(keys)
	for _, key := range keys {
		if err := e.retire(ctx, key); err != nil {
			e.log.Warn("retiring an idle endpoint", "key", key, "error", err)
			continue
		}
		e.log.Info("retired an idle endpoint", "key", key)
	}
}

// runIdle retires idle endpoints every idleCheck until ctx ends.
func (e *endpoints) runIdle(ctx context.Context, idle time.Duration) {
	for sleep(ctx, e.tm.idleCheck) {
		e.retireIdle(ctx, idle)
	}
}
