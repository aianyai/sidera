package store_test

import (
	"crypto/sha256"
	"errors"
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
	// did counts the rules that did work.
	did map[string]int
}

func newSim(t testing.TB, st *store.Store) *sim {
	return &sim{t: t, st: st, now: base.Add(2 * time.Hour), enabled: func(string) bool { return true }, did: map[string]int{}}
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

// alert is the alert a caller gives the settlement of a file or record of dir.
func (s *sim) alert(kind, dir, name string) store.QuarantineAlert {
	subject := dir + "/" + name
	return store.QuarantineAlert{Kind: kind, At: s.now, Detail: "damaged: " + subject, Subject: subject}
}

// alertOf is the alert record the settlement of dir/name writes when what shows
// the damage is evidence: the bytes of a damaged file, or the digest a record
// holds of the body that does not match. It restates how the store derives the id, so that a test
// can name the alert file a settlement must write.
func alertOf(a store.QuarantineAlert, dir, name string, evidence []byte) store.AlertRecord {
	h := sha256.New()
	h.Write([]byte(dir + "\x00" + name + "\x00"))
	h.Write(evidence)
	var id store.AlertID
	copy(id[:], h.Sum(nil))
	return store.AlertRecord{ID: id, Kind: a.Kind, At: a.At, Detail: a.Detail, Subject: a.Subject}
}

func (s *sim) lostOut(k store.OutKey) store.OutRecord { return lostOutAt(k, s.now) }

// replacement is the record that goes under the name of a damaged delivery
// record, or nil when there is none to build: only a source record has one.
func (s *sim) replacement(dir string, key any) store.Record {
	if k, ok := key.(store.OutKey); ok && dir == store.OutDir {
		return s.lostOut(k)
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

	// Damaged files are settled, a replacement and a closed session endpoint
	// with them.
	for _, d := range s.scan().Damaged {
		sc := s.scan()
		q := store.Quarantine{File: d, Scan: sc, Node: node(local)}
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
		q.Alert = s.alert(kindName, d.Dir, d.Name)
		s.must(s.st.Quarantine(q), "Quarantine "+d.Name)
		do("quarantine")
	}

	if eps := store.EffectReplays(s.scan()); len(eps) > 0 {
		s.must(s.st.CtlEffectReplay(eps), "CtlEffectReplay")
		do("ctl-effect-replay")
	}
	if eps := store.InstanceDrainWork(s.scan(), s.enabled); len(eps) > 0 {
		s.must(dropWritten(s.st.InstanceDrainReplay(eps, s.drainFor(s.scan(), eps))), "InstanceDrainReplay")
		do("instance-drain-replay")
	}
	if eps := store.OrphanEndpoints(s.scan()); len(eps) > 0 {
		s.must(s.st.OrphanGCEndpoint(eps), "OrphanGCEndpoint")
		do("orphan-gc-endpoint")
	}
	if b := s.drainBatch(s.scan()); len(b.In)+len(b.Ctl) > 0 {
		s.must(dropWritten(s.st.EndpointDrainReplay(b)), "EndpointDrainReplay")
		do("endpoint-drain-replay")
	}
	if outs := store.FeedbackReplays(s.scan()); len(outs) > 0 {
		s.must(s.st.FeedbackReplay(outs), "FeedbackReplay")
		do("feedback-replay")
	}
	if lost := store.OutLostReplays(s.scan()); len(lost) > 0 {
		s.must(s.st.OutLostClear(lost), "OutLostClear")
		do("feedback-replay")
	}
	if bodies := store.OrphanBodies(s.scan()); len(bodies) > 0 {
		s.must(s.st.OrphanGC(bodies), "OrphanGC")
		do("orphan-gc")
	}
	return worked
}

// drainBatch applies a forced close to the candidates of the drain and returns
// the records to write. Like the engine's, a record that no longer needs its body
// is written without the reference, and the store removes the body it owned in
// the transaction that writes the record.
func (s *sim) drainBatch(sc *store.Scan) store.DrainBatch {
	var b store.DrainBatch
	ins, ctls := store.DrainCandidates(sc)
	for _, in := range ins {
		rec, o, err := in.Record.Apply(s.now, delivery.Event{Type: delivery.EventRejected, Reject: delivery.RejectEndpointClosed})
		if err != nil || o.Result != delivery.ResultApplied {
			continue
		}
		in.Record = rec
		if !rec.NeedsBody() {
			in.Body = nil
		}
		b.In = append(b.In, in)
	}
	for _, c := range ctls {
		rec, o, err := c.Record.Apply(s.now, delivery.Event{Type: delivery.EventRejected, Reject: delivery.RejectEndpointClosed})
		if err != nil || o.Result != delivery.ResultApplied {
			continue
		}
		c.Record = rec
		if !rec.NeedsBody() {
			c.Body = nil
		}
		b.Ctl = append(b.Ctl, c)
	}
	return b
}

// bodyMismatch is what the engine does when a body it reads is not what its record
// says: every record whose body does not match is settled by its own call, a
// source record that still needs the body with the lost record that replaces it.
// The target records that need it are removed, so the scenario that runs this has
// no way to ask for a reference to be dropped from one.
func (s *sim) bodyMismatch() {
	s.t.Helper()
	sc := s.scan()
	settle := func(rec store.Record, ref *store.BodyRef, dir string, replacement store.Record) {
		if ref == nil {
			return
		}
		if _, err := s.st.ReadBody(rec.Name(), *ref); !errors.Is(err, store.ErrBodyMismatch) {
			return
		}
		s.must(dropWritten(s.st.QuarantineBody(store.BodyQuarantine{
			Record: rec, Replacement: replacement, Scan: sc,
			Alert: s.alert("corrupt_body", dir, rec.Name()),
		})), "QuarantineBody "+rec.Name())
	}
	for _, o := range sc.Out {
		var replacement store.Record
		if o.NeedsBody() {
			replacement = s.lostOut(o.Key)
		}
		settle(o, o.Body, store.OutDir, replacement)
	}
	for _, in := range sc.In {
		settle(in, in.Body, store.InDir, nil)
	}
	for _, c := range sc.Ctl {
		settle(c, c.Body, store.CtlDir, nil)
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
	er := store.EffectReplays(sc)
	add("ctl-effect-replay", len(er), func(i int) string { return er[i].ID.String() })
	id := store.InstanceDrainWork(sc, enabled)
	add("instance-drain-replay", len(id), func(i int) string { return id[i].ID.String() })
	fr := store.FeedbackReplays(sc)
	add("feedback-replay", len(fr), func(i int) string { return fr[i].Name() })
	lr := store.OutLostReplays(sc)
	add("feedback-replay", len(lr), func(i int) string { return lr[i].Name() })
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
