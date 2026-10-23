package handoff

import (
	"errors"
	"math/rand"
	"strings"
	"testing"
)

// The marker data of the two runtimes the design was measured on. They are
// test data: the package itself knows no runtime, an adapter declares its own.
var (
	// A runtime that replaces a large result with a notice saying that the
	// output was saved to a file.
	replacingRuntime = AckMarkers{
		Replaced: []Marker{{Literal: "<persisted-output>"}, {Literal: "Full output saved to:"}},
	}
	// A runtime that cuts a result in the middle and marks the cut, with the
	// single ellipsis character or with three dots.
	cuttingRuntime = AckMarkers{
		Truncated: []Marker{
			{Literal: "Warning: truncated output"},
			{Regexp: `(?:…|\.\.\.)\s*\d*\s*(?:tokens|chars|bytes) truncated\s*(?:…|\.\.\.)`},
		},
	}
	noMarkers = AckMarkers{}
)

func mustCompile(t *testing.T, m AckMarkers) MarkerSet {
	t.Helper()
	set, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func TestCheckBeforeAck(t *testing.T) {
	out := "{\"messages\":[{\"id\":\"m1\",\"from\":\"aa/bb\",\"body\":\"hello\"}],\"more\":0}\n"
	const savedNotice = "<persisted-output>\nOutput too large (34.5KB). Full output saved to: /tmp/tool-results/abc.txt\n\n" +
		"Preview (first 2KB):\n{\"messages\":[{\"id\":\"m1\",\"from\":\"aa/bb\",\"bo\n...\n</persisted-output>"
	// A body that talks about the markers is part of the expected output too.
	talkReplaced := "{\"messages\":[{\"id\":\"m3\",\"from\":\"aa/bb\",\"body\":\"it said Full output saved to: x\"}],\"more\":0}\n"
	talkCut := "{\"messages\":[{\"id\":\"m2\",\"from\":\"aa/bb\",\"body\":\"it printed ...50 tokens truncated... and Warning: truncated output\"}],\"more\":0}\n"

	both := AckMarkers{Replaced: replacingRuntime.Replaced, Truncated: cuttingRuntime.Truncated}

	rows := []struct {
		rule     string
		markers  AckMarkers
		observed string
		expected string
		verdict  AckVerdict
		reason   AckReason
	}{
		// Equal content.
		{"equal content with markers declared is accepted", both, out, out, AckAllowed, AckSeenInFull},
		{"equal content with no markers declared is accepted", noMarkers, out, out, AckAllowed, AckSeenInFull},
		{"surrounding whitespace is ignored: a trimmed result is accepted", both, "  " + strings.TrimSpace(out), out, AckAllowed, AckSeenInFull},
		{"a wrapper around the result is accepted", both, "Exit code: 0\nOutput:\n" + out + "(done)", out, AckAllowed, AckSeenInFull},

		// Replaced result.
		{"a replacement notice stands in for the result: refuse as replaced", replacingRuntime, savedNotice, out, AckRefused, AckReplaced},
		{"the second replaced marker alone is enough to refuse", replacingRuntime, "Full output saved to: /tmp/x", out, AckRefused, AckReplaced},
		{"the first replaced marker alone is enough to refuse", replacingRuntime, "<persisted-output>", out, AckRefused, AckReplaced},
		{"a notice next to the whole output is refused all the same", replacingRuntime, savedNotice + out, out, AckRefused, AckReplaced},
		{"a message that merely mentions the notice wording is accepted", replacingRuntime, talkReplaced, talkReplaced, AckAllowed, AckSeenInFull},
		{"one more replaced marker than the message itself contains is a replacement", replacingRuntime, talkReplaced + "<persisted-output>", talkReplaced, AckRefused, AckReplaced},

		// Truncated result, literal and regular-expression markers.
		{"a literal truncation marker is refused as truncated", cuttingRuntime, "Warning: truncated output (original token count: 51000)\n" + out, out, AckRefused, AckTruncated},
		{"a regex truncation marker with the ellipsis character is refused", cuttingRuntime, out[:30] + "…12345 tokens truncated…" + out[40:], out, AckRefused, AckTruncated},
		{"a regex truncation marker with three dots is refused", cuttingRuntime, out[:30] + "...12345 tokens truncated..." + out[40:], out, AckRefused, AckTruncated},
		{"a regex truncation marker counting characters is refused", cuttingRuntime, out[:30] + "…500 chars truncated…" + out[40:], out, AckRefused, AckTruncated},
		{"a regex truncation marker without a count is refused", cuttingRuntime, out[:30] + "…tokens truncated…" + out[40:], out, AckRefused, AckTruncated},
		{"a truncation marker next to the whole output is refused all the same", cuttingRuntime, out + "\n…9 tokens truncated…", out, AckRefused, AckTruncated},
		{"a message that discusses truncation is accepted", cuttingRuntime, talkCut, talkCut, AckAllowed, AckSeenInFull},
		{"one more truncation marker than the message itself contains is a cut", cuttingRuntime, talkCut + "…7 tokens truncated…", talkCut, AckRefused, AckTruncated},

		// The replaced family is judged before the truncated one.
		{"both kinds of marker present: replaced wins", both, savedNotice + "…9 tokens truncated…", out, AckRefused, AckReplaced},

		// Content mismatch.
		{"a result cut in the middle without a marker is a mismatch", cuttingRuntime, out[:30], out, AckRefused, AckContentMismatch},
		{"an unrelated result is a mismatch", both, "command not found: recv", out, AckRefused, AckContentMismatch},
		{"an empty result is a mismatch", both, "", out, AckRefused, AckContentMismatch},
		{"markers of another runtime are no marker but the content still differs", cuttingRuntime, savedNotice, out, AckRefused, AckContentMismatch},
		{"no markers declared and different content: refuse", noMarkers, "command not found: recv", out, AckRefused, AckContentMismatch},
		{"no markers declared and a notice instead of the output: refuse", noMarkers, savedNotice, out, AckRefused, AckContentMismatch},
		{"no markers declared and a cut result: refuse", noMarkers, out[:30] + "…12345 tokens truncated…" + out[40:], out, AckRefused, AckContentMismatch},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			v, why := CheckBeforeAck(mustCompile(t, r.markers), r.observed, r.expected)
			if v != r.verdict || why != r.reason {
				t.Fatalf("got verdict %d reason %d, want verdict %d reason %d", v, why, r.verdict, r.reason)
			}
		})
	}
}

