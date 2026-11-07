package cli

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/agentapi"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/termout"
)

// The identifiers below are the real lengths: a node is 52 characters after its prefix and an
// endpoint 32, which a table or a block must show whole whatever the width of the stream.
const (
	testNode     = "node1ek8w0qz5s7hn2xj9v4m6c3dtf8lyp0ra5gub7e2wk1qz9c4xvjhxn"
	testNodeB    = "node1m3y7t9d2v5g8h0k4n6q1w3r5u7x9z2b4c6f8j0l2p4s6v8y0a2dkq3"
	testEndpoint = "ep1vj52h3mxk8c4n7q0t2r6w9y1b5d3f8g0jlz7s4a6e2u9ph3"
	testMessage  = "msg1m4k9r2x7c1v5b8n3q6w0t4y2e7u9i1o5p3a8s6d0f2g4h7jkl"
	testLease    = "lease1v7c3x9z5b1n7m3q9w5e1r7t3y9u5i1o7p3a9s5d1f7g3h9jk"
)

var (
	testAddress = testNode + "/" + testEndpoint
	testRuntime = testNodeB + "/ep1zjgq8d4n0w6m2c8x4v0b6n2m8q4w0e6r2t8y4u0i6o2p8a"
)

// The management read commands decode the daemon's answer, so their fixtures are the answer
// as the daemon writes it. The peer id is what libp2p calls the key of a node.
const (
	testPeerID  = "12D3KooWRbVAYtd8nZL9uZq2fW5s6qB7cJxH4mNeT3kPaDgU1xYz"
	testContact = "/ip4/192.168.1.10/tcp/7744/p2p/" + testPeerID
	testQuic    = "/ip4/192.168.1.10/udp/7744/quic-v1/p2p/" + testPeerID
	testAlert   = "5f0c1d2e3a4b5c6d7e8f9a0b1c2d3e4f"
)

// answer decodes a result object the way the command does.
func answer[T any](t testing.TB, result string) T {
	t.Helper()
	var v T
	if err := decodeResult([]byte(`{"result":`+result+`}`), &v); err != nil {
		t.Fatalf("fixture does not decode: %v\n%s", err, result)
	}
	return v
}

var statusAnswer = `{"node":{"id":"` + testNode + `","version":"0.15.0"},
"daemon":{"state":"running","started_at":"2026-11-07T00:00:00Z","start_id":"0123456789abcdef"},
"reminders":[{"kind":"revocation_backup_unconfirmed","message":"The statement that can retire this node has not been confirmed as kept outside this device. Run ` + "`sidera identity export-revocation`" + `, keep the copy offline, then run ` + "`sidera identity confirm-backup <digest>`" + `."}],
"alerts":{"open":2},
"clock":{"rollback_paused":false,"high_water":"2026-11-07T01:59:00Z"},
"certificate":{"not_after":"2026-12-07T02:00:00Z","remaining_s":2592000},
"capacity":{"open_deliveries":3,"node_queue_limit":10000,"outbound_backlog":1,"disk_free_bytes":32200000000,"disk_reserve_bytes":268435456},
"deliveries":{"queued":2,"accepted":0,"delivered":1,"rejected":0,"expired_unconfirmed":0,"delivery_unknown":0,"closed_unconfirmed":0},
"endpoints":[{"address":"` + testAddress + `","kind":"session","state":"open"},{"address":"` + testRuntime + `","kind":"runtime","instance":"work","state":"open"},{"address":"` + testRuntime + `x","kind":"direct","instance":"console","state":"creating"}],
"home":"/Users/me/.sidera"}`

var networkAnswer = `{"node":"` + testNode + `","peer_id":"` + testPeerID + `","version":5,
"listen":{"configured":["/ip4/0.0.0.0/tcp/7744","/ip4/0.0.0.0/udp/7744/quic-v1"],"actual":["/ip4/0.0.0.0/tcp/7744"]},
"contact":"` + testNode + ` ` + testContact + ` ` + testQuic + `",
"peers":[{"node":"` + testNodeB + `","addresses":["/ip4/192.168.1.20/tcp/7744/p2p/` + testPeerID + `"],"certificate_held":true,"connected":true},
{"node":"` + testNode + `x","addresses":[],"certificate_held":true,"connected":false}]}`

var adaptersAnswer = `{"version":4,
"adapters":[{"id":"console","version":"0.15.0"},{"id":"claude","version":"0.15.0"},{"id":"codex","version":"0.15.0"},{"id":"hermes","version":"0.15.0","fault":{"reason":"package_invalid"}}],
"instances":[
{"instance":"console","adapter":"console","enabled":true,"probe":{"state":"passed","at":"2026-11-07T01:41:00Z","frontends":[{"preset":"light","frontend":"cli"}]}},
{"instance":"work","adapter":"claude","enabled":true,"probe":{"state":"passed","at":"2026-11-07T01:41:00Z","frontends":[]},
 "availability":[{"condition":"runtime_auth","detail":"claude is not logged in"}]},
{"instance":"scratch","adapter":"claude","enabled":false},
{"instance":"later","adapter":"codex","enabled":true,"probe":{"state":"pending","frontends":[{"preset":"light","frontend":"cli"}]},"fault":{"reason":"program_missing"}}]}`

var alertsAnswer = `{"alerts":[
{"id":"` + testAlert + `","kind":"evidence_error","since":"2026-11-07T01:10:00Z","subject":"` + testMessage + `","detail":"evidence is malformed","acknowledged":false},
{"id":"clock_rollback","kind":"clock_rollback","since":"","detail":"the wall clock was set back","acknowledged":false}]}`

