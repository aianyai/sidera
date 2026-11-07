package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/aianyai/sidera/sdk/go/termout"
)

// The human output of the management commands that change something. Each is a layout in
// the manner of agent_output.go and manage_read_output.go, and each has a type that reads
// the answer. The conventions they share:
//
//   - The first line says what was done, after the success marker. What follows is only
//     what the owner needs from the answer: the value that is now in force, a restart that
//     is required, a probe that is still running. A line that says what to run next is last.
//   - An identifier that is long (a node, an endpoint, a pairing) is a line of a block and
//     not part of a sentence, so that it is whole and can be copied; the names an owner
//     chose (instances, presets) and the short identifier of an alert are in the sentence.
//   - The operation identifier and the replay flag of the answer are for --json, and for the
//     error of an outcome that is not known. A request that was carried out before, under
//     the same operation, says so in a line of its own after the result: what the result
//     shows is the state now, and nothing changed this time.
//   - Like the other layouts, nothing here goes through the sanitizer: names, identifiers,
//     addresses and enumerations are checked where they enter the node.

// replayNote is the line under the result of a request that had been carried out already.
const replayNote = "This request had already been carried out under the same operation; nothing changed this time."

// changed lays out the result of a change: the marked sentence that says what was done,
// the facts under it, and the line that says what to run next.
func changed(s *termout.Stream, done string, facts []termout.Field, next string) string {
	text := done
	if block := strings.TrimSuffix(s.Block(facts), "\n"); block != "" {
		text += "\n" + block
	}
	if next != "" {
		text += "\n" + next
	}
	return s.OK(text) + "\n"
}

// instanceChange is the answer of the changes of an instance.
type instanceChange struct {
	Instance string `json:"instance"`
	Enabled  bool   `json:"enabled"`
	Removed  bool   `json:"removed"`
}

func renderEnabled(s *termout.Stream, _ request, v instanceChange) string {
	return changed(s, "Enabled instance "+v.Instance+".", nil,
		"Probing in progress. Run "+s.Command("sidera adapter list")+" to see the probe result.")
}

func renderDisabled(s *termout.Stream, _ request, v instanceChange) string {
	return changed(s, "Disabled instance "+v.Instance+".", nil, "")
}

func renderRemoved(s *termout.Stream, _ request, v instanceChange) string {
	return changed(s, "Removed instance "+v.Instance+".", nil, "")
}

// renderConfigured is `adapter config`. A running instance is restarted by the node to take
// the values, which is said only for an instance that is enabled.
func renderConfigured(s *termout.Stream, _ request, v instanceChange) string {
	var note string
	if v.Enabled {
		note = "The instance restarts to take the new configuration."
	}
	return changed(s, "Replaced the configuration of instance "+v.Instance+".", nil, note)
}

// installation is the answer of `adapter install`.
type installation struct {
	Adapter struct {
		ID      string `json:"id"`
		Version string `json:"version"`
	} `json:"adapter"`
	AlreadyCurrent bool `json:"already_current"`
	Replaced       bool `json:"replaced"`
}

// renderInstalled is `adapter install`: a package that was new, one that replaced the
// installed package, or one that was installed already. Only a new adapter has no instance
// yet, so only it is followed by the command that makes one.
func renderInstalled(s *termout.Stream, _ request, v installation) string {
	adapter := v.Adapter.ID + " " + v.Adapter.Version
	switch {
	case v.AlreadyCurrent:
		return changed(s, "Adapter "+adapter+" is already installed; nothing was replaced.", nil, "")
	case v.Replaced:
		return changed(s, "Installed adapter "+adapter+", replacing the package that was installed.", nil, "")
	}
	return changed(s, "Installed adapter "+adapter+".", nil,
		"Run "+s.Command("sidera adapter enable "+v.Adapter.ID)+" to make an instance of it.")
}

// presetChange is the answer of `preset add` and `preset remove`.
type presetChange struct {
	Name string `json:"name"`
}

func renderPresetAdded(s *termout.Stream, _ request, v presetChange) string {
	return changed(s, "Added preset "+v.Name+".", nil, "")
}

func renderPresetRemoved(s *termout.Stream, _ request, v presetChange) string {
	return changed(s, "Removed preset "+v.Name+".", nil, "")
}

// nodeChange is the answer of `node set`: all the settings, of which the request changed
// some.
type nodeChange struct {
	Settings nodeSettings `json:"settings"`
}

