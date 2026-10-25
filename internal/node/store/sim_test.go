package store_test

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/store"
)

// sim plays the part of the engine's startup scan: it evaluates the recovery
// predicates of the store in the order of the startup, applies the delivery
// rules the store does not know, and calls the store's methods to write. It
// is the oracle of the crash tests: what it leaves behind is what a restart
// would. It is deliberately simple; what it must get right is the order and the
// idempotence, not the delivery semantics.
type sim struct {
	t       testing.TB
	st      *store.Store
	now     time.Time
	enabled func(instance string) bool
	// chunk is the number of records of one chunk of a body quarantine.
	chunk int
	// did counts the rules that did work.
	did map[string]int
}

func newSim(t testing.TB, st *store.Store) *sim {
	return &sim{t: t, st: st, now: base.Add(2 * time.Hour), enabled: func(string) bool { return true }, chunk: 2, did: map[string]int{}}
}

func (s *sim) scan() *store.Scan {
	s.t.Helper()
	sc, err := s.st.Scan()
	if err != nil {
		s.t.Fatalf("Scan: %v", err)
	}
	return sc
}

func (s *sim) must(err error, what string) {
	s.t.Helper()
	if err != nil {
		s.t.Fatalf("%s: %v", what, err)
	}
}

// alertFor is the alert ID of the quarantine of a file: derived from its name, so
// that a quarantine repeated after a crash gets the same name.
func alertFor(dir, name string) store.AlertID {
	h := sha256.Sum256([]byte(dir + "/" + name))
	var id store.AlertID
	copy(id[:], h[:16])
	return id
}

func (s *sim) alert(id store.AlertID, kind, subject string) store.AlertRecord {
	return store.AlertRecord{ID: id, Kind: kind, At: s.now, Detail: "damaged: " + subject, Subject: subject}
}

func (s *sim) lostOut(k store.OutKey) store.OutRecord { return lostOutAt(k, s.now) }

func (s *sim) lostIn(k store.TargetKey) store.InRecord { return lostInAt(k, s.now) }

func (s *sim) lostCtl(k store.TargetKey) store.CtlRecord { return lostCtlAt(k, s.now) }

// replacement is the record that goes under the name of a damaged delivery
// record, or nil when there is none to build.
func (s *sim) replacement(dir string, key any) store.Record {
	switch k := key.(type) {
	case store.OutKey:
		if dir == store.OutDir {
			return s.lostOut(k)
		}
	case store.TargetKey:
		switch dir {
		case store.InDir:
			return s.lostIn(k)
		case store.CtlDir:
			return s.lostCtl(k)
		}
	}
	return nil
}

// recover runs the rules of the startup until none has work, as the engine's
// scan entry does, and returns how many passes it took.
func (s *sim) recoverAll() int {
	s.t.Helper()
	for pass := 1; pass <= 12; pass++ {
		if !s.pass() {
			return pass
		}
	}
	s.t.Fatalf("recovery did not settle in 12 passes")
	return 0
}

