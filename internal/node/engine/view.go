package engine

import (
	"fmt"
	"sort"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/store"
)

// endpointState is an endpoint as the loop holds it: the record and what the
// engine keeps in its declaration field.
type endpointState struct {
	rec  store.EndpointRecord
	meta endpointMeta
}

// view is the engine's picture of the records, built from a scan and kept in
// step with every write the loop makes, after its commit. The loop is the one
// writer of the disk and the one user of the view, so the two agree except in
// the window of a write that failed, where memory is left as it was.
//
// It holds what the loop decides with: the endpoints by id and by token, the
// deliveries queued for each endpoint, how many deliveries the node has not
// finished, and how many records refer to each body.
type view struct {
	node      ident.NodeID
	endpoints map[ident.EndpointID]*endpointState
	tokens    *tokenIndex

	in  map[store.TargetKey]store.InRecord
	ctl map[store.TargetKey]store.CtlRecord
	out map[string]store.OutRecord
	// queued lists, for each recipient, the target and control records that are
	// still accepted and whole: the ones a closed endpoint has to close.
	queued map[ident.EndpointID]map[store.TargetKey]bool

	// results are the stored results of administrative operations by request key,
	// as far as the loop needs them: the fingerprint that tells a resend of a
	// request from the reuse of its identifier, and the answer to hand back.
	results map[admin.RequestKey]storedResult

	bodyRefs   bodyCounts
	unfinished int
	alerts     []store.AlertRecord
	seq        *store.SeqBook
}

// storedResult is the part of a stored administrative result the loop reads.
type storedResult struct {
	fingerprint admin.Fingerprint
	result      []byte
}

// bodyCounts is how many records refer to each body, by the name of its file.
type bodyCounts map[string]int

func (c bodyCounts) add(r *store.BodyRef, n int) {
	if r == nil {
		return
	}
	if c[r.Name()]+n <= 0 {
		delete(c, r.Name())
		return
	}
	c[r.Name()] += n
}

func (c bodyCounts) clone() bodyCounts {
	out := make(bodyCounts, len(c))
	for k, v := range c {
		out[k] = v
	}
	return out
}

// buildView reads a scan into a view. An endpoint whose declaration field the
// engine cannot read is an error: the engine owns the field, and the record
// passed the store's checks, so what is wrong is not the store's to quarantine.
func buildView(node ident.NodeID, sc *store.Scan) (*view, error) {
	v := &view{
		node:      node,
		endpoints: map[ident.EndpointID]*endpointState{},
		tokens:    newTokenIndex(),
		in:        map[store.TargetKey]store.InRecord{},
		ctl:       map[store.TargetKey]store.CtlRecord{},
		out:       map[string]store.OutRecord{},
		queued:    map[ident.EndpointID]map[store.TargetKey]bool{},
		results:   map[admin.RequestKey]storedResult{},
		bodyRefs:  bodyCounts{},
		seq:       store.NewSeqBook(sc),
	}
	for _, a := range sc.Admin {
		v.putResult(a)
	}
	for _, ep := range sc.Endpoints {
		meta, err := decodeMeta(ep.Declaration)
		if err != nil {
			return nil, fmt.Errorf("engine: endpoint %s: %w", ep.ID, err)
		}
		v.putEndpoint(ep, meta)
	}
	for _, in := range sc.In {
		v.setIn(in)
	}
	for _, c := range sc.Ctl {
		v.setCtl(c)
	}
	for _, o := range sc.Out {
		v.setOut(o)
	}
	v.alerts = append(v.alerts, sc.Alerts...)
	sort.SliceStable(v.alerts, func(i, j int) bool {
		if !v.alerts[i].At.Equal(v.alerts[j].At) {
			return v.alerts[i].At.Before(v.alerts[j].At)
		}
		return v.alerts[i].ID.String() < v.alerts[j].ID.String()
	})
	return v, nil
}

// putEndpoint records an endpoint as it is on disk now. Only a session endpoint
// that is open holds a token that anyone presents, but the index takes the token
// of every endpoint that has one, and resolve decides by kind.
func (v *view) putEndpoint(rec store.EndpointRecord, meta endpointMeta) {
	v.endpoints[rec.ID] = &endpointState{rec: rec, meta: meta}
	if rec.Status == store.EndpointClosed || len(rec.Token) == 0 {
		v.tokens.remove(rec.ID)
		return
	}
	v.tokens.add(rec.ID, rec.Token)
}

// putResult records the stored result of an administrative operation as it is on
// disk now.
func (v *view) putResult(a store.AdminRecord) {
	v.results[a.Key] = storedResult{fingerprint: a.Fingerprint, result: a.Result}
}

// OpenEndpoints is how many endpoints are open or being created: the ones that
// count against the cap of the node.
func (v *view) OpenEndpoints() int {
	n := 0
	for _, e := range v.endpoints {
		if e.rec.Status != store.EndpointClosed {
			n++
		}
	}
	return n
}