// renderNodeSet is `node set`: the settings the request named, as `node show` words them.
func renderNodeSet(s *termout.Stream, req request, v nodeChange) string {
	patch, _ := req.params["settings"].(map[string]any)
	var facts []termout.Field
	for _, f := range v.Settings.fields() {
		if _, named := patch[f.name]; named {
			facts = append(facts, f.Field)
		}
	}
	return changed(s, "Changed the settings of the node.", facts, "")
}

// endpointSettingsChange is the answer of `endpoint set`: what the owner has given the
// endpoint now, which leaves out every setting that follows the node's default.
type endpointSettingsChange struct {
	Endpoint string `json:"endpoint"`
	Settings struct {
		MaxQueueS    int64 `json:"max_queue_s"`
		BacklogLimit int64 `json:"backlog_limit"`
	} `json:"settings"`
}

func renderEndpointSet(s *termout.Stream, _ request, v endpointSettingsChange) string {
	facts := []termout.Field{{Label: "Endpoint", Value: v.Endpoint}}
	st := v.Settings
	if st.MaxQueueS != 0 {
		facts = append(facts, termout.Field{Label: "Queue time", Value: termout.Duration(time.Duration(st.MaxQueueS)*time.Second) + " at most"})
	}
	if st.BacklogLimit != 0 {
		facts = append(facts, termout.Field{Label: "Backlog", Value: plural(st.BacklogLimit, "message", "messages") + " at most"})
	}
	if len(facts) == 1 {
		facts = append(facts, termout.Field{Label: "Settings", Value: "all follow the node's defaults"})
	}
	return changed(s, "Changed the settings of the endpoint.", facts, "")
}

// endpointClosure is the answer of `endpoint close`.
type endpointClosure struct {
	Endpoint string `json:"endpoint"`
	Outcome  string `json:"outcome"`
}

// renderEndpointClosed is `endpoint close`: the outcome is closed, closing (the native
// session of an endpoint the node made is still being released) or already closed.
func renderEndpointClosed(s *termout.Stream, _ request, v endpointClosure) string {
	var done string
	switch v.Outcome {
	case "closing":
		done = "Closing the endpoint; its session is still being released."
	case "already_closed":
		done = "The endpoint was closed already."
	default:
		done = "Closed the endpoint."
	}
	return changed(s, done, []termout.Field{{Label: "Endpoint", Value: v.Endpoint}}, "")
}

// alertAcknowledgement is the answer of `alerts ack`.
type alertAcknowledgement struct {
	Alert string `json:"alert"`
}

func renderAlertAcknowledged(s *termout.Stream, _ request, v alertAcknowledgement) string {
	return changed(s, "Acknowledged alert "+v.Alert+".", nil, "")
}

// peerAddresses is the answer of `network address set` and `network address remove`.
type peerAddresses struct {
	Node      string   `json:"node"`
	Addresses []string `json:"addresses"`
}

func renderPeerAddressesSet(s *termout.Stream, _ request, v peerAddresses) string {
	return changed(s, "Replaced the addresses kept for the node.", []termout.Field{
		{Label: "Node", Value: v.Node},
		{Label: "Addresses", Value: orNone(strings.Join(v.Addresses, "\n"))},
	}, "")
}

func renderPeerAddressesRemoved(s *termout.Stream, _ request, v peerAddresses) string {
	return changed(s, "Removed the addresses kept for the node.", []termout.Field{{Label: "Node", Value: v.Node}}, "")
}

// listenChange is the answer of `network listen set`.
type listenChange struct {
	Listen          []string `json:"listen"`
	RestartRequired bool     `json:"restart_required"`
}

// renderListenSet is `network listen set`: the node opens the new addresses only when it
// starts, so the daemon has to be restarted.
func renderListenSet(s *termout.Stream, _ request, v listenChange) string {
	var next string
	if v.RestartRequired {
		next = "Restart the daemon (" + s.Command("sidera daemon restart") + ") for the new listen addresses to take effect."
	}
	return changed(s, "Replaced the addresses the node listens on.",
		[]termout.Field{{Label: "Listen", Value: orNone(strings.Join(v.Listen, "\n"))}}, next)
}

// nodeExpectation is the answer of `trust expect`.
type nodeExpectation struct {
	Node      string `json:"node"`
	ExpiresAt string `json:"expires_at"`
}

func renderExpected(s *termout.Stream, req request, v nodeExpectation) string {
	return changed(s, "Now expecting a pairing with the node.", []termout.Field{
		{Label: "Node", Value: v.Node},
		{Label: "Expires", Value: timeWithAge(s, req.now, v.ExpiresAt)},
	}, "")
}