var endpointsAnswer = `{"endpoints":[
{"address":"` + testAddress + `","kind":"session","instance":"console","state":"open","queued":0,"draining":false,"origin":"attached"},
{"address":"` + testRuntime + `","kind":"runtime","instance":"work","state":"closed","queued":3,"draining":true}]}`

var presetsAnswer = `{"version":2,"presets":[
{"name":"light","local_sources":"deny","description":"Channel and light work","config":{"claude":{"model":"sonnet","append_system_prompt":"reply with sidera reply"},"codex":{"sandbox":"read-only"}}},
{"name":"bare","local_sources":"allow"}]}`

var nodeAnswer = `{"node":"` + testNode + `","version":3,"settings":{"reply_permission":true,"node_queue_limit":10000,"disk_reserve_bytes":268435456,"node_prompt_turns":8,"archive_retention_days":30,"log_total_bytes":157286400,"endpoint_defaults":{"max_queue_s":1800,"backlog_limit":200}}}`

// The management change commands decode the answer of the daemon too. A change's answer is
// the envelope the daemon writes: the operation, whether it is a replay, and the result.
const (
	testOperation = "0123456789abcdef0123456789abcdef"
	testPairing   = "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"
)

func envelope(result string, replayed bool) []byte {
	return []byte(fmt.Sprintf(`{"operation_id":%q,"replayed":%t,"result":%s}`, testOperation, replayed, result))
}

// changes are the answers of the change commands for one representative request each: the
// method, the params the command sent and the result.
var changes = []struct {
	name, method string
	params       map[string]any
	result       string
	// want is what the text says, squashed, on a stream that is not decorated.
	want []string
}{
	{"adapter enable", "adapters.enable", nil, `{"instance":"console","enabled":true,"version":5}`,
		[]string{"Enabled instance console. Probing in progress. Run `sidera adapter list` to see the probe result."}},
	{"adapter disable", "adapters.disable", nil, `{"instance":"console","enabled":false,"version":6}`, []string{"Disabled instance console."}},
	{"adapter config of an enabled instance", "adapters.config", nil, `{"instance":"work","enabled":true,"version":7}`,
		[]string{"Replaced the configuration of instance work. The instance restarts to take the new configuration."}},
	{"adapter config of a disabled instance", "adapters.config", nil, `{"instance":"work","enabled":false,"version":7}`,
		[]string{"Replaced the configuration of instance work."}},
	{"adapter remove", "adapters.remove", nil, `{"instance":"work","removed":true,"version":8}`, []string{"Removed instance work."}},
	{"adapter install", "adapters.install", nil, `{"adapter":{"id":"claude","version":"0.15.0"}}`,
		[]string{"Installed adapter claude 0.15.0. Run `sidera adapter enable claude` to make an instance of it."}},
	{"adapter install replacing", "adapters.install", nil, `{"adapter":{"id":"claude","version":"0.15.0"},"replaced":true}`,
		[]string{"Installed adapter claude 0.15.0, replacing the package that was installed."}},
	{"adapter install of the installed package", "adapters.install", nil, `{"adapter":{"id":"claude","version":"0.15.0"},"already_current":true}`,
		[]string{"Adapter claude 0.15.0 is already installed; nothing was replaced."}},
	{"preset add", "presets.add", nil, `{"name":"light","version":3}`, []string{"Added preset light."}},
	{"preset remove", "presets.remove", nil, `{"name":"light","version":4}`, []string{"Removed preset light."}},
	// Only the settings the request named are shown, with their units; the endpoint defaults are two lines of one name.
	{"node set", "node.set", map[string]any{"settings": map[string]any{"node_queue_limit": 20001, "endpoint_defaults": map[string]any{"backlog_limit": 5}}},
		`{"settings":{"reply_permission":true,"node_queue_limit":20001,"disk_reserve_bytes":268435456,"node_prompt_turns":8,"archive_retention_days":30,"log_total_bytes":157286400,"endpoint_defaults":{"max_queue_s":1800,"backlog_limit":5}},"version":4}`,
		[]string{"Changed the settings of the node. Delivery limit 20001 unfinished deliveries Endpoint queue time 30 min at most Endpoint backlog 5 messages at most"}},
	{"endpoint set", "endpoints.set", nil, `{"endpoint":"` + testAddress + `","settings":{"max_queue_s":1800,"backlog_limit":200},"version":9}`,
		[]string{"Changed the settings of the endpoint. Endpoint " + testAddress + " Queue time 30 min at most Backlog 200 messages at most"}},
	{"endpoint set back to the defaults", "endpoints.set", nil, `{"endpoint":"` + testAddress + `","settings":{},"version":10}`,
		[]string{"Settings all follow the node's defaults"}},
	{"endpoint close", "endpoints.close", nil, `{"endpoint":"` + testAddress + `","outcome":"closed"}`, []string{"Closed the endpoint. Endpoint " + testAddress}},
	{"endpoint close of a session", "endpoints.close", nil, `{"endpoint":"` + testAddress + `","outcome":"closing"}`,
		[]string{"Closing the endpoint; its session is still being released."}},
	{"endpoint close twice", "endpoints.close", nil, `{"endpoint":"` + testAddress + `","outcome":"already_closed"}`, []string{"The endpoint was closed already."}},
	{"alerts ack", "alerts.ack", nil, `{"alert":"` + testAlert + `","acknowledged":true}`, []string{"Acknowledged alert " + testAlert + "."}},
	{"network address set", "network.address.set", nil, `{"node":"` + testNodeB + `","addresses":["` + testContact + `","` + testQuic + `"],"version":6}`,
		[]string{"Replaced the addresses kept for the node. Node " + testNodeB + " Addresses " + testContact + " " + testQuic}},
	{"network address remove", "network.address.remove", nil, `{"node":"` + testNodeB + `","version":7}`,
		[]string{"Removed the addresses kept for the node. Node " + testNodeB}},
	{"network listen set", "network.listen.set", nil, `{"listen":["/ip4/0.0.0.0/tcp/7745"],"version":8,"restart_required":true}`,
		[]string{"Replaced the addresses the node listens on. Listen /ip4/0.0.0.0/tcp/7745 Restart the daemon (`sidera daemon restart`) for the new listen addresses to take effect."}},
	{"trust expect", "trust.expect", nil, `{"node":"` + testNodeB + `","expires_at":"2026-11-07T02:10:00Z"}`,
		[]string{"Now expecting a pairing with the node. Node " + testNodeB + " Expires 2026-11-07 02:10 UTC (in 10 min)"}},
	{"trust forget", "trust.forget", nil, `{"node":"` + testNodeB + `","forgotten":true}`,
		[]string{"Deleted the expectation for the node. Node " + testNodeB}},
	{"trust reject", "trust.reject", nil, `{"pairing_id":"` + testPairing + `","node":"` + testNodeB + `","status":"rejected"}`,
		[]string{"Ended the pairing request. Pairing " + testPairing + " Node " + testNodeB + " Status rejected"}},
	{"trust remove", "trust.remove", nil, `{"node":"` + testNodeB + `","revoked_credentials":["d1","d2"]}`,
		[]string{"Removed the node from this node's trust. The other node is not told. Node " + testNodeB + " Credentials revoked 2"}},
	{"trust clear", "trust.clear", nil, `{"node":"` + testNodeB + `","new_pairing_required":true}`,
		[]string{"Took the node back into this node's trust. Node " + testNodeB + " Its revoked credentials stay revoked: it is trusted again only after a new pairing."}},
	{"identity confirm-backup", "identity.confirm_backup", nil, `{"confirmed_at":"2026-11-07T01:55:00Z"}`,
		[]string{"Confirmed that the self-revocation statement is kept outside this device. Confirmed at 2026-11-07 01:55 UTC"}},
	{"identity retire", "identity.retire", nil, `{"node":"` + testNode + `","retired":true,"stopping":true}`,
		[]string{"Retired the identity of this node. The daemon is stopping. Node " + testNode}},
	{"clock clear-rollback", "clock.clear_rollback", nil, `{"cleared":true,"high_water":"2026-11-07T02:00:00Z","now":"2026-11-07T02:00:00Z"}`,
		[]string{"Accepted the current clock; the pause is lifted. Clock 2026-11-07 02:00 UTC (now)"}},
	{"clock clear-rollback without a pause", "clock.clear_rollback", nil, `{"cleared":false,"high_water":"2026-11-07T02:00:00Z","now":"2026-11-07T02:00:00Z"}`,
		[]string{"The clock was not paused; nothing changed."}},
	{"daemon stop", "daemon.stop", nil, `{"stopping":true}`, []string{"Daemon is stopping."}},
}