// The zero MarkerSet declares nothing and is held to containment alone.
func TestCheckBeforeAckZeroMarkerSet(t *testing.T) {
	out := "{\"messages\":[],\"more\":0}\n"
	if v, why := CheckBeforeAck(MarkerSet{}, out, out); v != AckAllowed || why != AckSeenInFull {
		t.Fatalf("equal content: verdict %d reason %d", v, why)
	}
	if v, why := CheckBeforeAck(MarkerSet{}, "other", out); v != AckRefused || why != AckContentMismatch {
		t.Fatalf("different content: verdict %d reason %d", v, why)
	}
}

func TestCompileMarkers(t *testing.T) {
	rows := []struct {
		rule string
		in   AckMarkers
		ok   bool
	}{
		{"nothing declared is valid", AckMarkers{}, true},
		{"literals and regular expressions are valid", AckMarkers{Replaced: []Marker{{Literal: "x"}}, Truncated: []Marker{{Regexp: `a+b`}}}, true},
		{"an empty literal in the replaced list is rejected", AckMarkers{Replaced: []Marker{{}}}, false},
		{"an empty marker in the truncated list is rejected", AckMarkers{Truncated: []Marker{{Literal: "x"}, {}}}, false},
		{"an invalid regular expression is rejected", AckMarkers{Truncated: []Marker{{Regexp: `(unclosed`}}}, false},
		{"a regular expression Go's engine does not support is rejected", AckMarkers{Replaced: []Marker{{Regexp: `(?=lookahead)x`}}}, false},
		{"a marker setting both fields is rejected", AckMarkers{Replaced: []Marker{{Literal: "x", Regexp: "x"}}}, false},
		{"a regular expression matching the empty text is rejected", AckMarkers{Truncated: []Marker{{Regexp: `x*`}}}, false},
		{"an anchor-only regular expression is rejected", AckMarkers{Replaced: []Marker{{Regexp: `^$`}}}, false},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			_, err := r.in.Compile()
			if (err == nil) != r.ok {
				t.Fatalf("Compile() = %v, want ok=%v", err, r.ok)
			}
			if err != nil && !errors.Is(err, ErrInvalidMarker) {
				t.Fatalf("error %v does not wrap ErrInvalidMarker", err)
			}
		})
	}
}

