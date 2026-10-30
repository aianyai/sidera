package recipes_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
)

type bodySendAgent struct {
	fakeagent.Agent
	replay fakeagent.ReplayState
}

func (a bodySendAgent) Send(_ context.Context, in fakeagent.SendInput) (fakeagent.SendResult, error) {
	return fakeagent.SendResult{MessageID: "00000000000000000000000000000001", To: in.To, Replay: a.replay}, nil
}

type bodySendEnv struct {
	scenario.Env
	agent   fakeagent.Agent
	address string
}

func (e bodySendEnv) Ctx() context.Context         { return context.Background() }
func (e bodySendEnv) Agent(string) fakeagent.Agent { return e.agent }
func (e bodySendEnv) Addr(string) string           { return e.address }

func TestBodyCleanupRequiresAConfirmedFreshSend(t *testing.T) {
	node, err := ident.NodeIDFromBytes(bytes.Repeat([]byte{1}, ident.NodeIDLen))
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := ident.EndpointIDFromBytes(bytes.Repeat([]byte{2}, ident.EndpointIDLen))
	if err != nil {
		t.Fatal(err)
	}
	address := (ident.Address{Node: node, Endpoint: endpoint}).String()
	for _, tc := range []struct {
		name   string
		replay fakeagent.ReplayState
		fresh  bool
	}{
		{"unknown", fakeagent.ReplayUnknown, false},
		{"repeated", fakeagent.ReplayRepeated, false},
		{"fresh", fakeagent.ReplayFresh, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, r := range recipes.New(recipes.Bindings{}).Scripts {
				if r.Script.Name != "recovery-body-feedback" {
					continue
				}
				err := r.Script.Steps[0].Do(bodySendEnv{agent: bodySendAgent{replay: tc.replay}, address: address})
				if tc.fresh && err != nil {
					t.Fatal(err)
				}
				if !tc.fresh && (err == nil || !strings.Contains(err.Error(), "source send did not create the requested delivery")) {
					t.Fatalf("unconfirmed new delivery accepted: %v", err)
				}
				return
			}
			t.Fatal("body cleanup recipe missing")
		})
	}
}
