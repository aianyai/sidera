package engine_test

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// seedFile writes a file onto the disk durably, outside any transaction: the
// state a crash or damage left. The engine never writes a file this way.
func (r *rig) seedFile(dir, name string, data []byte) {
	r.t.Helper()
	if err := fsx.Replace(r.mem, setupTag, path.Join(dir, name), data, fsx.ModePrivate); err != nil {
		r.t.Fatalf("seed %s/%s: %v", dir, name, err)
	}
}

func (r *rig) seedBody(b []byte) string {
	r.t.Helper()
	name := store.BodyRefOf(b).Name()
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

// The scan entry never opens over work that a rule no stream has wired would
// have to do: it says which rule, and it has changed nothing on the way. A
// damaged file is not quietly kept out of the view.
func TestOpenRefusesToOpenOverWorkOfARuleNoStreamHasWired(t *testing.T) {
	noLeaks(t)
	hash := strings.Repeat("a1", 32)
	for _, tc := range []struct {
		name  string
		seed  func(r *rig)
		rule  string
		check func(t *testing.T, r *rig)
	}{
		{"a damaged record", func(r *rig) { r.seedFile(store.InDir, hash+".rec", []byte("not a record")) }, "quarantine", nil},
		{"a quarantine without its alert", func(r *rig) {
			r.seedFile(store.InDir, hash+".rec."+strings.Repeat("0c", 16)+".corrupt", []byte("not a record"))
		}, "quarantine-resume", nil},
		{"a quarantined body without its alert", func(r *rig) {
			r.seedFile(store.BodiesDir, hash+"."+strings.Repeat("0c", 16)+".corrupt", []byte("x"))
		}, "quarantine-body", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, 10)
			tc.seed(r)
			before := r.snapshot()
			_, _, err := r.open()
			var pe *engine.PendingRecoveryError
			if !errors.As(err, &pe) || pe.Rule != tc.rule || pe.Items < 1 {
				t.Fatalf("Open error %v, want a PendingRecoveryError of rule %s", err, tc.rule)
			}
			if after := r.snapshot(); fmt.Sprint(after) != fmt.Sprint(before) {
				t.Errorf("the refused scan entry changed the disk")
			}
		})
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
	cnt, err := e.Counts(context.Background())
	if err != nil || cnt != (engine.Counts{Endpoints: 1, Unfinished: 4, Alerts: 1}) {
		t.Errorf("Counts = %+v, %v; want 1 endpoint, 4 unfinished deliveries, 1 alert", cnt, err)
	}
}

// The recovery-delivery entry is four phases in the order of the startup steps,
// each a step the streams that own it fill in; until then each begins and ends.
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
	for _, m := range r.log.messages() {
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

	if err := e.AdapterAdmitted(ctx, "chat", r.admit); err != nil {
		t.Fatalf("AdapterAdmitted while recovering: %v", err)
	}
	if _, err := e.Endpoints(ctx); err != nil {
		t.Errorf("Endpoints while recovering: %v", err)
	}
	if _, err := e.Counts(ctx); err != nil {
		t.Errorf("Counts while recovering: %v", err)
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

var _ = adapterproto.MaxRecipients
var _ ident.EndpointID