// pass runs every rule once, in the order of the design, on a fresh scan before
// each. It reports whether any did work.
func (s *sim) pass() bool {
	worked := false
	do := func(rule string) { worked = true; s.did[rule]++ }

	// Damaged files are quarantined, a replacement and a closed session endpoint
	// with them.
	for _, d := range s.scan().Damaged {
		sc := s.scan()
		id := alertFor(d.Dir, d.Name)
		q := store.Quarantine{File: d, Scan: sc}
		var kindName = "corrupt_record"
		if d.Damage.Class == store.BodyDamaged {
			q.Replacement = s.replacement(d.Dir, d.Key)
		}
		if d.Dir == store.CtlDir {
			// A session may remain without anyone controlling it.
			if len(store.QuarantineSessions(sc, d.Name)) > 0 {
				kindName = "control_unknown"
			}
		}
		q.Alert = s.alert(id, kindName, d.Dir+"/"+d.Name)
		s.must(s.st.Quarantine(q), "Quarantine "+d.Name)
		do("quarantine")
	}
	for _, c := range store.UnfinishedQuarantines(s.scan()) {
		sc := s.scan()
		q := store.QuarantineResume{File: c, Scan: sc}
		kindName := "corrupt_record"
		if c.Key != nil {
			q.Replacement = s.replacement(c.Dir, c.Key)
		}
		if c.Dir == store.CtlDir {
			if len(store.QuarantineSessions(sc, c.Original)) > 0 {
				kindName = "control_unknown"
			}
		}
		q.Alert = s.alert(c.Alert, kindName, c.Dir+"/"+c.Original)
		s.must(s.st.QuarantineResume(q), "QuarantineResume "+c.Name)
		do("quarantine-resume")
	}
	for _, c := range store.UnfinishedBodyQuarantines(s.scan()) {
		s.continueBodyQuarantine(c)
		do("quarantine-body")
	}

	if eps := store.EffectReplays(s.scan()); len(eps) > 0 {
		s.must(s.st.CtlEffectReplay(eps), "CtlEffectReplay")
		do("ctl-effect-replay")
	}
	if eps := store.InstanceDrainWork(s.scan(), s.enabled); len(eps) > 0 {
		s.must(s.st.InstanceDrainReplay(eps, s.drainFor(s.scan(), eps)), "InstanceDrainReplay")
		do("instance-drain-replay")
	}
	if eps := store.OrphanEndpoints(s.scan()); len(eps) > 0 {
		s.must(s.st.OrphanGCEndpoint(eps), "OrphanGCEndpoint")
		do("orphan-gc-endpoint")
	}
	if b := s.drainBatch(s.scan()); len(b.In)+len(b.Ctl) > 0 {
		s.must(s.st.EndpointDrainReplay(b), "EndpointDrainReplay")
		do("endpoint-drain-replay")
	}
	for _, w := range store.PromptReplays(s.scan()) {
		ins := s.apply(w, delivery.Event{Type: delivery.EventNotified})
		s.must(s.st.PromptReplay(ins), "PromptReplay")
		do("prompt-replay")
	}
	for _, w := range store.BlockedReplays(s.scan()) {
		ev := delivery.Event{Type: delivery.EventBlocked, Cause: w.Head.Pending.Cause}
		if w.Head.Pending.Event == store.PendingUnblocked {
			ev = delivery.Event{Type: delivery.EventUnblocked}
		}
		s.must(s.st.BlockedReplay(s.apply(w, ev)), "BlockedReplay")
		do("blocked-replay")
	}
	if outs := store.FeedbackReplays(s.scan()); len(outs) > 0 {
		s.must(s.st.FeedbackReplay(outs), "FeedbackReplay")
		do("feedback-replay")
	}
	if bodies := store.OrphanBodies(s.scan()); len(bodies) > 0 {
		s.must(s.st.OrphanGC(bodies), "OrphanGC")
		do("orphan-gc")
	}
	return worked
}

// apply applies an event to every follower of a head and returns the records
// to write.
func (s *sim) apply(w store.PendingWork, ev delivery.Event) []store.InRecord {
	var out []store.InRecord
	for _, f := range w.Followers {
		rec, o, err := f.Record.Apply(s.now, ev)
		if err != nil {
			s.t.Fatalf("applying %v to %s: %v", ev.Type, f.Key, err)
		}
		if o.Result != delivery.ResultApplied {
			// Nothing changed: the follower is written unchanged, which still
			// settles the head; the revision moves, which is what a write is.
			rec = f.Record
		}
		f.Record = rec
		out = append(out, f)
	}
	return out
}

// drainBatch applies a forced close to the candidates of the drain and returns
// the records to write, with the bodies nothing refers to afterwards.
func (s *sim) drainBatch(sc *store.Scan) store.DrainBatch {
	var b store.DrainBatch
	ins, ctls := store.DrainCandidates(sc)
	for _, in := range ins {
		rec, o, err := in.Record.Apply(s.now, delivery.Event{Type: delivery.EventRejected, Reject: delivery.RejectEndpointClosed})
		if err != nil || o.Result != delivery.ResultApplied {
			continue
		}
		in.Record = rec
		in.Body = nil
		b.In = append(b.In, in)
	}
	for _, c := range ctls {
		rec, o, err := c.Record.Apply(s.now, delivery.Event{Type: delivery.EventRejected, Reject: delivery.RejectEndpointClosed})
		if err != nil || o.Result != delivery.ResultApplied {
			continue
		}
		c.Record = rec
		b.Ctl = append(b.Ctl, c)
	}
	b.Release = unreferenced(sc, b.In, b.Ctl)
	return b
}

