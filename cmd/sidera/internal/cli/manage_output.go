package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/aianyai/sidera/internal/node/manageapi"
	"github.com/aianyai/sidera/sdk/go/termout"
)

// statusWithHome adds the data directory the command resolved to the node's status.
// The daemon does not report its own: the owner needs the directory that this
// command reached, and the command knows it from the same resolution that chose
// which daemon to ask. No field of the daemon's answer changes.
func statusWithHome(raw []byte, home string) ([]byte, error) {
	answer, err := manageapi.ReadAnswer(raw)
	if err != nil {
		return nil, err
	}
	var result map[string]json.RawMessage
	if err := answer.DecodeResult(&result); err != nil {
		return nil, err
	}
	var envelope map[string]json.RawMessage
	if result == nil || json.Unmarshal(raw, &envelope) != nil {
		return nil, answer.Unreadable()
	}
	encode := func(v any) ([]byte, error) {
		var out bytes.Buffer
		encoder := json.NewEncoder(&out)
		encoder.SetEscapeHTML(false)
		err := encoder.Encode(v)
		return bytes.TrimSuffix(out.Bytes(), []byte("\n")), err
	}
	if result["home"], err = encode(home); err != nil {
		return nil, err
	}
	if envelope["result"], err = encode(result); err != nil {
		return nil, err
	}
	out, err := encode(envelope)
	return append(out, '\n'), err
}

// request is what a layout needs of the request besides the answer: when the answer came,
// for the age of a time, and the params the command sent, for a layout that words the answer
// by what was asked (the settings a `node set` named, whether `alerts list` asked for them all).
type request struct {
	now    time.Time
	params map[string]any
}

// layout turns the answer of one method into the text of its human form. It is made by read
// or change, which read the answer into the type the layout names.
type layout func(s *termout.Stream, req request, raw []byte) (string, error)

// read is the layout of a method that only reads.
func read[V any](render func(s *termout.Stream, req request, v V) string) layout {
	return func(s *termout.Stream, req request, raw []byte) (string, error) {
		var v V
		if err := manageapi.DecodeResult(raw, &v); err != nil {
			return "", err
		}
		return render(s, req, v), nil
	}
}

// change is the layout of a method that changes something. An answer that says its request
// had been carried out before gets the line that says nothing changed this time; the
// operation identifier is not shown (see manage_change_output.go).
func change[V any](render func(s *termout.Stream, req request, v V) string) layout {
	return func(s *termout.Stream, req request, raw []byte) (string, error) {
		answer, err := manageapi.ReadAnswer(raw)
		if err != nil {
			return "", err
		}
		var v V
		if err := answer.DecodeResult(&v); err != nil {
			return "", err
		}
		text := render(s, req, v)
		if answer.Replayed {
			text += s.Warn(replayNote) + "\n"
		}
		return text, nil
	}
}

// humanLayouts is the human form of every method that `sidera` sends and prints the answer
// of. A method that is not here cannot be printed, and managementOutput fails instead of
// showing the answer as it came: TestEveryManagementCommandHasALayout names the commands
// whose methods must be here.
var humanLayouts = map[string]layout{
	"status":         read(func(s *termout.Stream, r request, v statusView) string { return renderNodeStatus(s, r.now, v) }),
	"node.show":      read(func(s *termout.Stream, _ request, v nodeView) string { return renderNodeSettings(s, v) }),
	"network.show":   read(func(s *termout.Stream, _ request, v networkView) string { return renderNetwork(s, v) }),
	"presets.list":   read(func(s *termout.Stream, _ request, v presetsView) string { return renderPresets(s, v) }),
	"adapters.list":  read(func(s *termout.Stream, _ request, v adaptersView) string { return renderAdapters(s, v) }),
	"endpoints.list": read(func(s *termout.Stream, _ request, v endpointsView) string { return renderEndpoints(s, v) }),
	"alerts.list": read(func(s *termout.Stream, r request, v alertsView) string {
		all, _ := r.params["include_acknowledged"].(bool)
		return renderAlerts(s, v, all)
	}),

	"adapters.install":        change(renderInstalled),
	"adapters.enable":         change(renderEnabled),
	"adapters.disable":        change(renderDisabled),
	"adapters.config":         change(renderConfigured),
	"adapters.remove":         change(renderRemoved),
	"presets.add":             change(renderPresetAdded),
	"presets.remove":          change(renderPresetRemoved),
	"node.set":                change(renderNodeSet),
	"endpoints.set":           change(renderEndpointSet),
	"endpoints.close":         change(renderEndpointClosed),
	"alerts.ack":              change(renderAlertAcknowledged),
	"network.address.set":     change(renderPeerAddressesSet),
	"network.address.remove":  change(renderPeerAddressesRemoved),
	"network.listen.set":      change(renderListenSet),
	"trust.expect":            change(renderExpected),
	"trust.forget":            change(renderForgotten),
	"trust.reject":            change(renderRejected),
	"trust.remove":            change(renderTrustRemoved),
	"trust.clear":             change(renderTrustCleared),
	"identity.confirm_backup": change(renderBackupConfirmed),
	"identity.retire":         change(renderRetired),
	"clock.clear_rollback":    change(renderClockCleared),
	"daemon.stop":             change(renderStopping),
}

// managementOutput writes the answer of a management command: the response itself with
// --json, the layout of its method otherwise.
func (c *command) managementOutput(plan managementPlan, raw []byte) error {
	if c.json {
		_, err := c.Stdout.Write(raw)
		return err
	}
	render, ok := humanLayouts[plan.method]
	if !ok {
		return fmt.Errorf("cli: no human output for %s", plan.method)
	}
	text, err := render(c.out, request{now: time.Now(), params: plan.params}, raw)
	if err != nil {
		return err
	}
	_, err = io.WriteString(c.out, text)
	return err
}
