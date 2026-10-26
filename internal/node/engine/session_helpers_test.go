package engine_test

import (
	"context"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/node/adapterhost"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// The helpers of the tests of session creation. A session endpoint comes only from
// a creation, so these make the node the way a deployment is: an adapter host that
// runs a fake adapter over one fake runtime, a direct endpoint for each sender, and
// the preset `init` makes. What a test learns it reads from the ledger of the fakes,
// from the records on the disk and from the status of the message its agent sent;
// nothing here reads a message, for no agent of a session has a prompt yet.

// restartAfter is how long the host waits before it starts an adapter again, on the
// test clock.
const restartAfter = 10 * time.Second

func sessionHostParams() adapterhost.Params {
	return adapterhost.Params{
		PingInterval: 30 * time.Second, PingTimeout: 10 * time.Second, ShutdownWait: 5 * time.Second,
		RestartBackoffMin: restartAfter, RestartBackoffMax: restartAfter,
	}
}

// sessionNode makes a node with an adapter that has a runtime, taken on, and a
// direct endpoint for each name.
func sessionNode(t *testing.T, o harness.Options, names ...string) *harness.H {
	t.Helper()
	o.Adapter = &harness.AdapterOptions{Host: sessionHostParams(), Runtime: true}
	h := harness.New(t, o)
	quiet(t, h)
	for _, n := range names {
		h.MustDirect(n)
	}
	quiet(t, h)
	return h
}

// adapterEnded ends the adapter process and returns when the node knows its adapter
// is gone and the host has done with the end of the process, so that the next move
// of the clock is the one that makes the host start another.
func adapterEnded(t testing.TB, h *harness.H, l *harness.AdapterLaunch) {
	t.Helper()
	l.End()
	deadline := time.Now().Add(10 * time.Second)
	for {
		v, err := h.Engine().Adapter(h.Ctx(), harness.Instance)
		if err == nil && !v.Online {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the node still holds the adapter online after its process ended (%v)", err)
		}
		time.Sleep(time.Millisecond)
	}
	engineQuiet(t, h)
}

// engineQuiet waits for the engine alone: while the adapter is dead the host still
// holds the counts of its connection until it has started another, and the fixture's
// quiet, which judges them, would wait for a number that cannot come.
func engineQuiet(t testing.TB, h *harness.H) {
	t.Helper()
	ctx, cancel := context.WithTimeout(h.Ctx(), 30*time.Second)
	defer cancel()
	if rep, err := h.Engine().Quiesce(ctx, engine.Expect{}); err != nil {
		t.Fatalf("the engine did not settle: %v (%+v)", err, rep)
	}
}

// adapterRestarted moves the clock to the moment the host starts the adapter again and
// returns when the instance has a process of the number given and the node has
// taken it on. The host's timer is the test clock's, and the process it starts is
// made by a goroutine, so the number is waited for on the real clock.
func adapterRestarted(t testing.TB, h *harness.H, launches int) {
	t.Helper()
	h.Advance(restartAfter)
	deadline := time.Now().Add(10 * time.Second)
	for len(h.AdapterLaunches(harness.Instance)) < launches {
		if time.Now().After(deadline) {
			t.Fatalf("the host started %d adapter processes, want %d", len(h.AdapterLaunches(harness.Instance)), launches)
		}
		time.Sleep(time.Millisecond)
	}
	quiet(t, h)
	deadline = time.Now().Add(10 * time.Second)
	for {
		v, err := h.Engine().Adapter(h.Ctx(), harness.Instance)
		if err == nil && v.Online {
			quiet(t, h) // what the node does as the adapter comes online is done
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the adapter is not online after it was started again (%v)", err)
		}
		time.Sleep(time.Millisecond)
		quiet(t, h)
	}
}

// create asks the runtime endpoint for a session with the preset, as the agent of
// from, and returns the message that carries the signal.
func create(t testing.TB, h *harness.H, from, preset string) adapterproto.MessageID {
	t.Helper()
	res, err := h.Agent(from).Control(h.Ctx(), fakeagent.ControlInput{To: h.RuntimeAddr(), Signal: adapterproto.SignalSessionCreate, Preset: preset})
	if err != nil {
		t.Fatalf("session.create with preset %q: %v", preset, err)
	}
	return res.MessageID
}

// controlReply is the control reply the status of the message shows its sender, nil
// while there is none.
func controlReply(t testing.TB, h *harness.H, from string, msg adapterproto.MessageID) *adapterproto.ControlReply {
	t.Helper()
	st, err := h.Agent(from).Status(h.Ctx(), fakeagent.StatusInput{MessageID: msg})
	if err != nil {
		t.Fatalf("status of %s: %v", msg, err)
	}
	return st.ControlReply
}

// wantFailed fails the test unless the reply is a failed creation for the reason.
func wantFailed(t testing.TB, r *adapterproto.ControlReply, reason adapterproto.ControlCreateReason) {
	t.Helper()
	if r == nil || r.Create == nil {
		t.Fatalf("no creation reply, want failed: %s", reason)
	}
	if r.Create.Outcome != adapterproto.CreateReplyFailed || r.Create.Reason != reason || r.Create.SessionEndpoint != "" {
		t.Errorf("the creation reply is %+v, want failed with %s and no session endpoint", *r.Create, reason)
	}
}

// nativeSessions is how many native sessions the runtime made, from the ledger.
func nativeSessions(t testing.TB, h *harness.H) int {
	t.Helper()
	log, err := h.Ledger()
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	return len(log.Kind(ledger.KindRuntimeSessionCreated))
}

// createRequests is how many session.create requests reached an adapter, from the
// ledger the adapters write.
func createRequests(t testing.TB, h *harness.H) int {
	t.Helper()
	log, err := h.Ledger()
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	return len(log.Kind(ledger.KindSessionCreateReceived))
}

// sessionEndpoints are the endpoint records of kind session on the disk.
func sessionEndpoints(t testing.TB, h *harness.H) []store.EndpointRecord {
	t.Helper()
	var out []store.EndpointRecord
	for _, ep := range scanOf(t, h).Endpoints {
		if ep.Kind == adapterreg.KindSession {
			out = append(out, ep)
		}
	}
	return out
}

// ctlRecords are the control records on the disk.
func ctlRecords(t testing.TB, h *harness.H) []store.CtlRecord { return scanOf(t, h).Ctl }

// sessionLedgerOK fails the test when a judgement of the ledger finds a creation
// attempt that made more than one native session.
func oneSessionPerAttempt(t testing.TB, h *harness.H) {
	t.Helper()
	if err := h.CheckLedger(); err != nil {
		t.Errorf("the ledger is judged: %v", err)
	}
}

