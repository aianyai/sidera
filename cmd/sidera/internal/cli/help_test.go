package cli

import (
	"bytes"
	"flag"
	"regexp"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/sdk/go/termout"
)

// dispatched is every command the dispatcher accepts. A command added to the
// dispatcher is added here, and then has to be in the help.
var dispatched = []string{
	"init", "daemon", "daemon stop", "daemon restart",
	"me", "declare", "peers", "directory", "send", "reply", "recv", "ack", "status", "control", "probe", "mcp",
	"version", "update",
	"alerts list", "alerts ack", "preset list", "preset add", "preset remove",
	"adapter install", "adapter list", "adapter enable", "adapter config", "adapter disable", "adapter remove", "adapter setup",
	"endpoint list", "endpoint close", "endpoint set", "node show", "node set",
	"network show", "network address set", "network address remove", "network listen set",
	"trust expect", "trust forget", "trust reject", "trust request", "trust list", "trust remove", "trust clear",
	"identity export-revocation", "identity confirm-backup", "identity retire", "clock clear-rollback",
}

func TestHelpListsEveryCommandOnce(t *testing.T) {
	grouped := map[string]int{}
	for _, g := range helpGroups {
		for _, name := range g.commands {
			grouped[name]++
		}
	}
	for _, name := range dispatched {
		if grouped[name] != 1 {
			t.Errorf("%q is in %d groups of the help, want exactly 1", name, grouped[name])
		}
		if _, ok := helpEntries[name]; !ok {
			t.Errorf("%q has no help entry", name)
		}
	}
	if len(grouped) != len(dispatched) || len(helpEntries) != len(dispatched) {
		t.Errorf("the help lists %d commands and has %d entries, the dispatcher accepts %d", len(grouped), len(helpEntries), len(dispatched))
	}
	_, out, _ := runCLI(t, nil, "--help")
	for _, name := range dispatched {
		if !regexp.MustCompile(`(?m)^  ` + regexp.QuoteMeta(name) + `( |$)`).MatchString(out) {
			t.Errorf("sidera --help does not list %q", name)
		}
	}
}

// Every command that the help lists is one the dispatcher knows: a command that was
// removed from the dispatcher but not from the help fails here, since a known command
// reports its flag and an unknown one reports itself.
func TestEveryListedCommandIsDispatched(t *testing.T) {
	env := map[string]string{"SIDERA_HOME": t.TempDir()}
	for _, name := range dispatched {
		code, _, errs := runCLI(t, env, append(strings.Fields(name), "--no-such-flag")...)
		if code != apiwire.ExitUsage || !strings.Contains(errs, "unknown flag --no-such-flag") {
			t.Errorf("%s --no-such-flag = exit %d, %q; want a usage error about the flag", name, code, errs)
		}
	}
}

func TestHelpEntriesAreShaped(t *testing.T) {
	for name, h := range helpEntries {
		switch {
		case h.summary == "" || strings.HasSuffix(h.summary, ".") || strings.ContainsAny(h.summary, "\n"):
			t.Errorf("%s: the summary %q must be one line with no final period", name, h.summary)
		case strings.Contains(h.details, "\n") || (h.details != "" && !strings.HasSuffix(h.details, ".")):
			t.Errorf("%s: the details %q must be sentences on one line", name, h.details)
		case !strings.HasPrefix(h.usage, name) || !strings.HasPrefix(h.example, "sidera "+name):
			t.Errorf("%s: usage %q and example %q must start with the command", name, h.usage, h.example)
		}
	}
}

var ansi = regexp.MustCompile("\x1b\\[[0-9;]*m")

func TestHelpListLayout(t *testing.T) {
	_, plain, errs := runCLI(t, nil, "--help")
	if errs != "" || ansi.MatchString(plain) {
		t.Fatalf("plain help: stderr %q, escape sequences %v", errs, ansi.MatchString(plain))
	}
	if !strings.HasPrefix(plain, "Usage: sidera [--home <dir>] [--json] [--color <when>] <command> [arguments]\n") {
		t.Errorf("the usage line is not first:\n%s", plain)
	}
	if !strings.HasSuffix(plain, "Run `sidera <command> --help` for a command's arguments and an example.\n") {
		t.Errorf("the closing hint is missing:\n%s", plain)
	}
	for _, line := range strings.Split(plain, "\n") {
		if w := termout.DisplayWidth(line); w > 80 || strings.HasSuffix(line, " ") {
			t.Errorf("line %q is %d columns wide or ends in a space; a pipe wraps at 80", line, w)
		}
	}
	// Colored, the names are cyan and the headings bold, and the words are the same.
	_, colored, _ := runCLI(t, nil, "--color=always", "--help")
	if !strings.Contains(colored, "\x1b[36msend\x1b[0m") || !strings.Contains(colored, "\x1b[1mMessages\x1b[0m") {
		t.Errorf("colored help has no cyan names or bold headings:\n%q", colored)
	}
	if ansi.ReplaceAllString(colored, "") != plain {
		t.Errorf("colored help differs from plain help in more than its styles")
	}
}

func TestCommandHelpShowsSummaryAndDetails(t *testing.T) {
	code, out, errs := runCLI(t, nil, "daemon", "restart", "--help")
	if code != 0 || errs != "" {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
	for _, want := range []string{"Usage: sidera daemon restart [--expect-version <version>] [--wait <duration>]\n", "Restart the running node and wait for the new daemon. The node shuts down", "\nExample: sidera daemon restart --expect-version 0.12.0\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("help is missing %q:\n%s", want, out)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if termout.DisplayWidth(line) > 80 {
			t.Errorf("line %q is wider than 80 columns", line)
		}
	}
	// A group names its commands one after the other.
	_, group, _ := runCLI(t, nil, "trust", "--help")
	if strings.Count(group, "Usage: sidera trust ") != 7 {
		t.Errorf("trust --help should describe the seven trust commands:\n%s", group)
	}
}