// What Pack prints is what CheckBeforeAck expects, including non-ASCII text,
// whatever the declared markers.
func TestCheckBeforeAckAcceptsPackedOutput(t *testing.T) {
	p, err := Pack([]Message{msg(t, "m1", "你好 <world> & 😀")}, testLease, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	for name, m := range map[string]AckMarkers{"replacing": replacingRuntime, "cutting": cuttingRuntime, "none": noMarkers} {
		if v, why := CheckBeforeAck(mustCompile(t, m), string(p.Output), string(p.Output)); v != AckAllowed || why != AckSeenInFull {
			t.Fatalf("%s: verdict %d reason %d", name, v, why)
		}
	}
}

// Random outputs, wrapped, marked and cut by construction. The expected
// verdict of each variant follows from how it was built, not from the code
// under test.
func TestCheckBeforeAckProperties(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	set := mustCompile(t, AckMarkers{Replaced: replacingRuntime.Replaced, Truncated: cuttingRuntime.Truncated})
	plain := mustCompile(t, noMarkers)

	// want is the outcome with the markers declared, plainWant the outcome
	// when none is declared and only the content decides.
	type outcome struct {
		verdict AckVerdict
		reason  AckReason
	}
	accepted := outcome{AckAllowed, AckSeenInFull}
	mismatch := outcome{AckRefused, AckContentMismatch}
	type variant struct {
		name      string
		observed  func(expected string, runes []rune) string
		want      outcome
		plainWant outcome
	}
	variants := []variant{
		{"exact", func(e string, _ []rune) string { return e }, accepted, accepted},
		{"wrapped", func(e string, _ []rune) string { return "Output:\n  " + strings.TrimSpace(e) + "\n(exit 0)\n" }, accepted, accepted},
		{"replaced notice only", func(string, []rune) string {
			return "<persisted-output>\nFull output saved to: /tmp/x\n</persisted-output>"
		}, outcome{AckRefused, AckReplaced}, mismatch},
		{"replaced notice before the whole output", func(e string, _ []rune) string { return "<persisted-output>" + e }, outcome{AckRefused, AckReplaced}, accepted},
		{"truncation marker after the whole output", func(e string, _ []rune) string { return e + "\n...42 bytes truncated..." }, outcome{AckRefused, AckTruncated}, accepted},
		{"cut in the middle with a marker", func(e string, r []rune) string {
			return string(r[:len(r)/3]) + "…99 tokens truncated…" + string(r[2*len(r)/3:])
		}, outcome{AckRefused, AckTruncated}, mismatch},
		{"cut in the middle without a marker", func(e string, r []rune) string {
			return string(r[:len(r)/3]) + string(r[2*len(r)/3:])
		}, mismatch, mismatch},
		{"empty", func(string, []rune) string { return "" }, mismatch, mismatch},
	}

	taken := map[string]int{}
	for i := 0; i < 300; i++ {
		// randomText draws no rune that appears in a marker, so the markers
		// in an observed text are exactly those a variant added; the body
		// is long enough for the middle cut to remove something.
		body := randomText(rng, 20+rng.Intn(200))
		expected := "{\"messages\":[{\"body\":\"" + body + "\"}],\"more\":0}\n"
		runes := []rune(strings.TrimSpace(expected))
		for _, v := range variants {
			observed := v.observed(expected, runes)
			if verdict, reason := CheckBeforeAck(set, observed, expected); (outcome{verdict, reason}) != v.want {
				t.Fatalf("%s: got verdict %d reason %d, want %+v\nobserved %q", v.name, verdict, reason, v.want, observed)
			}
			if verdict, reason := CheckBeforeAck(plain, observed, expected); (outcome{verdict, reason}) != v.plainWant {
				t.Fatalf("%s without markers: got verdict %d reason %d, want %+v", v.name, verdict, reason, v.plainWant)
			}
			taken[v.name]++
		}
	}
	for _, v := range variants {
		if taken[v.name] != 300 {
			t.Fatalf("variant %q ran %d times, want 300", v.name, taken[v.name])
		}
	}
}