// layoutOf is the text of a change or read answer on a stream, as the command writes it.
func layoutOf(t testing.TB, s *termout.Stream, method string, params map[string]any, raw []byte) string {
	t.Helper()
	render, ok := humanLayouts[method]
	if !ok {
		t.Fatalf("no layout for %s", method)
	}
	text, err := render(s, request{now: time.Date(2026, 11, 7, 2, 0, 0, 0, time.UTC), params: params}, raw)
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	return text
}

// streams are the three states of an output: plain (a pipe), decorated (a terminal that
// takes no color) and colored.
var streams = []struct {
	name               string
	decorated, colored bool
}{{"plain", false, false}, {"decorated", true, false}, {"colored", true, true}}

func newStream(decorated, colored bool) *termout.Stream {
	return termout.New(&bytes.Buffer{}, decorated, colored, 80)
}

// squash collapses runs of spaces, so that a check reads the words and not the padding.
func squash(s string) string { return strings.Join(strings.Fields(s), " ") }

var (
	escapeSequence = regexp.MustCompile("\x1b\\[[0-9;]*m")
	// A marker is the first thing on a line or, in the continuation of a table cell, the first
	// thing after the indentation that lines it up under its column.
	markerPrefix = regexp.MustCompile(`(?m)^( *)(✓|!|✗) `)
)

