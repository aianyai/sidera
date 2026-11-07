//go:build acceptance && netacceptance

package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// Two real daemons of this machine, each with its own home and port, pair with
// each other over libp2p as two owners would, then carry a message there and back
// over the trust the pairing made, show each other to an agent, and part again. A node's clock is a test clock that stands still, so a pull the engine would
// make a minute from now is made by moving that clock; every other wait is a poll
// with a deadline.

const networkWait = 30 * time.Second

// pullInterval is how far a node's clock is moved to make it pull the reports of
// the messages it sent; it is the engine's interval and a little more.
const pullInterval = time.Minute + time.Second

var networkBuild struct {
	once sync.Once
	path string
	err  error
}

// networkBinary is the node program with the test clock, built once.
func networkBinary(t *testing.T) string {
	t.Helper()
	networkBuild.once.Do(func() {
		ext := ""
		if runtime.GOOS == "windows" {
			ext = ".exe"
		}
		networkBuild.path = filepath.Join(testRoot, "net"+ext)
		args := []string{"build", "-tags", "testclock", "-o", networkBuild.path}
		if raceEnabled {
			args = append(args, "-race")
		}
		if out, err := exec.Command("go", append(args, "github.com/aianyai/sidera/cmd/sidera")...).CombinedOutput(); err != nil {
			networkBuild.err = fmt.Errorf("build: %w: %s", err, out)
		}
	})
	if networkBuild.err != nil {
		t.Fatal(networkBuild.err)
	}
	return networkBuild.path
}

// until polls ready every 20 ms until it holds, failing the test at the deadline.
func until(ctx context.Context, t *testing.T, what string, ready func() (bool, error)) {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, networkWait)
	defer cancel()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		ok, err := ready()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		if ok {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("%s: not within %s", what, networkWait)
		case <-tick.C:
		}
	}
}

// netNode is one daemon of the test with its endpoint.
type netNode struct {
	*plainNode
	env      *environment
	id       string
	address  string // of its endpoint
	endpoint string // name of the endpoint in env
}

// startNetNode starts a daemon whose direct endpoint is called name. With runtime
// the node also has an adapter that starts sessions, so that the test can call the
// agent methods a direct endpoint has no access to (`me`, `probe` and the like).
func startNetNode(ctx context.Context, t *testing.T, name string, runtime bool) *netNode {
	t.Helper()
	binaries := testBinaries
	binaries.Tagged, binaries.Plain = networkBinary(t), networkBinary(t)
	p := startPlainNode(ctx, t, runRoot(t), binaries, Options{Seed: t.Name() + "/" + name, ClockStart: time.Now().UTC()}, fixtureProfile{mainInstance: "chat", preset: "default", runtimeAdapter: runtime})
	n := &netNode{plainNode: p, env: newEnvironment(ctx, p.f), endpoint: name}
	t.Logf("node %s: daemon pid %d", name, p.p.native.Pid)
	n.id = n.contactLine()[0]
	var err error
	if n.address, err = n.env.Direct(name); err != nil {
		t.Fatal(err)
	}
	return n
}

// cli runs a command of the program against this node's home and decodes its JSON.
func (n *netNode) cli(into any, args ...string) {
	n.t.Helper()
	cmd := exec.CommandContext(n.ctx, n.f.binaries.Tagged, append([]string{"--home", n.f.home}, args...)...)
	cmd.Dir = n.f.root
	var out, diag bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &diag
	if err := cmd.Run(); err != nil {
		n.t.Fatalf("%v: %v: %s %s", args, err, &out, &diag)
	}
	if err := json.Unmarshal(out.Bytes(), into); err != nil {
		n.t.Fatalf("%v: %v: %s", args, err, &out)
	}
}

// contactLine is what the node's owner gives another owner: the node id, then the
// addresses of the node that another machine can reach.
func (n *netNode) contactLine() []string {
	n.t.Helper()
	var shown struct {
		Result struct {
			Contact string `json:"contact"`
		} `json:"result"`
	}
	n.cli(&shown, "network", "show", "--json")
	fields := strings.Fields(shown.Result.Contact)
	if len(fields) < 2 {
		n.t.Fatalf("node %s has no address to give: contact line %q", n.endpoint, shown.Result.Contact)
	}
	return fields
}

// trusted is the credential this node holds with each node it trusts, by node.
func (n *netNode) trusted() map[string]string {
	n.t.Helper()
	var listed struct {
		Result struct {
			Trusted []struct {
				Node       string `json:"node"`
				Credential string `json:"credential"`
			} `json:"trusted"`
		} `json:"result"`
	}
	n.cli(&listed, "trust", "list", "--json")
	held := map[string]string{}
	for _, t := range listed.Result.Trusted {
		held[t.Node] = t.Credential
	}
	return held
}

