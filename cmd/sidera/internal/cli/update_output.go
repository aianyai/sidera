package cli

import (
	"fmt"
	"strings"

	"github.com/aianyai/sidera/cmd/sidera/internal/update"
	"github.com/aianyai/sidera/sdk/go/termout"
)

// The human output of `update`. The plan, the question and the steps are written to
// standard error and are not decorated beyond a title; the report and the verdict of
// `--check` are standard output. The layouts follow the conventions of agent_output.go.
//
// The words of the client setup (a message, an item's message, a difference's summary)
// are written by the adapter's setup program, and no source checks them, so they go
// through termout.SanitizeLine before they are shown. Everything else is made by the
// program, the daemon or the signed release.

// renderUpdatePlan is what a run is about to do, shown before the question.
func renderUpdatePlan(s *termout.Stream, p update.Plan) string {
	title := "Update sidera " + p.Current + " to " + p.Target
	if p.Current == p.Target {
		title = "Finish the update to sidera " + p.Target
	}
	fields := []termout.Field{{Label: "Page", Value: p.ReleaseURL}}
	if len(p.Replace) > 0 {
		fields = append(fields, termout.Field{Label: "Programs", Value: "replace\n" + strings.Join(p.Replace, "\n")})
	}
	switch {
	case p.Restart:
		fields = append(fields, termout.Field{Label: "Daemon", Value: "restart (running " + p.DaemonVersion + ")"})
	case !p.Daemon:
		fields = append(fields, termout.Field{Label: "Daemon", Value: "not running, so it is not restarted, the adapters are not installed and their setup is not refreshed"})
	}
	var adapters []string
	for _, group := range []struct {
		what  string
		names []string
	}{
		{"install", p.Adapters},
		{"install again (the package cannot run)", p.Faulted},
		{"leave as they are (not in this release)", p.Missing},
		{"install by hand (the package cannot run and is not in this release)", p.FaultedMissing},
	} {
		if len(group.names) > 0 {
			adapters = append(adapters, group.what+": "+strings.Join(group.names, ", "))
		}
	}
	fields = append(fields, termout.Field{Label: "Adapters", Value: strings.Join(adapters, "\n")})
	if p.RefreshSetup {
		fields = append(fields, termout.Field{Label: "Setup", Value: "refresh what adapters recorded in their clients, where it is out of date"})
	}
	text := s.Bold(title) + "\n" + termout.Indent(s.Block(fields), 2)
	if !p.Daemon {
		text += termout.Indent("Start the daemon and run "+s.Command("sidera update")+" again once it runs.\n", 2)
	}
	return text
}

// renderUpdateCheck is the verdict of `--check`: whether an update is available and, if it
// is, whether this program may apply it and, when it may not, why.
func renderUpdateCheck(s *termout.Stream, v update.Verdict) string {
	facts := []termout.Field{
		{Label: "Running", Value: v.Current},
		{Label: "Target", Value: v.Target},
		{Label: "Page", Value: v.ReleaseURL},
		{Label: "Floor", Value: floorText(v.Floor)},
	}
	var headline string
	switch {
	case v.Refusal != "":
		headline = s.Warn(fmt.Sprintf("An update to %s cannot be applied: %s", v.Target, v.Refusal))
	case v.UpdateAvailable:
		headline = s.OK(fmt.Sprintf("An update is available: %s to %s, and this program may apply it.", v.Current, v.Target))
	case v.Current == v.Target:
		headline = s.OK(fmt.Sprintf("sidera %s is up to date.", v.Current))
	default:
		headline = s.OK(fmt.Sprintf("No update is available: release %s is not newer than the running %s.", v.Target, v.Current))
	}
	text := headline + "\n" + termout.Indent(s.Block(facts), termout.MarkerWidth)
	if v.UpdateAvailable && v.Refusal == "" {
		text += "\nRun " + s.Command("sidera update") + " to apply it.\n"
	}
	return text
}

