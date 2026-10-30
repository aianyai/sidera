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

func TestPlainBinaryCreatesSessionAndReceivesThroughCLI(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	root := runRoot(t)
	binaries := testBinaries
	binaries.Tagged = binaries.Plain
	f, err := newFixture(ctx, root, binaries, Options{Seed: t.Name(), ClockStart: time.Now().UTC()}, fixtureProfile{mainInstance: "chat", preset: "default", runtimeAdapter: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			t.Error(err)
		}
	}()
	// Reuse the fixture's input packages and process owner. Its tagged Start
	// requires test controls, so this plain run waits on public results instead.
	init, err := f.launch("init", filepath.Join(root, "plain-init.out"), filepath.Join(root, "plain-init.err"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer init.killAndWait()
	if err := init.wait(ctx); err != nil {
		t.Fatal(err)
	}
	p, err := f.launch("daemon", filepath.Join(root, "plain-daemon.out"), filepath.Join(root, "plain-daemon.err"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.killAndWait()
	defer func() {
		if t.Failed() {
			for _, name := range []string{"plain-daemon.out", "plain-daemon.err"} {
				raw, err := os.ReadFile(filepath.Join(root, name))
				t.Logf("%s: %s (%v)", name, raw, err)
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
	}()
	wait := func(ready func() (bool, error)) {
		t.Helper()
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		for {
			ok, err := ready()
			if err != nil {
				t.Fatal(err)
			}
			if ok {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-p.done:
				t.Fatalf("plain daemon exited before its public result: %v", p.err)
			case <-tick.C:
			}
		}
	}
	wait(func() (bool, error) {
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
	wait(func() (bool, error) {
		instances, err := f.instances(ctx)
		if err != nil {
			return false, err
		}
		for _, in := range instances {
			if in.Instance == "chat" && in.Enabled && in.Probe != nil {
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
	e := newEnvironment(ctx, f)
	if _, err := e.Direct("sender"); err != nil {
		t.Fatal(err)
	}
	sender := e.Agent("sender")
	_, err = sender.Control(ctx, fakeagent.ControlInput{To: runtimeAddress(e, "chat"), Signal: adapterproto.SignalSessionCreate, Preset: "default"})
	if err != nil {
		t.Fatal(err)
	}
	var endpoint string
	wait(func() (bool, error) {
		raw, err := f.Manage(ctx, "endpoints.list", manageapi.Request{})
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
				t.Fatal("plain creation exposed more than one open session")
			}
			address, err := ident.ParseAddress(ep.Address)
			if err != nil {
				return false, err
			}
			endpoint = address.Endpoint.String()
		}
		return endpoint != "", nil
	})
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
	if _, err := f.Manage(ctx, "daemon.stop", f.changeRequest(nil, nil)); err != nil {
		t.Fatal(err)
	}
	if err := p.wait(ctx); err != nil {
		t.Fatalf("plain normal exit: %v", err)
	}
}