// An example is a command to copy, so it is never wrapped, however long; the usage and the
// paragraph around it are.
func TestAnExampleIsNeverWrapped(t *testing.T) {
	example := "sidera network listen set /ip4/0.0.0.0/tcp/7745 /ip4/0.0.0.0/udp/7745/quic-v1 --expected-version 12"
	var out bytes.Buffer
	text := commandOn(&out).commandHelp(helpEntry{usage: "x", summary: "Do it", example: example})
	if !strings.Contains(text, "\nExample: "+example+"\n") {
		t.Errorf("the example is not on one line:\n%s", text)
	}
}

// A flag that is wrong is reported in the words of this program: the flag in its
// -- form, what it takes, and where its command is described.
func TestFlagErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"a duration that is not one", []string{"daemon", "restart", "--wait", "x"}, "--wait needs a duration such as 2m, not \"x\"; run `sidera daemon restart --help` for its flags"},
		{"a value joined with =", []string{"trust", "request", "n", "--wait=soon"}, "--wait needs a duration such as 2m, not \"soon\"; run `sidera trust request --help` for its flags"},
		{"an empty value joined with =", []string{"daemon", "restart", "--wait="}, "--wait needs a duration such as 2m, not \"\""},
		{"seconds that are not a number", []string{"recv", "-wait", "1m"}, "--wait needs a whole number of seconds, not \"1m\"; run `sidera recv --help` for its flags"},
		{"a deadline that is not a number", []string{"send", "--to", "a", "--body", "b", "--accept-before", "x"}, "--accept-before needs a whole number of seconds, not \"x\""},
		{"a number that is not one", []string{"directory", "--limit", "many"}, "--limit needs a whole number, not \"many\"; run `sidera directory --help` for its flags"},
		{"a version that is negative", []string{"node", "set", "a=1", "--expected-version", "-1"}, "--expected-version needs a whole number, 0 or more, not \"-1\""},
		{"a boolean that is not one", []string{"declare", "--paused=maybe"}, "--paused needs true or false, not \"maybe\"; run `sidera declare --help` for its flags"},
		{"an unknown flag", []string{"send", "--bogus", "1"}, "unknown flag --bogus; run `sidera send --help` for its flags"},
		{"a flag with three dashes", []string{"recv", "---wait", "1"}, "unknown flag ---wait; run `sidera recv --help` for its flags"},
		{"an unknown flag after a group", []string{"adapter", "setup", "work", "-apply"}, "unknown flag --apply; run `sidera adapter setup --help` for its flags"},
		{"a missing duration", []string{"daemon", "restart", "--wait"}, "--wait needs a duration such as 2m; run `sidera daemon restart --help` for its flags"},
		{"a missing text", []string{"send", "--to"}, "--to needs a value; run `sidera send --help` for its flags"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errs := runCLI(t, map[string]string{"SIDERA_HOME": t.TempDir()}, tc.args...)
			if code != apiwire.ExitUsage || out != "" || !strings.HasPrefix(errs, "error: "+tc.want) {
				t.Errorf("exit %d, stdout %q, stderr %q; want exit 2 and error: %s", code, out, errs, tc.want)
			}
		})
	}
}

// A number of seconds is read as the flag package reads an integer: 0x and 0 prefixes
// and digit separators are accepted, as for the other whole-number flags.
func TestSecondsAreReadInBase0(t *testing.T) {
	for text, want := range map[string]seconds{"8": 8, "0x10": 16, "010": 8, "1_0": 10, "0": 0} {
		var got seconds
		if err := got.Set(text); err != nil || got != want {
			t.Errorf("Set(%q) = %d, %v; want %d", text, got, err, want)
		}
	}
}

// Every flag the program registers is one valueExpected can describe, so that a flag
// of a new type is found here and not by a person whose value was wrong.
func TestEveryFlagCanBeDescribed(t *testing.T) {
	for _, name := range dispatched {
		arguments(strings.Fields(name)).flags.VisitAll(func(fl *flag.Flag) {
			if valueExpected(fl) == "" {
				t.Errorf("%s --%s has no description of its value", name, fl.Name)
			}
		})
	}
}

// The error line keeps its words: only the prefix is styled, and only on a colored
// stream. Tests, scripts and the reference read `error: <message>`.
func TestErrorLineStyling(t *testing.T) {
	const red = "\x1b[31merror:\x1b[0m"
	for _, tc := range []struct {
		name string
		args []string
		env  map[string]string
		want string
	}{
		{"a pipe", []string{"nonsense"}, nil, "error: unknown command: nonsense\n"},
		{"color never beats the environment", []string{"--color=never", "nonsense"}, map[string]string{"FORCE_COLOR": "1"}, "error: unknown command: nonsense\n"},
		{"color always", []string{"--color=always", "nonsense"}, nil, red + " unknown command: nonsense\n"},
		{"color forced by the environment", []string{"nonsense"}, map[string]string{"CLICOLOR_FORCE": "1"}, red + " unknown command: nonsense\n"},
		{"a command that is required", nil, nil, "error: a command is required; use --help\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errs := runCLI(t, tc.env, tc.args...)
			if code != apiwire.ExitUsage || out != "" || errs != tc.want {
				t.Errorf("exit %d, stdout %q, stderr %q; want exit 2 and %q", code, out, errs, tc.want)
			}
		})
	}
}
