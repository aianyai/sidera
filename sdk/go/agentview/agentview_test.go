package agentview_test

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/agentview"
	"github.com/aianyai/sidera/sdk/go/termout"
)

// The identifiers are the real lengths: a node is 52 characters after its prefix and an
// endpoint 32, which a table or a block must show whole whatever the width of the stream.
const (
	testNode     = "node1ek8w0qz5s7hn2xj9v4m6c3dtf8lyp0ra5gub7e2wk1qz9c4xvjhxn"
	testNodeB    = "node1m3y7t9d2v5g8h0k4n6q1w3r5u7x9z2b4c6f8j0l2p4s6v8y0a2dkq3"
	testEndpoint = "ep1vj52h3mxk8c4n7q0t2r6w9y1b5d3f8g0jlz7s4a6e2u9ph3"
	testMessage  = "msg1m4k9r2x7c1v5b8n3q6w0t4y2e7u9i1o5p3a8s6d0f2g4h7jkl"
)

var (
	testAddress = testNode + "/" + testEndpoint
	testRuntime = testNodeB + "/ep1zjgq8d4n0w6m2c8x4v0b6n2m8q4w0e6r2t8y4u0i6o2p8a"
)

var now = time.Date(2026, 11, 7, 2, 0, 0, 0, time.UTC)

// streams are the three states of an output: plain (a pipe), decorated (a terminal that
// takes no color) and colored.
var streams = []struct {
	name               string
	decorated, colored bool
}{{"plain", false, false}, {"decorated", true, false}, {"colored", true, true}}

// command is how a program that was run on an instance spells its commands: the program,
// the command, the flags that select the instance, then the arguments. The hints must be
// built with it, whatever they name.
func command(args ...string) string {
	return strings.Join(append([]string{"prog", args[0], "--instance", "work"}, args[1:]...), " ")
}

func view(decorated, colored bool) agentview.View {
	return agentview.View{Out: termout.New(&bytes.Buffer{}, decorated, colored, 80), Command: command}
}

// squash collapses runs of spaces, so that a check reads the words and not the padding.
func squash(s string) string { return strings.Join(strings.Fields(s), " ") }

var (
	escapeSequence = regexp.MustCompile("\x1b\\[[0-9;]*m")
	// A marker is the first thing on a line or, in the continuation of a table cell, the first
	// thing after the indentation that lines it up under its column.
	markerPrefix = regexp.MustCompile(`(?m)^( *)(✓|!|✗) `)
)

func undecorated(s string) string {
	return markerPrefix.ReplaceAllString(escapeSequence.ReplaceAllString(s, ""), "$1")
}

// outputs are the results this package prints, one representative each, as a function of the
// stream they are written on.
var outputs = map[string]func(agentview.View) string{
	"peers": func(v agentview.View) string {
		return v.Peers(adapterproto.PeersResult{Nodes: []adapterproto.PeerNode{
			{Node: testNode, Self: true, Trust: adapterproto.PeerSelf, Connected: true},
			{Node: testNodeB, Trust: adapterproto.PeerDirect},
		}})
	},
	"directory": func(v agentview.View) string {
		return v.Directory(adapterproto.DirectoryResult{Outcome: adapterproto.DirectoryOK, Endpoints: []adapterproto.DirectoryEntry{
			{Address: adapterproto.Address(testAddress), Kind: adapterproto.EndpointSession,
				Accepts: []adapterproto.EndpointAction{adapterproto.ActionMessageSend, adapterproto.ActionSessionClose}},
			{Address: adapterproto.Address(testRuntime), Kind: adapterproto.EndpointRuntime, DisplayName: "work (codex)",
				Accepts: []adapterproto.EndpointAction{adapterproto.ActionSessionCreate, adapterproto.ActionEndpointStatus}},
		}, NextCursor: "Zm9v"}, testNodeB)
	},
	"send": func(v agentview.View) string {
		return v.Sent(agentview.SentMessage, testMessage, []string{testAddress}, true)
	},
	"status": func(v agentview.View) string {
		return v.Status(now, adapterproto.MessageStatusResult{
			Recipients: []adapterproto.RecipientStatus{{To: adapterproto.Address(testRuntime), State: adapterproto.DeliveryDelivered}},
			ControlReply: &adapterproto.ControlReply{Signal: adapterproto.SignalEndpointStatus, Status: &adapterproto.ControlStatusReply{
				Activity: adapterproto.ActivityIdle, Queued: 2, AsOf: now.Add(-3 * time.Minute).Unix(),
				Conditions: []adapterproto.ControlCondition{adapterproto.ControlConditionRuntimeAuth},
				Presets:    []adapterproto.PresetAvailability{{Name: "default", Available: true}, {Name: "slow", Available: false}},
			}},
		})
	},
}