// state is what this node's source record says of a message it sent.
func (n *netNode) state(id adapterproto.MessageID) adapterproto.DeliveryState {
	n.t.Helper()
	res, err := n.env.Agent(n.endpoint).Status(n.ctx, fakeagent.StatusInput{MessageID: id})
	if err != nil {
		n.t.Fatal(err)
	}
	if len(res.Recipients) != 1 {
		n.t.Fatalf("status of %s: %+v", id, res)
	}
	return res.Recipients[0].State
}

// awaitState waits for the message to be in want, and fails at once when it ended
// in another state.
func (n *netNode) awaitState(id adapterproto.MessageID, want adapterproto.DeliveryState) {
	n.t.Helper()
	until(n.ctx, n.t, fmt.Sprintf("message %s is %s", id, want), func() (bool, error) {
		switch got := n.state(id); {
		case got == want:
			return true, nil
		case got == adapterproto.DeliveryRejected || got == adapterproto.DeliveryUnknown ||
			got == adapterproto.DeliveryExpiredUnconfirmed || got == adapterproto.DeliveryClosedUnconfirmed:
			return false, fmt.Errorf("the message is %s", got)
		default:
			return false, nil
		}
	})
}

// awaitReport waits for the report of the message to be pulled back: the node's
// clock is moved past the pull interval, and again after a second if the pull was
// not yet scheduled, until the message is delivered.
func (n *netNode) awaitReport(id adapterproto.MessageID) {
	n.t.Helper()
	moves := 0
	last := time.Now()
	until(n.ctx, n.t, fmt.Sprintf("the report of %s is pulled back", id), func() (bool, error) {
		if n.state(id) == adapterproto.DeliveryDelivered {
			return true, nil
		}
		if moves < 3 && time.Since(last) > time.Second {
			moves++
			last = time.Now()
			if err := n.f.SetTime(n.ctx, n.f.Now().Add(pullInterval)); err != nil {
				return false, err
			}
		}
		return false, nil
	})
}

// awaitUnreachableNotice waits for the node's notice to the endpoint that the
// message cannot reach its target. The node makes it after two minutes of failed
// contact on its own clock, which stands still, so the clock is moved past a
// minute at a time, at most five times and so never past the message's deadline of
// ten minutes.
func (n *netNode) awaitUnreachableNotice(id adapterproto.MessageID) {
	n.t.Helper()
	agent := n.env.Agent(n.endpoint)
	moves := 0
	last := time.Now()
	until(n.ctx, n.t, fmt.Sprintf("the notice that %s is unreachable", id), func() (bool, error) {
		page, err := agent.Recv(n.ctx, fakeagent.RecvInput{})
		if err != nil {
			return false, err
		}
		if len(page.Page.Messages) > 0 {
			if _, err := agent.Ack(n.ctx, fakeagent.AckInput{Lease: page.Page.Lease}); err != nil {
				return false, err
			}
			for _, m := range page.Page.Messages {
				if strings.Contains(m.Body, "unreachable: the target is not reachable") && strings.Contains(m.Body, string(id)) {
					return true, nil
				}
			}
		}
		if moves < 5 && time.Since(last) > time.Second {
			moves++
			last = time.Now()
			if err := n.f.SetTime(n.ctx, n.f.Now().Add(pullInterval)); err != nil {
				return false, err
			}
		}
		return false, nil
	})
}

// receive waits for the endpoint's next message, acknowledges it as completed and
// returns it.
func (n *netNode) receive(body string) adapterproto.MessageID {
	n.t.Helper()
	agent := n.env.Agent(n.endpoint)
	var got adapterproto.MessageID
	until(n.ctx, n.t, "a message arrives", func() (bool, error) {
		page, err := agent.Recv(n.ctx, fakeagent.RecvInput{})
		if err != nil || len(page.Page.Messages) == 0 {
			return false, err
		}
		if len(page.Page.Messages) != 1 || page.Page.Messages[0].Body != body {
			return false, fmt.Errorf("received %+v, want one message %q", page.Page.Messages, body)
		}
		got = page.Page.Messages[0].ID
		if _, err := agent.Ack(n.ctx, fakeagent.AckInput{Lease: page.Page.Lease}); err != nil {
			return false, err
		}
		return true, nil
	})
	return got
}

