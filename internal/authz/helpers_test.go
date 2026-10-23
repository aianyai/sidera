package authz

import (
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/ident"
)

func node(i byte) ident.NodeID {
	b := make([]byte, ident.NodeIDLen)
	b[0] = i
	id, err := ident.NodeIDFromBytes(b)
	if err != nil {
		panic(err)
	}
	return id
}

func ep(i byte) ident.EndpointID {
	b := make([]byte, ident.EndpointIDLen)
	b[0] = i
	id, err := ident.EndpointIDFromBytes(b)
	if err != nil {
		panic(err)
	}
	return id
}

func addr(n, e byte) ident.Address { return ident.Address{Node: node(n), Endpoint: ep(e)} }

var t0 = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

func mustPolicy(t testing.TB, rules ...Rule) Policy {
	t.Helper()
	p, err := NewPolicy(rules)
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}
	return p
}

var (
	allowed   = ConnectionTrust{State: TrustAllowed, Direct: true}
	inherited = ConnectionTrust{State: TrustAllowed, Direct: false}
	suspended = ConnectionTrust{State: TrustSuspended}
)

// send builds a message.send request from node 1 (endpoint 1) to local
// endpoint 10 under allowed direct trust, checked at intake; tests override
// fields.
func send() Request {
	return Request{
		Now:    t0,
		Trust:  allowed,
		Stage:  StageIntake,
		Sender: Sender{Node: node(1), Endpoint: ep(1)},
		Action: ActionMessageSend,
		Target: Target{Kind: TargetEndpoint, Endpoint: ep(10)},
	}
}

func create(preset string) Request {
	r := send()
	r.Action = ActionSessionCreate
	r.Target = Target{Kind: TargetRuntime, Endpoint: ep(20)}
	r.Preset = preset
	return r
}

func relay() Request {
	r := send()
	r.Action = ActionRelayReserve
	r.Sender.Endpoint = ident.EndpointID{}
	r.Target = Target{Kind: TargetNode}
	return r
}

func with(r Request, f func(*Request)) Request {
	f(&r)
	return r
}