// trustedNode is the answer of the changes of trust that name a node and say nothing more
// that is shown: `trust forget`.
type trustedNode struct {
	Node string `json:"node"`
}

func renderForgotten(s *termout.Stream, _ request, v trustedNode) string {
	return changed(s, "Deleted the expectation for the node.", []termout.Field{{Label: "Node", Value: v.Node}}, "")
}

// pairingEnd is the answer of `trust reject`: the request as it is now.
type pairingEnd struct {
	PairingID string `json:"pairing_id"`
	Node      string `json:"node"`
	Status    string `json:"status"`
}

// renderRejected is `trust reject`. The request is rejected when this node held it and
// withdrawn when this node started it; the status says which, in the words trust list uses.
func renderRejected(s *termout.Stream, _ request, v pairingEnd) string {
	return changed(s, "Ended the pairing request.", []termout.Field{
		{Label: "Pairing", Value: v.PairingID},
		{Label: "Node", Value: v.Node},
		{Label: "Status", Value: v.Status},
	}, "")
}

// nodeRemoval is the answer of `trust remove`.
type nodeRemoval struct {
	Node    string   `json:"node"`
	Revoked []string `json:"revoked_credentials"`
}

// renderTrustRemoved is `trust remove`. The other node is not told, which an owner who
// expects it to notice has to know.
func renderTrustRemoved(s *termout.Stream, _ request, v nodeRemoval) string {
	return changed(s, "Removed the node from this node's trust. The other node is not told.", []termout.Field{
		{Label: "Node", Value: v.Node},
		{Label: "Credentials revoked", Value: fmt.Sprint(len(v.Revoked))},
	}, "")
}

// nodeRestoration is the answer of `trust clear`.
type nodeRestoration struct {
	Node               string `json:"node"`
	NewPairingRequired bool   `json:"new_pairing_required"`
}

func renderTrustCleared(s *termout.Stream, _ request, v nodeRestoration) string {
	var note string
	if v.NewPairingRequired {
		note = "Its revoked credentials stay revoked: it is trusted again only after a new pairing."
	}
	return changed(s, "Took the node back into this node's trust.", []termout.Field{{Label: "Node", Value: v.Node}}, note)
}

// backupConfirmation is the answer of `identity confirm-backup`.
type backupConfirmation struct {
	ConfirmedAt string `json:"confirmed_at"`
}

func renderBackupConfirmed(s *termout.Stream, _ request, v backupConfirmation) string {
	return changed(s, "Confirmed that the self-revocation statement is kept outside this device.",
		[]termout.Field{{Label: "Confirmed at", Value: timestamp(v.ConfirmedAt)}}, "")
}

// retirement is the answer of `identity retire`.
type retirement struct {
	Node string `json:"node"`
}

func renderRetired(s *termout.Stream, _ request, v retirement) string {
	return changed(s, "Retired the identity of this node. The daemon is stopping.", []termout.Field{{Label: "Node", Value: v.Node}}, "")
}

// clockClearance is the answer of `clock clear-rollback`.
type clockClearance struct {
	Cleared   bool   `json:"cleared"`
	HighWater string `json:"high_water"`
	Now       string `json:"now"`
}

// renderClockCleared is `clock clear-rollback`: the pause is lifted and the node follows
// the clock as it reads, or there was no pause and nothing changed.
func renderClockCleared(s *termout.Stream, req request, v clockClearance) string {
	if !v.Cleared {
		return changed(s, "The clock was not paused; nothing changed.", nil, "")
	}
	return changed(s, "Accepted the current clock; the pause is lifted.",
		[]termout.Field{{Label: "Clock", Value: timeWithAge(s, req.now, v.Now)}}, "")
}

// renderStopping is `daemon stop`.
func renderStopping(s *termout.Stream, _ request, _ struct{}) string {
	return changed(s, "Daemon is stopping.", nil, "")
}

// forcedRemovalWarning is what `adapter remove --force` tells on the error stream: the
// check of the client setup was skipped, so whatever the setup installed in the client is
// still there. The result of the removal is on the other stream.
func forcedRemovalWarning(s *termout.Stream, instance string) string {
	return s.Warn("The client setup of "+instance+" was not checked, and whatever it installed in the client, if anything, is still there.\n"+
		"To clean it up by hand, see \"Cleaning up an adapter's client by hand\" in the adapters guide.") + "\n"
}