// unreferenced lists the bodies that no record of the scan refers to once the
// given records are replaced.
func unreferenced(sc *store.Scan, ins []store.InRecord, ctls []store.CtlRecord) []store.BodyRef {
	replaced := map[string]bool{}
	var candidates []store.BodyRef
	for _, in := range ins {
		replaced[in.Name()] = true
	}
	for _, c := range ctls {
		replaced[c.Name()] = true
	}
	refs := map[string]int{}
	add := func(name string, r *store.BodyRef) {
		if r != nil && !replaced[name] {
			refs[r.Name()]++
		}
	}
	for _, o := range sc.Out {
		add(o.Name(), o.Body)
	}
	for _, in := range sc.In {
		add(in.Name(), in.Body)
		if in.Body != nil && replaced[in.Name()] {
			candidates = append(candidates, *in.Body)
		}
	}
	for _, c := range sc.Ctl {
		add(c.Name(), c.Body)
		if c.Body != nil && replaced[c.Name()] {
			candidates = append(candidates, *c.Body)
		}
	}
	var out []store.BodyRef
	seen := map[string]bool{}
	for _, c := range candidates {
		if refs[c.Name()] == 0 && !seen[c.Name()] {
			seen[c.Name()] = true
			out = append(out, c)
		}
	}
	return out
}

// bodyMismatch is what the engine does when a body it reads does not hash to its
// name: the body is quarantined, and every record that refers to it is replaced
// by a lost record when it still needs the body, and stripped of the reference
// when it does not, in chunks of s.chunk records.
func (s *sim) bodyMismatch(name string) {
	s.t.Helper()
	id := alertFor(store.BodiesDir, name)
	s.quarantineBody(name, id, true)
}

func (s *sim) continueBodyQuarantine(c store.CorruptFile) {
	s.quarantineBody(c.Original, c.Alert, false)
}

func (s *sim) quarantineBody(name string, id store.AlertID, rename bool) {
	s.t.Helper()
	sc := s.scan()
	var outs []store.OutRecord
	var ins []store.InRecord
	var ctls []store.CtlRecord
	for _, o := range sc.Out {
		if o.Body != nil && o.Body.Name() == name {
			o.Body = nil
			for i, tr := range o.Outbound.Recipients {
				if tr.Delivery.NeedsBody() {
					lost, feedback := lostRecord(delivery.SideSource, false, s.now)
					o.Outbound.Recipients[i].Delivery = lost
					for _, f := range feedback {
						o.Feedback = append(o.Feedback, delivery.Item{
							Ref:      delivery.DeliveryRef{Sender: ident.Address{Node: node(local), Endpoint: o.Key.Sender}, Message: o.Key.Message, Recipient: tr.Address},
							Feedback: f, At: s.now,
						})
					}
				}
			}
			outs = append(outs, o)
		}
	}
	for _, in := range sc.In {
		if in.Body != nil && in.Body.Name() == name {
			if in.Record.NeedsBody() {
				in = s.lostIn(in.Key)
				in.Rev = 0
			} else {
				in.Body = nil
			}
			ins = append(ins, in)
		}
	}
	for _, c := range sc.Ctl {
		if c.Body != nil && c.Body.Name() == name {
			if c.Record.NeedsBody() {
				c = s.lostCtl(c.Key)
			} else {
				c.Body = nil
			}
			ctls = append(ctls, c)
		}
	}
	first := rename
	for {
		q := store.BodyQuarantine{Name: name, AlertID: id, Rename: first}
		room := s.chunk
		take := func(n int) int {
			if n > room {
				n = room
			}
			room -= n
			return n
		}
		n := take(len(outs))
		q.Out, outs = outs[:n], outs[n:]
		n = take(len(ins))
		q.In, ins = ins[:n], ins[n:]
		n = take(len(ctls))
		q.Ctl, ctls = ctls[:n], ctls[n:]
		last := len(outs)+len(ins)+len(ctls) == 0
		if last {
			a := s.alert(id, "corrupt_body", store.BodiesDir+"/"+name)
			q.Alert = &a
		}
		s.must(s.st.QuarantineBody(q), "QuarantineBody "+name)
		first = false
		if last {
			return
		}
	}
}