// outputs are the human outputs of the agent commands for one representative result each,
// as a function of the stream they are written on.
func outputs(t testing.TB) map[string]func(*termout.Stream) string {
	now := time.Date(2026, 11, 7, 2, 0, 0, 0, time.UTC)
	statusReply := adapterproto.ControlReply{Signal: adapterproto.SignalEndpointStatus, Status: &adapterproto.ControlStatusReply{
		Activity: adapterproto.ActivityIdle, Queued: 2, AsOf: now.Add(-3 * time.Minute).Unix(),
		Conditions: []adapterproto.ControlCondition{adapterproto.ControlConditionRuntimeAuth},
		Presets:    []adapterproto.PresetAvailability{{Name: "default", Available: true}, {Name: "slow", Available: false}},
	}}
	outs := map[string]func(*termout.Stream) string{
		"me": func(s *termout.Stream) string {
			return renderMe(s, agentapi.MeResult{
				Address: testAddress, Node: testNode, Kind: "session", DisplayName: "Name of creator",
				Declaration: agentapi.Declaration{AcceptFrom: []string{testRuntime}, RestrictSenders: true},
				Frontends: map[adapterproto.Frontend]agentapi.Frontend{
					"cli": {RecvWaitMaxS: 20, Budget: agentapi.Budget{Unit: "chars", Limit: 27000}},
					"mcp": {RecvWaitMaxS: 45, Budget: agentapi.Budget{Unit: "tokens", Limit: 22500}},
				},
			})
		},
		"declare": func(s *termout.Stream) string {
			return renderDeclared(s, agentapi.DeclareResult{Declaration: agentapi.Declaration{Paused: true}})
		},
		"peers": func(s *termout.Stream) string {
			return renderPeers(s, agentapi.PeersResult{Nodes: []agentapi.Peer{
				{Node: testNode, Self: true, Trust: "self", Connected: true},
				{Node: testNodeB, Trust: "direct"},
			}})
		},
		"directory": func(s *termout.Stream) string {
			return renderDirectory(s, agentapi.DirectoryResult{Endpoints: []agentapi.DirectoryEntry{
				{Address: testAddress, Kind: "session", Accepts: []string{"message.send", "session.close"}},
				{Address: testRuntime, Kind: "runtime", DisplayName: "work (codex)", Accepts: []string{"session.create", "endpoint.status"}},
			}, NextCursor: "Zm9v"}, testNodeB)
		},
		"send": func(s *termout.Stream) string {
			return renderSent(s, messageSent, testMessage, []string{testAddress}, true)
		},
		"ack": func(s *termout.Stream) string {
			return renderAck(s, agentapi.AckResult{Items: []adapterproto.PartAck{{MessageID: adapterproto.MessageID(testMessage), Part: 2, Ack: adapterproto.ItemCompleted}}})
		},
		"ack refused": func(s *termout.Stream) string {
			return renderAck(s, agentapi.AckResult{Items: []adapterproto.PartAck{
				{MessageID: adapterproto.MessageID(testMessage), Part: 1, Ack: adapterproto.ItemRecorded},
				{MessageID: adapterproto.MessageID(testMessage), Part: 2, Ack: adapterproto.ItemRefusedLease},
			}})
		},
		"node status": func(s *termout.Stream) string {
			return renderNodeStatus(s, now, answer[statusView](t, statusAnswer))
		},
		"node show": func(s *termout.Stream) string { return renderNodeSettings(s, answer[nodeView](t, nodeAnswer)) },
		"network show": func(s *termout.Stream) string {
			return renderNetwork(s, answer[networkView](t, networkAnswer))
		},
		"preset list": func(s *termout.Stream) string {
			return renderPresets(s, answer[presetsView](t, presetsAnswer))
		},
		"adapter list": func(s *termout.Stream) string {
			return renderAdapters(s, answer[adaptersView](t, adaptersAnswer))
		},
		"endpoint list": func(s *termout.Stream) string {
			return renderEndpoints(s, answer[endpointsView](t, endpointsAnswer))
		},
		"alerts list": func(s *termout.Stream) string {
			return renderAlerts(s, answer[alertsView](t, alertsAnswer), false)
		},
		"status": func(s *termout.Stream) string {
			return renderStatus(s, now, adapterproto.MessageStatusResult{
				Recipients:   []adapterproto.RecipientStatus{{To: adapterproto.Address(testRuntime), State: adapterproto.DeliveryDelivered}},
				ControlReply: &statusReply,
			})
		},
	}
	for _, tc := range changes {
		outs["change: "+tc.name] = func(s *termout.Stream) string {
			return layoutOf(t, s, tc.method, tc.params, envelope(tc.result, false))
		}
	}
	outs["change: replayed"] = func(s *termout.Stream) string {
		return layoutOf(t, s, "adapters.enable", nil, envelope(`{"instance":"console","enabled":true,"version":5}`, true))
	}
	outs["probe"] = func(s *termout.Stream) string {
		return renderProbe(s, agentapi.ProbeResult{OK: true, Preset: "light", Frontend: "cli"})
	}
	return outs
}

// An identifier is never shortened, and it comes whole on every state of the stream.
func TestIdentifiersAreComplete(t *testing.T) {
	want := map[string][]string{
		"me":        {testAddress, testNode, testRuntime},
		"peers":     {testNode, testNodeB},
		"directory": {testAddress, testRuntime, "--node " + testNodeB},
		"send":      {testMessage, testAddress},
		"ack":       {testMessage},
		"status":    {testRuntime},
		// The node of a status, the node and the peer id of a network and every address.
		"node status":   {testNode, "/Users/me/.sidera"},
		"network show":  {testNode, testNodeB, testPeerID, testContact, testQuic, "sidera network address set " + testNode + " " + testContact + " " + testQuic},
		"endpoint list": {testAddress, testRuntime},
		"alerts list":   {testAlert, testMessage},
		"node show":     {testNode},
		// The identifiers a change names are whole, each on a line of its own.
		"change: endpoint close":      {testAddress},
		"change: endpoint set":        {testAddress},
		"change: network address set": {testNodeB, testContact, testQuic},
		"change: trust expect":        {testNodeB},
		"change: trust reject":        {testPairing, testNodeB},
		"change: identity retire":     {testNode},
		"change: alerts ack":          {testAlert},
	}
	for name, render := range outputs(t) {
		for _, st := range streams {
			out := render(newStream(st.decorated, st.colored))
			for _, id := range want[name] {
				if !strings.Contains(out, id) {
					t.Errorf("%s on a %s stream does not hold %q whole:\n%s", name, st.name, id, out)
				}
			}
		}
	}
}

