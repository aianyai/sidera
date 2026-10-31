//go:build acceptance

package acceptance

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/manageapi"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// plainNode is a node started the way a user starts one: the fixture supplies its
// packages and its process owner, but the daemon is a program whose test controls
// the fixture cannot rely on, so readiness is judged by public results only.
type plainNode struct {
	t   *testing.T
	ctx context.Context
	f   *Fixture
	p   *process
}

// startPlainNode initializes a home under root, starts its daemon with binaries.Tagged
// as the node program, and returns when the instance of the profile has passed its
// probe. The daemon is stopped, and its output logged when the test failed, when the
// test ends.
func startPlainNode(ctx context.Context, t *testing.T, root string, binaries Binaries, opts Options, profile fixtureProfile) *plainNode {
	t.Helper()
	f, err := newFixture(ctx, root, binaries, opts, profile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.Close(); err != nil {
			t.Error(err)
		}
	})
	init, err := f.launch("init", filepath.Join(root, "plain-init.out"), filepath.Join(root, "plain-init.err"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer init.killAndWait()
	if err := init.wait(ctx); err != nil {
		t.Fatal(err)
	}
	g, err := f.begin(GenerationSpec{Number: 1}, "daemon")
	if err != nil {
		t.Fatal(err)
	}
	f.current = g
	n := &plainNode{t: t, ctx: ctx, f: f, p: g.p}
	t.Cleanup(func() { _, _ = n.p.killAndWait() })
	t.Cleanup(func() {
		if t.Failed() {
			for _, path := range []string{g.stdout, g.stderr, filepath.Join(f.home, "logs", "daemon.log")} {
				raw, err := os.ReadFile(path)
				t.Logf("%s: %s (%v)", filepath.Base(path), raw, err)
			}
			log, err := f.Ledger()
			if err != nil {
				t.Logf("plain ledger: %v", err)
			} else {
				for _, kind := range []string{ledger.KindSessionCreateReceived, ledger.KindRuntimeSessionCreated, ledger.KindNativeCreateReturned} {
					t.Logf("plain ledger %s: %d", kind, len(log.Kind(kind)))
				}
			}
		}
	})
	n.wait(func() (bool, error) {
		raw, err := f.Manage(ctx, "status", manageapi.Request{})
		if err != nil {
			// The public socket need not exist while the process is starting.
			return false, nil
		}
		var status struct {
			Daemon struct {
				State string `json:"state"`
			} `json:"daemon"`
		}
		err = resultBody(raw, &status)
		return status.Daemon.State == "running", err
	})
	if err := f.bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	n.wait(func() (bool, error) {
		instances, err := f.instances(ctx)
		if err != nil {
			return false, err
		}
		for _, in := range instances {
			if in.Instance == profile.instance() && in.Enabled && in.Probe != nil {
				if in.Probe.State != "pending" && in.Probe.State != "passed" {
					t.Fatalf("plain adapter probe: %s", in.Probe.State)
				}
				return in.Probe.State == "passed", nil
			}
		}
		return false, nil
	})
	if err := f.bindRuntimeAddresses(ctx); err != nil {
		t.Fatal(err)
	}
	return n
}

// wait polls ready until it holds, the test's context ends or the daemon exits.
func (n *plainNode) wait(ready func() (bool, error)) {
	n.t.Helper()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		ok, err := ready()
		if err != nil {
			n.t.Fatal(err)
		}
		if ok {
			return
		}
		select {
		case <-n.ctx.Done():
			n.t.Fatal(n.ctx.Err())
		case <-n.p.done:
			n.t.Fatalf("plain daemon exited before its public result: %v", n.p.err)
		case <-tick.C:
		}
	}
}

// stop asks the daemon to stop through its management interface and waits for its
// normal exit.
func (n *plainNode) stop() {
	n.t.Helper()
	if _, err := n.f.Manage(n.ctx, "daemon.stop", n.f.changeRequest(nil, nil)); err != nil {
		n.t.Fatal(err)
	}
	if err := n.p.wait(n.ctx); err != nil {
		n.t.Fatalf("plain normal exit: %v", err)
	}
}

// createSession has the node's runtime adapter start a session at the request of
// sender, a direct endpoint of the node, and waits until the node lists exactly one
// open session of the instance. It returns the endpoint of that session.
func (n *plainNode) createSession(e *environment, sender fakeagent.Agent) string {
	n.t.Helper()
	_, err := sender.Control(n.ctx, fakeagent.ControlInput{To: runtimeAddress(e, "chat"), Signal: adapterproto.SignalSessionCreate, Preset: "default"})
	if err != nil {
		n.t.Fatal(err)
	}
	var endpoint string
	n.wait(func() (bool, error) {
		raw, err := n.f.Manage(n.ctx, "endpoints.list", manageapi.Request{})
		if err != nil {
			return false, err
		}
		var listed struct {
			Endpoints []struct{ Address, Kind, Instance, State string } `json:"endpoints"`
		}
		if err := resultBody(raw, &listed); err != nil {
			return false, err
		}
		for _, ep := range listed.Endpoints {
			if ep.Kind != "session" || ep.Instance != "chat" || ep.State != "open" {
				continue
			}
			if endpoint != "" {
				n.t.Fatal("plain creation exposed more than one open session")
			}
			address, err := ident.ParseAddress(ep.Address)
			if err != nil {
				return false, err
			}
			endpoint = address.Endpoint.String()
		}
		return endpoint != "", nil
	})
	return endpoint
}

func TestPlainBinaryCreatesSessionAndReceivesThroughCLI(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	binaries := testBinaries
	binaries.Tagged = binaries.Plain
	n := startPlainNode(ctx, t, runRoot(t), binaries, Options{Seed: t.Name(), ClockStart: time.Now().UTC()}, fixtureProfile{mainInstance: "chat", preset: "default", runtimeAdapter: true})
	f, wait := n.f, n.wait
	e := newEnvironment(ctx, f)
	if _, err := e.Direct("sender"); err != nil {
		t.Fatal(err)
	}
	sender := e.Agent("sender")
	endpoint := n.createSession(e, sender)
	a := sessionAgentFor(e, endpoint)
	me, err := a.Me(ctx)
	if err != nil || me.Kind != "session" {
		t.Fatalf("plain CLI session identity: %+v %v", me, err)
	}
	const body = "A plain daemon delivers this actual body to its plain CLI."
	sent, err := sender.Send(ctx, fakeagent.SendInput{To: []string{me.Address}, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	var received fakeagent.RecvResult
	wait(func() (bool, error) {
		var err error
		received, err = a.Recv(ctx, fakeagent.RecvInput{})
		return len(received.Page.Messages) != 0, err
	})
	if len(received.Page.Messages) != 1 || received.Page.Messages[0].ID != sent.MessageID || received.Page.Messages[0].Body != body {
		t.Fatalf("plain CLI body: %+v", received.Page)
	}
	acked, err := a.Ack(ctx, fakeagent.AckInput{Lease: received.Page.Lease})
	if err != nil || len(acked.Items) != 1 || acked.Items[0].MessageID != sent.MessageID || acked.Items[0].Ack != adapterproto.ItemCompleted {
		t.Fatalf("plain CLI acknowledgement: %+v %v", acked, err)
	}
	// Ack's completed result and the empty public receive prove local delivery.
	// Source status is refreshed by the ordinary one-minute pull schedule.
	empty, err := a.Recv(ctx, fakeagent.RecvInput{})
	if err != nil || len(empty.Page.Messages) != 0 {
		t.Fatalf("plain CLI after acknowledgement: %+v %v", empty.Page, err)
	}
	n.stop()
}
