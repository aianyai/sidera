package setup

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/kanban"
)

// CronPlatform is the Hermes platform of scheduled jobs. A cron job is an agent turn
// that can delegate like a chat, but its toolsets are set apart from the chat
// platforms', so it is checked on its own.
const CronPlatform = "cron"

// Step is one thing the owner does so that Hermes agents can delegate, with the reason
// for it. It is either the placing of the Skill file (CopyFrom, CopyTo) or a Hermes
// command (Args), which the plan only shows as advice and never runs.
type Step struct {
	Why string
	// Args are the arguments of the hermes program, the profile option included.
	Args []string
	// CopyFrom and CopyTo place a file: the generated Skill into the skills directory
	// of a profile. Set when Args is empty.
	CopyFrom, CopyTo string
}

// IsCopy reports whether the step places a file instead of running Hermes.
func (s Step) IsCopy() bool { return len(s.Args) == 0 }

// Command is how the step reads to the owner, in a form to copy into a shell.
func (s Step) Command(hermes string) string {
	if s.IsCopy() {
		return "place " + quote(s.CopyFrom) + " as " + quote(s.CopyTo)
	}
	parts := append([]string{hermes}, s.Args...)
	for i, p := range parts {
		parts[i] = quote(p)
	}
	return strings.Join(parts, " ")
}

func quote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\"'$\\`") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Plan lists the steps `--apply` does: the Skill that tells the agent of each profile how
// to delegate and answer, placed in the profile's skills directory. skillFile is the
// generated Skill and skillDir returns the directory a profile keeps the Skill in. The
// Skill is placed as a file because `skills install` takes a registry identifier or an
// HTTP(S) URL and no local path. Hermes's configuration is not part of the plan: see
// ToolsetAdvice.
func Plan(profiles []string, skillFile string, skillDir func(profile string) string) []Step {
	steps := make([]Step, 0, len(profiles))
	for _, profile := range profiles {
		steps = append(steps, Step{
			Why:      "profile " + profile + ": give its agents the Skill that explains how to delegate and answer",
			CopyFrom: skillFile,
			CopyTo:   filepath.Join(skillDir(profile), "SKILL.md"),
		})
	}
	return steps
}

// Platforms are the platforms whose toolsets are checked: the ones asked for, sorted
// and without repeats, and cron.
func Platforms(asked []string) []string {
	platforms := append(slices.Clone(asked), CronPlatform)
	slices.Sort(platforms)
	return slices.Compact(platforms)
}

// ToolsetGap is a platform of a profile whose agents lack toolsets they delegate with.
type ToolsetGap struct {
	Profile, Platform string
	// ConfigFile is the profile's config.yaml, which holds platform_toolsets.
	ConfigFile string
	// Missing are the toolsets that are not enabled, never empty.
	Missing []string
}

// ToolsetAdvice tells the owner, for each gap, which toolsets are missing and the change
// to make by hand in the profile's configuration, then why setup does not make it. The
// text is empty when there is no gap.
//
// The change is not made by `hermes tools enable`: that command writes the whole list of
// the platform. Hermes 0.22.2 against a list [image_gen, no_mcp] wrote [feishu_doc,
// feishu_drive, image_gen, kanban]: the no_mcp entry, which switches off every MCP
// server on the platform, was dropped, and two toolsets the owner had not chosen were
// added; against a platform with no list it wrote the full list of the defaults. The
// owner adds one entry to a list they already read.
func ToolsetAdvice(gaps []ToolsetGap) string {
	if len(gaps) == 0 {
		return ""
	}
	var b strings.Builder
	for _, g := range gaps {
		items := strings.Join(g.Missing, ", ")
		fmt.Fprintf(&b, "\nWarning: the agent of profile %s on platform %s has not enabled the toolset(s) %s: "+
			"it cannot delegate over Sidera or load the Sidera Skill (an agent that has the kanban tools "+
			"and not the Skill creates plain cards that nothing sends). To enable %s, edit %s by hand:\n"+
			"  add %s to the list platform_toolsets.%s\n"+
			"  (if the file has no platform_toolsets.%s, add it as [%s, %s]: the platform's default toolset keeps the "+
			"toolsets it has now, which a list of %s alone would switch off)\n",
			g.Profile, g.Platform, items, items, g.ConfigFile, items, g.Platform, g.Platform,
			defaultToolset(g.Platform), items, items)
	}
	b.WriteString(`
setup does not run 'hermes tools enable' for this: it writes the platform's whole toolset list, so an entry
such as no_mcp (which switches off every MCP server on the platform) is dropped and other toolsets are
added. A toolset named in agent.disabled_toolsets stays off whatever the list says; remove it there.
'hermes -p <profile> tools list --platform <platform>' shows what is enabled, and setup reads it the same way.
`)
	return b.String()
}

// defaultToolset is the name Hermes gives the toolset that holds a platform's default
// tools: hermes-<platform>, with a dash for an underscore (hermes-api-server).
func defaultToolset(platform string) string {
	return "hermes-" + strings.ReplaceAll(platform, "_", "-")
}

// NotifierSteps are the commands that turn the Kanban notifier on in the profiles whose
// configuration has it off, each with what happens while it is off. A gateway reads the
// setting from the profile it was started from: the default profile for the one gateway
// that serves every profile, or the profile itself when it runs its own gateway. They
// are advice and not part of the plan: `setup` never changes that setting, nor does
// --apply run them, since a gateway that has it off may be configured so on purpose.
func NotifierSteps(off []string) []Step {
	steps := make([]Step, 0, len(off))
	for _, profile := range off {
		whose := "a gateway started from it wakes no conversation, so a completed card is written but no agent hears of it."
		if profile == kanban.DefaultProfile {
			whose = "the one gateway that serves every profile is started from it and wakes no conversation, " +
				"so a completed card is written but no agent hears of it."
		}
		steps = append(steps, Step{
			Why:  "the Kanban notifier is off in profile " + profile + " (" + kanban.NotifierSetting + " is false): " + whose + " To turn it on:",
			Args: []string{"-p", profile, "config", "set", kanban.NotifierSetting, "true"},
		})
	}
	return steps
}
