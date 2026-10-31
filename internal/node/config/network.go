package config

import (
	"errors"
	"fmt"
	"slices"

	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/pairing"
)

const (
	// DefaultListenPort is the port `init` makes the node listen on unless the
	// owner names another.
	DefaultListenPort = 7744
	// MaxAddressesPerNode is the most addresses one list holds: the listen
	// list, and the list of one peer. It is the limit of an address list in a
	// pairing record, so an address the book holds can be offered in one.
	MaxAddressesPerNode = pairing.MaxAddresses
	// MaxAddressLen is the longest address, in bytes, by the same reasoning.
	MaxAddressLen = pairing.MaxAddressLen
)

// Network is the content of network.json: where the node listens, and the
// addresses the owner entered for other nodes. A node that is learned about
// later is not here: only what the owner decided is configuration.
type Network struct {
	Version admin.Version
	// Listen holds multiaddrs without a peer identifier. An empty list means the
	// node does not listen: it can still dial.
	Listen []string
	// Peers holds, per node, the multiaddrs it can be reached on, each with an
	// optional trailing /p2p/<peer id>. A node with no address is not listed.
	Peers         map[ident.NodeID][]string
	LastOperation *Operation
}

// ValidatePort checks a port the node is told to listen on: a fixed one, so 0,
// which would ask the system for any, is refused.
func ValidatePort(port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("port %d is not between 1 and 65535", port)
	}
	return nil
}

// DefaultListen is the listen list `init` writes for a port: TCP and QUIC on
// every IPv4 interface.
func DefaultListen(port int) ([]string, error) {
	if err := ValidatePort(port); err != nil {
		return nil, err
	}
	return []string{
		fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", port),
		fmt.Sprintf("/ip4/0.0.0.0/udp/%d/quic-v1", port),
	}, nil
}

// ValidateListen checks an address the node listens on: /<host>/tcp/<port> or
// /<host>/udp/<port>/quic-v1, with an ip4, ip6, dns, dns4 or dns6 host. It is the
// one definition of what a listen address may be.
func ValidateListen(addr string) error {
	_, err := canonicalAddress(addr, false)
	return err
}

// ValidatePeerAddress checks an address entered for another node: the shapes of
// ValidateListen, and after them an optional /p2p/<peer id> that must be a
// libp2p peer identifier. A relay circuit is refused: relaying is not a way the
// owner can state, and a circuit address would name a third node.
func ValidatePeerAddress(addr string) error {
	_, err := canonicalAddress(addr, true)
	return err
}

// canonicalAddress checks addr and returns the multiaddr library's text for it,
// which is the form the file holds.
func canonicalAddress(addr string, withPeer bool) (string, error) {
	if n := len(addr); n == 0 || n > MaxAddressLen {
		return "", fmt.Errorf("an address has 1 to %d bytes, this one has %d", MaxAddressLen, n)
	}
	m, err := ma.NewMultiaddr(addr)
	if err != nil {
		return "", fmt.Errorf("not a multiaddr: %w", err)
	}
	parts := []ma.Component(m)
	for _, c := range parts {
		if c.Code() == ma.P_CIRCUIT {
			return "", errors.New("a relay circuit is not accepted")
		}
	}
	const shapes = "an address is /<host>/tcp/<port> or /<host>/udp/<port>/quic-v1"
	if len(parts) < 2 {
		return "", errors.New(shapes)
	}
	switch c := parts[0]; c.Code() {
	case ma.P_IP4, ma.P_IP6, ma.P_DNS, ma.P_DNS4, ma.P_DNS6:
	default:
		return "", fmt.Errorf("the host is %s, want ip4, ip6, dns, dns4 or dns6", c.Protocol().Name)
	}
	switch c := parts[1]; c.Code() {
	case ma.P_TCP:
		parts = parts[2:]
	case ma.P_UDP:
		if len(parts) < 3 || parts[2].Code() != ma.P_QUIC_V1 {
			return "", errors.New("udp carries quic-v1 only: " + shapes)
		}
		parts = parts[3:]
	default:
		return "", fmt.Errorf("the transport is %s: %s", c.Protocol().Name, shapes)
	}
	if m[1].Value() == "0" {
		return "", errors.New("port 0 asks the system for any port; an address names a fixed one")
	}
	if len(parts) > 0 {
		if !withPeer {
			return "", errors.New("a listen address names no peer")
		}
		if len(parts) > 1 || parts[0].Code() != ma.P_P2P {
			return "", errors.New("after the transport only /p2p/<peer id> may follow")
		}
		if _, err := peer.Decode(parts[0].Value()); err != nil {
			return "", fmt.Errorf("the peer identifier: %w", err)
		}
	}
	return m.String(), nil
}