// An identifier is never shortened, and it comes whole on every state of the stream.
func TestIdentifiersAreComplete(t *testing.T) {
	want := map[string][]string{
		"peers":     {testNode, testNodeB},
		"directory": {testAddress, testRuntime, "--node " + testNodeB},
		"send":      {testMessage, testAddress},
		"status":    {testRuntime},
	}
	for name, render := range outputs {
		for _, st := range streams {
			out := render(view(st.decorated, st.colored))
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
	for name, render := range outputs {
		plain := render(view(false, false))
		if escapeSequence.MatchString(plain) || markerPrefix.MatchString(plain) {
			t.Errorf("%s: a stream that is not decorated got decoration:\n%q", name, plain)
		}
		decorated := render(view(true, false))
		if escapeSequence.MatchString(decorated) {
			t.Errorf("%s: a stream that is not colored got an escape sequence:\n%q", name, decorated)
		}
		colored := render(view(true, true))
		if name == "send" && !escapeSequence.MatchString(colored) {
			t.Errorf("%s: a colored stream got no color:\n%q", name, colored)
		}
		for state, text := range map[string]string{"decorated": decorated, "colored": colored} {
			if stripped := undecorated(text); stripped != plain {
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
		{"status with the choices of a preset", adapterproto.ControlReply{Signal: adapterproto.SignalEndpointStatus, Status: &adapterproto.ControlStatusReply{
			Activity: adapterproto.ActivityIdle, AsOf: now.Unix(),
			Presets: []adapterproto.PresetAvailability{
				{Name: "fast", Available: true, Model: "claude-haiku-4-5", Effort: adapterproto.EffortLow, AllowedModels: []adapterproto.ModelAllowance{
					{Model: "claude-haiku-4-5", Efforts: []adapterproto.Effort{adapterproto.EffortLow, adapterproto.EffortMedium}},
					{Model: "claude-opus-4-5", Efforts: []adapterproto.Effort{adapterproto.EffortHigh}},
				}},
				{Name: "plain", Available: true},
			},
		}}, []string{"fast available", "defaults: model claude-haiku-4-5, effort low", "allowed models: claude-haiku-4-5 (low, medium), claude-opus-4-5 (high)", "plain available"}},
		{"status without presets or conditions", adapterproto.ControlReply{Signal: adapterproto.SignalEndpointStatus, Status: &adapterproto.ControlStatusReply{
			Activity: adapterproto.ActivityIdle, AsOf: now.Unix(),
		}}, []string{"idle", "none"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, st := range streams {
				out := view(st.decorated, st.colored).Status(now, adapterproto.MessageStatusResult{
					Recipients:   []adapterproto.RecipientStatus{{To: adapterproto.Address(testNodeB + "/ep1runtime"), State: adapterproto.DeliveryDelivered}},
					ControlReply: &tc.reply,
				})
				plain := squash(undecorated(out))
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
	out := view(false, false).Status(now, adapterproto.MessageStatusResult{Recipients: []adapterproto.RecipientStatus{
		{To: adapterproto.Address(testAddress), State: adapterproto.DeliveryRejected, Reason: adapterproto.RejectEndpointDeclined},
		{To: adapterproto.Address(testRuntime), State: adapterproto.DeliveryQueued},
	}})
	want := "STATE                           TO\n" +
		"rejected (endpoint_declined)    " + testAddress + "\n" +
		"queued                          " + testRuntime + "\n"
	// The widths follow the cells, so compare the cells and not the padding.
	if squash(out) != squash(want) {
		t.Errorf("status:\n%s", out)
	}
}

// An empty result is one sentence; a lone row is not mistaken for the whole story.
func TestEmptyResultsAreSentences(t *testing.T) {
	v := view(false, false)
	for name, tc := range map[string]struct{ got, want string }{
		"directory": {v.Directory(adapterproto.DirectoryResult{Outcome: adapterproto.DirectoryOK}, ""), "No endpoints.\n"},
		"peers": {v.Peers(adapterproto.PeersResult{Nodes: []adapterproto.PeerNode{{Node: testNode, Self: true, Trust: adapterproto.PeerSelf, Connected: true}}}),
			"TRUST  CONNECTION  NAME  SYSTEM  SIDERA  NODE\nself   connected   -     -       -       " + testNode + "\n\nNo other node is trusted directly.\n"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s: got %q, want %q", name, tc.got, tc.want)
		}
	}
}

// The node a person asks about is told apart by what it says about itself. The name is shown
// with the host name when they differ, the system with the architecture of the platform, and a
// node with no profile, or with a profile that leaves a member out, has dashes where the
// member would be.
func TestPeersShowWhatTheNodesSayAboutThemselves(t *testing.T) {
	const testNodeC = "node1q7w3e9r5t1y7u3i9o5p1a7s3d9f5g1h7j3k9l5z1x7c3v9b5n1mgpd"
	profile := func(p adapterproto.PeerProfile) *adapterproto.PeerProfile { p.AsOf = 1; return &p }
	out := view(false, false).Peers(adapterproto.PeersResult{Nodes: []adapterproto.PeerNode{
		{Node: testNode, Self: true, Trust: adapterproto.PeerSelf, Connected: true, Profile: profile(adapterproto.PeerProfile{
			Name: "imac", HostName: "imac-1", Platform: "darwin/arm64", System: "macOS 27.0.1", SystemBuild: "25A354", SideraVersion: "0.17.0"})},
		{Node: testNodeB, Trust: adapterproto.PeerDirect, Connected: true, Profile: profile(adapterproto.PeerProfile{
			Name: "build-box", HostName: "build-box", Platform: "linux/amd64", System: "Ubuntu 24.04.1 LTS", SideraVersion: "0.16.0"})},
		{Node: testNodeC, Trust: adapterproto.PeerDirect},
		{Node: testNodeC + "-2", Trust: adapterproto.PeerDirect, Profile: profile(adapterproto.PeerProfile{HostName: "vmwin11", Platform: "windows/arm64"})},
		{Node: testNodeC + "-3", Trust: adapterproto.PeerDirect, Profile: profile(adapterproto.PeerProfile{Name: "lab", Platform: "plan9"})},
	}})
	want := "TRUST CONNECTION NAME SYSTEM SIDERA NODE\n" +
		"self connected imac (imac-1) macOS 27.0.1 · arm64 0.17.0 " + testNode + "\n" +
		"direct connected build-box Ubuntu 24.04.1 LTS · amd64 0.16.0 " + testNodeB + "\n" +
		"direct not connected - - - " + testNodeC + "\n" +
		"direct not connected vmwin11 arm64 - " + testNodeC + "-2\n" +
		"direct not connected lab plan9 - " + testNodeC + "-3\n"
	if squash(out) != squash(want) {
		t.Errorf("peers:\n%s\nwant, up to the spacing:\n%s", out, want)
	}
	if !strings.Contains(out, "NAME") || strings.HasSuffix(strings.Split(out, "\n")[0], " ") {
		t.Errorf("header: %q", strings.Split(out, "\n")[0])
	}
}

// The adapter column says which adapter serves an entry, and the runtime behind it when the
// node knows it; an entry no adapter serves has a dash.
func TestDirectoryShowsTheAdapterOfAnEntry(t *testing.T) {
	out := view(false, false).Directory(adapterproto.DirectoryResult{Outcome: adapterproto.DirectoryOK, Endpoints: []adapterproto.DirectoryEntry{
		{Address: adapterproto.Address(testRuntime), Kind: adapterproto.EndpointRuntime, DisplayName: "work (codex)",
			Accepts: []adapterproto.EndpointAction{adapterproto.ActionSessionCreate},
			Adapter: &adapterproto.DirectoryAdapter{Instance: "work", ID: "codex", Version: "0.17.0",
				Runtime: &adapterproto.RuntimeInfo{Name: "Codex CLI", Version: "0.163.0"}}},
		{Address: adapterproto.Address(testAddress), Kind: adapterproto.EndpointDirect, DisplayName: "Owner console",
			Accepts: []adapterproto.EndpointAction{adapterproto.ActionMessageSend},
			Adapter: &adapterproto.DirectoryAdapter{Instance: "console", ID: "console", Version: "0.17.0"}},
		{Address: adapterproto.Address(testAddress + "-2"), Kind: adapterproto.EndpointDirect, DisplayName: "Fresh",
			Accepts: []adapterproto.EndpointAction{adapterproto.ActionMessageSend},
			Adapter: &adapterproto.DirectoryAdapter{Instance: "fresh", ID: "claude"}},
		{Address: adapterproto.Address(testAddress + "-3"), Kind: adapterproto.EndpointSession,
			Accepts: []adapterproto.EndpointAction{adapterproto.ActionMessageSend}},
	}}, "")
	want := "KIND NAME ACCEPTS ADAPTER ADDRESS\n" +
		"runtime work (codex) session.create codex 0.17.0 · Codex CLI 0.163.0 " + testRuntime + "\n" +
		"direct Owner console message.send console 0.17.0 " + testAddress + "\n" +
		"direct Fresh message.send claude " + testAddress + "-2\n" +
		"session - message.send - " + testAddress + "-3\n"
	if squash(out) != squash(want) {
		t.Errorf("directory:\n%s\nwant, up to the spacing:\n%s", out, want)
	}
}

// A cell with nothing to show is a dash, so that the columns stay in line.
func TestEmptyCellsAreDashes(t *testing.T) {
	out := view(false, false).Directory(adapterproto.DirectoryResult{Outcome: adapterproto.DirectoryOK, Endpoints: []adapterproto.DirectoryEntry{
		{Address: adapterproto.Address(testAddress), Kind: adapterproto.EndpointDirect},
	}}, "")
	if !strings.Contains(squash(out), "direct - - - "+testAddress) {
		t.Errorf("directory:\n%s", out)
	}
}

// The command that reads the next page is spelled by the calling program, so it keeps what
// the program was run with; it names the node that was asked (a cursor belongs to it) and
// quotes a cursor that needs it.
func TestNextPageCommandIsTheCallersOwn(t *testing.T) {
	page := adapterproto.DirectoryResult{Outcome: adapterproto.DirectoryOK, NextCursor: "c-2"}
	v := view(false, false)
	for _, tc := range []struct{ node, want string }{
		{"", "Run `prog directory --instance work --cursor c-2` for the next page."},
		{testNodeB, "Run `prog directory --instance work --node " + testNodeB + " --cursor c-2` for the next page."},
	} {
		if out := v.Directory(page, tc.node); !strings.Contains(out, tc.want) {
			t.Errorf("node %q: %q is missing from\n%s", tc.node, tc.want, out)
		}
	}
}

// The sentence after a send names the calling program's status command, as the program spells
// it, and what it is for follows the kind of the send.
func TestSentNamesTheStatusCommandAndWhatItShows(t *testing.T) {
	v := view(false, false)
	for _, tc := range []struct {
		kind agentview.SentKind
		want string
	}{
		{agentview.SentMessage, "Message accepted for delivery.\n"},
		{agentview.SentReply, "Reply accepted for delivery.\n"},
		{agentview.SentControl, "Control signal accepted for delivery.\n"},
	} {
		out := v.Sent(tc.kind, testMessage, []string{testAddress}, false)
		if !strings.HasPrefix(out, tc.want) || !strings.Contains(out, "Run `prog status --instance work "+testMessage+"` to ") {
			t.Errorf("kind %d:\n%s", tc.kind, out)
		}
		if strings.Contains(out, "accepted before") {
			t.Errorf("kind %d: a first request is said to be a replay:\n%s", tc.kind, out)
		}
	}
	// A reply without a known recipient leaves the To line out.
	if out := v.Sent(agentview.SentReply, testMessage, nil, false); strings.Contains(out, "To") {
		t.Errorf("a send without recipients has a To line:\n%s", out)
	}
}
