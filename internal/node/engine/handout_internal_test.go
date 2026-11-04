package engine

import (
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/revocation"
)

// noRulesConfig is the configuration of a node whose policy has no rule at all and
// that holds no credential: it trusts no node, so a message from one is refused by
// the node's rules, while a source on the node itself is never judged by them.
type noRulesConfig struct{ Config }

func (noRulesConfig) Policy() config.PolicyFile      { return config.PolicyFile{Version: 1} }
func (noRulesConfig) RevocationSet() *revocation.Set { return revocation.NewSet() }

// The second check at the hand-out judges a message from this node by the declarations
// of the endpoint alone and a message from any other node by the node's rules, for an
// ordinary message and for the delivery of an initial message derived from a creation
// alike. The record keeps the sender's node and not the origin the message came over;
// that is enough because a remote connection that presents this node's identifier is
// refused at intake, so every accepted record from this node came over the loopback.
func TestTheSecondCheckJudgesASenderOnThisNodeByDeclarationsAndAnyOtherByTheNodeRules(t *testing.T) {
	self, other := nodeOf(t, 7), nodeOf(t, 2)
	runtime := endpointRec(t, 1, adapterreg.KindRuntime, store.EndpointOpen, "claude")
	session := endpointRec(t, 2, adapterreg.KindSession, store.EndpointOpen, "claude")
	v, err := buildView(self, &store.Scan{Endpoints: []store.EndpointRecord{runtime, session}})
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{d: Deps{Node: self, Config: noRulesConfig{}}, trust: &trustState{}, view: v}
	now := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	derivedAuth, err := encodeDerivedAuth("default")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name    string
		sender  ident.NodeID
		derived bool
		want    delivery.AuthorizationKind
	}{
		{"a message from this node", self, false, delivery.AuthorizationAllow},
		{"a message from another node", other, false, delivery.AuthorizationDeny},
		{"an initial message from this node", self, true, delivery.AuthorizationAllow},
		{"an initial message from another node", other, true, delivery.AuthorizationDeny},
	} {
		in := inRec(t, 1, session.ID, delivery.StateAccepted)
		in.Key.SenderNode = c.sender
		if c.derived {
			in.Derived = &store.DerivedInfo{From: runtime.ID, AuthRequest: derivedAuth}
		}
		got, err := e.handoutAuthorization(now, v.endpoints[session.ID], in)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got != c.want {
			t.Errorf("%s: the second check gave %v, want %v", c.name, got, c.want)
		}
	}
}
