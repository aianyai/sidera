package config_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/config"
)

const (
	peerAText = "12D3KooWPjceQrSwdWXPyLLeABRXmuqt69Rg3sBYbU1Nft9HyQ6X"
	peerBText = "12D3KooWH3uVF6wv47WnArKHk5p6cvgCJEb74UTmxztmQDc298L3"

	goldenNetwork = `{
  "format": 1,
  "version": 2,
  "listen": [
    "/ip4/0.0.0.0/tcp/7744",
    "/ip4/0.0.0.0/udp/7744/quic-v1"
  ],
  "peers": {
    "` + node2Text + `": [
      "/ip4/192.168.1.20/tcp/7744/p2p/` + peerAText + `",
      "/dns4/node2.example.org/udp/7744/quic-v1"
    ]
  },
  "last_operation": {
    "id": "` + opIDText + `",
    "person": "local",
    "fingerprint": "` + fingerHex + `",
    "result": {
      "version": 2
    }
  }
}
`

	initNetwork = `{
  "format": 1,
  "version": 1,
  "listen": [
    "/ip4/0.0.0.0/tcp/7744",
    "/ip4/0.0.0.0/udp/7744/quic-v1"
  ],
  "peers": {}
}
`
)

// What a listen address and an address of a peer may be: one definition, used by
// the file decoder and by every change.
func TestAddressRules(t *testing.T) {
	long := "/dns/" + strings.Repeat("a", 245) + "/tcp/1"
	rows := []struct {
		addr   string
		listen bool // accepted as a listen address
		peer   bool // accepted as the address of a peer
	}{
		{"/ip4/0.0.0.0/tcp/7744", true, true},
		{"/ip4/0.0.0.0/udp/7744/quic-v1", true, true},
		{"/ip6/::/tcp/7744", true, true},
		{"/ip6/0:0:0:0:0:0:0:1/udp/7744/quic-v1", true, true},
		{"/dns/example.org/tcp/7744", true, true},
		{"/dns4/example.org/tcp/7744", true, true},
		{"/dns6/example.org/udp/7744/quic-v1", true, true},
		{"/ip4/192.168.1.20/tcp/7744/p2p/" + peerAText, false, true},
		{"/dns4/example.org/udp/7744/quic-v1/p2p/" + peerBText, false, true},

		{"", false, false},
		{"not an address", false, false},
		{"/ip4/0.0.0.0", false, false},
		{"/ip4/0.0.0.0/tcp", false, false},
		{"/ip4/0.0.0.0/tcp/0", false, false},
		{"/ip4/0.0.0.0/tcp/65536", false, false},
		{"/ip4/0.0.0.0/udp/7744", false, false},
		{"/ip4/0.0.0.0/udp/7744/quic", false, false},
		{"/ip4/0.0.0.0/udp/7744/quic-v1/webtransport", false, false},
		{"/ip4/0.0.0.0/tcp/7744/ws", false, false},
		{"/ip4/0.0.0.0/tcp/7744/tls/ws", false, false},
		{"/dnsaddr/example.org", false, false},
		{"/ip6zone/en0/ip6/fe80::1/tcp/7744", false, false},
		{"/unix/run/node.sock", false, false},
		{"/tcp/7744", false, false},
		{"/p2p/" + peerAText, false, false},
		{"/ip4/192.168.1.20/tcp/7744/p2p/" + peerAText + "/p2p-circuit", false, false},
		{"/ip4/192.168.1.20/tcp/7744/p2p/" + peerAText + "/p2p/" + peerBText, false, false},
		{"/ip4/192.168.1.20/tcp/7744/p2p-circuit", false, false},
		{"/ip4/192.168.1.20/tcp/7744/p2p/" + peerAText + "x", false, false},
		{"/ip4/192.168.1.20/tcp/7744/p2p/Qmnotapeer", false, false},
		{"/p2p-circuit/ip4/0.0.0.0/tcp/7744", false, false},
		{"/ip4/0.0.0.0/tcp/7744/", true, true},
		{long, true, true},
		{long + strings.Repeat("0", 10), false, false},
	}
	for _, r := range rows {
		t.Run(r.addr, func(t *testing.T) {
			if got := config.ValidateListen(r.addr) == nil; got != r.listen {
				t.Errorf("ValidateListen accepted = %v, want %v: %v", got, r.listen, config.ValidateListen(r.addr))
			}
			if got := config.ValidatePeerAddress(r.addr) == nil; got != r.peer {
				t.Errorf("ValidatePeerAddress accepted = %v, want %v: %v", got, r.peer, config.ValidatePeerAddress(r.addr))
			}
		})
	}
}

