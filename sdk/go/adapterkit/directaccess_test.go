package adapterkit_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/aianyai/sidera/sdk/go/adapterkit"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func directCaps() adapterproto.Capabilities {
	return adapterproto.Capabilities{Direct: &adapterproto.DirectCapabilities{MaxEndpoints: 4}}
}

// daemonRecorder answers the three requests of direct endpoint access the way a
// daemon would and records the raw parameters it was sent.
type daemonRecorder struct {
	mu     sync.Mutex
	params map[string]json.RawMessage
}

func (d *daemonRecorder) serve(s *side) {
	d.params = map[string]json.RawMessage{}
	answer := func(method string, result any) {
		s.daemon.Handle(method, func(_ context.Context, raw json.RawMessage) (any, error) {
			d.mu.Lock()
			d.params[method] = raw
			d.mu.Unlock()
			return result, nil
		})
	}
	answer(adapterproto.MethodEndpointDirectory, adapterproto.DirectoryResult{
		Outcome: adapterproto.DirectoryUnavailable, Reason: adapterproto.DirectoryReasonUnavailable, Endpoints: []adapterproto.DirectoryEntry{}})
	answer(adapterproto.MethodEndpointPeers, adapterproto.PeersResult{Nodes: []adapterproto.PeerNode{{Node: "node-a", Self: true, Trust: adapterproto.PeerSelf, Connected: true}}})
	answer(adapterproto.MethodEndpointDeclare, adapterproto.DeclareResult{Declaration: adapterproto.Declaration{HiddenFromDirectory: true, AcceptFrom: []adapterproto.Address{}}})
}

func (d *daemonRecorder) sent(method string) (json.RawMessage, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	raw, ok := d.params[method]
	return raw, ok
}

// The three helpers of direct endpoint access are the adapter's to use only when
// the daemon declared direct_endpoint_access: without it they return an error and
// send nothing, and with it they send the request and decode the result.
func TestDirectEndpointAccessNeedsTheDaemonsCapability(t *testing.T) {
	for _, tc := range []struct {
		name     string
		declared bool
	}{{"declared", true}, {"not declared", false}} {
		t.Run(tc.name, func(t *testing.T) {
			lock := filepath.Join(t.TempDir(), "state", "instance.lock")
			app := &stub{}
			s := serveOn(t, config(app, directCaps()))
			var daemon daemonRecorder
			daemon.serve(s)
			params := initParams(lock)
			params.Capabilities.DirectEndpointAccess = tc.declared
			var init adapterproto.InitializeResult
			if err := s.call(adapterproto.MethodInitialize, params, &init); err != nil {
				t.Fatal(err)
			}
			conn := app.conn
			if got := conn.DaemonCapabilities().DirectEndpointAccess; got != tc.declared {
				t.Fatalf("DaemonCapabilities().DirectEndpointAccess = %v, want %v", got, tc.declared)
			}
			ctx := context.Background()
			dir, dirErr := conn.Directory(ctx, adapterproto.DirectoryParams{Endpoint: endpoint, Limit: 10})
			peers, peersErr := conn.Peers(ctx, adapterproto.PeersParams{Endpoint: endpoint})
			decl, declErr := conn.Declare(ctx, adapterproto.DeclareParams{Endpoint: endpoint, HiddenFromDirectory: ptr(true)})
			methods := []string{adapterproto.MethodEndpointDirectory, adapterproto.MethodEndpointPeers, adapterproto.MethodEndpointDeclare}
			if !tc.declared {
				for i, err := range []error{dirErr, peersErr, declErr} {
					if !errors.Is(err, adapterkit.ErrDirectEndpointAccess) {
						t.Errorf("%s: error %v, want ErrDirectEndpointAccess", methods[i], err)
					}
					if _, sent := daemon.sent(methods[i]); sent {
						t.Errorf("%s was sent although the daemon did not declare the capability", methods[i])
					}
				}
				return
			}
			if dirErr != nil || dir.Outcome != adapterproto.DirectoryUnavailable || dir.Reason != adapterproto.DirectoryReasonUnavailable {
				t.Errorf("Directory = %+v, %v", dir, dirErr)
			}
			if peersErr != nil || len(peers.Nodes) != 1 || !peers.Nodes[0].Self {
				t.Errorf("Peers = %+v, %v", peers, peersErr)
			}
			if declErr != nil || !decl.Declaration.HiddenFromDirectory {
				t.Errorf("Declare = %+v, %v", decl, declErr)
			}
			for method, want := range map[string]string{
				adapterproto.MethodEndpointDirectory: `{"endpoint":"` + endpoint + `","limit":10}`,
				adapterproto.MethodEndpointPeers:     `{"endpoint":"` + endpoint + `"}`,
				adapterproto.MethodEndpointDeclare:   `{"endpoint":"` + endpoint + `","hidden_from_directory":true}`,
			} {
				raw, ok := daemon.sent(method)
				if !ok || string(raw) != want {
					t.Errorf("%s was sent as %s (sent %v), want %s", method, raw, ok, want)
				}
			}
		})
	}
}

// Before the daemon has initialized the adapter there are no daemon capabilities.
func TestDaemonCapabilitiesAreEmptyBeforeInitialize(t *testing.T) {
	var conn adapterkit.Conn
	if conn.DaemonCapabilities().DirectEndpointAccess {
		t.Error("a connection that was not initialized has the capability")
	}
	if _, err := conn.Peers(context.Background(), adapterproto.PeersParams{Endpoint: endpoint}); !errors.Is(err, adapterkit.ErrDirectEndpointAccess) {
		t.Errorf("Peers = %v", err)
	}
}

func ptr[T any](v T) *T { return &v }