// openDirect is how many open direct endpoints the instance owns.
func (v *view) openDirect(instance string) int {
	n := 0
	for _, e := range v.endpoints {
		if e.rec.Kind == adapterreg.KindDirect && e.rec.Instance == instance && e.rec.Status != store.EndpointClosed {
			n++
		}
	}
	return n
}

// Unfinished is how many deliveries the node holds that are not final.
func (v *view) Unfinished() int { return v.unfinished }

// BodyRefs is how many records refer to the body of that name.
func (v *view) BodyRefs(name string) int { return v.bodyRefs[name] }

// Draining reports whether the endpoint is closed and records are still queued
// for it that its closing has not closed yet.
func (v *view) Draining(id ident.EndpointID) bool {
	e := v.endpoints[id]
	return e != nil && e.rec.Status == store.EndpointClosed && len(v.queued[id]) > 0
}

// resolve finds the endpoint a caller is. A token is the way in of an open
// session endpoint only; an adapter acts for the open direct endpoints of its
// own instance. Everything else is refused, and the refusal says nothing about
// which of the reasons it was beyond what the reason itself says.
func (v *view) resolve(c Caller) (*endpointState, error) {
	switch c.kind {
	case callerToken:
		id, ok := v.tokens.lookup(c.token)
		if !ok {
			return nil, refuse(ReasonUnauthenticated)
		}
		e := v.endpoints[id]
		if e == nil || e.rec.Kind != adapterreg.KindSession || e.rec.Status != store.EndpointOpen {
			return nil, refuse(ReasonUnauthenticated)
		}
		return e, nil
	case callerAdapter:
		id, err := ident.ParseEndpointID(string(c.endpoint))
		if err != nil {
			return nil, refuse(ReasonEndpointUnknown)
		}
		e := v.endpoints[id]
		if e == nil || e.rec.Kind != adapterreg.KindDirect || e.rec.Instance != c.instance || e.rec.Status != store.EndpointOpen {
			return nil, refuse(ReasonEndpointUnknown)
		}
		return e, nil
	}
	return nil, refuse(ReasonUnauthenticated)
}

// afterWrite is a target record as the store left it on disk: one revision on,
// a lost record having none. The store writes the next revision of every target
// record it is given and does not hand the record back, so the view takes it
// from here; TestViewAgreesWithTheDiskAfterAWrite holds this to what the store
// does.
func afterWrite(in store.InRecord) store.InRecord {
	if !in.Lost {
		in.Rev++
	}
	return in
}

func inUnfinished(in store.InRecord) bool  { return !in.Record.State.IsTerminal() }
func ctlUnfinished(c store.CtlRecord) bool { return !c.Record.State.IsTerminal() }

// outUnfinished is how many deliveries of a source record no receipt answered.
func outUnfinished(o store.OutRecord) int {
	n := 0
	for _, r := range o.Outbound.Recipients {
		if r.Delivery.State == delivery.StateQueued {
			n++
		}
	}
	return n
}

func (v *view) queue(recipient ident.EndpointID, key store.TargetKey, queued bool) {
	if !queued {
		if m := v.queued[recipient]; m != nil {
			delete(m, key)
			if len(m) == 0 {
				delete(v.queued, recipient)
			}
		}
		return
	}
	if v.queued[recipient] == nil {
		v.queued[recipient] = map[store.TargetKey]bool{}
	}
	v.queued[recipient][key] = true
}

// setIn records a target record as it is on disk now, taking the place of the
// one the view held.
func (v *view) setIn(in store.InRecord) {
	if old, ok := v.in[in.Key]; ok {
		v.bodyRefs.add(old.Body, -1)
		if inUnfinished(old) {
			v.unfinished--
		}
	}
	v.in[in.Key] = in
	v.bodyRefs.add(in.Body, 1)
	if inUnfinished(in) {
		v.unfinished++
	}
	v.queue(in.Key.Recipient, in.Key, !in.Lost && in.Record.State == delivery.StateAccepted)
}

// setCtl is setIn for a control record.
func (v *view) setCtl(c store.CtlRecord) {
	if old, ok := v.ctl[c.Key]; ok {
		v.bodyRefs.add(old.Body, -1)
		if ctlUnfinished(old) {
			v.unfinished--
		}
	}
	v.ctl[c.Key] = c
	v.bodyRefs.add(c.Body, 1)
	if ctlUnfinished(c) {
		v.unfinished++
	}
	v.queue(c.Key.Recipient, c.Key, !c.Lost && c.Record.State == delivery.StateAccepted)
}

// setOut is setIn for a source record.
func (v *view) setOut(o store.OutRecord) {
	if old, ok := v.out[o.Name()]; ok {
		v.bodyRefs.add(old.Body, -1)
		v.unfinished -= outUnfinished(old)
	}
	v.out[o.Name()] = o
	v.bodyRefs.add(o.Body, 1)
	v.unfinished += outUnfinished(o)
}