// renderUpdateReport is what a run did. runFailed says that the run ended with an error,
// which is printed after the report: the report then says that it stopped, and what was
// done before.
func renderUpdateReport(s *termout.Stream, rep update.Report, runFailed bool) string {
	var headline string
	switch {
	case runFailed:
		headline = s.Warn("The update to " + rep.Target + " stopped before it finished. This is what was done.")
	case rep.AlreadyCurrent:
		headline = s.OK("Already up to date at " + rep.Target + ".")
	case !rep.Daemon.Running && !rep.Daemon.Restarted && (rep.Program.Updated || len(rep.Program.Restored) > 0):
		headline = s.OK("Updated the programs to " + rep.Target + ".")
	case !rep.Daemon.Running && !rep.Daemon.Restarted:
		// Nothing was done: the programs were current, and with no daemon the adapters were not looked at.
		headline = s.OK("The programs are already at " + rep.Target + " and the daemon is not running.")
	default:
		headline = s.OK("Updated to " + rep.Target + ".")
	}
	var b strings.Builder
	b.WriteString(headline + "\n")
	fields := []termout.Field{
		{Label: "Programs", Value: programsText(rep.Program)},
		{Label: "Daemon", Value: daemonText(rep.Daemon)},
		{Label: "Adapters", Value: adaptersText(rep.Adapters)},
		{Label: "Setup", Value: setupText(s, rep.Setup)},
	}
	b.WriteString(termout.Indent(s.Block(fields), termout.MarkerWidth))

	var hints []string
	if rep.Program.Updated {
		hints = append(hints, "The previous programs are kept as <name>.old, to go back to; the next update that replaces the programs removes them.")
	}
	if rep.Program.ConsoleMissing {
		hints = append(hints, "sidera-adapter-console was not found next to the program, so it was not installed.")
	}
	for _, a := range rep.Adapters {
		if a.Replaced {
			hints = append(hints, "The old version directories of replaced adapters are removed at the next daemon start.")
			break
		}
	}
	if !rep.Daemon.Running && !rep.Daemon.Restarted {
		hints = append(hints, "Start the daemon, then run "+s.Command("sidera update")+" again to install the adapters.")
	}
	for _, st := range rep.Setup {
		if st.Fix != "" {
			hints = append(hints, "Run "+s.Command(st.Fix)+" to settle the setup of "+st.Instance+".")
		}
	}
	if len(hints) > 0 {
		b.WriteString("\n" + strings.Join(hints, "\n") + "\n")
	}
	return b.String()
}

func programsText(p update.ProgramReport) string {
	var lines []string
	if len(p.Restored) > 0 {
		lines = append(lines, "restored (an earlier update had taken them from their names):\n"+strings.Join(p.Restored, "\n"))
	}
	if p.Updated {
		lines = append(lines, "replaced\n"+strings.Join(p.Replaced, "\n"))
	}
	if len(p.Left) > 0 {
		lines = append(lines, "left (a running process still uses them; the next update removes them):\n"+strings.Join(p.Left, "\n"))
	}
	if len(lines) == 0 {
		return "current"
	}
	return strings.Join(lines, "\n")
}

func daemonText(d update.DaemonReport) string {
	switch {
	case d.Restarted:
		return "restarted, running " + d.Version
	case d.Running:
		return "running " + d.Previous
	}
	return "not running: it was not restarted and the adapters were not looked at"
}

func adaptersText(adapters []update.AdapterReport) string {
	lines := make([]string, len(adapters))
	for i, a := range adapters {
		switch {
		case a.Error != "":
			lines[i] = a.ID + " failed: " + a.Error
		case a.Skipped != "":
			lines[i] = a.ID + " skipped: " + a.Skipped
		default:
			state := "already current"
			if a.Replaced {
				state = "replaced"
			}
			if a.Faulted {
				state = "was faulted, installed again"
				if a.Replaced {
					state += " and replaced"
				}
			}
			lines[i] = a.ID + " " + a.Version + " " + state
		}
	}
	return strings.Join(lines, "\n")
}

// setupText is one entry for each instance, with what the refresh did under it. A message
// of the adapter's setup program is sanitized: nothing checks it where it is made.
func setupText(s *termout.Stream, setups []update.SetupReport) string {
	var lines []string
	for _, st := range setups {
		var head string
		switch st.Status {
		case update.SetupRefreshed:
			head = st.Instance + " refreshed"
		case update.SetupNone:
			head = st.Instance + " has no recorded setup"
		case update.SetupDrift:
			head = st.Instance + " has drifted from its setup"
		case update.SetupSkipped:
			head = st.Instance + " skipped: " + termout.SanitizeLine(st.Message)
		case update.SetupError:
			head = st.Instance + " failed: " + termout.SanitizeLine(st.Message)
		default:
			head = st.Instance + " " + string(st.Status)
		}
		var under []string
		for _, it := range st.Items {
			line := it.ID + " " + it.Status
			if it.Message != "" {
				line += ": " + termout.SanitizeLine(it.Message)
			}
			under = append(under, line)
		}
		for _, d := range st.Drift {
			under = append(under, d.ID+" "+d.Problem+": "+termout.SanitizeLine(d.Summary))
		}
		if len(under) > 0 {
			head += "\n" + termout.Indent(strings.Join(under, "\n"), 2)
		}
		lines = append(lines, head)
	}
	return strings.Join(lines, "\n")
}

// floorText says what the signed floor of a release means, or nothing when it has none.
func floorText(floor string) string {
	if floor == "" {
		return ""
	}
	return floor + " (the oldest version that may update to this release)"
}
