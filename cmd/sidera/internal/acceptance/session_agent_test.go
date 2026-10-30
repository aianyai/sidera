//go:build acceptance

package acceptance

import (
	"errors"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/fakeruntime"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func TestBinarySessionBindingRunsSharedRecipeAcrossGeneration(t *testing.T) {
	var recorded []adapterproto.MessageID
	// Identical scenario seeds must reproduce actual CLI identifiers despite
	// different roots and PIDs. Each native session survives daemon replacement.
	for run := range 2 {
		f, ctx := profiledFixture(t, fixtureProfile{mainInstance: "chat", preset: "default", runtimeAdapter: true})
		if err := f.Start(ctx, GenerationSpec{Number: 1}); err != nil {
			t.Fatal(err)
		}
		e := newEnvironment(ctx, f)
		b := recipes.Bindings{LocalNode: func(e scenario.Env) (ident.NodeID, error) { return envFrom(e).localNode() }, Sessions: newSessionOps()}
		var recipe recipes.ScriptRecipe
		for _, candidate := range recipes.New(b).Scripts {
			if candidate.Script.Name == "delivery-register-declare" {
				recipe = candidate
			}
		}
		if recipe.Script.Name == "" {
			t.Fatal("shared registration recipe absent")
		}
		if err := scenario.Run(e, recipe.Script); err != nil {
			t.Fatal(err)
		}
		if err := recipe.Drain(e); err != nil {
			t.Fatal(err)
		}
		if err := recipe.Check(e); err != nil {
			t.Fatal(err)
		}
		if len(e.sessions) != 1 {
			t.Fatalf("shared recipe bound %d sessions", len(e.sessions))
		}
		var endpoint string
		for endpoint = range e.sessions {
		}
		a := b.Sessions.Session(e, endpoint)
		facts := recipes.NewDeliveryEvidence(b.LocalNode)
		var ids []adapterproto.MessageID
		for generation := 1; generation <= 2; generation++ {
			// The same retained Agent must perform actual CLI sends after daemon
			// replacement, with identifiers supplied by the daemon's responses.
			sent, err := a.Send(ctx, fakeagent.SendInput{To: []string{e.Addr("sender")}, Body: "The native session sends through its current CLI environment."})
			if err != nil {
				t.Fatal(err)
			}
			if len(ids) != 0 && sent.MessageID == ids[0] {
				t.Fatal("daemon replacement repeated a CLI message identifier")
			}
			ids = append(ids, sent.MessageID)
			facts.Accepted(sent, "The native session sends through its current CLI environment.")
			if err := e.Quiesce(); err != nil {
				t.Fatal(err)
			}
			if err := facts.RecvAndAck(e, e.Agent("sender")); err != nil {
				t.Fatal(err)
			}
			me, err := a.Me(ctx)
			if err != nil {
				t.Fatal(err)
			}
			received, err := e.Agent("sender").Send(ctx, fakeagent.SendInput{To: []string{me.Address}, Body: "Read this actual CLI page before acknowledging."})
			if err != nil {
				t.Fatal(err)
			}
			facts.Accepted(received, "Read this actual CLI page before acknowledging.")
			if err := e.Quiesce(); err != nil {
				t.Fatal(err)
			}
			if err := facts.RecvAndAck(e, a); err != nil {
				t.Fatal(err)
			}
			if err := facts.Check(e); err != nil {
				t.Fatal(err)
			}
			if generation == 1 {
				if err := f.Kill(ctx); err != nil {
					t.Fatal(err)
				}
				if _, err := f.ReadTrace(1); err != nil {
					t.Fatal(err)
				}
				e.Advance(time.Minute)
				failure := e.Quiesce()
				if failure == nil {
					t.Fatal("killed daemon accepted a clock RPC")
				}
				assertSessionAgentFailure(t, e, a, failure)
				if err := f.Start(ctx, GenerationSpec{Number: 2}); err != nil {
					t.Fatal(err)
				}
				if _, err := a.Me(ctx); err != failure {
					t.Fatalf("replacement cleared generation failure: %v", err)
				}
				if err := e.resetGeneration(ctx); err != nil {
					t.Fatal(err)
				}
				if err := recipe.Check(e); err != nil {
					t.Fatal(err)
				}
			}
		}
		if _, err := b.Sessions.Session(e, "missing-session").Me(ctx); !errors.Is(err, fakeruntime.ErrUnknownSession) {
			t.Fatalf("unknown session did not fail ownership lookup: %v", err)
		}
		ref, err := e.sessionReference(endpoint)
		if err != nil {
			t.Fatal(err)
		}
		runtime, err := e.Runtime()
		if err != nil {
			t.Fatal(err)
		}
		if err := runtime.KillNative(ref); err != nil {
			t.Fatal(err)
		}
		if _, err := a.Me(ctx); !errors.Is(err, fakeruntime.ErrUnknownSession) && !errors.Is(err, fakeruntime.ErrNotAlive) {
			t.Fatalf("exited native owner did not fail session lookup: %v", err)
		}
		if run == 0 {
			recorded = ids
		} else {
			for i := range ids {
				if ids[i] != recorded[i] {
					t.Fatalf("actual CLI message %d changed on deterministic replay", i)
				}
			}
		}
		if err := f.Kill(ctx); err != nil {
			t.Fatal(err)
		}
	}
}

func assertSessionAgentFailure(t *testing.T, e scenario.Env, a fakeagent.Agent, failure error) {
	t.Helper()
	for name, call := range map[string]func() error{
		"me":        func() error { _, err := a.Me(e.Ctx()); return err },
		"peers":     func() error { _, err := a.Peers(e.Ctx()); return err },
		"directory": func() error { _, err := a.Directory(e.Ctx(), fakeagent.DirectoryInput{}); return err },
		"probe":     func() error { _, err := a.Probe(e.Ctx(), fakeagent.ProbeInput{}); return err },
		"send":      func() error { _, err := a.Send(e.Ctx(), fakeagent.SendInput{}); return err },
		"reply":     func() error { _, err := a.Reply(e.Ctx(), fakeagent.ReplyInput{}); return err },
		"recv":      func() error { _, err := a.Recv(e.Ctx(), fakeagent.RecvInput{}); return err },
		"ack":       func() error { _, err := a.Ack(e.Ctx(), fakeagent.AckInput{}); return err },
		"control":   func() error { _, err := a.Control(e.Ctx(), fakeagent.ControlInput{}); return err },
		"declare":   func() error { return a.Declare(e.Ctx(), fakeagent.DeclareInput{}) },
		"status":    func() error { _, err := a.Status(e.Ctx(), fakeagent.StatusInput{}); return err },
	} {
		if err := call(); err != failure {
			t.Errorf("session %s lost retained generation failure: %v", name, err)
		}
	}
}
