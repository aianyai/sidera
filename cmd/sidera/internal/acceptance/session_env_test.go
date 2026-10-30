//go:build acceptance

package acceptance

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	cliagent "github.com/aianyai/sidera/cmd/sidera/internal/acceptance/agent/cli"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/manageapi"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func TestSessionEnvDrivesRealCLIAcrossDaemonGeneration(t *testing.T) {
	f, ctx := profiledFixture(t, fixtureProfile{mainInstance: "chat", preset: "default", runtimeAdapter: true})
	if err := f.Start(ctx, GenerationSpec{Number: 1}); err != nil {
		t.Fatal(err)
	}
	e := newEnvironment(ctx, f)
	if _, err := e.Direct("sender"); err != nil {
		t.Fatal(err)
	}
	raw, err := f.Manage(ctx, "endpoints.list", manageapi.Request{})
	if err != nil {
		t.Fatal(err)
	}
	var listed struct {
		Endpoints []struct{ Address, Kind, Instance, State string } `json:"endpoints"`
	}
	if err := resultBody(raw, &listed); err != nil {
		t.Fatal(err)
	}
	var runtime string
	for _, ep := range listed.Endpoints {
		if ep.Kind == "runtime" && ep.Instance == "chat" && ep.State == "open" {
			runtime = ep.Address
		}
	}
	if runtime == "" {
		t.Fatal("no public runtime endpoint")
	}
	sender := e.Agent("sender")
	created, err := sender.Control(ctx, fakeagent.ControlInput{To: runtime, Signal: adapterproto.SignalSessionCreate, Preset: "default"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Quiesce(); err != nil {
		t.Fatal(err)
	}
	e.Advance(time.Minute)
	if err := e.Quiesce(); err != nil {
		t.Fatal(err)
	}
	status, err := sender.Status(ctx, fakeagent.StatusInput{MessageID: created.MessageID})
	if err != nil || status.ControlReply == nil || status.ControlReply.Create == nil || status.ControlReply.Create.Outcome != adapterproto.CreateReplyCreated {
		t.Fatal("native session creation did not succeed")
	}
	address, err := ident.ParseAddress(runtime)
	if err != nil {
		t.Fatal(err)
	}
	address.Endpoint, err = ident.ParseEndpointID(string(status.ControlReply.Create.SessionEndpoint))
	if err != nil {
		t.Fatal(err)
	}
	log, err := f.Ledger()
	if err != nil {
		t.Fatal(err)
	}
	natives := log.Kind(ledger.KindRuntimeSessionCreated)
	if len(natives) != 1 {
		t.Fatal("expected one actual native session")
	}
	ref := natives[0].Attrs.NativeRef
	var token string
	for generation := 1; generation <= 2; generation++ {
		// Read on every generation; never derive credentials from node state or
		// retain an old CLI Agent across replacement.
		env, err := f.Runtime().SessionEnv(ref)
		if err != nil || env["SIDERA_TOKEN"] == "" || env["SIDERA_HOME"] != f.home {
			t.Fatal("actual runtime did not return the injected tool environment")
		}
		token = env["SIDERA_TOKEN"]
		agent := cliagent.New(cliagent.Config{Binary: f.binaries.Plain, Home: env["SIDERA_HOME"], Dir: f.root, Token: token})
		me, err := agent.Me(ctx)
		if err != nil || me.Address != address.String() || me.Kind != "session" {
			t.Fatal("real CLI Me did not authenticate the created session")
		}
		hidden := generation == 1
		if err := agent.Declare(ctx, fakeagent.DeclareInput{HiddenFromDirectory: &hidden}); err != nil {
			t.Fatal("real CLI declaration failed")
		}
		me, err = agent.Me(ctx)
		if err != nil || me.Declaration.HiddenFromDirectory != hidden {
			t.Fatal("real CLI declaration was not visible")
		}
		sent, err := sender.Send(ctx, fakeagent.SendInput{To: []string{address.String()}, Body: "Read and acknowledge through the actual session CLI."})
		if err != nil {
			t.Fatal(err)
		}
		if err := e.Quiesce(); err != nil {
			t.Fatal(err)
		}
		recv, err := agent.Recv(ctx, fakeagent.RecvInput{Frontend: adapterproto.FrontendCLI})
		if err != nil || len(recv.Page.Messages) != 1 || recv.Page.Messages[0].ID != sent.MessageID {
			t.Fatal("real CLI did not receive the session message")
		}
		if bytes.Contains(recv.Raw, []byte(token)) {
			t.Fatal("session credential reached CLI output")
		}
		ack, err := agent.Ack(ctx, fakeagent.AckInput{Lease: recv.Page.Lease, Items: []adapterproto.PartRef{{MessageID: sent.MessageID, Part: 1}}})
		if err != nil || len(ack.Items) != 1 || ack.Items[0].Ack != adapterproto.ItemCompleted {
			t.Fatal("real CLI acknowledgement failed")
		}
		if err := e.Quiesce(); err != nil {
			t.Fatal(err)
		}
		if generation == 1 {
			if err := f.Kill(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := f.ReadTrace(1); err != nil {
				t.Fatal(err)
			}
			if err := f.Start(ctx, GenerationSpec{Number: 2}); err != nil {
				t.Fatal(err)
			}
			if err := e.resetGeneration(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Scan public outputs, static inputs, runtime records and diagnostics. Node
	// business records hold credentials by design and are not getter outputs.
	paths := []string{filepath.Join(f.root, "l"), filepath.Join(f.home, "logs")}
	for _, in := range f.inputs {
		input, err := json.Marshal(in.process)
		if err != nil || bytes.Contains(input, []byte(token)) {
			t.Fatal("session credential reached static process input")
		}
		paths = append(paths, in.process.RuntimeDir)
	}
	for _, gen := range f.gens {
		paths = append(paths, gen.stdout, gen.stderr, gen.spec.Record)
	}
	for _, path := range paths {
		err := filepath.WalkDir(path, func(name string, entry fs.DirEntry, err error) error {
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			raw, err := os.ReadFile(name)
			if err != nil {
				return err
			}
			if bytes.Contains(raw, []byte(token)) {
				t.Fatal("session credential reached a recordable output")
			}
			return nil
		})
		if err != nil {
			t.Fatal("cannot inspect recordable output")
		}
	}
}