// Markers and escape sequences are only decoration: a pipe gets neither, a terminal without
// color gets markers, and what is left when both are taken off is the same text on all three.
func TestDecorationOnlyStrengthensTheSameWords(t *testing.T) {
	for name, render := range outputs(t) {
		plain := render(newStream(false, false))
		if escapeSequence.MatchString(plain) || markerPrefix.MatchString(plain) {
			t.Errorf("%s: a stream that is not decorated got decoration:\n%q", name, plain)
		}
		decorated := render(newStream(true, false))
		if escapeSequence.MatchString(decorated) {
			t.Errorf("%s: a stream that is not colored got an escape sequence:\n%q", name, decorated)
		}
		colored := render(newStream(true, true))
		if name == "send" && !escapeSequence.MatchString(colored) {
			t.Errorf("%s: a colored stream got no color:\n%q", name, colored)
		}
		// The outcome of a part that was not acknowledged is yellow, the one that was is not.
		if name == "ack refused" {
			if !strings.Contains(colored, "\x1b[33mrefused_lease\x1b[0m") {
				t.Errorf("%s: a refused outcome is not yellow on a colored stream:\n%q", name, colored)
			}
			if strings.Contains(colored, "\x1b[33mrecorded") {
				t.Errorf("%s: an acknowledged outcome is yellow:\n%q", name, colored)
			}
		}
		for state, text := range map[string]string{"decorated": decorated, "colored": colored} {
			stripped := markerPrefix.ReplaceAllString(escapeSequence.ReplaceAllString(text, ""), "$1")
			if stripped != plain {
				t.Errorf("%s: the %s text is not the plain text under its decoration:\n%q\n%q", name, state, stripped, plain)
			}
		}
		for _, line := range strings.Split(plain, "\n") {
			if line != strings.TrimRight(line, " ") {
				t.Errorf("%s: trailing spaces in %q", name, line)
			}
		}
	}
}

// Every item of a control reply that an agent acts on is in the text: the endpoint of a
// created session, why a creation or an action did not go through, and what a status says.
func TestControlReplyShowsEveryField(t *testing.T) {
	now := time.Date(2026, 11, 7, 2, 0, 0, 0, time.UTC)
	runtimeNode := testNodeB
	for _, tc := range []struct {
		name  string
		reply adapterproto.ControlReply
		want  []string
	}{
		{"created", adapterproto.ControlReply{Signal: adapterproto.SignalSessionCreate, Create: &adapterproto.ControlCreateReply{
			Outcome: adapterproto.CreateReplyCreated, SessionEndpoint: "ep1newsession",
		}}, []string{"session.create", "created", "Session endpoint ep1newsession"}},
		{"unconfirmed", adapterproto.ControlReply{Signal: adapterproto.SignalSessionCreate, Create: &adapterproto.ControlCreateReply{
			Outcome: adapterproto.CreateReplyUnconfirmed, SessionEndpoint: "ep1newsession", Reason: adapterproto.CreateReasonReadyTimeout,
		}}, []string{"unconfirmed", "Session endpoint ep1newsession", "ready_timeout"}},
		{"failed", adapterproto.ControlReply{Signal: adapterproto.SignalSessionCreate, Create: &adapterproto.ControlCreateReply{
			Outcome: adapterproto.CreateReplyFailed, Reason: adapterproto.CreateReasonPresetUnavailable,
		}}, []string{"failed", "preset_unavailable"}},
		{"closed", adapterproto.ControlReply{Signal: adapterproto.SignalSessionClose, Close: &adapterproto.ControlCloseReply{
			Outcome: adapterproto.CloseReplyClosed,
		}}, []string{"session.close", "closed"}},
		{"close unconfirmed", adapterproto.ControlReply{Signal: adapterproto.SignalSessionClose, Close: &adapterproto.ControlCloseReply{
			Outcome: adapterproto.CloseReplyUnconfirmed, Reason: adapterproto.CloseReasonTurnStillRunning,
		}}, []string{"close_unconfirmed", "turn_still_running"}},
		{"interrupt failed", adapterproto.ControlReply{Signal: adapterproto.SignalSessionInterrupt, Interrupt: &adapterproto.ControlInterruptReply{
			Outcome: adapterproto.InterruptReplyFailed, Reason: adapterproto.InterruptReasonNativeError,
		}}, []string{"session.interrupt", "failed", "native_error"}},
		{"interrupt stopped", adapterproto.ControlReply{Signal: adapterproto.SignalSessionInterrupt, Interrupt: &adapterproto.ControlInterruptReply{
			Outcome: adapterproto.InterruptReplyStopped,
		}}, []string{"stopped"}},
		{"status", adapterproto.ControlReply{Signal: adapterproto.SignalEndpointStatus, Status: &adapterproto.ControlStatusReply{
			Activity: adapterproto.ActivityBusy, Queued: 4, AsOf: now.Add(-90 * time.Second).Unix(),
			Conditions: []adapterproto.ControlCondition{adapterproto.ControlConditionAdapterOffline, adapterproto.ControlConditionRuntimeAuth},
			Presets:    []adapterproto.PresetAvailability{{Name: "default", Available: true}, {Name: "slow", Available: false}},
		}}, []string{"endpoint.status", "busy", "4", "2026-11-07 01:58 UTC (2 min ago)", "adapter_offline, runtime_auth", "default available", "slow not available"}},
		{"status without presets or conditions", adapterproto.ControlReply{Signal: adapterproto.SignalEndpointStatus, Status: &adapterproto.ControlStatusReply{
			Activity: adapterproto.ActivityIdle, AsOf: now.Unix(),
		}}, []string{"idle", "none"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, st := range streams {
				out := renderStatus(newStream(st.decorated, st.colored), now, adapterproto.MessageStatusResult{
					Recipients:   []adapterproto.RecipientStatus{{To: adapterproto.Address(runtimeNode + "/ep1runtime"), State: adapterproto.DeliveryDelivered}},
					ControlReply: &tc.reply,
				})
				plain := squash(markerPrefix.ReplaceAllString(escapeSequence.ReplaceAllString(out, ""), "$1"))
				for _, w := range tc.want {
					if !strings.Contains(plain, w) {
						t.Errorf("%s stream: %q is missing from\n%s", st.name, w, plain)
					}
				}
			}
		})
	}
}

