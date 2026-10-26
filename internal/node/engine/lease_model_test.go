package engine_test

import (
	"fmt"
	"math/rand"
	"slices"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/fakeruntime"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// The property that makes the node safe to leave alone: whatever agents do or do not
// do, every record the node holds is closed in the end, and no message was ever
// acknowledged that the agent was not shown. The run is random with a fixed seed and
// covers what agents do with a page (acknowledge it, acknowledge part of it, not at
// all, long after its lease ran out, through the model's own turn with the adapter
// reporting what the model was shown whole, cut or replaced), what time does to
// messages nobody reads, a target that cannot be reached, and every control signal
// the generator of the fixture draws. Its clock moves in minutes, the scale leases and
// prompts work at, and after the last step it moves past every report_by.
//
// The oracle is not the node's own state for what was acknowledged: the ledger of the
// fakes says what the model was shown, and the adapter's own entries say what it
// reported as observed.

const modelSeed = 20261026

// modelSteps is how many random steps one run takes; with the weights below every
// path is taken several times (the test asserts it).
const modelSteps = 500

func TestEveryRecordEventuallyCloses(t *testing.T) {
	m := newModelRun(t, modelSeed)
	for i := range modelSteps {
		m.step(i)
	}
	m.finish()
	m.judge()
}

// lateAck is an acknowledgement an agent makes after a while.
type lateAck struct {
	agent fakeagent.Agent
	lease adapterproto.LeaseRef
	at    time.Time
}

// sentMessage is a message an agent sent, for the sender's own view at the end. A control
// signal is no page and no model is shown it, so what the model saw says nothing of it.
type sentMessage struct {
	from    string
	id      adapterproto.MessageID
	control bool
}

type modelRun struct {
	t     *testing.T
	h     *harness.H
	rng   *rand.Rand
	gen   *harness.ControlGen
	names []string // the direct endpoints of the fixture's first instance
	other string   // a direct endpoint of the second instance
	sent  []sentMessage
	late  []lateAck
	paths map[string]int
	down  bool // the target of every send is unreachable

	leases map[fakeagent.Agent]adapterproto.LeaseRef // the page each agent was last handed
}

func newModelRun(t *testing.T, seed int64) *modelRun {
	t.Helper()
	names := []string{"a", "b", "c"}
	h := sessionNode(t, harness.Options{Seed: uint64(seed), Adapter: &harness.AdapterOptions{ExtraInstances: []string{otherInstance}}}, names...)
	other, err := h.DirectOn(otherInstance, "d")
	if err != nil || other == "" {
		t.Fatalf("a direct endpoint on the second adapter: %v", err)
	}
	quiet(t, h)
	return &modelRun{
		t: t, h: h, rng: rand.New(rand.NewSource(seed)), names: names, other: "d", paths: map[string]int{}, leases: map[fakeagent.Agent]adapterproto.LeaseRef{},
		gen: harness.NewControlGen(h, seed, names, []string{"b", "c", "d"}),
	}
}

// The paths of the generator of this test, beside those of the control generator.
const (
	pathSendDirect      = "send-direct"
	pathSendSession     = "send-session"
	pathSendLong        = "send-long"
	pathRecvAckAll      = "recv-ack-all"
	pathRecvAckOne      = "recv-ack-first-part"
	pathRecvNoAck       = "recv-no-ack"
	pathRecvLateAck     = "recv-ack-after-a-while"
	pathRecvChained     = "recv-acks-the-page-before"
	pathModelWhole      = "model-turn-reads-whole"
	pathModelCut        = "model-turn-reads-cut"
	pathModelReplaced   = "model-turn-reads-replaced"
	pathEndTurn         = "end-turn"
	pathUnreachable     = "target-unreachable"
	pathCreateOnOther   = "create-session-on-the-second-adapter"
	pathSessionRecvFail = "session-recv-refused"
	pathLeaseRenewed    = "lease-extended-beyond-one-duration"
	pathLeaseAtCap      = "lease-extended-to-the-cap"
)

var modelPaths = []string{
	pathSendDirect, pathSendSession, pathSendLong, pathRecvAckAll, pathRecvAckOne, pathRecvNoAck, pathRecvLateAck,
	pathRecvChained, pathModelWhole, pathModelCut, pathModelReplaced, pathEndTurn, pathUnreachable, pathCreateOnOther,
}

func (m *modelRun) took(path string) { m.paths[path]++ }

// openSessions are the session endpoints that are open, by the records on the disk.
func (m *modelRun) openSessions() []store.EndpointRecord {
	var out []store.EndpointRecord
	for _, ep := range sessionEndpoints(m.t, m.h) {
		if ep.Status == store.EndpointOpen {
			out = append(out, ep)
		}
	}
	return out
}

func (m *modelRun) pickSession() (store.EndpointRecord, bool) {
	s := m.openSessions()
	if len(s) == 0 {
		return store.EndpointRecord{}, false
	}
	return s[m.rng.Intn(len(s))], true
}

func (m *modelRun) addrOf(ep store.EndpointRecord) string {
	return fmt.Sprintf("%s/%s", harness.NodeID(), ep.ID)
}

func (m *modelRun) send(from, to, body string) adapterproto.MessageID {
	m.t.Helper()
	res, err := m.h.Agent(from).Send(m.h.Ctx(), fakeagent.SendInput{To: []string{to}, Body: body})
	if err != nil {
		m.t.Fatalf("send %s -> %s: %v", from, to, err)
	}
	m.sent = append(m.sent, sentMessage{from: from, id: res.MessageID})
	return res.MessageID
}

// step is one random step: one thing an agent or the world does, then time passes.
func (m *modelRun) step(i int) {
	t, h := m.t, m.h
	m.runLate()
	switch n := m.rng.Intn(100); {
	case n < 12:
		from := m.names[m.rng.Intn(len(m.names))]
		targets := append(slices.Clone(m.names), "d")
		to := targets[m.rng.Intn(len(targets))]
		if to != from {
			m.send(from, h.Addr(to), fmt.Sprintf("direct message %d", i))
			m.took(pathSendDirect)
		}
	case n < 20:
		if ep, ok := m.pickSession(); ok {
			m.send(m.names[m.rng.Intn(len(m.names))], m.addrOf(ep), fmt.Sprintf("session message %d", i))
			m.took(pathSendSession)
		}
	case n < 23:
		if ep, ok := m.pickSession(); ok {
			m.send(m.names[m.rng.Intn(len(m.names))], m.addrOf(ep), bigText(fmt.Sprintf("long message %d", i), 60*1024))
			m.took(pathSendLong)
		}
	case n < 37:
		to := append(slices.Clone(m.names), "d")[m.rng.Intn(len(m.names)+1)]
		m.recv(h.Agent(to))
	case n < 45:
		if ep, ok := m.pickSession(); ok {
			m.recv(h.Session(ep.ID.String()))
		}
	case n < 59:
		m.modelTurn()
	case n < 65:
		if ep, ok := m.pickSession(); ok {
			rt, err := h.RuntimeOf(ep.Instance)
			if err != nil {
				t.Fatal(err)
			}
			if err := rt.EndTurn(ep.Session.NativeRef, fakeruntime.FinalCompleted); err == nil {
				m.took(pathEndTurn)
			}
		}
	case n < 68:
		m.down = !m.down
		h.Unreachable(m.down)
		if m.down {
			m.took(pathUnreachable)
		}
	case n < 71:
		createOn(t, h, otherInstance, m.names[m.rng.Intn(len(m.names))], "default")
		m.took(pathCreateOnOther)
	case n < 74:
		m.walkLease()
	default:
		step, err := m.gen.Next()
		if err != nil {
			t.Fatalf("control step %d: %v", i, err)
		}
		m.sent = append(m.sent, sentMessage{from: step.Sender, id: step.Message, control: true})
	}
	quiet(t, h)
	h.Advance([]time.Duration{30 * time.Second, 2 * time.Minute, 2 * time.Minute, 6 * time.Minute, 6 * time.Minute, 20 * time.Minute}[m.rng.Intn(6)])
	quiet(t, h)
}

// walkLease follows a lease of a session whose turn is running through its whole life,
// a step of the size leases are extended at (two minutes, within the extension's lead of
// two and a half) at a time: the page is read and never acknowledged, so the lease is
// extended until it reaches the cap and then runs out. The other steps of the run move
// the clock by one jump, which gives an extension no time to be made.
func (m *modelRun) walkLease() {
	t, h := m.t, m.h
	var ep store.EndpointRecord
	found := false
	for _, c := range m.openSessions() {
		v, err := h.Engine().Adapter(h.Ctx(), c.Instance)
		if err != nil {
			t.Fatal(err)
		}
		if slices.Contains(v.Busy, adapterproto.EndpointRef(c.ID.String())) {
			ep, found = c, true
			break
		}
	}
	if !found || m.down {
		return
	}
	msg := m.send(m.names[m.rng.Intn(len(m.names))], m.addrOf(ep), "read by a turn that does not acknowledge")
	quiet(t, h)
	res, err := h.Session(ep.ID.String()).Recv(h.Ctx(), fakeagent.RecvInput{Frontend: adapterproto.FrontendCLI})
	if err != nil || !slices.ContainsFunc(res.Page.Messages, func(pm adapterproto.PageMessage) bool { return pm.ID == msg }) {
		return // the session closed meanwhile, or the page does not hold the message
	}
	longest := time.Duration(0)
	for range 10 {
		h.Advance(2 * time.Minute)
		quiet(t, h)
		for _, in := range inRecords(t, h, msg) {
			if l := in.Record.Lease; l.Active {
				longest = max(longest, l.ExpiresAt.Sub(l.IssuedAt))
			}
		}
	}
	if longest > delivery.LeaseDuration {
		m.took(pathLeaseRenewed)
	}
	if longest == delivery.LeaseRenewalCap {
		m.took(pathLeaseAtCap)
	}
}

// recv is a recv by an agent that is not the model of a turn, and what it does with
// the page.
func (m *modelRun) recv(a fakeagent.Agent) {
	in := fakeagent.RecvInput{Frontend: adapterproto.FrontendCLI}
	if m.rng.Intn(5) == 0 {
		in.AckLease = m.lastLease(a)
	}
	res, err := a.Recv(m.h.Ctx(), in)
	if err != nil {
		m.took(pathSessionRecvFail) // a session that closed since it was looked at
		return
	}
	if in.AckLease != "" {
		m.took(pathRecvChained)
	}
	if len(res.Page.Messages) == 0 {
		return
	}
	m.leases[a] = res.Page.Lease
	switch m.rng.Intn(4) {
	case 0:
		m.ack(a, res.Page.Lease)
		m.took(pathRecvAckAll)
	case 1:
		first := res.Page.Messages[0]
		part := 1
		if first.Part != nil {
			part = first.Part.Index
		}
		m.ack(a, res.Page.Lease, adapterproto.PartRef{MessageID: first.ID, Part: part})
		m.took(pathRecvAckOne)
	case 2:
		m.took(pathRecvNoAck)
	default:
		m.late = append(m.late, lateAck{a, res.Page.Lease, m.h.Clock().Now().Add(time.Duration(6+m.rng.Intn(30)) * time.Minute)})
		m.took(pathRecvLateAck)
	}
}

func (m *modelRun) lastLease(a fakeagent.Agent) adapterproto.LeaseRef { return m.leases[a] }

// ack acknowledges and ignores what the node answers: a lease whose records were
// closed meanwhile is refused, which is not what this test judges.
func (m *modelRun) ack(a fakeagent.Agent, lease adapterproto.LeaseRef, items ...adapterproto.PartRef) {
	_, _ = a.Ack(m.h.Ctx(), fakeagent.AckInput{Lease: lease, Items: items})
}

func (m *modelRun) runLate() {
	now := m.h.Clock().Now()
	rest := m.late[:0]
	for _, l := range m.late {
		if l.at.After(now) {
			rest = append(rest, l)
			continue
		}
		m.ack(l.agent, l.lease)
	}
	m.late = rest
}

// modelTurn makes the model of a session read its queue in its next turn, with the
// adapter reporting the result whole, cut or replaced, and wakes the session with a
// message.
func (m *modelRun) modelTurn() {
	ep, ok := m.pickSession()
	if !ok {
		return
	}
	mode, path := fakeadapter.ObservedFaithful, pathModelWhole
	switch m.rng.Intn(4) { // a whole result is half of them: it is the one that needs a page the node still holds
	case 2:
		mode, path = fakeadapter.ObservedTruncated, pathModelCut
	case 3:
		mode, path = fakeadapter.ObservedReplaced, pathModelReplaced
	}
	ctl, err := m.h.Adapter(ep.Instance)
	if err != nil {
		m.t.Fatal(err)
	}
	if err := ctl.SetObserved(m.h.Ctx(), mode); err != nil {
		m.t.Fatal(err)
	}
	rt, err := m.h.RuntimeOf(ep.Instance)
	if err != nil {
		m.t.Fatal(err)
	}
	plan := fakeruntime.TurnPlan{Actions: []fakeruntime.TurnAction{{Kind: fakeruntime.ActionRecv}}, EndAfter: m.rng.Intn(2) == 0}
	if err := rt.SetPlan(ep.Session.NativeRef, plan); err != nil {
		m.t.Fatal(err)
	}
	m.send(m.names[m.rng.Intn(len(m.names))], m.addrOf(ep), fmt.Sprintf("for the model %d", len(m.sent)))
	m.took(path)
}

// finish stops what the run starts and moves the clock beyond every report_by, until
// no record has one in the future.
func (m *modelRun) finish() {
	t, h := m.t, m.h
	h.Unreachable(false)
	for _, l := range m.late {
		m.ack(l.agent, l.lease)
	}
	m.late = nil
	quiet(t, h)
	for range 600 {
		if !h.Clock().Now().Before(latestReportBy(scanOf(t, h)).Add(time.Hour)) {
			return
		}
		h.Advance(5 * time.Minute)
		quiet(t, h)
	}
	t.Fatalf("the clock is at %v and some record still has a report_by after it", h.Clock().Now())
}

// latestReportBy is the latest report_by of any record of the scan.
func latestReportBy(sc *store.Scan) time.Time {
	var latest time.Time
	note := func(at time.Time) {
		if at.After(latest) {
			latest = at
		}
	}
	for _, in := range sc.In {
		note(in.Record.ReportBy)
	}
	for _, c := range sc.Ctl {
		note(c.Record.ReportBy)
	}
	for _, o := range sc.Out {
		for _, d := range outDeliveries(o) {
			note(d.ReportBy)
		}
	}
	return latest
}

func outDeliveries(o store.OutRecord) []delivery.Record {
	if o.Lost != nil {
		return o.Lost.Deliveries
	}
	var out []delivery.Record
	for _, d := range o.Outbound.Deliveries() {
		out = append(out, d.Delivery)
	}
	return out
}

// judge holds the finished run to the property and to the coverage the run claims.
func (m *modelRun) judge() {
	t, h := m.t, m.h
	sc := scanOf(t, h)
	t.Logf("paths of the run: %v; control paths: %v", m.paths, m.gen.Paths())
	for _, p := range modelPaths {
		if m.paths[p] < 3 {
			t.Errorf("the path %s was taken %d times, want at least 3", p, m.paths[p])
		}
	}
	// Every extension a lease can have is reached by the walk of a lease, which follows
	// one through its life in short steps: the other steps move the clock past a lease
	// in one jump.
	for _, p := range []string{pathLeaseRenewed, pathLeaseAtCap} {
		if m.paths[p] == 0 {
			t.Errorf("no lease of the run was %s", p)
		}
	}
	for _, p := range harness.ControlPaths {
		if m.gen.Paths()[p] == 0 {
			t.Errorf("the control path %s was never taken", p)
		}
	}

	states := map[delivery.State]int{}
	open := func(kind, name string, r delivery.Record) {
		states[r.State]++
		if !r.State.IsTerminal() {
			t.Errorf("%s %s is %s after every report_by has passed, want it closed", kind, name, r.State)
		}
	}
	for _, in := range sc.In {
		open("target record", in.Name(), in.Record)
	}
	for _, c := range sc.Ctl {
		open("control record", c.Name(), c.Record)
	}
	for _, o := range sc.Out {
		for _, d := range outDeliveries(o) {
			open("source record", o.Name(), d)
		}
	}
	t.Logf("how the records ended: %v", states)
	for _, s := range []delivery.State{delivery.StateDelivered, delivery.StateRejected, delivery.StateClosedUnconfirmed} {
		if states[s] == 0 {
			t.Errorf("no record ended %s: the run does not reach every way a record closes (%v)", s, states)
		}
	}

	log, err := h.Ledger()
	if err != nil {
		t.Fatal(err)
	}
	shown := modelShown(log)

	// What the sender is told is final as well, and a sender is told a message was
	// delivered only when the model was shown all of it.
	for _, s := range m.sent {
		st, err := h.Agent(s.from).Status(h.Ctx(), fakeagent.StatusInput{MessageID: s.id})
		if err != nil {
			t.Errorf("the status of %s sent by %s: %v", s.id, s.from, err)
			continue
		}
		for _, r := range st.Recipients {
			switch r.State {
			case adapterproto.DeliveryDelivered:
				if s.control {
					continue
				}
				to, err := ident.ParseAddress(string(r.To))
				if err != nil {
					t.Errorf("the address %q a status of %s names: %v", r.To, s.id, err)
				} else if !shown.whole(to.Endpoint.String(), string(s.id)) {
					t.Errorf("the sender %s is told %s was delivered to %s and the model was not shown all of it", s.from, s.id, r.To)
				}
			case adapterproto.DeliveryRejected, adapterproto.DeliveryClosedUnconfirmed:
			default:
				t.Errorf("the sender %s is told %s for %s to %s after every report_by, want a final state", s.from, r.State, s.id, r.To)
			}
		}
	}

	m.judgeAcknowledgements(sc, log, shown)
	if err := h.CheckLedger(); err != nil {
		t.Error(err)
	}
}

// shownSet is what the ledger says the models were shown: the parts of each message by
// endpoint and message, and how many parts each message has.
type shownSet struct {
	parts map[string]map[int]bool
	total map[string]int
}

func modelShown(log *ledger.Log) shownSet {
	s := shownSet{parts: map[string]map[int]bool{}, total: map[string]int{}}
	for _, e := range log.Kind(ledger.KindModelSaw) {
		k := e.Attrs.Endpoint + " " + e.Attrs.MessageID
		if s.parts[k] == nil {
			s.parts[k] = map[int]bool{}
		}
		s.parts[k][max(e.Attrs.Part, 1)] = true
		s.total[k] = max(e.Attrs.Parts, 1)
	}
	return s
}

// whole says whether the endpoint was shown every part of the message.
func (s shownSet) whole(endpoint, message string) bool {
	k := endpoint + " " + message
	return len(s.parts[k]) > 0 && len(s.parts[k]) >= s.total[k]
}

// judgeAcknowledgements: no delivery of a message of an agent is delivered unless the
// ledger shows the endpoint was shown every part of it, and the node never allowed an
// acknowledgement on behalf of the agent for a result the adapter reported cut or
// replaced.
func (m *modelRun) judgeAcknowledgements(sc *store.Scan, log *ledger.Log, shown shownSet) {
	t := m.t
	for _, in := range sc.In {
		if in.Record.State != delivery.StateDelivered || in.Key.Sender.IsSystem() {
			continue
		}
		if !shown.whole(in.Key.Recipient.String(), string(in.Key.Message)) {
			t.Errorf("message %s is delivered to %s and the model was not shown all of it", in.Key.Message, in.Key.Recipient)
		}
	}
	answers := map[string]int{} // by the mode the adapter reported and the verdict it got
	for _, e := range log.Kind(ledger.KindObservedSent) {
		answers[e.Attrs.Mode+" "+e.Attrs.Verdict]++
		if e.Attrs.Mode != string(fakeadapter.ObservedFaithful) && e.Attrs.Verdict == string(adapterproto.AckAllowed) {
			t.Errorf("the node allowed an acknowledgement for a result reported as %s (lease %s)", e.Attrs.Mode, e.Attrs.Lease)
		}
	}
	t.Logf("recv.observed answers: %v", answers)
	// The run reaches the answers it judges: a whole result is acknowledged, a cut or a
	// replaced one is refused.
	for _, want := range []string{"faithful allowed", "truncated refused", "replaced refused"} {
		if answers[want] < 3 {
			t.Errorf("recv.observed was answered %q %d times, want at least 3 (%v)", want, answers[want], answers)
		}
	}
}
