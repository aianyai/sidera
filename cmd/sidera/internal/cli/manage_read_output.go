package cli

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/aianyai/sidera/sdk/go/termout"
)

// The human output of the management commands that only read: `status`, `node show`,
// `network show` and the lists `preset list`, `adapter list`, `endpoint list` and
// `alerts list`. Each is a layout in the manner of agent_output.go (a function from the
// stream and the daemon's answer to text, sharing its conventions), and each has a type
// that reads the answer, which is the one place the CLI names the answer's fields.
//
// A field the answer gains has to be given a human form here, or deliberately left to
// --json and listed below: a type that does not name it drops it from the human output
// without a sound.
//
// What stays out of the human form, and is in --json: the start identifier of the daemon,
// the seconds a certificate has left (the date is shown), the values of a preset's
// configuration (the keys are), the state of every delivery that is zero, and the
// address and kind of each endpoint in `status` (`endpoint list` has them).
//
// Text that no source checks goes through termout.SanitizeLine: the detail an adapter
// writes for an availability problem, an alert's subject and detail (which can quote what a
// peer sent), and a preset's description and the names of its settings. Names, identifiers,
// versions and enumerations are checked where they enter the node.

// versionNote is the line that gives the version of a collection: a change names the one it
// is based on with --expected-version, and when the owner leaves it out the command reads
// the current one itself, so the number is only for an owner who wants a change to fail
// when the collection moved in between.
func versionNote(what string, version uint64) termout.Field {
	return termout.Field{Label: what + " version", Value: fmt.Sprintf("%d (for --expected-version)", version)}
}

// wireTime reads a time as the management interface writes one. A text that is not a time
// is returned as the zero time: the daemon is the only writer, and a field that is missing
// reads as no time.
func wireTime(text string) time.Time {
	t, err := time.Parse(time.RFC3339, text)
	if err != nil {
		return time.Time{}
	}
	return t
}

// timestamp is a wire time without its distance from now, for a table; empty when there is none.
func timestamp(text string) string {
	t := wireTime(text)
	if t.IsZero() {
		return ""
	}
	return termout.Timestamp(t)
}

// timeWithAge is a wire time with its distance from now; empty when there is none.
func timeWithAge(s *termout.Stream, now time.Time, text string) string {
	t := wireTime(text)
	if t.IsZero() {
		return ""
	}
	return s.Time(t, now)
}