// A delivery that was rejected says why, and a message with several recipients has a row for each.
func TestStatusRowsPerRecipient(t *testing.T) {
	out := renderStatus(newStream(false, false), time.Now(), adapterproto.MessageStatusResult{Recipients: []adapterproto.RecipientStatus{
		{To: adapterproto.Address(testAddress), State: adapterproto.DeliveryRejected, Reason: adapterproto.RejectEndpointDeclined},
		{To: adapterproto.Address(testRuntime), State: adapterproto.DeliveryQueued},
	}})
	want := "STATE                           TO\n" +
		"rejected (endpoint_declined)    " + testAddress + "\n" +
		"queued                          " + testRuntime + "\n"
	// The widths follow the cells, so compare the cells and not the padding.
	if strings.Join(strings.Fields(out), " ") != strings.Join(strings.Fields(want), " ") {
		t.Errorf("status:\n%s", out)
	}
}

// What the declaration says about senders depends on the restriction and on the list together;
// a list that is kept but not in force is said so.
func TestDeclarationSenders(t *testing.T) {
	list := []string{testAddress, testRuntime}
	for _, tc := range []struct {
		name string
		d    agentapi.Declaration
		want []string
	}{
		{"open", agentapi.Declaration{}, []string{"Senders anyone Shared"}},
		{"open with a saved list", agentapi.Declaration{AcceptFrom: list}, []string{"anyone (the sender list is saved and not in use)", "Sender list " + testAddress + " " + testRuntime}},
		{"restricted to a list", agentapi.Declaration{RestrictSenders: true, AcceptFrom: list}, []string{"only the senders on the list", testRuntime}},
		{"restricted to nobody", agentapi.Declaration{RestrictSenders: true}, []string{"Senders nobody"}},
		{"everything off", agentapi.Declaration{Paused: true, RefuseMultiRecipient: true, HiddenFromDirectory: true}, []string{"Receiving paused", "Shared messages refused", "Directory hidden"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := renderDeclared(newStream(false, false), agentapi.DeclareResult{Declaration: tc.d})
			for _, w := range tc.want {
				if !strings.Contains(squash(out), w) {
					t.Errorf("%q is missing from\n%s", w, out)
				}
			}
		})
	}
}

// An empty result is one sentence; a lone row is not mistaken for the whole story.
func TestEmptyResultsAreSentences(t *testing.T) {
	s := newStream(false, false)
	for name, tc := range map[string]struct{ got, want string }{
		"directory":  {renderDirectory(s, agentapi.DirectoryResult{}, ""), "No endpoints.\n"},
		"ack":        {renderAck(s, agentapi.AckResult{}), "No parts were acknowledged.\n"},
		"endpoints":  {renderEndpoints(s, answer[endpointsView](t, `{"endpoints":[]}`)), "No endpoints.\n"},
		"alerts":     {renderAlerts(s, answer[alertsView](t, `{"alerts":[]}`), false), "No open alerts.\n"},
		"all alerts": {renderAlerts(s, answer[alertsView](t, `{"alerts":[]}`), true), "No alerts.\n"},
		"presets": {renderPresets(s, answer[presetsView](t, `{"version":2,"presets":[]}`)),
			"No presets.\n\nPresets version  2 (for --expected-version)\n"},
		"adapters": {renderAdapters(s, answer[adaptersView](t, `{"version":4,"adapters":[],"instances":[]}`)),
			"No adapters are installed.\n\nInstances version  4 (for --expected-version)\n"},
		"no peers": {renderNetwork(s, answer[networkView](t, `{"node":"`+testNode+`","peer_id":"`+testPeerID+`","version":1,"listen":{"configured":[],"actual":[]},"contact":"","peers":[]}`)),
			"Node               " + testNode + "\nPeer ID            " + testPeerID + "\nListen configured  none\nListening now      none\nReachable at       none\nNetwork version    1 (for --expected-version)\n\nNo other nodes are known.\n"},
		"peers": {renderPeers(s, agentapi.PeersResult{Nodes: []agentapi.Peer{{Node: testNode, Self: true, Trust: "self", Connected: true}}}),
			"TRUST  CONNECTION  NODE\nself   connected   " + testNode + "\n\nNo other node is trusted directly.\n"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s: got %q, want %q", name, tc.got, tc.want)
		}
	}
}

// A cell with nothing to show is a dash, in a table of the management commands too.
func TestManagementEmptyCellsAreDashes(t *testing.T) {
	presets := squash(renderPresets(newStream(false, false), answer[presetsView](t, presetsAnswer)))
	if !strings.Contains(presets, "bare allow - -") {
		t.Errorf("preset list:\n%s", presets)
	}
	alerts := squash(renderAlerts(newStream(false, false), answer[alertsView](t, alertsAnswer), false))
	if !strings.Contains(alerts, "- clock_rollback open") {
		t.Errorf("alerts list:\n%s", alerts)
	}
}

