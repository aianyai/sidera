package setup

import (
	"fmt"
	"slices"
	"strings"

	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/kanban"
)

// CronPlatform is the Hermes platform of scheduled jobs. A cron job is an agent turn
// that can delegate like a chat, but its toolsets are set apart from the chat
// platforms'. A plan always advises on it; a check reads it only when setup was run with
// it named.
const CronPlatform = "cron"

// ToolsetGap is a platform of a profile whose agents lack toolsets they delegate with.
type ToolsetGap struct {
	Profile, Platform string
	// ConfigFile is the profile's config.yaml, which holds platform_toolsets.
	ConfigFile string
	// Enabled are the toolsets the platform has now, as Hermes lists them.
	Enabled []string
	// Missing are the toolsets that are not enabled, never empty.
	Missing []string
}

// Advice tells the owner which toolsets the platform lacks and the change to make by
// hand in the profile's configuration. It is advice and nothing more: setup never makes
// the change.
//
// The change is not made by `hermes tools enable`: that command writes the whole list of
// the platform. Hermes 0.22.2 against a list [image_gen, no_mcp] wrote [feishu_doc,
// feishu_drive, image_gen, kanban]: the no_mcp entry, which switches off every MCP
// server on the platform, was dropped, and two toolsets the owner had not chosen were
// added; against a platform with no list it wrote the full list of the defaults. The
// owner adds one entry to a list they already read, or, for a platform with no list, the
// list of what the platform has now plus what is missing: a list that held only the
// missing toolsets would switch the rest off, and the composite toolset that stands for
// a platform's defaults is not a safe stand-in (it means different things on different
// platforms: discord's opts in to platform-only toolsets, whatsapp_cloud's is not named
// after the platform, a plugin platform has none).
func (g ToolsetGap) Advice() string {
	items := strings.Join(g.Missing, ", ")
	explicit := "[" + strings.Join(append(slices.Clone(g.Enabled), g.Missing...), ", ") + "]"
	return fmt.Sprintf("the agent of profile %s on platform %s has not enabled the toolset(s) %s, so it cannot delegate over Sidera "+
		"or load the Sidera Skill: edit %s by hand so that the list platform_toolsets.%s holds %s, and agent.disabled_toolsets does not "+
		"(if the file has no platform_toolsets.%s, add it as %s: the toolsets the platform has now, then %s); "+
		"setup does not run 'hermes tools enable', which rewrites the platform's whole list",
		g.Profile, g.Platform, items, g.ConfigFile, g.Platform, items, g.Platform, explicit, items)
}

// notifierAdvice says what happens in a profile whose Kanban notifier is off and the
// command that turns it on. It is advice and never an item: setup does not change that
// setting, since a gateway that has it off may be configured so on purpose. A plan carries
// it as a note, and a check as drift, because with the notifier off the replies Sidera
// writes to cards never wake an agent, which is what a check is there to find. A
// gateway reads the setting from the profile it was started from: the default profile
// for the one gateway that serves every profile, or the profile itself when it runs its
// own gateway.
func notifierAdvice(profile string) string {
	whose := "a gateway started from it wakes no conversation, so a completed card is written but no agent hears of it"
	if profile == kanban.DefaultProfile {
		whose = "the one gateway that serves every profile is started from it and wakes no conversation, " +
			"so a completed card is written but no agent hears of it"
	}
	return fmt.Sprintf("the Kanban notifier is off in profile %s (%s is false): %s; to turn it on: hermes -p %s config set %s true",
		profile, kanban.NotifierSetting, whose, profile, kanban.NotifierSetting)
}
