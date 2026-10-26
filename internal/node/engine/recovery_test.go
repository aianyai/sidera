package engine_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/txn"
)

// seedFile writes a file onto the disk durably, outside any transaction: the
// state a crash or damage left. The engine never writes a file this way.
func (r *rig) seedFile(dir, name string, data []byte) {
	r.t.Helper()
	if err := fsx.Replace(r.mem, setupTag, path.Join(dir, name), data, fsx.ModePrivate); err != nil {
		r.t.Fatalf("seed %s/%s: %v", dir, name, err)
	}
}

// seedBody writes the body file of a record that is not on the disk: what a crash
// leaves between writing a body and the record that owns it. The name is the body
// file name of a record named after the digest of the content.
func (r *rig) seedBody(b []byte) string {
	r.t.Helper()
	digest := store.BodyRefOf(b).Digest
	name := store.BodyFileName(hex.EncodeToString(digest[:]) + ".rec")
	r.seedFile(store.BodiesDir, name, b)
	return name
}

// A disk that no crash and no damage touched has no recovery work, and the scan
// entry does nothing to it.
func TestOpenOfACleanDiskDoesNoRecoveryWork(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e := r.start()
	a := register(t, e, "a")
	register(t, e, "b")
	r.restart()
	r.queue(1, 3, a)
	r.restart()

	_, rep, err := r.open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if rep.Passes != 1 || len(rep.Rules) != 0 {
		t.Errorf("report %+v, want one pass and no rule run", rep)
	}
	if rep.Endpoints != 2 || rep.Unfinished != 3 {
		t.Errorf("report %+v, want 2 endpoints and 3 unfinished deliveries", rep)
	}
	plan := engine.PlanRecovery(r.scan(), t0, func(string) bool { return true })
	if !plan.Empty() || len(plan.Items()) != 0 {
		t.Errorf("a clean disk has recovery work: %v", plan.Items())
	}
}

// The scan entry runs the rules of the store in the order its README fixes and
// repeats until none has work. Each of these is what a crash leaves.
func TestOpenRunsTheRecoveryRulesAndLeavesNoWork(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e := r.start()
	closed := register(t, e, "closed")
	disabled := register(t, e, "disabled-with-instance")
	_ = disabled
	r.restart()

	// 1. A closed endpoint whose queue was not closed: the crash came between the
	// head record and the chunks (endpoint-drain-replay).
	var closedRec store.EndpointRecord
	for _, ep := range r.scan().Endpoints {
		if ep.ID == closed {
			closedRec = ep
		}
	}
	closedRec.Status, closedRec.Token = store.EndpointClosed, nil
	if err := r.st.EndpointClose(closedRec, nil); err != nil {
		t.Fatal(err)
	}
	r.queue(1, 3, closed)
	// 2. A body no record refers to (orphan-gc).
	orphan := r.seedBody([]byte("nobody refers to this"))
	// 3. A session endpoint still being created that no control record creates
	// (orphan-gc-endpoint): the intake failed before its commit point.
	stray := store.EndpointRecord{
		ID: mustEndpoint(t, 60), Kind: sessionKind, Instance: "chat", Status: store.EndpointCreating,
		Token: make([]byte, 32), Session: &store.SessionInfo{Preset: "default"}, CreatedBy: strings.Repeat("ab", 32),
	}
	for i := range stray.Token {
		stray.Token[i] = byte(i + 1)
	}
	r.seedRecord(stray)

	e2, rep, err := r.open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { r.stop2(e2) }()
	for _, rule := range []string{"endpoint-drain-replay", "orphan-gc", "orphan-gc-endpoint"} {
		if rep.Rules[rule] != 1 {
			t.Errorf("rule %s ran %d times, want once (report %v)", rule, rep.Rules[rule], rep.Rules)
		}
	}
	sc := r.scan()
	for _, in := range sc.In {
		if in.Record.State != delivery.StateRejected || in.Record.Reject != delivery.RejectEndpointClosed {
			t.Errorf("delivery %s of the closed endpoint: %v %v", in.Key.Message, in.Record.State, in.Record.Reject)
		}
	}
	if len(sc.Bodies) != 0 {
		t.Errorf("bodies left %v, want none (the orphan %s and the three of the closed queue)", sc.Bodies, orphan[:8])
	}
	for _, ep := range sc.Endpoints {
		if ep.ID == stray.ID {
			t.Errorf("the stray creating endpoint is still there")
		}
	}
	plan := engine.PlanRecovery(sc, t0, func(string) bool { return true })
	if !plan.Empty() {
		t.Errorf("work left after Open: %v", plan.Items())
	}
}

