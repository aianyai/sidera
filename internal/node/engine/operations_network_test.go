package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/engine"
)

func encodeNetwork(n config.Network) ([]byte, error) {
	return json.Marshal(struct {
		Version uint64 `json:"version"`
	}{uint64(n.Version)})
}

func networkCommand(b byte, content string, expected admin.Version, op engine.Operation) engine.ManagementCommand {
	return engine.ManagementCommand{Key: requestKey(b), Fingerprint: fingerprintOf(content), Expected: expected, Name: "network", Operation: op}
}

// The address book reaches the network after the write that made it durable, and
// after no other: not after a refusal, whether the network's rule, a version
// conflict or an unknown node; and not after a change of the listen list, which
// takes effect with the next start. The view tells what the network and the
// engine know of each node.
func TestNetworkChangesPushTheAddressBookOnlyAfterADurableChange(t *testing.T) {
	noLeaks(t)
	net := &fakeNetwork{}
	r := trustRig(t, withNetwork(net))
	e := r.start()
	ctx := context.Background()
	peer, other := newPeerNode(t, 1), newPeerNode(t, 2)
	addrs := []string{"/ip4/10.0.0.1/tcp/7744"}
	version := r.cfg.Network().Version

	if _, err := e.Execute(ctx, networkCommand(101, "set", version, engine.SetPeerAddressesOp(peer.node, addrs, encodeNetwork))); err != nil {
		t.Fatal(err)
	}
	if got, want := net.addressBooks(), []map[ident.NodeID][]string{{peer.node: addrs}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("books pushed after a set: %v, want %v", got, want)
	}

	before := r.snapshot()
	net.checkErr = &config.FieldError{Field: "addresses[0]", Err: errors.New("the rule")}
	_, err := e.Execute(ctx, networkCommand(102, "refused", version+1, engine.SetPeerAddressesOp(other.node, addrs, encodeNetwork)))
	var ce *config.ChangeError
	if !errors.As(err, &ce) || ce.Reason != config.ChangeInvalid || ce.Field != "addresses[0]" {
		t.Errorf("an address the network refuses: %v, want an invalid change at addresses[0]", err)
	}
	net.checkErr = nil
	_, err = e.Execute(ctx, networkCommand(103, "stale", version, engine.SetPeerAddressesOp(other.node, addrs, encodeNetwork)))
	if !errors.As(err, &ce) || ce.Reason != config.ChangeVersionConflict {
		t.Errorf("a stale change: %v", err)
	}
	_, err = e.Execute(ctx, networkCommand(104, "unknown", version+1, engine.RemovePeerOp(other.node, encodeNetwork)))
	if !errors.As(err, &ce) || ce.Reason != config.ChangePeerUnknown {
		t.Errorf("removing a node that has no address: %v", err)
	}
	if !reflect.DeepEqual(before, r.snapshot()) || len(net.addressBooks()) != 1 {
		t.Error("a refused change wrote to the disk or pushed a book")
	}

	if _, err := e.Execute(ctx, networkCommand(105, "listen", version+1, engine.SetListenOp([]string{"/ip4/0.0.0.0/tcp/9000"}, encodeNetwork))); err != nil {
		t.Fatal(err)
	}
	if got := r.cfg.Network().Listen; !reflect.DeepEqual(got, []string{"/ip4/0.0.0.0/tcp/9000"}) || len(net.addressBooks()) != 1 {
		t.Errorf("listen %v, %d books: the list is kept and nothing is pushed", got, len(net.addressBooks()))
	}

	cert := peer.certificate(1)
	if err := e.NotePeerCertificate(ctx, cert.Cert, cert.Rec, cert.Seen); err != nil {
		t.Fatal(err)
	}
	net.connected = map[ident.NodeID]bool{peer.node: true}
	v, err := e.NetworkView(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []engine.NetworkPeer{{Node: peer.node, Addresses: addrs, CertificateHeld: true, Connected: true}}
	if !reflect.DeepEqual(v.Peers, want) || v.PeerID != "peer-id" || v.Config.Version != version+2 || len(v.Contact) != 1 || len(v.Listening) != 1 {
		t.Errorf("the view is %+v", v)
	}

	if _, err := e.Execute(ctx, networkCommand(106, "remove", version+2, engine.RemovePeerOp(peer.node, encodeNetwork))); err != nil {
		t.Fatal(err)
	}
	if books := net.addressBooks(); len(books) != 2 || len(books[1]) != 0 {
		t.Errorf("books after the removal: %v, want an empty one last", books)
	}
	if v, _ = e.NetworkView(ctx); len(v.Peers) != 1 || v.Peers[0].Addresses != nil || !v.Peers[0].CertificateHeld {
		t.Errorf("a node known by its certificate only: %+v", v.Peers)
	}
}
