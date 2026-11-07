package cli

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/aianyai/sidera/sdk/go/adaptersetup"
	"github.com/aianyai/sidera/sdk/go/termout"
)

// The human output of `adapter setup`. The plan, the questions and the notes go to standard
// error (`setupDialog`), the report to standard output; the layouts follow the conventions of
// agent_output.go.
//
// A setup program is written by the adapter. Its ids, the status of an outcome and the problem
// of a difference are words that adaptersetup.Result.Validate has held to a single word
// (setupSession.call applies it), so they are shown as they are. The free text is not
// validated: the summary and target of an item, a message, a note and the summary of a
// difference are shown through termout.SanitizeLine, and so is the question built from a
// summary. The tail of a failed program's standard error, which comes from the program or from
// the client it drives, goes through termout.SanitizeText where the command quotes it
// (execSetup).

// itemSummary is the words of an item as they are shown and asked: the adapter's summary,
// which is written as an instruction ("register the sidera MCP server with Claude Code"),
// sanitized. The adapter's action is the verb of that instruction, so it is not written a
// second time.
func itemSummary(it adaptersetup.Item) string { return termout.SanitizeLine(it.Summary) }

// renderSetupPlan is the listing of what a setup or a removal would do: one item for each
// instruction, wrapped, with the place under it when the instruction does not already name it.
func renderSetupPlan(s *termout.Stream, heading string, items []adaptersetup.Item) string {
	var b strings.Builder
	b.WriteString(s.Bold(heading) + "\n")
	width := s.TextWidth() - 2
	for _, it := range items {
		summary := itemSummary(it)
		b.WriteString(termout.Indent(termout.Wrap(summary, width), 2) + "\n")
		if target := termout.SanitizeLine(it.Target); target != "" && !strings.Contains(summary, target) {
			b.WriteString(termout.Indent(s.Dim("at "+target), 4) + "\n")
		}
		if !it.Consent {
			b.WriteString(termout.Indent(s.Dim("(no question: it stays inside the adapter's own state)"), 4) + "\n")
		}
	}
	return b.String()
}

// renderLeftAlone says that an item is neither asked nor done, and why.
func renderLeftAlone(s *termout.Stream, it adaptersetup.Item, why string) string {
	return s.Warn("Left alone: "+itemSummary(it)+" ("+why+")") + "\n"
}

// renderSetupNote is what the adapter advises that is not an item to ask about.
func renderSetupNote(s *termout.Stream, note string) string {
	return termout.Wrap("Note: "+termout.SanitizeLine(strings.Join(strings.Fields(note), " ")), s.TextWidth()) + "\n"
}

// renderDeclined says what the adapter will remember of the answers.
func renderDeclined(s *termout.Stream, instance string, it adaptersetup.Item) string {
	return "Declined: " + itemSummary(it) + "\n" +
		termout.Indent("Update and --check do not report it again; run "+s.Command("sidera adapter setup "+instance)+" to be offered it again.\n", 2)
}

// setupFixCommand is the command that settles the drift of an instance's setup, as a person
// types it: a node whose data directory is not the default is named by --home, as the
// command a client starts for the MCP server names it.
func setupFixCommand(env setupEnv, instance string) string {
	words := []string{"sidera"}
	if !env.homeIsDefault {
		words = append(words, "--home", shellWord(runtime.GOOS, env.home))
	}
	return strings.Join(append(words, "adapter", "setup", instance), " ")
}

// renderSetupReport is the report of a mode. applied says that items were applied, so that
// res is the result of the apply; otherwise a plan or a removal listing was shown and
// declined, or had nothing in it.
func renderSetupReport(s *termout.Stream, mode setupMode, instance string, res adaptersetup.Result, applied bool, fix string) string {
	var headline string
	listed := mode == modeSetup || mode == modeRemove
	switch {
	case listed && !applied && len(res.Items) == 0:
		headline = s.OK("Nothing to do for " + instance + ".")
	case listed && !applied:
		return "Nothing was applied for " + instance + ".\n"
	case mode == modeCheck || mode == modeRefresh:
		headline = checkHeadline(s, mode, instance, res)
	case res.Status == adaptersetup.StatusError && mode == modeRemove:
		headline = s.Fail("Removing the setup of " + instance + " did not complete.")
	case res.Status == adaptersetup.StatusError:
		headline = s.Fail("The setup of " + instance + " did not complete.")
	case mode == modeRemove:
		headline = s.OK("Removed the setup of " + instance + ".")
	default:
		headline = s.OK("Set up " + instance + ".")
	}

	var parts []string
	if res.Recorded != nil {
		recorded := "no"
		if *res.Recorded {
			recorded = "yes"
		}
		parts = append(parts, s.Block([]termout.Field{{Label: "Recorded", Value: recorded}}))
	}
	if len(res.Outcomes) > 0 {
		rows := make([][]string, len(res.Outcomes))
		for i, o := range res.Outcomes {
			status, message := outcomeWords(o)
			result := string(status)
			switch status {
			case adaptersetup.OutcomeSkipped:
				result = s.Yellow(result)
			case adaptersetup.OutcomeFailed:
				result = s.Red(result)
			}
			rows[i] = []string{o.ID, result, termout.SanitizeLine(message)}
		}
		parts = append(parts, s.Table([]string{"Item", "Result", "Message"}, rows))
	}
	if len(res.Drift) > 0 {
		rows := make([][]string, len(res.Drift))
		for i, d := range res.Drift {
			rows[i] = []string{d.ID, string(d.Problem), termout.SanitizeLine(d.Summary)}
		}
		parts = append(parts, s.Table([]string{"Item", "Problem", "Detail"}, rows))
	}
	text := headline + "\n"
	if len(parts) > 0 {
		text += termout.Indent(strings.Join(parts, "\n"), termout.MarkerWidth)
	}
	if res.Op == adaptersetup.OpCheck { // a plan's notes were shown with its questions
		for _, n := range res.Notes {
			text += "\n" + renderSetupNote(s, n)
		}
	}
	if len(res.Drift) > 0 {
		text += "\nRun " + s.Command(fix) + " to settle it.\n"
	}
	return text
}

// checkHeadline says what a check or a refresh found.
func checkHeadline(s *termout.Stream, mode setupMode, instance string, res adaptersetup.Result) string {
	switch res.Status {
	case adaptersetup.StatusDrift:
		return s.Warn(fmt.Sprintf("%s has drifted from its setup: %s.", instance, differences(len(res.Drift))))
	case adaptersetup.StatusError:
		return s.Fail("The refresh of the setup of " + instance + " did not complete.")
	case adaptersetup.StatusNone:
		return s.OK(instance + " has no recorded setup.")
	case adaptersetup.StatusRefreshed:
		return s.OK("Refreshed the setup of " + instance + ".")
	}
	if mode == modeRefresh {
		return s.OK("The setup of " + instance + " is up to date.")
	}
	return s.OK("The setup of " + instance + " is as recorded.")
}

func differences(n int) string {
	if n == 1 {
		return "1 difference"
	}
	return fmt.Sprintf("%d differences", n)
}