// An instance the owner disabled while the node was stopped has its endpoints
// closed by the scan entry, and the queues of them with it.
func TestOpenClosesTheEndpointsOfADisabledInstance(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e := r.start()
	a := register(t, e, "a")
	r.restart()
	r.queue(1, 2, a)
	var op admin.OperationID
	op[0] = 77
	req := configRequest(op, r.cfg.Instances().Version)
	if _, err := r.cfg.DisableAdapter(req, "chat", nil, jsonResult); err != nil {
		t.Fatalf("DisableAdapter: %v", err)
	}

	e2, rep, err := r.open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.stop2(e2)
	if rep.Rules["instance-drain-replay"] != 1 {
		t.Errorf("report %v, want instance-drain-replay once", rep.Rules)
	}
	for _, ep := range r.scan().Endpoints {
		if ep.Status != store.EndpointClosed {
			t.Errorf("endpoint %s of the disabled instance is %v", ep.ID, ep.Status)
		}
	}
	for _, in := range r.scan().In {
		if in.Record.State != delivery.StateRejected {
			t.Errorf("delivery %s is %v, want rejected", in.Key.Message, in.Record.State)
		}
	}
}

// A damaged file of any other directory is settled by the scan entry, which opens
// over it: the file is gone, the owner has an alert, and the node starts.
func TestOpenQuarantinesADamagedFileAndOpens(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	name := strings.Repeat("a1", 32) + ".rec"
	r.seedFile(store.InDir, name, []byte("not a record"))
	e, rep, err := r.open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.stop2(e)
	if rep.Rules["quarantine"] != 1 || len(rep.Order) == 0 || rep.Order[0] != "quarantine" {
		t.Errorf("the rules that ran: %v in the order %v, want the quarantine, first", rep.Rules, rep.Order)
	}
	sc := r.scan()
	if len(sc.Damaged) != 0 || len(sc.In) != 0 {
		t.Errorf("the disk holds %d damaged files and %d target records after the scan entry, want none", len(sc.Damaged), len(sc.In))
	}
	if len(sc.Alerts) != 1 || sc.Alerts[0].Kind != "corrupt_record" {
		t.Errorf("the owner has the alerts %+v, want one of kind corrupt_record", sc.Alerts)
	}
}

// The quarantine is the first of the rules, because a settlement overwrites source
// records, raises floors and removes records that every later rule reads: a record
// that is damaged and queued for a closed endpoint is removed before the drain looks
// at the queue, and the drain does not close what is not there.
func TestTheQuarantineRunsBeforeTheOtherRecoveryRules(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e := r.start()
	closed := register(t, e, "closed")
	r.restart()
	var closedRec store.EndpointRecord
	for _, ep := range r.scan().Endpoints {
		if ep.ID == closed {
			closedRec = ep
		}
	}
	closedRec.Status, closedRec.Token = store.EndpointClosed, nil
	if err := r.st.EndpointClose(closedRec, nil); err != nil {
		t.Fatal(err)
	}
	r.queue(1, 2, closed)
	orphan := r.seedBody([]byte("nobody refers to this"))
	r.seedFile(store.InDir, strings.Repeat("a1", 32)+".rec", []byte("not a record"))
	e2, rep, err := r.open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.stop2(e2)
	want := []string{"quarantine", "endpoint-drain-replay", "orphan-gc"}
	if fmt.Sprint(rep.Order) != fmt.Sprint(want) {
		t.Errorf("the rules ran in the order %v, want %v (the orphan body %s is the last)", rep.Order, want, orphan[:8])
	}
}