func TestTwoNodesOfOneMachineExchangeMessagesOverTheNetwork(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	begun := time.Now()
	a := startNetNode(ctx, t, "alice", true)
	b := startNetNode(ctx, t, "bob", false)
	t.Logf("both nodes serve after %s", time.Since(begun).Round(time.Millisecond))

	// The owners pair the nodes as they would: each gives its node the other's
	// contact line and the expectation of the other's request, then one asks the
	// other, which confirms in advance by the expectation, and waits for the pairing
	// to complete.
	for _, pair := range [][2]*netNode{{a, b}, {b, a}} {
		self, other := pair[0], pair[1]
		contact := other.contactLine()
		var set, expected struct{}
		self.cli(&set, append([]string{"network", "address", "set", contact[0]}, append(contact[1:], "--json")...)...)
		self.cli(&expected, "trust", "expect", contact[0], "--json")
	}
	var requested struct{}
	a.cli(&requested, "trust", "request", b.id, "--wait", "10s", "--json")
	heldByA, heldByB := a.trusted(), b.trusted()
	if heldByA[b.id] == "" || heldByA[b.id] != heldByB[a.id] || len(heldByA) != 1 || len(heldByB) != 1 {
		t.Fatalf("the nodes trust %v and %v: want one credential, the same on both", heldByA, heldByB)
	}

	send := func(from, to *netNode, body string) adapterproto.MessageID {
		sent, err := from.env.Agent(from.endpoint).Send(ctx, fakeagent.SendInput{To: []string{to.address}, Body: body})
		if err != nil {
			t.Fatal(err)
		}
		return sent.MessageID
	}

	began := time.Now()
	first := send(a, b, "hello bob")
	a.awaitState(first, adapterproto.DeliveryAccepted)
	t.Logf("accepted by the other node after %s", time.Since(began).Round(time.Millisecond))
	received := b.receive("hello bob")
	a.awaitReport(first)
	t.Logf("report pulled back after %s", time.Since(began).Round(time.Millisecond))

	reply, err := b.env.Agent(b.endpoint).Reply(ctx, fakeagent.ReplyInput{MessageID: received, Body: "hello alice"})
	if err != nil {
		t.Fatal(err)
	}
	b.awaitState(reply.MessageID, adapterproto.DeliveryAccepted)
	a.receive("hello alice")
	b.awaitReport(reply.MessageID)

	var shown struct {
		Result struct {
			Peers []struct {
				Node            string `json:"node"`
				CertificateHeld bool   `json:"certificate_held"`
				Connected       bool   `json:"connected"`
			} `json:"peers"`
		} `json:"result"`
	}
	for _, pair := range [][2]*netNode{{a, b}, {b, a}} {
		pair[0].cli(&shown, "network", "show", "--json")
		if peers := shown.Result.Peers; len(peers) != 1 || peers[0].Node != pair[1].id || !peers[0].CertificateHeld || !peers[0].Connected {
			t.Errorf("node %s sees its peers as %+v", pair[0].endpoint, peers)
		}
	}
	t.Logf("whole exchange took %s", time.Since(began).Round(time.Millisecond))

	// Alice's session reads what the node shows an agent of the nodes around it: the
	// node itself and the one it trusts, connected, and the endpoints the other
	// node lets it see.
	session, err := sessionAgentFor(a.env, a.createSession(a.env, a.env.Agent(a.endpoint)))
	if err != nil {
		t.Fatal(err)
	}
	peers, err := session.Peers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wantPeers := []fakeagent.PeerNode{{Node: a.id, Self: true, Trust: "self", Connected: true}, {Node: b.id, Trust: "direct", Connected: true}}
	if len(peers.Nodes) > 0 && peers.Nodes[0].Profile == nil {
		t.Errorf("the node itself has no profile: %+v", peers.Nodes[0])
	}
	if !slices.Equal(withoutProfiles(peers.Nodes), wantPeers) {
		t.Errorf("peers = %+v, want %+v", peers.Nodes, wantPeers)
	}
	directory, err := session.Directory(ctx, fakeagent.DirectoryInput{Node: b.id})
	if err != nil {
		t.Fatal(err)
	}
	if len(directory.Endpoints) != 1 || directory.Endpoints[0].Address != b.address || directory.NextCursor != "" {
		t.Errorf("directory of bob = %+v, want its one endpoint %s", directory, b.address)
	}

	// Alice removes bob from its trust, and bob is not told. A message to it then
	// cannot be delivered: it stays queued while alice keeps trying until its
	// deadline, and after two minutes of failed contact alice's endpoint is told that
	// the target is not reachable. A directory read of bob is unavailable at once.
	var removed struct{}
	a.cli(&removed, "trust", "remove", b.id, "--json")
	lost := send(a, b, "hello again bob")
	a.awaitUnreachableNotice(lost)
	if peers, err := session.Peers(ctx); err != nil || !slices.Equal(withoutProfiles(peers.Nodes), wantPeers[:1]) {
		t.Errorf("peers after the removal = %+v, %v, want only the node itself", peers.Nodes, err)
	}
	var refused *fakeagent.Error
	if _, err := session.Directory(ctx, fakeagent.DirectoryInput{Node: b.id}); !errors.As(err, &refused) || refused.Code != "directory_unavailable" {
		t.Errorf("directory of the node removed from trust: %v, want directory_unavailable", err)
	}
	if got := a.state(lost); got != adapterproto.DeliveryQueued {
		t.Errorf("the message to the node removed from trust is %s, want queued while it is retried", got)
	}
	for _, n := range []*netNode{a, b} {
		n.stop()
	}
}

// withoutProfiles is the nodes with what they say about themselves left out: it is
// a fact of the clock and of the machine a test runs on, and a node shows it only once
// its profile has been fetched.
func withoutProfiles(nodes []fakeagent.PeerNode) []fakeagent.PeerNode {
	out := slices.Clone(nodes)
	for i := range out {
		out[i].Profile = nil
	}
	return out
}