// The file's own rules: lists are bounded, repeats collapse, a node with no
// address is not listed, and what is wrong is named by its path.
func TestNetworkFileRules(t *testing.T) {
	addrs := func(n int) string {
		var out []string
		for i := 1; i <= n; i++ {
			out = append(out, fmt.Sprintf(`"/ip4/10.0.0.1/tcp/%d"`, i))
		}
		return strings.Join(out, ",")
	}
	file := func(listen, peers string) string {
		return `{"format":1,"version":1,"listen":[` + listen + `],"peers":{` + peers + `}}`
	}
	rows := []struct {
		name  string
		doc   string
		field string // empty: accepted
	}{
		{"empty lists", file("", ""), ""},
		{"listen at the limit", file(addrs(config.MaxAddressesPerNode), ""), ""},
		{"listen over the limit", file(addrs(config.MaxAddressesPerNode+1), ""), "listen"},
		{"peer at the limit", file("", `"`+node2Text+`":[`+addrs(config.MaxAddressesPerNode)+`]`), ""},
		{"peer over the limit", file("", `"`+node2Text+`":[`+addrs(config.MaxAddressesPerNode+1)+`]`), "peers." + node2Text},
		{"listen with a peer id", file(`"/ip4/0.0.0.0/tcp/1/p2p/`+peerAText+`"`, ""), "listen[0]"},
		{"second listen address is wrong", file(`"/ip4/0.0.0.0/tcp/1","/ip4/0.0.0.0/udp/1"`, ""), "listen[1]"},
		{"peer address is a circuit", file("", `"`+node2Text+`":["/ip4/1.1.1.1/tcp/1/p2p/`+peerAText+`/p2p-circuit"]`), "peers." + node2Text + "[0]"},
		{"peer with no address", file("", `"`+node2Text+`":[]`), "peers." + node2Text},
		{"node that is not an id", file("", `"node1abc":["/ip4/1.1.1.1/tcp/1"]`), "peers.node1abc"},
		{"listen missing", `{"format":1,"version":1,"peers":{}}`, "listen"},
		{"peers missing", `{"format":1,"version":1,"listen":[]}`, "peers"},
		{"listen null", `{"format":1,"version":1,"listen":null,"peers":{}}`, "listen"},
		{"unknown field", `{"format":1,"version":1,"listen":[],"peers":{},"relays":[]}`, "relays"},
		{"repeated key", `{"format":1,"version":1,"listen":[],"listen":[],"peers":{}}`, "listen"},
		{"version zero", `{"format":1,"version":0,"listen":[],"peers":{}}`, "version"},
		{"format 2", `{"format":2,"version":1,"listen":[],"peers":{}}`, "format"},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			_, err := config.DecodeNetwork([]byte(r.doc))
			if r.field == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("accepted")
			}
			if fe := fileError(t, err); fe.Path != config.NetworkPath || fe.Kind != config.KindNetwork || fe.Field != r.field {
				t.Errorf("error = %+v, want %s at %q", fe, config.NetworkPath, r.field)
			}
		})
	}
}

// The canonical file: the golden one decodes to the value below and encodes to
// the same bytes, and a hand-written layout, with repeats and another spelling
// of an address, encodes to the canonical text with the first appearance kept
// and the nodes sorted.
func TestGoldenNetwork(t *testing.T) {
	node2 := mustNode(t, node2Text)
	want, err := config.DecodeNetwork([]byte(goldenNetwork))
	if err != nil {
		t.Fatalf("DecodeNetwork: %v", err)
	}
	if want.Version != 2 || !reflect.DeepEqual(want.Listen, []string{"/ip4/0.0.0.0/tcp/7744", "/ip4/0.0.0.0/udp/7744/quic-v1"}) ||
		!reflect.DeepEqual(want.Peers, map[ident.NodeID][]string{node2: {"/ip4/192.168.1.20/tcp/7744/p2p/" + peerAText, "/dns4/node2.example.org/udp/7744/quic-v1"}}) ||
		want.LastOperation == nil || string(want.LastOperation.Result) != `{"version":2}` {
		t.Errorf("DecodeNetwork = %+v", want)
	}
	enc, err := want.Encode()
	if err != nil || string(enc) != goldenNetwork {
		t.Errorf("Encode = %s, %v\nwant\n%s", enc, err, goldenNetwork)
	}

	node1 := mustNode(t, node1Text)
	hand := `{ "format": 1, "version": 5,
  "listen": ["/ip6/0:0:0:0:0:0:0:1/tcp/9", "/ip4/0.0.0.0/tcp/8", "/ip6/::1/tcp/9"],
  "peers": { "` + node2Text + `": ["/ip4/1.1.1.1/tcp/2"], "` + node1Text + `": ["/ip4/2.2.2.2/tcp/3", "/ip4/2.2.2.2/tcp/3"] } }`
	got, err := config.DecodeNetwork([]byte(hand))
	if err != nil {
		t.Fatalf("DecodeNetwork(hand): %v", err)
	}
	if !reflect.DeepEqual(got.Listen, []string{"/ip6/::1/tcp/9", "/ip4/0.0.0.0/tcp/8"}) || !reflect.DeepEqual(got.Peers[node1], []string{"/ip4/2.2.2.2/tcp/3"}) {
		t.Errorf("normalized = %+v", got)
	}
	enc, err = got.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if i, j := strings.Index(string(enc), node1Text), strings.Index(string(enc), node2Text); i < 0 || j < 0 || i > j {
		t.Errorf("nodes are not sorted:\n%s", enc)
	}
	again, err := config.DecodeNetwork(enc)
	if err != nil || !reflect.DeepEqual(again, got) {
		t.Errorf("the canonical form decodes to %+v, %v, want %+v", again, err, got)
	}
}