// drainFor is the batch of a drain of the deliveries queued for the given
// endpoints, which are about to be closed.
func (s *sim) drainFor(sc *store.Scan, closed []store.EndpointRecord) store.DrainBatch {
	gone := map[ident.EndpointID]bool{}
	for _, e := range closed {
		gone[e.ID] = true
	}
	shadow := *sc
	shadow.Endpoints = nil
	for _, e := range sc.Endpoints {
		if gone[e.ID] {
			e.Status = store.EndpointClosed
		}
		shadow.Endpoints = append(shadow.Endpoints, e)
	}
	b := s.drainBatch(&shadow)
	return b
}

// work lists, by rule, what the predicates of the store find in a scan: the
// check that a run without a crash or damage has no recovery work, and that a
// finished recovery has none left. Each entry is "rule: item".
func work(sc *store.Scan, enabled func(string) bool) []string {
	var out []string
	add := func(rule string, n int, items func(i int) string) {
		for i := 0; i < n; i++ {
			out = append(out, rule+": "+items(i))
		}
	}
	bodies := store.OrphanBodies(sc)
	add("orphan-gc", len(bodies), func(i int) string { return bodies[i] })
	eps := store.OrphanEndpoints(sc)
	add("orphan-gc-endpoint", len(eps), func(i int) string { return eps[i].ID.String() })
	uq := store.UnfinishedQuarantines(sc)
	add("quarantine-resume", len(uq), func(i int) string { return uq[i].Name })
	ub := store.UnfinishedBodyQuarantines(sc)
	add("quarantine-body", len(ub), func(i int) string { return ub[i].Name })
	er := store.EffectReplays(sc)
	add("ctl-effect-replay", len(er), func(i int) string { return er[i].ID.String() })
	id := store.InstanceDrainWork(sc, enabled)
	add("instance-drain-replay", len(id), func(i int) string { return id[i].ID.String() })
	pr := store.PromptReplays(sc)
	add("prompt-replay", len(pr), func(i int) string { return pr[i].Head.Endpoint.String() })
	br := store.BlockedReplays(sc)
	add("blocked-replay", len(br), func(i int) string { return br[i].Head.Endpoint.String() })
	fr := store.FeedbackReplays(sc)
	add("feedback-replay", len(fr), func(i int) string { return fr[i].Name() })
	// A candidate of the drain is work only when a forced close changes it.
	for _, in := range func() []store.InRecord { a, _ := store.DrainCandidates(sc); return a }() {
		if _, o, err := in.Record.Apply(base, delivery.Event{Type: delivery.EventRejected, Reject: delivery.RejectEndpointClosed}); err == nil && o.Result == delivery.ResultApplied {
			out = append(out, "endpoint-drain-replay: "+in.Key.String())
		}
	}
	for _, c := range func() []store.CtlRecord { _, b := store.DrainCandidates(sc); return b }() {
		if _, o, err := c.Record.Apply(base, delivery.Event{Type: delivery.EventRejected, Reject: delivery.RejectEndpointClosed}); err == nil && o.Result == delivery.ResultApplied {
			out = append(out, "endpoint-drain-replay: "+c.Key.String())
		}
	}
	for _, d := range sc.Damaged {
		out = append(out, "quarantine: "+d.Dir+"/"+d.Name)
	}
	sort.Strings(out)
	return out
}

func noWork(t testing.TB, where string, sc *store.Scan, enabled func(string) bool) {
	t.Helper()
	if w := work(sc, enabled); len(w) > 0 {
		t.Errorf("%s: recovery work found: %v", where, w)
	}
}

func (s *sim) String() string { return fmt.Sprint(s.did) }