// A cell with nothing to show is a dash, so that the columns stay in line.
func TestEmptyCellsAreDashes(t *testing.T) {
	out := renderDirectory(newStream(false, false), agentapi.DirectoryResult{Endpoints: []agentapi.DirectoryEntry{{Address: testAddress, Kind: "direct"}}}, "")
	if !strings.Contains(squash(out), "direct - - "+testAddress) {
		t.Errorf("directory:\n%s", out)
	}
}

// What each management read command says, for the facts an owner reads it for. The text is
// squashed and taken from a stream that is not decorated, which is the text of every state.
func TestManagementReadsShowTheirFacts(t *testing.T) {
	now := time.Date(2026, 11, 7, 2, 0, 0, 0, time.UTC)
	status := func(edit func(*statusView)) func() string {
		return func() string {
			v := answer[statusView](t, statusAnswer)
			if edit != nil {
				edit(&v)
			}
			return renderNodeStatus(newStream(false, false), now, v)
		}
	}
	for _, tc := range []struct {
		name string
		out  func() string
		want []string
	}{
		{"status", status(nil), []string{
			"Daemon running, version 0.15.0, started 2026-11-07 00:00 UTC (2 h ago)", "Home /Users/me/.sidera",
			"Certificate valid until 2026-12-07 02:00 UTC (in 30 days)", "Clock ok, latest time seen 2026-11-07 01:59 UTC (1 min ago)",
			"Deliveries 3 open (limit 10000), 1 waiting to be sent", "Delivery states delivered 1, queued 2",
			"Disk 32.2 GB free, 268.4 MB kept in reserve", "Endpoints 3 (1 creating, 2 open)", "Alerts 2 open, see `sidera alerts list`",
		}},
		{"status of a quiet node", status(func(v *statusView) {
			v.Reminders, v.Alerts.Open, v.Endpoints = nil, 0, nil
			v.Deliveries = map[string]int{"queued": 0}
		}), []string{"Endpoints none", "Alerts none"}},
		{"status with the clock paused", status(func(v *statusView) {
			v.Clock.RollbackPaused, v.Clock.RollbackPausedSince = true, "2026-11-07T01:50:00Z"
		}), []string{"Clock paused: the wall clock was set back, since 2026-11-07 01:50 UTC (10 min ago)", "Run `sidera clock clear-rollback`"}},
		{"status with an expired certificate", status(func(v *statusView) { v.Certificate.RemainingS = -5 }), []string{"Certificate expired 2026-12-07 02:00 UTC"}},
		{"node show", func() string { return renderNodeSettings(newStream(false, false), answer[nodeView](t, nodeAnswer)) }, []string{
			"Unsolicited replies allowed", "Delivery limit 10000 unfinished deliveries", "Disk reserve 268.4 MB", "Archive kept for 30 days",
			"Log space 157.3 MB at most", "Endpoint queue time 30 min at most", "Endpoint backlog 200 messages at most", "Settings version 3 (for --expected-version)",
		}},
		{"network show", func() string { return renderNetwork(newStream(false, false), answer[networkView](t, networkAnswer)) }, []string{
			"Listen configured /ip4/0.0.0.0/tcp/7744 /ip4/0.0.0.0/udp/7744/quic-v1", "Listening now /ip4/0.0.0.0/tcp/7744",
			"connected held " + testNodeB + " /ip4/192.168.1.20/tcp/7744/p2p/" + testPeerID, "not connected held " + testNode + "x",
		}},
		{"adapter list", func() string { return renderAdapters(newStream(false, false), answer[adaptersView](t, adaptersAnswer)) }, []string{
			"console console 0.15.0 enabled passed 2026-11-07 01:41 UTC", "work claude 0.15.0 enabled passed 2026-11-07 01:41 UTC runtime_auth: claude is not logged in",
			"scratch claude 0.15.0 disabled -", "later codex 0.15.0 enabled pending reached light (cli) package cannot run: program_missing",
			"- hermes 0.15.0 no instance - package cannot run: package_invalid", "Instances version 4 (for --expected-version)",
		}},
		{"endpoint list", func() string {
			return renderEndpoints(newStream(false, false), answer[endpointsView](t, endpointsAnswer))
		}, []string{
			"session (attached) console open 0 " + testAddress, "runtime work closed (draining) 3 " + testRuntime,
		}},
		{"alerts list", func() string { return renderAlerts(newStream(false, false), answer[alertsView](t, alertsAnswer), true) }, []string{
			"2026-11-07 01:10 UTC evidence_error open " + testAlert + " evidence is malformed about " + testMessage,
		}},
		{"preset list", func() string { return renderPresets(newStream(false, false), answer[presetsView](t, presetsAnswer)) }, []string{
			"light deny Channel and light work claude: append_system_prompt, model codex: sandbox", "Presets version 2 (for --expected-version)",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := squash(tc.out())
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("%q is missing from\n%s", w, out)
				}
			}
		})
	}
	// The reminder comes before every fact, and the command that clears it is in it.
	if out := status(nil)(); !strings.HasPrefix(out, "The statement that can retire this node") || !strings.Contains(out, "`sidera identity confirm-backup <digest>`") {
		t.Errorf("the reminder does not come first:\n%s", out)
	}
}

