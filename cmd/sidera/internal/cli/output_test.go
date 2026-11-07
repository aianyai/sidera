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
	markerPrefix   = regexp.MustCompile(`(?m)^(✓|!|✗) `)
)

// outputs are the human outputs of the agent commands for one representative result each,
// as a function of the stream they are written on.
func outputs() map[string]func(*termout.Stream) string {
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
	}
	for name, render := range outputs() {
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
	for name, render := range outputs() {
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
			stripped := markerPrefix.ReplaceAllString(escapeSequence.ReplaceAllString(text, ""), "")
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
				plain := squash(markerPrefix.ReplaceAllString(escapeSequence.ReplaceAllString(out, ""), ""))
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
		"directory": {renderDirectory(s, agentapi.DirectoryResult{}, ""), "No endpoints.\n"},
		"ack":       {renderAck(s, agentapi.AckResult{}), "No parts were acknowledged.\n"},
		"peers": {renderPeers(s, agentapi.PeersResult{Nodes: []agentapi.Peer{{Node: testNode, Self: true, Trust: "self", Connected: true}}}),
			"TRUST  CONNECTION  NODE\nself   connected   " + testNode + "\n\nNo other node is trusted directly.\n"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s: got %q, want %q", name, tc.got, tc.want)
		}
	}
}

// A cell with nothing to show is a dash, so that the columns stay in line.
func TestEmptyCellsAreDashes(t *testing.T) {
	out := renderDirectory(newStream(false, false), agentapi.DirectoryResult{Endpoints: []agentapi.DirectoryEntry{{Address: testAddress, Kind: "direct"}}}, "")
	if !strings.Contains(squash(out), "direct - - "+testAddress) {
		t.Errorf("directory:\n%s", out)
	}
}