// What is not valid is not encoded, so no start meets a file it refuses.
func TestEncodeNetworkRefusesWhatTheDecoderRefuses(t *testing.T) {
	node2 := mustNode(t, node2Text)
	for name, n := range map[string]config.Network{
		"a listen address with a peer": {Version: 1, Listen: []string{"/ip4/0.0.0.0/tcp/1/p2p/" + peerAText}},
		"a peer with no address":       {Version: 1, Peers: map[ident.NodeID][]string{node2: nil}},
		"the zero node":                {Version: 1, Peers: map[ident.NodeID][]string{{}: {"/ip4/1.1.1.1/tcp/1"}}},
	} {
		if data, err := n.Encode(); err == nil {
			t.Errorf("%s was encoded:\n%s", name, data)
		}
	}
}

// The changes the management layer makes: each is a versioned replacement that
// records its operation, refuses what the decoder would, and leaves the file
// alone when it refuses.
func TestNetworkChanges(t *testing.T) {
	node1, node2 := mustNode(t, node1Text), mustNode(t, node2Text)
	var seen []config.Network
	res := result(&seen, `{"ok":true}`)

	rg, c := loaded(t)
	n, err := c.SetPeerAddresses(request(1, 2), node1, []string{"/ip4/10.0.0.1/tcp/1", "/ip4/10.0.0.1/tcp/1"}, res)
	if err != nil {
		t.Fatal(err)
	}
	if n.Version != 3 || !reflect.DeepEqual(n.Peers[node1], []string{"/ip4/10.0.0.1/tcp/1"}) || len(n.Peers) != 2 {
		t.Errorf("after SetPeerAddresses: %+v", n)
	}
	if n, err = c.SetPeerAddresses(request(2, 3), node2, []string{"/ip4/10.0.0.2/tcp/2"}, res); err != nil || !reflect.DeepEqual(n.Peers[node2], []string{"/ip4/10.0.0.2/tcp/2"}) {
		t.Errorf("replacing the addresses of a node: %+v, %v", n, err)
	}
	if n, err = c.SetListen(request(3, 4), nil, res); err != nil || len(n.Listen) != 0 || n.Version != 5 {
		t.Errorf("an empty listen list: %+v, %v", n, err)
	}
	if n, err = c.RemovePeer(request(4, 5), node1, res); err != nil || len(n.Peers) != 1 || n.Version != 6 {
		t.Errorf("RemovePeer: %+v, %v", n, err)
	}
	if n.LastOperation == nil || n.LastOperation.Key() != request(4, 5).Key {
		t.Errorf("the operation is not recorded: %+v", n.LastOperation)
	}
	if got := rg.load().Network(); !reflect.DeepEqual(got, c.Network()) {
		t.Errorf("a restart reads %+v, memory holds %+v", got, c.Network())
	}

	before := snapshot(t, rg.mem)
	for name, do := range map[string]func() (config.Network, error){
		"removing a node that is not listed": func() (config.Network, error) { return c.RemovePeer(request(5, 6), node1, res) },
		"no address":                         func() (config.Network, error) { return c.SetPeerAddresses(request(5, 6), node1, nil, res) },
		"a listen address with a peer": func() (config.Network, error) {
			return c.SetListen(request(5, 6), []string{"/ip4/0.0.0.0/tcp/1/p2p/" + peerAText}, res)
		},
		"a bad peer address": func() (config.Network, error) {
			return c.SetPeerAddresses(request(5, 6), node1, []string{"/ip4/1.1.1.1/udp/1"}, res)
		},
		"the zero node": func() (config.Network, error) {
			return c.SetPeerAddresses(request(5, 6), ident.NodeID{}, []string{"/ip4/1.1.1.1/tcp/1"}, res)
		},
	} {
		_, err := do()
		ce := changeReason(t, err)
		want := config.ChangeInvalid
		if strings.HasPrefix(name, "removing") {
			want = config.ChangePeerUnknown
		}
		if ce.Reason != want {
			t.Errorf("%s: %+v, want %v", name, ce, want)
		}
	}
	if !reflect.DeepEqual(before, snapshot(t, rg.mem)) {
		t.Error("a refused change wrote to the disk")
	}
	// A version that is stale is a conflict even when the content is invalid.
	if _, err := c.SetListen(request(6, 1), []string{"nonsense"}, res); changeReason(t, err).Reason != config.ChangeVersionConflict {
		t.Errorf("stale and invalid: %v", err)
	}
}
