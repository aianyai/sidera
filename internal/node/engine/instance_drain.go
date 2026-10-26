package engine

import (
	"sort"
	"time"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/store"
)

// cmdInstanceDrain closes the next chunk of the open endpoints of a disabled
// instance. The disabling itself is the change of the configuration; this follows
// it, a chunk of at most Params.ChunkReplaces endpoints at a time, so that an
// instance with a thousand endpoints does not hold the loop. Each endpoint closes
// as any closed endpoint does: its record is the head, and the records queued for
// it are closed by cmdDrain after. A crash between two chunks leaves a disabled
// instance with endpoints still open, which the scan entry closes the same way.
type cmdInstanceDrain struct{ instance string }

func (c *cmdInstanceDrain) gate() gate { return gateAlways }
func (c *cmdInstanceDrain) fail(error) {}
func (c *cmdInstanceDrain) run(e *Engine, now time.Time) {
	err := e.drainInstanceChunk(c.instance)
	if err = e.classify(err); err != nil && e.fatal == nil {
		e.log.Warn("closing the endpoints of a disabled instance failed; it is tried again", "instance", c.instance, "error", err.Error())
		e.sched.set(schedKey{schedInstanceDrain, c.instance}, now.Add(e.p.RetryBackoff))
	}
}

func (e *Engine) drainInstanceChunk(instance string) error {
	var open []string
	for id, s := range e.view.endpoints {
		if s.rec.Instance == instance && s.rec.Status != store.EndpointClosed {
			open = append(open, id.String())
		}
	}
	if len(open) == 0 {
		return nil
	}
	sort.Strings(open)
	rest := 0
	if len(open) > e.p.ChunkReplaces {
		open, rest = open[:e.p.ChunkReplaces], len(open)-e.p.ChunkReplaces
	}
	closed := make([]store.EndpointRecord, 0, len(open))
	ids := make([]ident.EndpointID, 0, len(open))
	for _, name := range open {
		id, err := ident.ParseEndpointID(name)
		if err != nil {
			panic("engine: an endpoint identifier of the view does not parse: " + err.Error())
		}
		rec := e.view.endpoints[id].rec
		rec.Status, rec.Token = store.EndpointClosed, nil
		closed = append(closed, rec)
		ids = append(ids, id)
	}
	if err := e.d.Store.InstanceDrain(closed, store.DrainBatch{}); err != nil {
		return err
	}
	for i, id := range ids {
		e.view.putEndpoint(closed[i], e.view.endpoints[id].decl)
		if e.view.Draining(id) {
			e.later(&cmdDrain{endpoint: id})
		} else {
			e.releaseParked(id)
		}
	}
	if rest > 0 {
		e.later(&cmdInstanceDrain{instance: instance})
	}
	return nil
}
