package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/handoff"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/manifest"
)

// Pulling. An agent gets what was sent to its endpoint by asking for it: recv hands
// a page of the queue out under a lease (a page is one lease, whatever number of
// messages it holds), ack says the page was seen, and a lease that runs out, which
// Tick settles, puts the message back for the next recv, marked as one the agent
// may have seen. What the delivery rules say about all of it is in package delivery;
// this file is the order in which the engine asks them, and the writes.
//
// One recv is one command of the loop. It acknowledges the page before it when it is
// asked to, ticks the endpoint's own records that are due (a message past a deadline
// is refused here and not handed out and settled behind the agent's back), judges
// every message again with the declarations as they are now (the second check), builds
// the page within the budget of the front end, takes the leases of what the page
// holds and closes what the check refused, writes all of it in one transaction, and
// only then answers. An agent that gets no answer holds no page, and what was
// written stays leased: the next recv gets it back marked as a possible duplicate.

// RecvRequest is a recv.
type RecvRequest struct {
	// Frontend says through which front end of the Agent interface the agent reads;
	// it decides the budget of the page and the longest wait. A direct endpoint has no
	// front end and the field is not read.
	Frontend adapterproto.Frontend
	// Wait is how long to wait for a message when none is queued, not negative. The
	// engine cuts it to what the adapter declares for the front end (a direct
	// endpoint: adapterproto.MaxWaitSeconds), so the rule is in one place.
	Wait time.Duration
	// Msg and Part ask for one part of one message instead of the queue, as the
	// pointer of a page names it; they go together and exclude a wait.
	Msg  adapterproto.MessageID
	Part int
	// AckLease acknowledges the whole of the page it names before the recv is made.
	// It costs the recv nothing when it fails: the agent is told what is queued, and
	// what it did not acknowledge comes back marked as a possible duplicate.
	AckLease adapterproto.LeaseRef
}

// AckRequest acknowledges parts of a page.
type AckRequest struct {
	// Lease names the page.
	Lease adapterproto.LeaseRef
	// Items are the parts to acknowledge; none means every part of the page.
	Items []adapterproto.PartRef
}

// pageMemory is how long the node remembers which parts a page held, which is what
// an acknowledgement of the whole page needs: the longest a lease lives.
const pageMemory = delivery.LeaseRenewalCap

// issuedPage is what one page handed out, in the order it printed it.
type issuedPage struct {
	endpoint ident.EndpointID
	at       time.Time
	items    []pageItem
}

type pageItem struct {
	key  store.TargetKey
	part int
}

type cmdRecv struct {
	call[recvAnswer]
	caller Caller
	req    RecvRequest
}

// recvAnswer is the page, or the waiter that will have it.
type recvAnswer struct {
	page []byte
	w    *waiter
}

func (c *cmdRecv) gate() gate { return gateWrite }
func (c *cmdRecv) run(e *Engine, now time.Time) {
	ans, err := e.recv(now, c.caller, c.req)
	c.done(ans, e.classify(err))
}

// Recv hands the page of the queue of the caller's endpoint out, as the bytes the
// recv command prints. With a wait it returns when a page with a message is ready,
// the wait ends (an empty page), a newer recv of the endpoint comes (an empty page),
// the node begins to stop (an empty page) or ctx ends (an error; the wait is given up).
func (e *Engine) Recv(ctx context.Context, c Caller, r RecvRequest) ([]byte, error) {
	cmd := &cmdRecv{call: newCall[recvAnswer](), caller: c, req: r}
	ans, err := roundTrip(ctx, e, submitExternal, cmd, cmd.call)
	if err != nil {
		return nil, err
	}
	if ans.w == nil {
		return ans.page, nil
	}
	o, err := e.awaitWaiter(ctx, ans.w)
	if err != nil {
		// The command is queued like any other; if it is dropped the waiter ends with
		// its deadline.
		_ = e.enqueue(context.WithoutCancel(ctx), submitInternal, cmdUnwait{w: ans.w})
		return nil, err
	}
	if o.End == waitWoken {
		return o.Page, nil
	}
	return ans.w.empty, nil
}