// The plan is a pure function of the scan: each rule names its work, and the
// check that a clean disk has none reads the same list.
func TestPlanRecoveryNamesTheWorkOfEveryRule(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e := r.start()
	a := register(t, e, "a")
	b := register(t, e, "b")
	r.restart()
	r.queue(1, 2, a)
	r.queue(5, 1, b)
	var closed store.EndpointRecord
	for _, ep := range r.scan().Endpoints {
		if ep.ID == a {
			closed = ep
		}
	}
	closed.Status, closed.Token = store.EndpointClosed, nil
	if err := r.st.EndpointClose(closed, nil); err != nil {
		t.Fatal(err)
	}
	orphan := r.seedBody([]byte("orphan"))
	r.seedFile(store.InDir, strings.Repeat("cd", 32)+".rec", []byte("garbage"))

	plan := engine.PlanRecovery(r.scan(), t0, func(instance string) bool { return instance != "chat" })
	got := plan.Items()
	sort.Strings(got)
	// The oracle is written out: every open endpoint of the disabled instance, the
	// two queued deliveries of the closed endpoint, the orphan body, the damaged file.
	want := []string{
		"endpoint-drain-replay: " + queuedIn(t, 1, a).Key.String(),
		"endpoint-drain-replay: " + queuedIn(t, 2, a).Key.String(),
		"instance-drain-replay: " + b.String(),
		"orphan-gc: " + orphan,
		"quarantine: state/in/" + strings.Repeat("cd", 32) + ".rec",
	}
	sort.Strings(want)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("plan items\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if plan.Empty() {
		t.Error("a plan with work is empty")
	}
}

func TestOpenBuildsTheViewOfWhatSurvived(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e := r.start()
	a := register(t, e, "a")
	if _, err := e.RaiseAlert(context.Background(), "disk_low", "state", "free space below the reserve"); err != nil {
		t.Fatal(err)
	}
	r.restart()
	r.queue(1, 4, a)
	e = r.start()
	st, err := e.Status(context.Background())
	if err != nil || openEndpoints(t, e) != 1 || st.Capacity.OpenDeliveries != 4 || st.AlertsOpen != 1 {
		t.Errorf("Status = %d open endpoints, %d open deliveries, %d open alerts (%v); want 1, 4 and 1",
			openEndpoints(t, e), st.Capacity.OpenDeliveries, st.AlertsOpen, err)
	}
}

// The recovery-delivery entry is four phases in the order of the startup steps,
// each a step that is filled in with the feature it belongs to; until then each begins and ends.
func TestRecoverDeliveryRunsItsPhasesInTheOrderOfTheStartupSteps(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e, _, err := r.open()
	if err != nil {
		t.Fatal(err)
	}
	e.Start()
	t.Cleanup(func() { r.stop2(e) })
	if err := e.RecoverDelivery(context.Background()); err != nil {
		t.Fatalf("RecoverDelivery: %v", err)
	}
	var steps []string
	for _, m := range r.log.Messages() {
		if strings.HasPrefix(m, "startup step ") {
			steps = append(steps, m)
		}
	}
	want := []string{
		"startup step 11 begin", "startup step 11 end", "startup step 12 begin", "startup step 12 end",
		"startup step 13 begin", "startup step 13 end", "startup step 14 begin", "startup step 14 end",
	}
	if fmt.Sprint(steps) != fmt.Sprint(want) {
		t.Errorf("logged\n%v\nwant\n%v", steps, want)
	}
	if e.Phase() != engine.PhaseRecovering {
		t.Errorf("phase %v after the recovery entry, want it to stay recovering until Serve", e.Phase())
	}
	e.Serve()
	if e.Phase() != engine.PhaseRunning {
		t.Errorf("phase %v after Serve, want running", e.Phase())
	}
}

