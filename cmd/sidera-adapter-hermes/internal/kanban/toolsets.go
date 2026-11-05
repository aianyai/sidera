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

// ToolsetsMissing returns those of RequiredToolsets that are not enabled for the
// platform in the profile, as `hermes -p <profile> tools list --platform=<platform>`
// resolves it. That is the answer the agent's own sessions use: an explicit
// platform_toolsets list or the platform's defaults (which leave kanban off), with
// agent.disabled_toolsets applied last. It reads the configuration; it does not say
// that a gateway is running or has been restarted since the configuration changed.
//
// Hermes lists the toolsets with a status mark and no machine-readable form, and
// answers an unknown platform with a message on standard output and exit 0. A required
// toolset that has no row in the answer is therefore ErrUnexpectedOutput, not "missing":
// the platform is not known to Hermes or its listing changed.
func (c *Client) ToolsetsMissing(ctx context.Context, profile, platform string) ([]string, error) {
	out, err := c.run(ctx, "tools list", nil, []string{"-p", profile, "tools", "list", "--platform=" + platform})
	if err != nil {
		return nil, err
	}
	missing, err := parseMissingToolsets(string(out), RequiredToolsets)
	if err != nil {
		return nil, &Error{Op: "tools list", Class: Transient, ExitCode: -1,
			Err: fmt.Errorf("%w: platform %s: %v", ErrUnexpectedOutput, platform, err)}
	}
	return missing, nil
}

// parseMissingToolsets reads the rows of `tools list`, which look like
//
//	✓ enabled  skills  📚 Skills
//	✗ disabled  kanban  📌 Kanban
//
// below the headings "Built-in toolsets (<platform>):" and "Plugin toolsets
// (<platform>):", and returns those of required that are disabled. The status mark is
// not read, since a console may print it differently; the word after it is. The MCP
// servers that follow the toolsets are not toolsets and are not read.
func parseMissingToolsets(out string, required []string) ([]string, error) {
	enabled := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "MCP servers") {
			break
		}
		f := strings.Fields(line)
		if len(f) < 3 || (f[1] != "enabled" && f[1] != "disabled") {
			continue
		}
		enabled[f[2]] = f[1] == "enabled"
	}
	var missing []string
	for _, name := range required {
		on, listed := enabled[name]
		switch {
		case !listed:
			return nil, fmt.Errorf("no row for toolset %s in %q", name, firstLine(out))
		case !on:
			missing = append(missing, name)
		}
	}
	return missing, nil
}