func (e *Engine) recv(now time.Time, c Caller, r RecvRequest) (recvAnswer, error) {
	st, err := e.view.resolve(c)
	if err != nil {
		return recvAnswer{}, err
	}
	budget, wait, err := e.recvLimits(st, r)
	if err != nil {
		return recvAnswer{}, err
	}
	// A newer recv ends the older wait of the endpoint, whether or not it waits: a
	// page handed to two readers would show the agent the same message twice.
	e.supersede(st.rec.ID)
	lease, err := delivery.NewLeaseID(e.d.Rand)
	if err != nil {
		return recvAnswer{}, err
	}
	if r.AckLease != "" {
		if _, err := e.ack(now, st, AckRequest{Lease: r.AckLease}); err != nil {
			if txnFatal(e.classify(err)) {
				return recvAnswer{}, err
			}
			e.log.Info("the page before this recv was not acknowledged", "error", err.Error())
		}
	}
	if err := e.tickEndpoint(now, st.rec.ID); err != nil {
		return recvAnswer{}, err // fatal: nothing more is built or written
	}
	if r.Msg != "" {
		page, err := e.recvPart(now, st, lease, budget, r)
		return recvAnswer{page: page}, err
	}
	page, handed, err := e.buildPage(now, st, lease, budget)
	if err != nil {
		return recvAnswer{}, err
	}
	if handed > 0 || wait <= 0 {
		return recvAnswer{page: page}, nil
	}
	empty, err := e.emptyPage(lease, budget)
	if err != nil {
		return recvAnswer{}, err
	}
	w := newWaiter(st.rec.ID, now.Add(wait))
	w.lease, w.budget, w.empty = lease, budget, empty
	e.waitFor(w)
	return recvAnswer{w: w}, nil
}

// recvLimits checks the request and gives the budget of the page and the wait the
// adapter allows through the front end the request names.
func (e *Engine) recvLimits(st *endpointState, r RecvRequest) (handoff.Budget, time.Duration, error) {
	switch {
	case r.Wait < 0:
		return handoff.Budget{}, 0, invalidArgument("wait", errors.New("a wait is not negative"))
	case (r.Msg == "") != (r.Part == 0):
		return handoff.Budget{}, 0, invalidArgument("part", errors.New("a message and a part are asked for together"))
	case r.Msg != "" && r.Wait > 0:
		return handoff.Budget{}, 0, invalidArgument("wait", errors.New("a part of a message is fetched at once"))
	}
	adm, _ := e.admission(st.rec.Instance) // an adapter not admitted declares nothing: the defaults
	if st.rec.Kind == adapterreg.KindDirect {
		return adm.Budget(manifest.FrontendDirect), min(r.Wait, adapterproto.MaxWaitSeconds*time.Second), nil
	}
	if !r.Frontend.Valid() {
		return handoff.Budget{}, 0, invalidArgument("frontend", errors.New("not a front end of the Agent interface"))
	}
	return adm.Budget(manifest.Frontend(r.Frontend)), min(r.Wait, adm.RecvWait(r.Frontend)), nil
}

// txnFatal says whether the engine will stop after the command that met the error.
func txnFatal(err error) bool {
	r, ok := ReasonOf(err)
	return ok && r == ReasonStorageFatal
}

// tickEndpoint ticks the records of the endpoint that are due, so that what a recv
// hands out has been judged by the passing of time first. Only a fatal storage error
// is returned (tickRecords); a tick that could not be written is tried again later, and
// a record that stays due is not handed out.
func (e *Engine) tickEndpoint(now time.Time, id ident.EndpointID) error {
	var due []string
	for _, in := range e.view.queuedIn(id) {
		if next := in.Record.NextTick(); !next.IsZero() && !next.After(now) {
			due = append(due, inTick(in.Key))
		}
	}
	if len(due) == 0 {
		return nil
	}
	return e.tickRecords(now, due)
}

// candidate is a queued message of the endpoint, as one recv judges it.
type candidate struct {
	in   store.InRecord
	auth delivery.AuthorizationKind
	// place is where the message stands among all that were judged, the refused and
	// the suspended ones included.
	place int
}

// buildPage makes the page of the queue and returns it with how many messages it holds.
// The page is built and the records are changed in memory before anything is written;
// when nothing needs to be written the answer is the empty page.
func (e *Engine) buildPage(now time.Time, st *endpointState, lease delivery.LeaseID, budget handoff.Budget) ([]byte, int, error) {
	if e.fatal != nil {
		return nil, 0, e.fatal // the loop is over: nothing more is handed out or written
	}
	skip := map[store.TargetKey]bool{}
	for {
		page, handed, retry, err := e.tryPage(now, st, lease, budget, skip)
		if err != nil || !retry {
			return page, handed, err
		}
	}
}

