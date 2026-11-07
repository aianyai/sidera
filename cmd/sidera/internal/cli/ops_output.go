package cli

import (
	"strings"

	"github.com/aianyai/sidera/sdk/go/termout"
)

// The human output of the commands that run an operation rather than read a value:
// `daemon restart`, `init`, `identity export-revocation`, `trust list`, `trust request`
// and the controller notice. They follow the conventions of agent_output.go: a line that
// says what was done, blocks of labelled lines, tables with the identifier last, protocol
// words as the protocol names them, identifiers whole. Everything printed here is made by the
// program or by a source that checks it where it enters the node (node identifiers,
// pairing states and reasons, codes), except the controller notice, whose detail an
// adapter writes.

// renderRestarting is what `daemon restart` says as soon as the daemon has accepted the
// restart, while the new one is not yet up: what is being started.
func renderRestarting(s *termout.Stream, r restartResult) string {
	return "Restarting the daemon.\n" + termout.Indent(s.Block([]termout.Field{
		{Label: "Program", Value: r.Program},
		{Label: "Version", Value: r.Version},
	}), 2)
}

// renderRestarted is what `daemon restart` ends with. waited says whether the command
// waited for the new daemon; without a wait the command knows only that the restart was
// accepted. A replayed request is one the daemon had already carried out: it is not
// restarted again, which the person who sees no new daemon needs to know.
func renderRestarted(s *termout.Stream, r restartResult, waited bool) string {
	var text string
	if waited {
		text = s.OK("The daemon is running again, version "+r.Version+".") + "\n"
	} else {
		text = s.OK("The daemon accepted the restart and is starting again.\nRun "+s.Command("sidera status")+" to see when it runs.") + "\n"
	}
	if r.Replayed {
		text += s.Warn("This request had already been carried out under the same operation; the daemon was not restarted again.") + "\n"
	}
	return text
}

// renderInitialized is `init`: the reminder that is still to be done comes first.
func renderInitialized(s *termout.Stream, node, home string) string {
	return s.Warn("Back up your self-revocation outside this device.") + "\n\n" +
		s.OK("Initialized this node.\n"+s.Block([]termout.Field{
			{Label: "Node", Value: node},
			{Label: "Home", Value: home},
		})) + "\nRun " + s.Command("sidera identity export-revocation") + " to export it.\n"
}

// renderExportReminder is what `identity export-revocation` tells the owner on standard
// error, so that the statement itself, on standard output, stays what a script redirects.
func renderExportReminder(s *termout.Stream, digest string) string {
	return s.Warn("Keep this self-revocation outside this device,\nthen run "+s.Command("sidera identity confirm-backup "+digest)+".") + "\n"
}

// renderControllerNotice is the notice of a session that is attached as a controller but
// cannot be woken. The cause includes text the adapter wrote, so the notice is sanitized.
func renderControllerNotice(s *termout.Stream, notice string) string {
	return s.Warn(termout.Wrap(termout.SanitizeLine(notice), s.TextWidth()-termout.MarkerWidth)) + "\n"
}

// renderPairingRequested is `trust request` once the node has answered. The verification
// code comes first and on a line of its own: the owner of the other node is told it while
// this command waits, and it is read aloud as it is written ("482 019").
func renderPairingRequested(s *termout.Stream, a pairingAnswer, replayed bool) string {
	text := s.Bold("Verification code: "+a.Code) + "\n\n" +
		s.OK("Pairing requested.\n"+s.Block([]termout.Field{
			{Label: "Node", Value: a.Node},
			{Label: "Pairing", Value: a.PairingID},
			{Label: "Status", Value: a.Status},
		})) + "\n" +
		"The code is for information: trust is complete when both nodes expect each other (" + s.Command("sidera trust expect") + ").\n" +
		"To cancel a request that is stuck, run " + s.Command("sidera trust reject "+a.PairingID) + ".\n"
	if replayed {
		text += s.Warn("This request had already been carried out under the same operation; nothing was sent again.") + "\n"
	}
	return text
}

// renderPaired closes `trust request` once the pairing is complete.
func renderPaired(s *termout.Stream, node string) string {
	return s.OK("Paired with "+node+".") + "\n"
}

// renderTrust is `trust list`: the five sections, each a titled table or one sentence that
// says it is empty. The columns that say something come first, the identifiers last.
func renderTrust(s *termout.Stream, v trustView) string {
	var sections []string
	section := func(title, empty string, header []string, rows [][]string) {
		text := s.Bold(title) + "\n"
		if len(rows) == 0 {
			text += empty + "\n"
		} else {
			text += s.Table(header, rows)
		}
		sections = append(sections, text)
	}

	rows := make([][]string, len(v.Trusted))
	for i, t := range v.Trusted {
		rows[i] = []string{connection(t.Connected), timestamp(t.CreatedAt), orNever(timestamp(t.NotAfter)), t.Credential, t.Node}
	}
	section("Trusted nodes", "No node is trusted.", []string{"Connection", "Created", "Valid until", "Credential", "Node"}, rows)

	rows = make([][]string, len(v.Expectations))
	for i, e := range v.Expectations {
		rows[i] = []string{timestamp(e.ExpiresAt), e.Node}
	}
	section("Expectations", "No node is expected.", []string{"Expires", "Node"}, rows)

	rows = make([][]string, len(v.Incoming))
	for i, in := range v.Incoming {
		expected := "not expected"
		if in.Expected {
			expected = "expected"
		}
		rows[i] = []string{in.Status, in.Code, expected, timestamp(in.ExpiresAt), in.PairingID, in.Node}
	}
	section("Incoming requests", "No request is waiting for this node.", []string{"Status", "Code", "Expectation", "Expires", "Pairing", "Node"}, rows)

	rows = make([][]string, len(v.Outgoing))
	for i, out := range v.Outgoing {
		rows[i] = []string{out.Status, out.Code, out.Reason, out.PairingID, out.Node}
	}
	section("Outgoing requests", "This node has made no request.", []string{"Status", "Code", "Reason", "Pairing", "Node"}, rows)

	rows = make([][]string, len(v.LocallyRevoked))
	for i, n := range v.LocallyRevoked {
		rows[i] = []string{n}
	}
	section("Revoked here", "No node is revoked here.", []string{"Node"}, rows)

	return strings.Join(sections, "\n")
}

// connection is a boolean as the word the person reads.
func connection(connected bool) string {
	if connected {
		return "connected"
	}
	return "not connected"
}

// orNever is the text, or "never" when it is empty: a credential that does not expire.
func orNever(text string) string {
	if text == "" {
		return "never"
	}
	return text
}