// plural writes a count with its noun: "1 day", "30 days".
func plural(n int64, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// orNone is the text, or "none" when it is empty.
func orNone(text string) string {
	if text == "" {
		return "none"
	}
	return text
}

// sortedNames are the keys of a map in order.
func sortedNames[V any](m map[string]V) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// statusView is the answer of `status`, with `home`, which the command adds.
type statusView struct {
	Node struct {
		ID      string `json:"id"`
		Version string `json:"version"`
	} `json:"node"`
	Daemon struct {
		State     string `json:"state"`
		StartedAt string `json:"started_at"`
		StartID   string `json:"start_id"`
	} `json:"daemon"`
	Reminders []struct {
		Kind    string `json:"kind"`
		Message string `json:"message"`
	} `json:"reminders"`
	Alerts struct {
		Open int `json:"open"`
	} `json:"alerts"`
	Clock struct {
		RollbackPaused      bool   `json:"rollback_paused"`
		RollbackPausedSince string `json:"rollback_paused_since"`
		HighWater           string `json:"high_water"`
	} `json:"clock"`
	Certificate struct {
		NotAfter   string `json:"not_after"`
		RemainingS int64  `json:"remaining_s"`
	} `json:"certificate"`
	Capacity struct {
		OpenDeliveries   int   `json:"open_deliveries"`
		NodeQueueLimit   int   `json:"node_queue_limit"`
		OutboundBacklog  int   `json:"outbound_backlog"`
		DiskFreeBytes    int64 `json:"disk_free_bytes"`
		DiskReserveBytes int64 `json:"disk_reserve_bytes"`
	} `json:"capacity"`
	Deliveries map[string]int `json:"deliveries"`
	Endpoints  []struct {
		Address  string `json:"address"`
		Kind     string `json:"kind"`
		Instance string `json:"instance"`
		State    string `json:"state"`
	} `json:"endpoints"`
	Home string `json:"home"`
}

// renderNodeStatus is `status`: what the owner has to do first, as warnings, then the
// facts of the node in groups. now is what the times are counted from.
func renderNodeStatus(s *termout.Stream, now time.Time, v statusView) string {
	var b strings.Builder
	for _, r := range v.Reminders {
		message := r.Message
		if message == "" {
			message = r.Kind
		}
		b.WriteString(s.Warn(termout.Wrap(message, s.TextWidth()-termout.MarkerWidth)) + "\n")
	}
	if len(v.Reminders) > 0 {
		b.WriteString("\n")
	}
	daemon := v.Daemon.State
	if v.Node.Version != "" {
		daemon += ", version " + v.Node.Version
	}
	if started := timeWithAge(s, now, v.Daemon.StartedAt); started != "" {
		daemon += ", started " + started
	}
	deliveries := fmt.Sprintf("%d open (limit %d), %d waiting to be sent", v.Capacity.OpenDeliveries, v.Capacity.NodeQueueLimit, v.Capacity.OutboundBacklog)
	var states []string
	for _, name := range sortedNames(v.Deliveries) {
		if n := v.Deliveries[name]; n != 0 {
			states = append(states, fmt.Sprintf("%s %d", name, n))
		}
	}
	alerts := "none"
	if v.Alerts.Open > 0 {
		alerts = fmt.Sprintf("%d open, see %s", v.Alerts.Open, s.Command("sidera alerts list"))
	}
	b.WriteString(s.Block([]termout.Field{
		{Label: "Node", Value: v.Node.ID},
		{Label: "Daemon", Value: daemon},
		{Label: "Home", Value: v.Home},
		{Label: "Certificate", Value: certificateText(s, now, v)},
		{Label: "Clock", Value: clockText(s, now, v)},
		{Label: "Deliveries", Value: deliveries},
		{Label: "Delivery states", Value: strings.Join(states, ", ")},
		{Label: "Disk", Value: fmt.Sprintf("%s free, %s kept in reserve", termout.Bytes(v.Capacity.DiskFreeBytes), termout.Bytes(v.Capacity.DiskReserveBytes))},
		{Label: "Endpoints", Value: endpointSummary(v)},
		{Label: "Alerts", Value: alerts},
	}))
	return b.String()
}

func certificateText(s *termout.Stream, now time.Time, v statusView) string {
	until := timeWithAge(s, now, v.Certificate.NotAfter)
	switch {
	case until == "":
		return "none"
	case v.Certificate.RemainingS <= 0:
		return "expired " + until
	}
	return "valid until " + until
}

// clockText says whether the node's clock is trusted. While it is paused the node accepts
// nothing that is signed or that depends on a deadline, and the owner has one command to
// run, so the text names it.
func clockText(s *termout.Stream, now time.Time, v statusView) string {
	seen := ""
	if t := timeWithAge(s, now, v.Clock.HighWater); t != "" {
		seen = "latest time seen " + t
	}
	if !v.Clock.RollbackPaused {
		if seen == "" {
			return "ok"
		}
		return "ok, " + seen
	}
	lines := []string{"paused: the wall clock was set back"}
	if since := timeWithAge(s, now, v.Clock.RollbackPausedSince); since != "" {
		lines[0] += ", since " + since
	}
	if seen != "" {
		lines = append(lines, seen)
	}
	return strings.Join(append(lines, "Run "+s.Command("sidera clock clear-rollback")+" to accept the current clock."), "\n")
}

// endpointSummary counts the endpoints by state; `endpoint list` has each one.
func endpointSummary(v statusView) string {
	if len(v.Endpoints) == 0 {
		return "none"
	}
	byState := map[string]int{}
	for _, ep := range v.Endpoints {
		byState[ep.State]++
	}
	parts := make([]string, 0, len(byState))
	for _, state := range sortedNames(byState) {
		parts = append(parts, fmt.Sprintf("%d %s", byState[state], state))
	}
	return fmt.Sprintf("%d (%s)", len(v.Endpoints), strings.Join(parts, ", "))
}

// nodeView is the answer of `node show`.
type nodeView struct {
	Node     string `json:"node"`
	Version  uint64 `json:"version"`
	Settings struct {
		ReplyPermission      bool  `json:"reply_permission"`
		NodeQueueLimit       int64 `json:"node_queue_limit"`
		DiskReserveBytes     int64 `json:"disk_reserve_bytes"`
		NodePromptTurns      int64 `json:"node_prompt_turns"`
		ArchiveRetentionDays int64 `json:"archive_retention_days"`
		LogTotalBytes        int64 `json:"log_total_bytes"`
		EndpointDefaults     struct {
			MaxQueueS    int64 `json:"max_queue_s"`
			BacklogLimit int64 `json:"backlog_limit"`
		} `json:"endpoint_defaults"`
	} `json:"settings"`
}

// renderNodeSettings is `node show`: each setting under a label that says what it is, with
// its unit. The names that `node set` takes are in --json.
func renderNodeSettings(s *termout.Stream, v nodeView) string {
	st := v.Settings
	replies := "refused"
	if st.ReplyPermission {
		replies = "allowed"
	}
	return s.Block([]termout.Field{
		{Label: "Node", Value: v.Node},
		{Label: "Unsolicited replies", Value: replies},
		{Label: "Delivery limit", Value: plural(st.NodeQueueLimit, "unfinished delivery", "unfinished deliveries")},
		{Label: "Disk reserve", Value: termout.Bytes(st.DiskReserveBytes) + " (new work is refused when less is free)"},
		{Label: "Concurrent prompt turns", Value: fmt.Sprint(st.NodePromptTurns)},
		{Label: "Archive kept for", Value: plural(st.ArchiveRetentionDays, "day", "days")},
		{Label: "Log space", Value: termout.Bytes(st.LogTotalBytes) + " at most"},
		{Label: "Endpoint queue time", Value: termout.Duration(time.Duration(st.EndpointDefaults.MaxQueueS)*time.Second) + " at most"},
		{Label: "Endpoint backlog", Value: plural(st.EndpointDefaults.BacklogLimit, "message", "messages") + " at most"},
		versionNote("Settings", v.Version),
	})
}

// networkView is the answer of `network show`.
type networkView struct {
	Node    string `json:"node"`
	PeerID  string `json:"peer_id"`
	Version uint64 `json:"version"`
	Listen  struct {
		Configured []string `json:"configured"`
		Actual     []string `json:"actual"`
	} `json:"listen"`
	Contact string `json:"contact"`
	Peers   []struct {
		Node            string   `json:"node"`
		Addresses       []string `json:"addresses"`
		CertificateHeld bool     `json:"certificate_held"`
		Connected       bool     `json:"connected"`
	} `json:"peers"`
}

// renderNetwork is `network show`. The addresses that reach this node are each on a line of
// their own, so that the ones another machine cannot use are easy to leave out. Below them is
// the command that enters this node on another machine, on one line that can be copied
// whole (so it is not in backticks, which a shell would run): the node, then the addresses,
// which is what `network address set` takes.
func renderNetwork(s *termout.Stream, v networkView) string {
	// The contact line is `<node id> <multiaddr>...`.
	reach := strings.Join(strings.Fields(v.Contact), " ")
	_, addresses, _ := strings.Cut(reach, " ")
	fields := []termout.Field{
		{Label: "Node", Value: v.Node},
		{Label: "Peer ID", Value: v.PeerID},
		{Label: "Listen configured", Value: orNone(strings.Join(v.Listen.Configured, "\n"))},
		{Label: "Listening now", Value: orNone(strings.Join(v.Listen.Actual, "\n"))},
		{Label: "Reachable at", Value: orNone(strings.ReplaceAll(addresses, " ", "\n"))},
		versionNote("Network", v.Version),
	}
	text := s.Block(fields)
	if reach != "" {
		text += "\nTo enter this node on another machine, run there:\n" + termout.Indent(s.Cyan("sidera network address set "+reach), 2) + "\n"
	}
	if len(v.Peers) == 0 {
		return text + "\nNo other nodes are known.\n"
	}
	rows := make([][]string, len(v.Peers))
	for i, p := range v.Peers {
		connection, certificate := "not connected", "not held"
		if p.Connected {
			connection = "connected"
		}
		if p.CertificateHeld {
			certificate = "held"
		}
		rows[i] = []string{connection, certificate, strings.Join(append([]string{p.Node}, p.Addresses...), "\n")}
	}
	return text + "\n" + s.Bold("Known nodes") + "\n" + s.Table([]string{"Connection", "Certificate", "Node and its addresses"}, rows)
}

// presetsView is the answer of `preset list`.
type presetsView struct {
	Version uint64 `json:"version"`
	Presets []struct {
		Name         string                    `json:"name"`
		LocalSources string                    `json:"local_sources"`
		Description  string                    `json:"description"`
		Config       map[string]map[string]any `json:"config"`
	} `json:"presets"`
}

// renderPresets is `preset list`. The settings a preset gives each adapter are shown by
// name; their values can be long instructions, and are in --json.
func renderPresets(s *termout.Stream, v presetsView) string {
	var text string
	if len(v.Presets) == 0 {
		text = "No presets.\n"
	} else {
		rows := make([][]string, len(v.Presets))
		for i, p := range v.Presets {
			var settings []string
			for _, adapter := range sortedNames(p.Config) {
				// The adapter is checked where the preset enters the node; the names of its settings are not.
				names := sortedNames(p.Config[adapter])
				for i, name := range names {
					names[i] = termout.SanitizeLine(name)
				}
				settings = append(settings, adapter+": "+strings.Join(names, ", "))
			}
			rows[i] = []string{p.Name, p.LocalSources, termout.SanitizeLine(p.Description), strings.Join(settings, "\n")}
		}
		text = s.Table([]string{"Name", "Local sources", "Description", "Adapter settings"}, rows)
	}
	return text + "\n" + s.Block([]termout.Field{versionNote("Presets", v.Version)})
}

// adaptersView is the answer of `adapter list`.
type adaptersView struct {
	Version  uint64 `json:"version"`
	Adapters []struct {
		ID      string      `json:"id"`
		Version string      `json:"version"`
		Fault   *faultState `json:"fault"`
	} `json:"adapters"`
	Instances []struct {
		Instance string      `json:"instance"`
		Adapter  string      `json:"adapter"`
		Enabled  bool        `json:"enabled"`
		Fault    *faultState `json:"fault"`
		Probe    *struct {
			State     string `json:"state"`
			At        string `json:"at"`
			Frontends []struct {
				Preset   string `json:"preset"`
				Frontend string `json:"frontend"`
			} `json:"frontends"`
		} `json:"probe"`
		Availability []struct {
			Condition string   `json:"condition"`
			Endpoints []string `json:"endpoints"`
			Detail    string   `json:"detail"`
		} `json:"availability"`
	} `json:"instances"`
}

type faultState struct {
	Reason string `json:"reason"`
}

// renderAdapters is `adapter list`: a row for each instance, then one for each installed
// adapter that has none. What keeps an instance from working is a warning under the row's
// probe: a package that cannot run, and each availability condition its adapter reports,
// with the adapter's own words sanitized since no source checks them. A probe that has not
// passed says which front ends it did reach.
func renderAdapters(s *termout.Stream, v adaptersView) string {
	if len(v.Adapters) == 0 && len(v.Instances) == 0 {
		return "No adapters are installed.\n\n" + s.Block([]termout.Field{versionNote("Instances", v.Version)})
	}
	versions, faults := map[string]string{}, map[string]*faultState{}
	for _, a := range v.Adapters {
		versions[a.ID], faults[a.ID] = a.Version, a.Fault
	}
	used := map[string]bool{}
	faultWarning := func(f *faultState) string { return s.Warn("package cannot run: " + f.Reason) }
	var rows [][]string
	for _, in := range v.Instances {
		used[in.Adapter] = true
		state, probe := "disabled", []string{"-"}
		if in.Enabled {
			state = "enabled"
		}
		if p := in.Probe; p != nil {
			probe = []string{strings.TrimSpace(p.State + " " + timestamp(p.At))}
			if p.State != "passed" && len(p.Frontends) > 0 {
				reached := make([]string, len(p.Frontends))
				for i, f := range p.Frontends {
					reached[i] = fmt.Sprintf("%s (%s)", f.Preset, f.Frontend)
				}
				probe = append(probe, "reached "+strings.Join(reached, ", "))
			}
		}
		var warnings []string
		for _, f := range []*faultState{faults[in.Adapter], in.Fault} {
			if f != nil && !slices.Contains(warnings, faultWarning(f)) {
				warnings = append(warnings, faultWarning(f))
			}
		}
		for _, a := range in.Availability {
			text := a.Condition
			if len(a.Endpoints) > 0 {
				text += " on " + strings.Join(a.Endpoints, ", ")
			}
			if a.Detail != "" {
				text += ": " + termout.SanitizeLine(a.Detail)
			}
			warnings = append(warnings, s.Warn(text))
		}
		rows = append(rows, []string{in.Instance, in.Adapter, versions[in.Adapter], state, strings.Join(append(probe, warnings...), "\n")})
	}
	for _, a := range v.Adapters {
		if used[a.ID] {
			continue
		}
		cell := "-"
		if a.Fault != nil {
			cell += "\n" + faultWarning(a.Fault)
		}
		rows = append(rows, []string{"", a.ID, a.Version, "no instance", cell})
	}
	return s.Table([]string{"Instance", "Adapter", "Version", "State", "Probe"}, rows) + "\n" + s.Block([]termout.Field{versionNote("Instances", v.Version)})
}

// endpointsView is the answer of `endpoint list`.
type endpointsView struct {
	Endpoints []struct {
		Address  string `json:"address"`
		Kind     string `json:"kind"`
		Instance string `json:"instance"`
		State    string `json:"state"`
		Queued   int    `json:"queued"`
		Draining bool   `json:"draining"`
		Origin   string `json:"origin"`
	} `json:"endpoints"`
}

// renderEndpoints is `endpoint list`, the address last.
func renderEndpoints(s *termout.Stream, v endpointsView) string {
	if len(v.Endpoints) == 0 {
		return "No endpoints.\n"
	}
	rows := make([][]string, len(v.Endpoints))
	for i, ep := range v.Endpoints {
		kind, state := ep.Kind, ep.State
		if ep.Origin != "" {
			kind += " (" + ep.Origin + ")"
		}
		if ep.Draining {
			state += " (draining)"
		}
		rows[i] = []string{kind, ep.Instance, state, fmt.Sprint(ep.Queued), ep.Address}
	}
	return s.Table([]string{"Kind", "Instance", "State", "Queued", "Address"}, rows)
}

// alertsView is the answer of `alerts list`.
type alertsView struct {
	Alerts []struct {
		ID           string `json:"id"`
		Kind         string `json:"kind"`
		Since        string `json:"since"`
		Subject      string `json:"subject"`
		Detail       string `json:"detail"`
		Acknowledged bool   `json:"acknowledged"`
	} `json:"alerts"`
}

// renderAlerts is `alerts list`. The identifier, which `alerts ack` takes, is whole; the
// detail, which is free text and can quote what a peer sent, comes last with the subject
// under it, both sanitized. all says that the acknowledged alerts were asked for too, which
// changes what an empty list means.
func renderAlerts(s *termout.Stream, v alertsView, all bool) string {
	if len(v.Alerts) == 0 {
		if all {
			return "No alerts.\n"
		}
		return "No open alerts.\n"
	}
	rows := make([][]string, len(v.Alerts))
	for i, a := range v.Alerts {
		state := "open"
		if a.Acknowledged {
			state = "acknowledged"
		}
		detail := termout.SanitizeLine(a.Detail)
		if a.Subject != "" {
			detail += "\nabout " + termout.SanitizeLine(a.Subject)
		}
		rows[i] = []string{timestamp(a.Since), a.Kind, state, a.ID, detail}
	}
	return s.Table([]string{"Since", "Kind", "State", "ID", "Detail"}, rows)
}