// tryPage is one attempt at the page. A record that cannot be handed out (its body is
// gone, which settles it and so changes the queue, it was handed out in another
// number of parts than this budget gives, or it is due and nothing has ticked it) is
// left out and the attempt is made again.
func (e *Engine) tryPage(now time.Time, st *endpointState, lease delivery.LeaseID, budget handoff.Budget, skip map[store.TargetKey]bool) (page []byte, handed int, retry bool, err error) {
	queue := e.view.queuedIn(st.rec.ID)
	delivery.SortForRecv(queue, func(in store.InRecord) delivery.Record { return in.Record }, now)
	var cands, denied []candidate
	judged := 0
	for _, in := range queue {
		if skip[in.Key] {
			continue
		}
		auth, err := e.handoutAuthorization(now, st, in)
		if err != nil {
			e.log.Error("a queued message is not handed out", "message", string(in.Key.Message), "error", err.Error())
			skip[in.Key] = true
			continue
		}
		switch auth {
		case delivery.AuthorizationSuspend:
			// Neither handed out, nor refused, nor prompted for, and nobody is told.
		case delivery.AuthorizationDeny:
			denied = append(denied, candidate{in: in, auth: auth, place: judged})
		default:
			cands = append(cands, candidate{in: in, auth: auth, place: judged})
		}
		judged++
	}
	pageOf, retry, err := e.pack(now, lease, budget, cands, skip)
	if err != nil || retry {
		return nil, 0, retry, err
	}
	// What the page reached: a refused message ahead of the last one it holds, or any
	// when it holds none, is closed now; one behind it is not judged by anything yet,
	// since the declaration may be loosened before it would be handed out, and the next
	// recv reaches it.
	lastAt := -1
	if n := len(pageOf.handed); n > 0 {
		lastAt = pageOf.at[n-1]
	}
	var writes []store.InRecord
	for _, h := range pageOf.handed {
		writes = append(writes, h.in)
	}
	for _, d := range denied {
		if d.place > lastAt && lastAt >= 0 {
			continue
		}
		in, closed, err := e.closeRefused(now, d.in, lease)
		if err != nil {
			return nil, 0, false, err
		}
		if closed {
			writes = append(writes, in)
		}
	}
	if err := e.writeLeases(now, st.rec.ID, lease, writes, pageOf); err != nil {
		return nil, 0, false, err
	}
	return pageOf.out, len(pageOf.handed), false, nil
}

// pageEntry is a message the page holds with the record as the hand-out makes it.
type pageEntry struct {
	in   store.InRecord
	part int
}

// packed is a page and what it holds.
type packed struct {
	out    []byte
	handed []pageEntry
	// at is, for each message the page holds, its place among the messages judged.
	at []int
}