// Text that no source checks reaches the output as visible escapes and never as the bytes a
// terminal acts on: what an adapter writes for an availability problem, an alert's detail and
// subject (which can quote a peer), and a preset's description and the names of its settings.
func TestManagementReadsEscapeForeignText(t *testing.T) {
	const raw = `\u001b]0;pwned\u0007\u202etxt`
	const shown = `\x1b]0;pwned\x07\u202etxt`
	for name, out := range map[string]string{
		"availability detail": renderAdapters(newStream(true, true), answer[adaptersView](t,
			`{"version":1,"adapters":[],"instances":[{"instance":"i","adapter":"a","enabled":true,"availability":[{"condition":"runtime_auth","detail":"`+raw+`"}]}]}`)),
		"alert detail": renderAlerts(newStream(true, true), answer[alertsView](t,
			`{"alerts":[{"id":"x","kind":"k","since":"2026-11-07T01:10:00Z","detail":"`+raw+`"}]}`), false),
		"alert subject": renderAlerts(newStream(true, true), answer[alertsView](t,
			`{"alerts":[{"id":"x","kind":"k","since":"2026-11-07T01:10:00Z","subject":"`+raw+`","detail":"d"}]}`), false),
		"preset description": renderPresets(newStream(true, true), answer[presetsView](t,
			`{"version":1,"presets":[{"name":"p","local_sources":"allow","description":"`+raw+`"}]}`)),
		"preset setting name": renderPresets(newStream(true, true), answer[presetsView](t,
			`{"version":1,"presets":[{"name":"p","local_sources":"allow","config":{"a":{"`+raw+`":1}}}]}`)),
	} {
		if !strings.Contains(out, shown) {
			t.Errorf("%s: the escaped text %q is not in\n%q", name, shown, out)
		}
		// The colored stream writes its own sequences; the text's must not be among them.
		if stripped := escapeSequence.ReplaceAllString(out, ""); strings.ContainsAny(stripped, "\x1b\x07\u202e") {
			t.Errorf("%s: foreign control characters reached the output:\n%q", name, out)
		}
	}
}

// What each change command says, for the facts an owner needs from it. The text is squashed
// and taken from a stream that is not decorated, which is the text of every state.
func TestManagementChangesShowTheirFacts(t *testing.T) {
	for _, tc := range changes {
		t.Run(tc.name, func(t *testing.T) {
			out := squash(layoutOf(t, newStream(false, false), tc.method, tc.params, envelope(tc.result, false)))
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("%q is missing from\n%s", w, out)
				}
			}
		})
	}
}

// The operation identifier and the replay flag are for --json: a change says what it did. A
// request that had been carried out before says so, once, under the result.
func TestManagementChangesLeaveOutTheOperationAndSayReplay(t *testing.T) {
	for _, tc := range changes {
		for _, replayed := range []bool{false, true} {
			out := layoutOf(t, newStream(false, false), tc.method, tc.params, envelope(tc.result, replayed))
			if strings.Contains(out, testOperation) || strings.Contains(out, "operation_id") || strings.Contains(out, "replayed") {
				t.Errorf("%s: the operation or the flag is printed:\n%s", tc.name, out)
			}
			if got := strings.Count(out, replayNote); got != btoi(replayed) {
				t.Errorf("%s (replayed %t): the replay note is printed %d times:\n%s", tc.name, replayed, got, out)
			}
			if replayed && !strings.HasSuffix(out, replayNote+"\n") {
				t.Errorf("%s: the replay note is not the last line:\n%s", tc.name, out)
			}
		}
	}
	// On a decorated stream the note is a warning, so a person sees that this run did nothing.
	out := layoutOf(t, newStream(true, true), "adapters.enable", nil, envelope(`{"instance":"console","enabled":true,"version":5}`, true))
	if !strings.Contains(out, "\x1b[33m!\x1b[0m "+replayNote) {
		t.Errorf("the replay note is not a yellow warning:\n%q", out)
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// A command whose method has no layout would print nothing it understands; every command that
// reaches managementOutput must have one. The commands handled before that point, which print
// by their own code, are the exceptions. help lists every command, so a command that is added
// to managementPlan and not given a layout is found here.
func TestEveryManagementCommandHasALayout(t *testing.T) {
	printsItself := map[string]bool{"daemon.restart": true, "trust.request": true, "trust.list": true, "identity.export_revocation": true}
	seen := 0
	for _, name := range helpOrder {
		c := command{Config: Config{Getenv: func(string) string { return "" }}}
		args := strings.Fields(name)
		c.arguments = arguments(args)
		// A plan comes back with its method even when the arguments are wrong for it, as here.
		plan, _ := c.managementPlan(args)
		if plan.method == "" || printsItself[plan.method] {
			continue
		}
		seen++
		if _, ok := humanLayouts[plan.method]; !ok {
			t.Errorf("%q sends %s, which has no human layout", name, plan.method)
		}
	}
	if seen < 30 {
		t.Errorf("only %d commands were checked: the walk no longer finds the management commands", seen)
	}
}

func TestForcedRemovalWarningIsAWarningOnTheErrorStream(t *testing.T) {
	plain := forcedRemovalWarning(newStream(false, false), "work")
	if !strings.HasPrefix(plain, "The client setup of work was not checked") || !strings.Contains(plain, "adapters guide.\n") {
		t.Errorf("plain:\n%s", plain)
	}
	if colored := forcedRemovalWarning(newStream(true, true), "work"); !strings.HasPrefix(colored, "\x1b[33m!\x1b[0m The client setup") {
		t.Errorf("colored:\n%q", colored)
	}
}
