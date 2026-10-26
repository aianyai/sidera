package engine_test

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/daemon"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/keys"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/txn"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// A node that was never crashed, damaged or made to fail has nothing to recover. The
// criterion of each rule that repairs the disk at a start is a function its owner
// exports, and the rule and this test read the same one: the work of the engine's
// recovery rules on a scan, the leftovers of interrupted writes, the garbage of the
// adapter directory, the directories of the layout, the interrupted renewal of the keys.
// After every step of a run through the events of a node's life, and after a clean
// restart that follows each of the events that leave state behind, every one of them
// finds nothing. A normal path that leaves work for a recovery rule would run that
// rule, and its writes, at every start of a node that never crashed.

func TestCleanScenariosLeaveNoRecoveryWork(t *testing.T) {
	h := sessionNode(t, harness.Options{ArchiveSweepEvery: time.Hour}, "a", "b")
	c := &cleanRun{t: t, h: h}
	c.check("the first start")

	// Messages, reads and acknowledgements between two endpoints.
	msg := send(t, h, "a", "b", "hello b")
	c.check("a message sent")
	page := recvAs(t, h, h.Agent("b"), fakeagent.RecvInput{})
	c.check("a page handed out")
	ackAs(t, h, h.Agent("b"), page.Page.Lease)
	c.check("the page acknowledged")
	if inOf(t, h, msg).Record.State != delivery.StateDelivered {
		t.Fatalf("the message is %s, want delivered: the scenario does not do what it says", inOf(t, h, msg).Record.State)
	}
	c.restart("the acknowledgement")

	// A message nobody reads runs out, and the sender is told.
	send(t, h, "a", "b", "nobody reads this")
	h.Advance(delivery.DefaultEndpointMaxQueue + 2*time.Minute)
	c.check("a queue time that ran out")
	h.Advance(2 * time.Hour)
	c.check("the sender told once")
	h.Unreachable(true)
	send(t, h, "a", "b", "to a node that cannot be reached")
	h.Advance(24 * time.Hour)
	h.Unreachable(false)
	c.check("a target that could not be reached")
	c.restart("the feedback")

	// A session: its creation settles, it reads a message with its model's turn, it is
	// interrupted and closed, and its native session is released.
	session, id, created := startedSession(t, h, "a", "start with the tests")
	c.check("a creation settled")
	if in := inOf(t, h, created); in.Derived == nil {
		t.Fatalf("the creation has no derived delivery: the scenario does not do what it says")
	}
	wantOnly(t, recvAs(t, h, session, fakeagent.RecvInput{}), created)
	c.check("the initial message read")
	c.restart("the creation of a session")

	signal(t, h, "a", addressOf(id), adapterproto.SignalSessionInterrupt, "")
	c.check("a session interrupted")
	closing := signal(t, h, "a", addressOf(id), adapterproto.SignalSessionClose, "")
	wantClose(t, replyOf(t, h, "a", closing), adapterproto.CloseReplyClosed, 0)
	c.check("a session closed and its native session released")
	if _, err := h.Agent("b").Send(h.Ctx(), fakeagent.SendInput{To: []string{addressOf(id)}, Body: "to a session that is gone"}); err != nil {
		t.Fatal(err)
	}
	c.check("a message to a closed session")
	c.restart("the closing of a session")

	// The owner closes an endpoint that has messages queued for it.
	queued := send(t, h, "a", "b", "queued for an endpoint the owner closes")
	if _, err := h.Engine().CloseEndpoint(h.Ctx(), engine.CloseRequest{Endpoint: addrOf(t, h, "b").Endpoint}); err != nil {
		t.Fatal(err)
	}
	c.check("an endpoint closed with a queue")
	if inOf(t, h, queued).Record.State != delivery.StateRejected {
		t.Fatalf("the queued message is %s, want rejected: the scenario does not do what it says", inOf(t, h, queued).Record.State)
	}
	c.restart("the closing of an endpoint")

	// The adapter's process ends and starts again.
	launch := h.AdapterLaunches(harness.Instance)
	adapterEnded(t, h, launch[len(launch)-1])
	c.checkEngine("an adapter that ended")
	adapterRestarted(t, h, len(launch)+1)
	c.check("an adapter that started again")

	// The owner disables the instance and enables it again.
	c.execute(disableCommand(1, "disable chat", admin.Version(h.Config().Instances().Version), harness.Instance))
	c.check("an instance disabled")
	if h.Config().Instances().Items[harness.Instance].Enabled {
		t.Fatal("the instance is enabled after it was disabled: the scenario does not do what it says")
	}
	c.restart("the disabling of an instance")
	c.execute(enableCommand(2, "enable chat", admin.Version(h.Config().Instances().Version), "chat", harness.Instance, ""))
	c.check("an instance enabled again")
	if !h.Config().Instances().Items[harness.Instance].Enabled {
		t.Fatal("the instance is disabled after it was enabled again: the scenario does not do what it says")
	}
	c.restart("the enabling of an instance")

	// An alert is raised for the owner.
	if _, err := h.Engine().RaiseAlert(h.Ctx(), "disk_low", "state", "below the reserve"); err != nil {
		t.Fatal(err)
	}
	c.check("an alert raised")

	// Retention passes and the sweep takes what it owns out of the live files.
	for range 2 {
		h.Advance(3*24*time.Hour + time.Minute)
		quiet(t, h)
		for range 3 {
			h.Advance(time.Hour)
			c.check("a pass of the sweep")
		}
	}
	c.restart("the sweep")

	c.judge()
}