// pack builds the page of the messages the check lets through and takes the lease of
// each message it holds, in memory. The bodies are read as far as the page reaches:
// the page is built from the first few, and from more when it reached the last of
// them. A message that cannot be handed out is put in skip and the attempt is made
// again.
func (e *Engine) pack(now time.Time, lease delivery.LeaseID, budget handoff.Budget, cands []candidate, skip map[store.TargetKey]bool) (packed, bool, error) {
	bodies := make([]string, len(cands))
	loaded := 0
	load := func(upto int) (bool, error) {
		for ; loaded < min(upto, len(cands)); loaded++ {
			in := cands[loaded].in
			if in.Body == nil {
				e.log.Error("a queued message holds no body; it is not handed out", "message", string(in.Key.Message))
				skip[in.Key] = true
				return true, nil
			}
			body, err := e.readBody(now, in, *in.Body)
			switch {
			case errors.Is(err, store.ErrBodyMismatch) || errors.Is(err, store.ErrBodyMissing):
				// The record was settled and is no longer queued.
				skip[in.Key] = true
				return true, nil
			case err != nil:
				return false, err
			case !utf8.Valid(body):
				e.log.Error("a queued message is not text and cannot be put on a page; it is not handed out", "message", string(in.Key.Message))
				skip[in.Key] = true
				return true, nil
			}
			bodies[loaded] = string(body)
		}
		return false, nil
	}
	for want := 8; ; want *= 2 {
		if retry, err := load(want); err != nil || retry {
			return packed{}, retry, err
		}
		msgs := make([]handoff.Message, len(cands))
		for i, c := range cands[:loaded] {
			next := max(c.in.Record.NextPart(), 1)
			msgs[i] = handoff.Message{
				ID: string(c.in.Key.Message), From: ident.Address{Node: c.in.Key.SenderNode, Endpoint: c.in.Key.Sender},
				Body: bodies[i], PossiblyDuplicate: c.in.Record.PossibleDuplicate(next), NextPart: c.in.Record.NextPart(),
			}
		}
		page, err := handoff.Pack(msgs, lease, budget)
		if err != nil {
			return packed{}, false, fmt.Errorf("engine: the page of %d messages: %w", len(cands), err)
		}
		if len(page.HandedOut) > loaded {
			continue // it reached messages whose bodies are not read: read more
		}
		out := packed{out: page.Output}
		for i, h := range page.HandedOut {
			c := cands[i]
			rec, res, err := c.in.Record.Handout(now, c.auth, h.Parts, h.Part, lease)
			switch {
			case errors.Is(err, delivery.ErrInvalidInput):
				// The message was handed out before in another number of parts: another
				// front end or another budget cuts it differently, and a part of one cut
				// is not a part of the other.
				e.log.Error("a queued message was handed out in another number of parts and is not handed out now", "message", string(c.in.Key.Message), "error", err.Error())
				skip[c.in.Key] = true
				return packed{}, true, nil
			case err != nil:
				return packed{}, false, err
			case res.Action == delivery.HandoutUnavailable:
				// The message is due and nothing has ticked it (the tick waits for the clock to
				// be trusted, for a write to be retried or for a lease to be released), so the
				// time has not judged it yet. It is not on this page; a later recv reaches it.
				e.log.Info("a queued message is not handed out until it has been ticked", "message", string(c.in.Key.Message), "reason", res.Reason)
				skip[c.in.Key] = true
				return packed{}, true, nil
			case res.Action != delivery.HandoutGranted && res.Action != delivery.HandoutResent:
				return packed{}, false, fmt.Errorf("engine: message %s was judged to be handed out and the delivery rules say %d", c.in.Key.Message, res.Action)
			}
			c.in.Record = rec
			out.handed = append(out.handed, pageEntry{in: c.in, part: h.Part})
			out.at = append(out.at, c.place)
		}
		return out, false, nil
	}
}

// closeRefused ends the delivery of a message the second check refused: rejected when its
// body was never handed out, unknown when it was, which is what the delivery rules
// say, and the body goes with the record. It reports false, and leaves the record as it
// is, for a message that is due and has not been ticked: the time judges it before the
// check does, and the next recv reaches it.
func (e *Engine) closeRefused(now time.Time, in store.InRecord, lease delivery.LeaseID) (store.InRecord, bool, error) {
	chunks := max(in.Record.Lease.Chunks, 1)
	rec, res, err := in.Record.Handout(now, delivery.AuthorizationDeny, chunks, 1, lease)
	if err != nil {
		return in, false, fmt.Errorf("engine: closing message %s: %w", in.Key.Message, err)
	}
	switch res.Action {
	case delivery.HandoutDenied:
		in.Record = rec
		return settledIn(in), true, nil
	case delivery.HandoutUnavailable:
		return in, false, nil
	}
	return in, false, fmt.Errorf("engine: message %s was refused by the second check and the delivery rules say %d", in.Key.Message, res.Action)
}

// writeLeases writes what a recv changed, in one transaction, takes it into the view
// and remembers what the page held.
func (e *Engine) writeLeases(now time.Time, endpoint ident.EndpointID, lease delivery.LeaseID, ins []store.InRecord, p packed) error {
	if len(ins) == 0 {
		return nil
	}
	if err := e.d.Store.Lease(ins); err != nil {
		return err
	}
	for _, in := range ins {
		e.view.setIn(in)
	}
	if len(p.handed) > 0 {
		page := &issuedPage{endpoint: endpoint, at: now}
		for _, h := range p.handed {
			page.items = append(page.items, pageItem{key: h.in.Key, part: h.part})
		}
		e.rememberPage(now, lease, page)
	}
	return nil
}

