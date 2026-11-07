package cli

import (
	"bytes"
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
	return map[string]func(*termout.Stream) string{
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
