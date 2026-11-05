package kanban

import (
	"context"
	"fmt"
	"strings"
)

// The Hermes toolsets an agent needs on the platform it delegates from: kanban holds
// the tools that create and read cards, skills lets the agent load the Sidera Skill
// that says how to address them. Without skills the agent has the kanban tools and no
// instructions, and delegates as a plain card that this adapter never sees.
const (
	ToolsetKanban = "kanban"
	ToolsetSkills = "skills"
)

// RequiredToolsets are the toolsets a delegating agent needs, in the order they are
// reported.
var RequiredToolsets = []string{ToolsetKanban, ToolsetSkills}

// ToolsetReading is what `tools list` says about the toolsets of a platform.
type ToolsetReading struct {
	// Enabled are the toolsets listed as enabled, in the order of the listing.
	Enabled []string
	// Missing are those of RequiredToolsets that are listed as disabled.
	Missing []string
}

// Toolsets reads the toolsets of the platform in the profile, as `hermes -p <profile>
// tools list --platform=<platform>` resolves them. That is the answer the agent's own
// sessions use: an explicit platform_toolsets list or the platform's defaults (which
// leave kanban off), with agent.disabled_toolsets applied last. It reads the
// configuration; it does not say that a gateway is running or has been restarted since
// the configuration changed.
//
// Hermes lists the toolsets with a status mark and no machine-readable form, and
// answers an unknown platform with a message on standard output and exit 0. A required
// toolset that has no row in the answer is therefore ErrUnexpectedOutput, not "missing":
// the platform is not known to Hermes or its listing changed.
func (c *Client) Toolsets(ctx context.Context, profile, platform string) (ToolsetReading, error) {
	out, err := c.run(ctx, "tools list", nil, []string{"-p", profile, "tools", "list", "--platform=" + platform})
	if err != nil {
		return ToolsetReading{}, err
	}
	reading, err := parseToolsets(string(out), RequiredToolsets)
	if err != nil {
		return ToolsetReading{}, &Error{Op: "tools list", Class: Transient, ExitCode: -1,
			Err: fmt.Errorf("%w: platform %s: %v", ErrUnexpectedOutput, platform, err)}
	}
	return reading, nil
}

// parseToolsets reads the rows of `tools list`, which look like
//
//	✓ enabled  skills  📚 Skills
//	✗ disabled  kanban  📌 Kanban
//
// below the headings "Built-in toolsets (<platform>):" and "Plugin toolsets
// (<platform>):", and returns the enabled ones and those of required that are disabled.
// The status mark is not read, since a console may print it differently; the word after
// it is. The MCP servers that follow the toolsets are not toolsets and are not read.
func parseToolsets(out string, required []string) (ToolsetReading, error) {
	var reading ToolsetReading
	listed := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "MCP servers") {
			break
		}
		f := strings.Fields(line)
		if len(f) < 3 || (f[1] != "enabled" && f[1] != "disabled") {
			continue
		}
		listed[f[2]] = f[1] == "enabled"
		if f[1] == "enabled" {
			reading.Enabled = append(reading.Enabled, f[2])
		}
	}
	for _, name := range required {
		on, ok := listed[name]
		switch {
		case !ok:
			return ToolsetReading{}, fmt.Errorf("no row for toolset %s in %q", name, firstLine(out))
		case !on:
			reading.Missing = append(reading.Missing, name)
		}
	}
	return reading, nil
}