// rememberPage keeps what a page held for pageMemory, and forgets the pages older
// than that.
func (e *Engine) rememberPage(now time.Time, lease delivery.LeaseID, p *issuedPage) {
	for id, old := range e.pages {
		if now.Sub(old.at) > pageMemory {
			delete(e.pages, id)
		}
	}
	e.pages[lease] = p
}

// handoutAuthorization is the second check, made at the moment the body would be
// handed out: the declarations of the endpoint as they are now, and for a message
// from another node the rules of the node. A notice of the system endpoint is
// exempt traffic and has none.
func (e *Engine) handoutAuthorization(now time.Time, st *endpointState, in store.InRecord) (delivery.AuthorizationKind, error) {
	if in.Record.Exempt {
		return 0, nil
	}
	if in.Derived != nil {
		return 0, errors.New("a delivery derived from a control signal is judged by the request it was admitted with, which nothing records yet")
	}
	action, _ := envelope.KindMessage.Action()
	var size int64
	if in.Body != nil {
		size = int64(in.Body.Len)
	}
	// Local is the node's own transport: another node cannot present this node's
	// identifier.
	v := e.evaluate(now, authCheck{
		stage: authz.StageRecv, local: in.Key.SenderNode == e.d.Node,
		sender: authz.Sender{Node: in.Key.SenderNode, Endpoint: in.Key.Sender}, action: action,
		target: authz.Target{Kind: authz.TargetEndpoint, Endpoint: in.Key.Recipient}, bodySize: size,
		reply: in.ReplyDecision.Granted(), decl: st.decl,
	})
	auth := delivery.IntakeAuthorization(v).Kind
	if auth == 0 {
		return 0, fmt.Errorf("the check of message %s gave no verdict", in.Key.Message)
	}
	return auth, nil
}

// emptyPage is the page that holds no message, under the lease of the recv.
func (e *Engine) emptyPage(lease delivery.LeaseID, budget handoff.Budget) ([]byte, error) {
	empty, err := handoff.Pack(nil, lease, budget)
	return empty.Output, err
}

// recvPart hands out one part of one message, by number. A message that is suspended,
// or that is due and has not been ticked, is still queued and has no part to give now:
// the answer is the empty page, and asking again gets the part. A message the endpoint
// has no longer is message_unknown.
func (e *Engine) recvPart(now time.Time, st *endpointState, lease delivery.LeaseID, budget handoff.Budget, r RecvRequest) ([]byte, error) {
	var in store.InRecord
	found := false
	for _, k := range e.view.received[msgKey{st.rec.ID, delivery.MessageID(r.Msg)}] {
		if cand := e.view.in[k]; cand.Record.State == delivery.StateAccepted && (!found || cand.AcceptedAt.Before(in.AcceptedAt)) {
			in, found = cand, true
		}
	}
	if !found {
		return nil, refuse(ReasonMessageUnknown)
	}
	auth, err := e.handoutAuthorization(now, st, in)
	if err != nil {
		return nil, err
	}
	switch auth {
	case delivery.AuthorizationSuspend:
		return e.emptyPage(lease, budget)
	case delivery.AuthorizationDeny:
		closed, ok, err := e.closeRefused(now, in, lease)
		if err != nil {
			return nil, err
		}
		if !ok {
			return e.emptyPage(lease, budget) // due and not ticked: asked for again, it is judged
		}
		if err := e.writeLeases(now, st.rec.ID, lease, []store.InRecord{closed}, packed{}); err != nil {
			return nil, err
		}
		return nil, refuse(ReasonMessageUnknown)
	}
	if in.Body == nil {
		return nil, fmt.Errorf("engine: message %s is queued and holds no body", in.Key.Message)
	}
	body, err := e.readBody(now, in, *in.Body)
	switch {
	case errors.Is(err, store.ErrBodyMismatch) || errors.Is(err, store.ErrBodyMissing):
		return nil, refuse(ReasonMessageUnknown)
	case err != nil:
		return nil, err
	case !utf8.Valid(body):
		return nil, fmt.Errorf("engine: message %s is not text and cannot be put on a page", in.Key.Message)
	}
	msg := handoff.Message{
		ID: string(in.Key.Message), From: ident.Address{Node: in.Key.SenderNode, Endpoint: in.Key.Sender},
		Body: string(body), PossiblyDuplicate: in.Record.PossibleDuplicate(r.Part), NextPart: in.Record.NextPart(),
	}
	page, err := handoff.Part(msg, r.Part, lease, budget)
	if errors.Is(err, handoff.ErrNoSuchPart) {
		return nil, invalidArgument("part", err)
	}
	if err != nil {
		return nil, fmt.Errorf("engine: part %d of message %s: %w", r.Part, in.Key.Message, err)
	}
	h := page.HandedOut[0]
	rec, res, err := in.Record.Handout(now, auth, h.Parts, h.Part, lease)
	switch {
	case errors.Is(err, delivery.ErrInvalidInput):
		return nil, invalidArgument("part", fmt.Errorf("the message was handed out in another number of parts: %w", err))
	case err != nil:
		return nil, err
	case res.Action == delivery.HandoutUnavailable:
		// Due and not ticked, as in a page: the message is still queued and has no part to give now.
		return e.emptyPage(lease, budget)
	case res.Action != delivery.HandoutGranted && res.Action != delivery.HandoutResent:
		return nil, fmt.Errorf("engine: message %s was judged to be handed out and the delivery rules say %d", in.Key.Message, res.Action)
	}
	in.Record = rec
	p := packed{handed: []pageEntry{{in: in, part: h.Part}}}
	if err := e.writeLeases(now, st.rec.ID, lease, []store.InRecord{in}, p); err != nil {
		return nil, err
	}
	return page.Output, nil
}

