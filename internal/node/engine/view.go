package engine

import (
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// endpointState is an endpoint as the loop holds it: the record and its
// declaration, read once from the record's declaration field.
type endpointState struct {
	rec  store.EndpointRecord
	decl authz.Declaration
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

	// checkpoints are the states the adapters asked the node to keep, by scope
	// and id.
	checkpoints map[store.CheckpointKey][]byte

	// results are the stored results of administrative operations by request key,
	// as far as the loop needs them: the fingerprint that tells a resend of a
	// request from the reuse of its identifier, and the answer to hand back. They
	// come from the two places results are kept, state/admin/ and the operation
	// each versioned file of config/ records.
	results map[admin.RequestKey]storedResult

	// idem finds a source record by the idempotency key its sender gave, for the
	// records that have one; byMessage finds one by its sender and message id; the
	// target records of a message are found by their recipient and message id;
	// backlog is how many records occupy a slot of an endpoint's backlog.
	idem      map[idemKey]string
	byMessage map[msgKey]string
	received  map[msgKey][]store.TargetKey
	arrived   map[arrivalKey][]store.TargetKey
	backlog   map[ident.EndpointID]int

	// leased finds the target records on which a page was handed out, by the lease of
	// the page: an acknowledgement names a page, and may come long after the lease ran
	// out.
	leased map[delivery.LeaseID][]store.TargetKey

	unfinished int
	alerts     []store.AlertRecord
	seq        *store.SeqBook

	// onTick, when set, is told, whenever a record is taken in or dropped, the
	// instant of the next time-driven change of the record (the zero time for none):
	// the record is "in/" + the text of its key, or "out/" + its file name. The
	// engine keeps its schedule of ticks from it, so the schedule follows the view
	// and no write has to remember to move it.
	onTick func(record string, next time.Time)

	// onQueued, when set, is told whenever a target record that waits to be handed out
	// with no lease on it is taken in: a message has arrived, or a lease has run out.
	onQueued func(recipient ident.EndpointID)
}

// idemKey names an idempotency key: the key is the sender's own.
type idemKey struct {
	sender ident.EndpointID
	key    string
}

// msgKey names a message from the point of view of one local endpoint: the one
// that sent it, or the one it was sent to.
type msgKey struct {
	endpoint ident.EndpointID
	message  delivery.MessageID
}

// arrivalKey names a message as the node it came from sent it: what a pull of its
// reports asks for. It finds the target records of the message in in/ and the
// control record in ctl/ alike: whichever the node holds is what a pull is
// answered from.
type arrivalKey struct {
	node    ident.NodeID
	message delivery.MessageID
}

// storedResult is the part of a stored administrative result the loop reads.
type storedResult struct {
	fingerprint admin.Fingerprint
	result      []byte
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
		idem:      map[idemKey]string{},
		byMessage: map[msgKey]string{},
		received:  map[msgKey][]store.TargetKey{},
		arrived:   map[arrivalKey][]store.TargetKey{},
		backlog:   map[ident.EndpointID]int{},
		leased:    map[delivery.LeaseID][]store.TargetKey{},

		checkpoints: map[store.CheckpointKey][]byte{},
		seq:         store.NewSeqBook(sc),
	}
	for _, a := range sc.Admin {
		v.putResult(a)
	}
	for _, ep := range sc.Endpoints {
		decl, err := decodeDeclaration(ep.Declaration)
		if err != nil {
			return nil, fmt.Errorf("engine: endpoint %s: %w", ep.ID, err)
		}
		v.putEndpoint(ep, decl)
	}
	for _, c := range sc.Checkpoints {
		v.checkpoints[c.Key] = c.State
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
	sortAlerts(v.alerts)
	return v, nil
}

// putEndpoint records an endpoint as it is on disk now. Only a session endpoint
// that is open holds a token that anyone presents, but the index takes the token
// of every endpoint that has one, and resolve decides by kind.
func (v *view) putEndpoint(rec store.EndpointRecord, decl authz.Declaration) {
	v.endpoints[rec.ID] = &endpointState{rec: rec, decl: decl}
	if rec.Status == store.EndpointClosed || len(rec.Token) == 0 {
		v.tokens.remove(rec.ID)
		return
	}
	v.tokens.add(rec.ID, rec.Token)
}

// noteAlerts takes the alerts of a scan that the view does not hold yet, which a
// settlement of damage wrote, keeping the order the view lists them in.
func (v *view) noteAlerts(alerts []store.AlertRecord) {
	added := false
	for _, a := range alerts {
		if !slices.ContainsFunc(v.alerts, func(x store.AlertRecord) bool { return x.ID == a.ID }) {
			v.alerts = append(v.alerts, a)
			added = true
		}
	}
	if added {
		sortAlerts(v.alerts)
	}
}

func sortAlerts(alerts []store.AlertRecord) {
	sort.SliceStable(alerts, func(i, j int) bool {
		if !alerts[i].At.Equal(alerts[j].At) {
			return alerts[i].At.Before(alerts[j].At)
		}
		return alerts[i].ID.String() < alerts[j].ID.String()
	})
}

// putResult records the stored result of an administrative operation as it is on
// disk now.
func (v *view) putResult(a store.AdminRecord) { v.putStored(a.Key, a.Fingerprint, a.Result) }

// putStored records a result that is on disk, wherever its operation kept it.
func (v *view) putStored(key admin.RequestKey, fp admin.Fingerprint, result []byte) {
	v.results[key] = storedResult{fingerprint: fp, result: result}
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

// Unfinished is how many deliveries the node holds that still have work behind
// them (see inUnfinished).
func (v *view) Unfinished() int { return v.unfinished }

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

// resolveSession finds the session endpoint of an adapter instance that is open,
// by the reference the adapter uses. A direct endpoint is not one, nor is a
// session of another instance: an adapter keeps state only for what it owns.
func (v *view) resolveSession(instance string, ref adapterproto.EndpointRef) (*endpointState, error) {
	return v.resolveSessionIn(instance, ref, store.EndpointOpen)
}

// resolveSessionBeingMade is resolveSession for a session endpoint that is open or
// whose creation has not ended: the adapter keeps the state of a session while it
// creates it, before the node has been told that it exists.
func (v *view) resolveSessionBeingMade(instance string, ref adapterproto.EndpointRef) (*endpointState, error) {
	return v.resolveSessionIn(instance, ref, store.EndpointOpen, store.EndpointCreating)
}

func (v *view) resolveSessionIn(instance string, ref adapterproto.EndpointRef, statuses ...store.EndpointStatus) (*endpointState, error) {
	id, err := ident.ParseEndpointID(string(ref))
	if err != nil {
		return nil, refuse(ReasonEndpointUnknown)
	}
	e := v.endpoints[id]
	if e == nil || e.rec.Kind != adapterreg.KindSession || e.rec.Instance != instance || !slices.Contains(statuses, e.rec.Status) {
		return nil, refuse(ReasonEndpointUnknown)
	}
	return e, nil
}

// resolveAttemptBeingMade is the session endpoint an attempt belongs to, for an
// attempt of this instance: the creation attempt of a session endpoint of the
// instance, and the first prompt that follows from it, while the endpoint is open or
// being created. Any other attempt, a made-up one included, belongs to nobody and is
// refused as the unknown endpoint is. The attempts of later prompts are not minted by
// the engine yet; when they are, they are resolved here too, so that no attempt
// escapes the instance that owns it.
func (v *view) resolveAttemptBeingMade(instance string, attempt adapterproto.AttemptID) (*endpointState, error) {
	for _, c := range v.ctl {
		if c.Intent == nil || (c.Intent.Attempt != attempt && firstPromptAttempt(c.Intent.Attempt) != attempt) {
			continue
		}
		return v.resolveSessionBeingMade(instance, adapterproto.EndpointRef(c.Intent.Endpoint.String()))
	}
	return nil, refuse(ReasonEndpointUnknown)
}

// What counts as an unfinished delivery is what still has work behind it that the
// node took in through an entry of new work (the capacity check bounds exactly
// that). A target message is unfinished while the delivery rules say it still
// needs its body: accepted, queued or leased, waiting to be handed out. A control
// signal is unfinished while it is accepted: it holds no body, but its effect is
// still to be made. A delivery of a message this node sent is unfinished while no
// receipt answered it. A delivery whose outcome is unknown holds nothing to hand
// out and is not counted.

func inUnfinished(in store.InRecord) bool { return in.Record.NeedsBody() }

func ctlUnfinished(c store.CtlRecord) bool { return c.Record.State == delivery.StateAccepted }

// outUnfinished is how many deliveries of a source record no receipt answered.
func outUnfinished(o store.OutRecord) int {
	n := 0
	for _, r := range o.Outbound.Recipients {
		if r.Delivery.NeedsBody() {
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

// queuedIn lists the target records queued for the endpoint in the order they were
// taken in: what recv has to hand out. The queue view of an endpoint holds its
// control records too, because a closing endpoint closes them as well, but a control
// signal is never handed to an agent.
func (v *view) queuedIn(id ident.EndpointID) []store.InRecord {
	ins, _ := v.queuedFor(id, 0)
	sort.SliceStable(ins, func(i, j int) bool { return ins[i].AcceptedAt.Before(ins[j].AcceptedAt) })
	return ins
}

// setIn records a target record as it is on disk now, taking the place of the
// one the view held.
func (v *view) setIn(in store.InRecord) {
	if old, ok := v.in[in.Key]; ok {
		if inUnfinished(old) {
			v.unfinished--
		}
		if old.Record.OccupiesBacklogSlot() {
			v.backlog[in.Key.Recipient]--
		}
	} else {
		k := msgKey{in.Key.Recipient, in.Key.Message}
		v.received[k] = append(v.received[k], in.Key)
		a := arrivalKey{in.Key.SenderNode, in.Key.Message}
		v.arrived[a] = append(v.arrived[a], in.Key)
	}
	if in.Record.OccupiesBacklogSlot() {
		v.backlog[in.Key.Recipient]++
	}
	v.in[in.Key] = in
	if inUnfinished(in) {
		v.unfinished++
	}
	v.queue(in.Key.Recipient, in.Key, in.Record.State == delivery.StateAccepted)
	v.ticks(inTick(in.Key), in.Record.NextTick())
	for _, id := range in.Record.Lease.IDs {
		if !slices.Contains(v.leased[id], in.Key) {
			v.leased[id] = append(v.leased[id], in.Key)
		}
	}
	if v.onQueued != nil && in.Record.State == delivery.StateAccepted && !in.Record.Lease.Active {
		v.onQueued(in.Key.Recipient)
	}
}

// dropIn takes a target record out of the view, as its file being removed does.
func (v *view) dropIn(key store.TargetKey) {
	old, ok := v.in[key]
	if !ok {
		return
	}
	if inUnfinished(old) {
		v.unfinished--
	}
	if old.Record.OccupiesBacklogSlot() {
		v.backlog[key.Recipient]--
	}
	delete(v.in, key)
	for _, id := range old.Record.Lease.IDs {
		if v.leased[id] = slices.DeleteFunc(v.leased[id], func(x store.TargetKey) bool { return x == key }); len(v.leased[id]) == 0 {
			delete(v.leased, id)
		}
	}
	v.queue(key.Recipient, key, false)
	k := msgKey{key.Recipient, key.Message}
	v.received[k] = slices.DeleteFunc(v.received[k], func(x store.TargetKey) bool { return x == key })
	if len(v.received[k]) == 0 {
		delete(v.received, k)
	}
	a := arrivalKey{key.SenderNode, key.Message}
	v.arrived[a] = slices.DeleteFunc(v.arrived[a], func(x store.TargetKey) bool { return x == key })
	if len(v.arrived[a]) == 0 {
		delete(v.arrived, a)
	}
	v.ticks(inTick(key), time.Time{})
}

// ticks tells the watcher of the schedule when a record next changes with time.
func (v *view) ticks(record string, next time.Time) {
	if v.onTick != nil {
		v.onTick(record, next)
	}
}

func inTick(key store.TargetKey) string { return "in/" + key.String() }

func ctlTick(key store.TargetKey) string { return "ctl/" + key.String() }

func outTick(name string) string { return "out/" + name }

// outNextTick is the earliest time-driven change of a source record: that of the
// first of its deliveries (the replacement of a lost record has its own).
func outNextTick(o store.OutRecord) time.Time {
	var recs []delivery.Record
	if o.Lost != nil {
		recs = o.Lost.Deliveries
	} else {
		for _, t := range o.Outbound.Deliveries() {
			recs = append(recs, t.Delivery)
		}
	}
	var next time.Time
	for _, r := range recs {
		if n := r.NextTick(); !n.IsZero() && (next.IsZero() || n.Before(next)) {
			next = n
		}
	}
	return next
}

// setCtl is setIn for a control record. A control record is found by a pull as a
// target record is, by the node and the message it came from.
func (v *view) setCtl(c store.CtlRecord) {
	if old, ok := v.ctl[c.Key]; ok {
		if ctlUnfinished(old) {
			v.unfinished--
		}
	} else {
		a := arrivalKey{c.Key.SenderNode, c.Key.Message}
		v.arrived[a] = append(v.arrived[a], c.Key)
	}
	v.ctl[c.Key] = c
	if ctlUnfinished(c) {
		v.unfinished++
	}
	v.queue(c.Key.Recipient, c.Key, c.Record.State == delivery.StateAccepted)
	v.ticks(ctlTick(c.Key), c.Record.NextTick())
}

// setOut is setIn for a source record.
func (v *view) setOut(o store.OutRecord) {
	if old, ok := v.out[o.Name()]; ok {
		v.unfinished -= outUnfinished(old)
		if old.Idempotency != nil && v.idem[idemKey{old.Key.Sender, old.Idempotency.Key}] == o.Name() {
			delete(v.idem, idemKey{old.Key.Sender, old.Idempotency.Key})
		}
	}
	v.byMessage[msgKey{o.Key.Sender, o.Key.Message}] = o.Name()
	if o.Idempotency != nil {
		v.idem[idemKey{o.Key.Sender, o.Idempotency.Key}] = o.Name()
	}
	v.out[o.Name()] = o
	v.unfinished += outUnfinished(o)
	v.ticks(outTick(o.Name()), outNextTick(o))
}

// dropCtl is dropIn for a control record.
func (v *view) dropCtl(key store.TargetKey) {
	old, ok := v.ctl[key]
	if !ok {
		return
	}
	if ctlUnfinished(old) {
		v.unfinished--
	}
	delete(v.ctl, key)
	v.queue(key.Recipient, key, false)
	a := arrivalKey{key.SenderNode, key.Message}
	v.arrived[a] = slices.DeleteFunc(v.arrived[a], func(x store.TargetKey) bool { return x == key })
	if len(v.arrived[a]) == 0 {
		delete(v.arrived, a)
	}
	v.ticks(ctlTick(key), time.Time{})
}

// dropOut is dropIn for a source record, by its file name.
func (v *view) dropOut(name string) {
	old, ok := v.out[name]
	if !ok {
		return
	}
	v.unfinished -= outUnfinished(old)
	if m := (msgKey{old.Key.Sender, old.Key.Message}); v.byMessage[m] == name {
		delete(v.byMessage, m)
	}
	if old.Idempotency != nil {
		if k := (idemKey{old.Key.Sender, old.Idempotency.Key}); v.idem[k] == name {
			delete(v.idem, k)
		}
	}
	delete(v.out, name)
	v.ticks(outTick(name), time.Time{})
}

// dropSwept takes a record that the sweep removed out of the view, whatever kind it
// is. A kind the view holds no picture of (an attempts record, a floor file) leaves
// nothing to take out, and a source record is dropped by name (dropOut): the sweep
// is given it whole.
func (v *view) dropSwept(rec store.Record) {
	switch r := rec.(type) {
	case store.InRecord:
		v.dropIn(r.Key)
	case store.CtlRecord:
		v.dropCtl(r.Key)
	case store.EndpointRecord:
		v.tokens.remove(r.ID)
		delete(v.endpoints, r.ID)
		// The floor file of a session endpoint goes with its record and its identifier
		// is never used again. A direct endpoint keeps its identifier for ever, and so
		// its floors: the sweep leaves the file, and the book keeps the pairs.
		if r.Kind == adapterreg.KindSession {
			v.seq.Forget(r.ID)
		}
	case store.CheckpointRecord:
		delete(v.checkpoints, r.Key)
	case store.AdminRecord:
		delete(v.results, r.Key)
	case store.AlertRecord:
		v.alerts = slices.DeleteFunc(v.alerts, func(a store.AlertRecord) bool { return a.ID == r.ID })
	}
}