// While the engine recovers, what changes state is refused and what only reads
// answers; what an adapter asks of the endpoints waits for the running phase.
func TestWhileRecoveringSideEffectsAreRefusedReadsAnswerAndAdapterRequestsWait(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e, _, err := r.open()
	if err != nil {
		t.Fatal(err)
	}
	e.Start()
	t.Cleanup(func() { r.stop2(e) })
	ctx := context.Background()

	if err := admitOn(ctx, e, "chat", r.admit); err != nil {
		t.Fatalf("AdapterAdmitted while recovering: %v", err)
	}
	if _, err := e.Endpoints(ctx); err != nil {
		t.Errorf("Endpoints while recovering: %v", err)
	}
	if _, err := e.Status(ctx); err != nil {
		t.Errorf("Status while recovering: %v", err)
	}
	_, err = e.CloseEndpoint(ctx, engine.CloseRequest{Endpoint: mustEndpoint(t, 1)})
	wantReason(t, err, engine.ReasonRecovering)
	_, err = e.Declare(ctx, engine.ByAdapter("chat", "x"), engine.DeclarationPatch{Paused: ptr(true)})
	wantReason(t, err, engine.ReasonRecovering)

	done := make(chan error, 1)
	var served bool
	go func() {
		_, err := e.RegisterDirect(ctx, "chat", registerParams("a"))
		if err == nil && !isServed(&served) {
			err = errors.New("the registration was answered before the engine served")
		}
		done <- err
	}()
	waitUntil(t, func() bool { return engine.WaitingForRunning(e) == 1 })
	setServed(&served)
	e.Serve()
	if err := <-done; err != nil {
		t.Errorf("RegisterDirect issued while recovering: %v", err)
	}
}

// The rules that have work run in the order the store's guarantee fixes, each
// reading what the one before it left: the control record's effect first, the
// removal of endpoints no record creates before the closing of what is queued for
// closed endpoints, the removal of bodies last.
func TestOpenRunsTheRulesInTheOrderTheStoreFixes(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e := r.start()
	closed := register(t, e, "closed")
	r.restart()

	var closedRec store.EndpointRecord
	for _, ep := range r.scan().Endpoints {
		if ep.ID == closed {
			closedRec = ep
		}
	}
	closedRec.Status, closedRec.Token = store.EndpointClosed, nil
	if err := r.st.EndpointClose(closedRec, nil); err != nil {
		t.Fatal(err)
	}
	r.queue(1, 2, closed)
	r.seedBody([]byte("orphan"))
	token := func(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }
	// A creating endpoint with no record that creates it: garbage.
	r.seedRecord(store.EndpointRecord{ID: mustEndpoint(t, 60), Kind: sessionKind, Instance: "chat", Status: store.EndpointCreating,
		Token: token(1), Session: &store.SessionInfo{Preset: "default"}, CreatedBy: strings.Repeat("ab", 32)})
	// A creating endpoint whose control record says the session was created: it opens.
	ctl := sessionCtlRecord(t, 5, mustEndpoint(t, 61))
	r.seedRecord(store.EndpointRecord{ID: mustEndpoint(t, 61), Kind: sessionKind, Instance: "chat", Status: store.EndpointCreating,
		Token: token(2), Session: &store.SessionInfo{Preset: "default"}, CreatedBy: strings.TrimSuffix(ctl.Name(), ".rec")})
	r.seedRecord(ctl)

	e2, rep, err := r.open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.stop2(e2)
	want := []string{"ctl-effect-replay", "orphan-gc-endpoint", "endpoint-drain-replay", "orphan-gc"}
	if fmt.Sprint(rep.Order) != fmt.Sprint(want) {
		t.Errorf("the rules ran in the order %v, want %v", rep.Order, want)
	}
	for _, ep := range r.scan().Endpoints {
		if ep.ID == mustEndpoint(t, 61) && ep.Status != store.EndpointOpen {
			t.Errorf("the endpoint whose session was created is %v, want open", ep.Status)
		}
	}
}

// An adapter request that waits for the running phase is let go when the node
// shuts down, and is told so.
func TestShutdownEndsTheWaitOfAnAdapterRequestThatWaitsForTheRunningPhase(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e, _, err := r.open()
	if err != nil {
		t.Fatal(err)
	}
	e.Start()
	t.Cleanup(func() { r.stop2(e) })
	if err := admitOn(context.Background(), e, "chat", r.admit); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := e.RegisterDirect(context.Background(), "chat", registerParams("a")); done <- err }()
	waitUntil(t, func() bool { return engine.WaitingForRunning(e) == 1 })
	e.BeginShutdown()
	select {
	case err := <-done:
		wantReason(t, err, engine.ReasonShuttingDown)
	case <-time.After(5 * time.Second):
		t.Fatal("the request that waits for the running phase was not let go at shutdown")
	}
}