// ---------------------------------------------------------------------------
// ack

type cmdAck struct {
	call[[]adapterproto.PartAck]
	caller Caller
	req    AckRequest
}

func (c *cmdAck) gate() gate { return gateWrite }
func (c *cmdAck) run(e *Engine, now time.Time) {
	st, err := e.view.resolve(c.caller)
	if err != nil {
		c.done(nil, err)
		return
	}
	res, err := e.ack(now, st, c.req)
	c.done(res, e.classify(err))
}

// Ack acknowledges parts of a page the caller's endpoint was handed, and says what
// became of each. A lease the endpoint was never handed anything under is refused as
// unknown, whoever else holds it.
func (e *Engine) Ack(ctx context.Context, c Caller, r AckRequest) ([]adapterproto.PartAck, error) {
	cmd := &cmdAck{call: newCall[[]adapterproto.PartAck](), caller: c, req: r}
	return roundTrip(ctx, e, submitExternal, cmd, cmd.call)
}

func (e *Engine) ack(now time.Time, st *endpointState, r AckRequest) ([]adapterproto.PartAck, error) {
	lease, err := delivery.ParseLeaseID(string(r.Lease))
	if err != nil {
		return nil, invalidArgument("lease", err)
	}
	// The lease must be one this endpoint was handed: a record of another endpoint
	// that holds it does not make it this endpoint's.
	var own []store.TargetKey
	for _, k := range e.view.leased[lease] {
		if k.Recipient == st.rec.ID {
			own = append(own, k)
		}
	}
	if len(own) == 0 {
		return nil, refuse(ReasonLeaseUnknown)
	}
	items := e.itemsToAck(st.rec.ID, lease, own, r.Items)
	working := map[store.TargetKey]store.InRecord{}
	var changed []store.TargetKey
	var out []adapterproto.PartAck
	for _, it := range items {
		verdict, err := e.ackPart(now, st.rec.ID, lease, it, working, &changed)
		if err != nil {
			return nil, err
		}
		out = append(out, adapterproto.PartAck{MessageID: it.MessageID, Part: it.Part, Ack: verdict})
	}
	if len(changed) > 0 {
		ins := make([]store.InRecord, len(changed))
		for i, k := range changed {
			ins[i] = working[k]
		}
		if err := e.d.Store.Ack(ins); err != nil {
			return nil, err
		}
		for _, in := range ins {
			e.view.setIn(in)
		}
	}
	return out, nil
}

