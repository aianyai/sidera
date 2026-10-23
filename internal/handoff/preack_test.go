package handoff

import (
	"strings"
	"testing"
)

func TestCheckBeforeAck(t *testing.T) {
	out := "{\"messages\":[{\"id\":\"m1\",\"from\":\"aa/bb\",\"body\":\"hello\"}],\"more\":0}\n"
	const (
		claudeNotice = "<persisted-output>\nOutput too large (34.5KB). Full output saved to: /tmp/tool-results/abc.txt\n\n" +
			"Preview (first 2KB):\n{\"messages\":[{\"id\":\"m1\",\"from\":\"aa/bb\",\"bo\n...\n</persisted-output>"
	)
	// A body that talks about truncation is part of the expected output too.
	talk := "{\"messages\":[{\"id\":\"m2\",\"from\":\"aa/bb\",\"body\":\"codex printed ...50 tokens truncated... and Warning: truncated output\"}],\"more\":0}\n"
	talkClaude := "{\"messages\":[{\"id\":\"m3\",\"from\":\"aa/bb\",\"body\":\"claude said Full output saved to: x\"}],\"more\":0}\n"

	rows := []struct {
		rule     string
		rt       Runtime
		observed string
		expected string
		verdict  AckVerdict
		reason   AckReason
	}{
		// Claude
		{"claude: the result is exactly what recv printed", RuntimeClaude, out, out, AckAllowed, AckSeenInFull},
		{"claude: surrounding whitespace is ignored", RuntimeClaude, "\n  " + strings.TrimSpace(out) + "\n\n", out, AckAllowed, AckSeenInFull},
		{"claude: a wrapper around the result is accepted", RuntimeClaude, "Result:\n" + out + "(exit 0)", out, AckAllowed, AckSeenInFull},
		{"claude: a saved-to-file notice replaced the result: do not acknowledge", RuntimeClaude, claudeNotice, out, AckRefused, AckSavedToFile},
		{"claude: only the saved-to-file sentence is enough to refuse", RuntimeClaude, "Full output saved to: /tmp/x", out, AckRefused, AckSavedToFile},
		{"claude: a notice that still contains the whole output is refused all the same", RuntimeClaude, claudeNotice + out, out, AckRefused, AckSavedToFile},
		{"claude: a message that merely mentions the notice wording is acknowledged", RuntimeClaude, talkClaude, talkClaude, AckAllowed, AckSeenInFull},
		{"claude: a result cut in the middle is not what was handed out", RuntimeClaude, out[:20] + "\n[... 40 characters truncated ...]\n" + out[len(out)-10:], out, AckRefused, AckContentMismatch},
		{"claude: an unrelated result is a mismatch", RuntimeClaude, "command not found: recv", out, AckRefused, AckContentMismatch},
		{"claude: an empty result is a mismatch", RuntimeClaude, "", out, AckRefused, AckContentMismatch},

		// Codex
		{"codex: the result is exactly what recv printed", RuntimeCodex, out, out, AckAllowed, AckSeenInFull},
		{"codex: the status header Codex adds is accepted", RuntimeCodex, "Exit code: 0\nWall time: 0.1 seconds\nOutput:\n" + out, out, AckAllowed, AckSeenInFull},
		{"codex: the warning line marks a cut result", RuntimeCodex, "Warning: truncated output (original token count: 51000)\n" + out, out, AckRefused, AckTruncated},
		{"codex: the ellipsis marker marks a cut result", RuntimeCodex, out[:30] + "…12345 tokens truncated…" + out[40:], out, AckRefused, AckTruncated},
		{"codex: the three-dot marker marks a cut result", RuntimeCodex, out[:30] + "...12345 tokens truncated..." + out[40:], out, AckRefused, AckTruncated},
		{"codex: the character-count marker of the byte policy marks a cut result", RuntimeCodex, out[:30] + "…500 chars truncated…" + out[40:], out, AckRefused, AckTruncated},
		{"codex: a marker without a count is still a marker", RuntimeCodex, out[:30] + "…tokens truncated…" + out[40:], out, AckRefused, AckTruncated},
		{"codex: a marker is refused even when the whole output is also present", RuntimeCodex, out + "\n…9 tokens truncated…", out, AckRefused, AckTruncated},
		{"codex: a message that discusses truncation is acknowledged", RuntimeCodex, talk, talk, AckAllowed, AckSeenInFull},
		{"codex: one more marker than the message itself contains is a cut", RuntimeCodex, talk + "…7 tokens truncated…", talk, AckRefused, AckTruncated},
		{"codex: a result cut without any marker is a mismatch", RuntimeCodex, out[:30], out, AckRefused, AckContentMismatch},
		{"codex: a claude notice is no marker but is still not the output", RuntimeCodex, claudeNotice, out, AckRefused, AckContentMismatch},

		// Not understood
		{"unknown runtime: refuse", Runtime(0), out, out, AckRefused, AckUnknownRuntime},
		{"unnamed runtime value: refuse", Runtime(99), out, out, AckRefused, AckUnknownRuntime},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			v, why := CheckBeforeAck(r.rt, r.observed, r.expected)
			if v != r.verdict || why != r.reason {
				t.Fatalf("got verdict %d reason %d, want verdict %d reason %d", v, why, r.verdict, r.reason)
			}
		})
	}
}

// What Pack prints is what CheckBeforeAck expects, including non-ASCII text.
func TestCheckBeforeAckAcceptsPackedOutput(t *testing.T) {
	p, err := Pack([]Message{msg(t, "m1", "你好 <world> & 😀")}, testLease, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	for _, rt := range []Runtime{RuntimeClaude, RuntimeCodex} {
		if v, why := CheckBeforeAck(rt, string(p.Output), string(p.Output)); v != AckAllowed || why != AckSeenInFull {
			t.Fatalf("runtime %d: verdict %d reason %d", rt, v, why)
		}
	}
}