// normalizeAddresses checks a list of addresses and returns it in canonical
// text with repeats dropped, in the order each address first appears: the order
// the owner wrote is kept, because it is theirs to choose and nothing here
// depends on it.
func normalizeAddresses(field string, addrs []string, withPeer bool) ([]string, error) {
	if len(addrs) > MaxAddressesPerNode {
		return nil, invalidf(field, "%d addresses, at most %d", len(addrs), MaxAddressesPerNode)
	}
	out := make([]string, 0, len(addrs))
	for i, a := range addrs {
		canon, err := canonicalAddress(a, withPeer)
		if err != nil {
			return nil, invalid(fmt.Sprintf("%s[%d]", field, i), err)
		}
		if !slices.Contains(out, canon) {
			out = append(out, canon)
		}
	}
	return out, nil
}

// normalizePeers checks the address book: every node is a valid node id with at
// least one address, and every address is valid.
func normalizePeers(peers map[ident.NodeID][]string) (map[ident.NodeID][]string, error) {
	out := make(map[ident.NodeID][]string, len(peers))
	for node, addrs := range peers {
		field := "peers." + node.String()
		if node.IsZero() {
			return nil, invalidf("peers", "the zero node id is not a node")
		}
		if len(addrs) == 0 {
			return nil, invalidf(field, "a node with no address is not listed")
		}
		norm, err := normalizeAddresses(field, addrs, true)
		if err != nil {
			return nil, err
		}
		out[node] = norm
	}
	return out, nil
}

// networkWire is network.json.
type networkWire struct {
	Format        int                 `json:"format"`
	Version       uint64              `json:"version"`
	Listen        []string            `json:"listen"`
	Peers         map[string][]string `json:"peers"`
	LastOperation *operationWire      `json:"last_operation,omitempty"`
}

func decodeNetwork(data []byte) (Network, error) {
	var w networkWire
	if err := decodeStrict(data, &w); err != nil {
		return Network{}, err
	}
	if err := checkHeader(w.Format, w.Version, true); err != nil {
		return Network{}, err
	}
	n := Network{Version: versionOf(w.Version)}
	var err error
	if n.Listen, err = normalizeAddresses("listen", w.Listen, false); err != nil {
		return Network{}, err
	}
	n.Peers = make(map[ident.NodeID][]string, len(w.Peers))
	for _, text := range sortedKeys(w.Peers) {
		node, err := ident.ParseNodeID(text)
		if err != nil {
			return Network{}, invalid("peers."+text, err)
		}
		addrs := w.Peers[text]
		if len(addrs) == 0 {
			return Network{}, invalidf("peers."+text, "a node with no address is not listed")
		}
		if n.Peers[node], err = normalizeAddresses("peers."+text, addrs, true); err != nil {
			return Network{}, err
		}
	}
	if w.LastOperation != nil {
		if n.LastOperation, err = w.LastOperation.operation(); err != nil {
			return Network{}, err
		}
	}
	return n, nil
}

// DecodeNetwork decodes network.json. The error is a *FileError that names the
// file.
func DecodeNetwork(data []byte) (Network, error) {
	n, err := decodeNetwork(data)
	if err != nil {
		return Network{}, fileErrorFor(NetworkPath, KindNetwork, err)
	}
	return n, nil
}