// itemsToAck are the parts an acknowledgement is about: the ones it names, or, when
// it names none, every part the page held. The page is remembered for as long as a
// lease lives; after that, or after a restart, only a message that fits whole is known
// to have handed out its one part, and a part of a message that was cut cannot be told
// from the others, so it is left for the agent to name.
func (e *Engine) itemsToAck(endpoint ident.EndpointID, lease delivery.LeaseID, own []store.TargetKey, named []adapterproto.PartRef) []adapterproto.PartRef {
	if len(named) > 0 {
		return named
	}
	var out []adapterproto.PartRef
	if p := e.pages[lease]; p != nil && p.endpoint == endpoint {
		for _, it := range p.items {
			out = append(out, adapterproto.PartRef{MessageID: adapterproto.MessageID(it.key.Message), Part: it.part})
		}
		return out
	}
	slices.SortFunc(own, func(a, b store.TargetKey) int { return compareStrings(a.String(), b.String()) })
	for _, k := range own {
		if in := e.view.in[k]; in.Record.Lease.Chunks == 1 {
			out = append(out, adapterproto.PartRef{MessageID: adapterproto.MessageID(k.Message), Part: 1})
		} else {
			e.log.Warn("the parts of a message cut in more than one are not known after a restart; the agent names them", "message", string(k.Message))
		}
	}
	return out
}

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// ackPart acknowledges one part on the working copy of its record. A message the
// endpoint holds no record of has nothing to acknowledge.
func (e *Engine) ackPart(now time.Time, endpoint ident.EndpointID, lease delivery.LeaseID, it adapterproto.PartRef,
	working map[store.TargetKey]store.InRecord, changed *[]store.TargetKey) (adapterproto.ItemAck, error) {
	var key store.TargetKey
	found := false
	for _, k := range e.view.received[msgKey{endpoint, delivery.MessageID(it.MessageID)}] {
		// The record the lease was issued on, or else any: the lease is judged by the rules.
		if !found || slices.Contains(e.view.in[k].Record.Lease.IDs, lease) {
			key, found = k, true
		}
	}
	if !found {
		return adapterproto.ItemRefusedNoHandout, nil
	}
	in, ok := working[key]
	if !ok {
		in = e.view.in[key]
	}
	rec, verdict, _, err := in.Record.Ack(now, delivery.Ack{Part: it.Part, Lease: lease, TokenMatches: true})
	if err != nil {
		return "", fmt.Errorf("engine: acknowledging message %s: %w", key.Message, err)
	}
	switch verdict {
	case delivery.AckRecorded, delivery.AckCompleted:
		in.Record = rec
		working[key] = settledIn(in)
		if !slices.Contains(*changed, key) {
			*changed = append(*changed, key)
		}
	}
	return itemAck(verdict), nil
}

// itemAck is what the protocol calls the outcome of an acknowledgement of one part.
func itemAck(v delivery.AckVerdict) adapterproto.ItemAck {
	switch v {
	case delivery.AckRecorded:
		return adapterproto.ItemRecorded
	case delivery.AckCompleted:
		return adapterproto.ItemCompleted
	case delivery.AckDuplicate:
		return adapterproto.ItemDuplicate
	case delivery.AckRefusedLease:
		return adapterproto.ItemRefusedLease
	case delivery.AckTooLate:
		return adapterproto.ItemTooLate
	}
	// AckRefusedNoHandout, and the refusal of a token, which the caller makes
	// impossible by acknowledging for the endpoint it is.
	return adapterproto.ItemRefusedNoHandout
}

// ---------------------------------------------------------------------------
// waiting

// serveReady hands a page to the wait of each endpoint that a command made a message
// ready for, after the command, so that the arrival and the hand-out are not two
// moments another reader could take the message between.
//
// It is a step of the loop like a command or the due entries of the schedule: it reads the
// clock once and observes it, and a worker may be started from it.
func (e *Engine) serveReady() {
	if len(e.ready) == 0 {
		return
	}
	e.inLoop.Store(true)
	defer e.inLoop.Store(false)
	now := e.d.Clock.Now()
	e.observe(now)
	if e.fatal != nil {
		return
	}
	ready := e.ready
	e.ready = map[ident.EndpointID]bool{}
	for id := range ready {
		w := e.waits[id]
		st := e.view.endpoints[id]
		if w == nil || st == nil || st.rec.Status != store.EndpointOpen {
			continue
		}
		page, handed, err := e.buildPage(now, st, w.lease, w.budget)
		if err != nil {
			e.classify(err)
			e.log.Warn("a waiting recv could not be handed its page", "error", err.Error())
			continue
		}
		if handed > 0 {
			e.wake(id, page)
		}
	}
}