// commitLog is a transaction observer that keeps the name of every transaction that
// reached its commit point, and which startup steps the log had shown by then.
type commitLog struct {
	log atomic.Pointer[harness.LogCapture] // set once the rig has its log, which the rig makes after the observer
	mu  sync.Mutex
	got []string
	// seen12 is how many times the log showed the begin of step 12 when watching
	// began: only a later one counts.
	seen12 int
}

// step12Begun counts the begins of step 12 the log shows.
func (c *commitLog) step12Begun() int {
	l := c.log.Load()
	if l == nil {
		return 0
	}
	return strings.Count(strings.Join(l.Messages(), "\n")+"\n", "startup step 12 begin\n")
}

// watch forgets what was seen so far: the commits that follow are those of the
// process being started.
func (c *commitLog) watch() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.got, c.seen12 = nil, c.step12Begun()
}

func (c *commitLog) Reached(p fsx.Point, _ txn.Kind) {
	if p.Step != fsx.StepCommit {
		return
	}
	stage := "before step 12"
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.step12Begun() > c.seen12 {
		stage = "from step 12"
	}
	c.got = append(c.got, p.Tag.Tx+" "+stage)
}

func (c *commitLog) count(tx, stage string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Count(strings.Join(c.got, "\n")+"\n", tx+" "+stage+"\n")
}

// A record that fell due while the node was down is ticked by step 12 of the start,
// after the adapters have said which turns run, and not by the schedule as soon as
// the loop runs: the lease of a record is released only once it is known whether the
// turn it was handed to still runs. The entries stay in the schedule, due, until
// their step; one that falls due after the step and before Serve waits for Serve.
func TestRecordsDueDuringDowntimeAreTickedByStepTwelveAndNotBefore(t *testing.T) {
	noLeaks(t)
	obs := &commitLog{}
	r := newRig(t, 10, withObserver(obs))
	obs.log.Store(r.log)
	e := r.start()
	a := register(t, e, "a")
	r.restart()
	r.queue(1, 1, a)
	later := queuedIn(t, 2, a)
	rec, err := delivery.NewTargetRecord(t0.Add(2*time.Second), t0.Add(2*time.Second), later.Record.ReportBy, delivery.DefaultEndpointMaxQueue)
	if err != nil {
		t.Fatal(err)
	}
	later.Record = rec
	if err := r.st.Deliver(bodyOf(2), []store.InRecord{later}); err != nil {
		t.Fatal(err)
	}
	// The queue time of the first message ran out while the node was down, and that of
	// the second, two seconds later, has not.
	r.clk.Advance(delivery.DefaultEndpointMaxQueue + time.Second)

	obs.watch()
	e, _, err = r.open()
	if err != nil {
		t.Fatal(err)
	}
	e.Start()
	t.Cleanup(func() { r.stop2(e) })
	// The loop runs and the adapters have not yet reported: every entry that is due
	// would have fired by now, as time moves.
	settle(t, e)
	r.clk.Advance(100 * time.Millisecond)
	settle(t, e)
	if n := obs.count("tick", "before step 12") + obs.count("feedback-form", "before step 12"); n != 0 {
		t.Fatalf("%d tick or feedback transactions committed before step 12 began (%v)", n, obs.got)
	}

	if err := e.RecoverDelivery(context.Background()); err != nil {
		t.Fatalf("RecoverDelivery: %v", err)
	}
	if n := obs.count("tick", "before step 12"); n != 0 {
		t.Errorf("%d tick transactions committed before step 12 began (%v)", n, obs.got)
	}
	ticked := obs.count("tick", "from step 12")
	if ticked != 1 {
		t.Fatalf("%d tick transactions committed in step 12 (%v), want the one that settles the record that is due", ticked, obs.got)
	}

	// The second record falls due after the step and before Serve: the loop is still
	// recovering, so it waits, and Serve lets it go.
	r.clk.Advance(time.Second)
	settle(t, e)
	if n := obs.count("tick", "from step 12"); n != ticked {
		t.Fatalf("%d tick transactions before Serve, %d after step 12: the record that fell due waited for Serve", n, ticked)
	}
	// Nothing is sent to the loop after Serve: it has to look at its schedule by itself.
	e.Serve()
	waitUntil(t, func() bool { return obs.count("tick", "from step 12") == ticked+1 })
	settle(t, e)
}