// Encode returns the bytes of network.json, in canonical form: addresses as the
// multiaddr library writes them, repeats dropped, nodes sorted by id.
func (n Network) Encode() ([]byte, error) {
	if err := checkEncodeVersion(uint64(n.Version)); err != nil {
		return nil, err
	}
	listen, err := normalizeAddresses("listen", n.Listen, false)
	if err != nil {
		return nil, err
	}
	peers, err := normalizePeers(n.Peers)
	if err != nil {
		return nil, err
	}
	op, err := operationWireOf(n.LastOperation)
	if err != nil {
		return nil, err
	}
	w := networkWire{Format: FormatVersion, Version: uint64(n.Version), Listen: listen,
		Peers: make(map[string][]string, len(peers)), LastOperation: op}
	for node, addrs := range peers {
		w.Peers[node.String()] = addrs
	}
	return encodeJSON(w)
}

func cloneNetwork(n Network) Network {
	n.Listen = slices.Clone(n.Listen)
	peers := make(map[ident.NodeID][]string, len(n.Peers))
	for node, addrs := range n.Peers {
		peers[node] = slices.Clone(addrs)
	}
	n.Peers = peers
	n.LastOperation = cloneOperation(n.LastOperation)
	return n
}

// Network returns network.json.
func (c *Config) Network() Network { return cloneNetwork(c.read().network) }

// SetListen replaces the listen list. An empty list is valid: the node then
// does not listen.
func (c *Config) SetListen(req Request, listen []string, result ResultFunc[Network]) (Network, error) {
	norm, err := normalizeAddresses("listen", listen, false)
	return c.changeNetwork(req, result, err, func(next *Network) error {
		next.Listen = norm
		return nil
	})
}

// SetPeerAddresses replaces the addresses of a node, adding the node when it is
// not listed. At least one address is given: removing a node is RemovePeer.
func (c *Config) SetPeerAddresses(req Request, node ident.NodeID, addrs []string, result ResultFunc[Network]) (Network, error) {
	var norm []string
	var err error
	switch {
	case node.IsZero():
		err = invalidf("node", "the zero node id is not a node")
	case len(addrs) == 0:
		err = invalidf("addresses", "a node with no address is removed, not listed with none")
	default:
		norm, err = normalizeAddresses("addresses", addrs, true)
	}
	return c.changeNetwork(req, result, err, func(next *Network) error {
		next.Peers[node] = norm
		return nil
	})
}

// RemovePeer removes a node and its addresses from the book. A node that is not
// listed is refused with ChangePeerUnknown.
func (c *Config) RemovePeer(req Request, node ident.NodeID, result ResultFunc[Network]) (Network, error) {
	return c.changeNetwork(req, result, nil, func(next *Network) error {
		if _, ok := next.Peers[node]; !ok {
			return &ChangeError{Reason: ChangePeerUnknown, Field: node.String()}
		}
		delete(next.Peers, node)
		return nil
	})
}

// changeNetwork is the shape of every change of network.json: the version is
// compared first, then the content the caller already checked (bad, when it was not
// valid), then the change is applied to a copy, and the operation and
// the new version are written in one replacement.
func (c *Config) changeNetwork(req Request, result ResultFunc[Network], bad error, apply func(next *Network) error) (Network, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	cur := c.read().network
	if err := checkVersion(req, cur.Version); err != nil {
		return Network{}, err
	}
	if bad != nil {
		return Network{}, invalidChange(bad)
	}
	next := cloneNetwork(cur)
	version, err := nextVersion(cur.Version)
	if err != nil {
		return Network{}, err
	}
	next.Version = version
	if err := apply(&next); err != nil {
		return Network{}, err
	}
	if next.LastOperation, err = operationOf(req, result, next); err != nil {
		return Network{}, err
	}
	data, err := next.Encode()
	if err != nil {
		return Network{}, err
	}
	if err := c.replaceFile(networkSet, RoleNetworkConfig, NetworkPath, data); err != nil {
		return Network{}, err
	}
	c.mu.Lock()
	c.st.network = next
	c.mu.Unlock()
	return c.Network(), nil
}
