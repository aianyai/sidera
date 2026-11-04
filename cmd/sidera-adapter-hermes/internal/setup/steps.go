package setup

import (
	"path/filepath"
	"slices"
	"strings"
)

// CronPlatform is the Hermes platform of scheduled jobs. A cron job is an agent turn
// that can delegate like a chat, but its toolsets are set apart from the chat
// platforms', so it is enabled on its own.
const CronPlatform = "cron"

// Step is one thing the owner does so that Hermes agents can delegate, with the reason
// for it. It is either a Hermes command (Args) or the placing of the Skill file (Copy).
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

// Plan lists the steps that let the Hermes agents of the profiles delegate over
// Sidera: the kanban tools on every platform they delegate from and on cron, then the
// Skill that tells the agent how to use them. skillFile is the generated Skill and
// skillDir returns the directory a profile keeps the Skill in. Only Hermes's own
// commands change Hermes's configuration; the file is never edited directly, since
// Hermes owns its format. The Skill is placed as a file because `skills install`
// takes a registry identifier or an HTTP(S) URL and no local path.
func Plan(profiles, platforms []string, skillFile string, skillDir func(profile string) string) []Step {
	platforms = append(slices.Clone(platforms), CronPlatform)
	slices.Sort(platforms)
	platforms = slices.Compact(platforms)

	var steps []Step
	for _, profile := range profiles {
		for _, platform := range platforms {
			steps = append(steps, Step{
				Why:  "profile " + profile + ": give the agents of " + platform + " the kanban tools, which a delegation is made with",
				Args: []string{"-p", profile, "tools", "enable", "kanban", "--platform", platform},
			})
		}
		steps = append(steps, Step{
			Why:      "profile " + profile + ": give its agents the Skill that explains how to delegate and answer",
			CopyFrom: skillFile,
			CopyTo:   filepath.Join(skillDir(profile), "SKILL.md"),
		})
	}
	return steps
}