// cleanRun is the run and what it has checked.
type cleanRun struct {
	t      *testing.T
	h      *harness.H
	checks int
	seen   struct{ in, out, ctl, endpoints, closed, archive, alerts int }
}

// execute runs a command of the owner and requires that it was done.
func (c *cleanRun) execute(cmd engine.ManagementCommand) {
	c.t.Helper()
	if res, err := c.h.Engine().Execute(c.h.Ctx(), cmd); err != nil || res.Replayed {
		c.t.Fatalf("%s: %+v, %v", cmd.Name, res, err)
	}
}

// restart stops the node cleanly and starts it again, and checks the disk the start
// left.
func (c *cleanRun) restart(after string) {
	c.t.Helper()
	c.h.Restart()
	c.check("a clean restart after " + after)
}

// check waits for the node to settle and holds the disk to every criterion.
func (c *cleanRun) check(what string) {
	c.t.Helper()
	quiet(c.t, c.h)
	c.judgeDisk(what)
}

// checkEngine is check for a moment at which the adapter is away, which the fixture's
// quiet does not wait out.
func (c *cleanRun) checkEngine(what string) {
	c.t.Helper()
	engineQuiet(c.t, c.h)
	c.judgeDisk(what)
}

func (c *cleanRun) judgeDisk(what string) {
	t, h := c.t, c.h
	t.Helper()
	c.checks++
	where := fmt.Sprintf("after %s (check %d)", what, c.checks)
	sc := scanOf(t, h)
	enabled := func(instance string) bool { return h.Config().Instances().Items[instance].Enabled }
	if plan := engine.PlanRecovery(sc, h.Clock().Now(), enabled); !plan.Empty() {
		t.Errorf("%s: the recovery rules of the engine have work: %v", where, plan.Items())
	}
	disk := h.Disk()
	if work, err := txn.CleanupWork(disk); err != nil || len(work) != 0 {
		t.Errorf("%s: leftovers of interrupted writes: %v, %v", where, work, err)
	}
	if work, err := config.AdapterGCWork(disk); err != nil || len(work) != 0 {
		t.Errorf("%s: garbage in the adapter directory: %v, %v", where, work, err)
	}
	// The fixture makes the directories of the keys and of state/, and not the runtime
	// directories the daemon's own init makes; only those may be missing.
	if work, err := daemon.LayoutWork(disk); err != nil || !slices.Equal(work, []string{"run", "manage", "logs"}) {
		t.Errorf("%s: directories of the layout missing: %v, %v; want only the runtime directories the fixture does not make", where, work, err)
	}
	if missing, err := store.MissingStateDirs(disk); err != nil || len(missing) != 0 {
		t.Errorf("%s: directories of state/ missing: %v, %v", where, missing, err)
	}
	if work, err := keys.RecoveryWork(disk); err != nil || len(work) != 0 {
		t.Errorf("%s: the keys have work to recover: %v, %v", where, work, err)
	}
	c.seen.in = max(c.seen.in, len(sc.In))
	c.seen.out = max(c.seen.out, len(sc.Out))
	c.seen.ctl = max(c.seen.ctl, len(sc.Ctl))
	c.seen.endpoints = max(c.seen.endpoints, len(sc.Endpoints))
	c.seen.archive = max(c.seen.archive, len(sc.Archive))
	c.seen.alerts = max(c.seen.alerts, len(sc.Alerts))
	for _, ep := range sc.Endpoints {
		if ep.Status == store.EndpointClosed {
			c.seen.closed = max(c.seen.closed, 1)
		}
	}
}

// judge holds the run to the coverage it claims: it checked the disk after many steps,
// and the disk it checked held every kind of record and a closed endpoint, so that an
// empty plan is not the plan of an empty node.
func (c *cleanRun) judge() {
	t := c.t
	t.Logf("%d checks; most records held at once: target %d, source %d, control %d, endpoints %d, alerts %d, archive files %d; a closed endpoint seen: %v",
		c.checks, c.seen.in, c.seen.out, c.seen.ctl, c.seen.endpoints, c.seen.alerts, c.seen.archive, c.seen.closed == 1)
	if c.checks < 25 {
		t.Errorf("%d checks, want at least 25", c.checks)
	}
	if c.seen.in == 0 || c.seen.out == 0 || c.seen.ctl == 0 || c.seen.endpoints == 0 || c.seen.alerts == 0 || c.seen.closed == 0 || c.seen.archive == 0 {
		t.Errorf("the disk the run checked was missing a kind of record (%+v): the scenario does not reach every event it names", c.seen)
	}
}
