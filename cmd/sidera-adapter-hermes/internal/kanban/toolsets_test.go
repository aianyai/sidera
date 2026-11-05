package kanban

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/kanban/kanbantest"
)

// What `tools list` printed on Hermes 0.22.2 for each state a platform's toolsets can
// be in, which of the required toolsets are not enabled, and how many toolsets are
// enabled (what setup lists when it advises a list for a platform that has none). unset is the platform with
// no platform_toolsets entry: the defaults, which leave kanban off. disabled-globally
// has both in the platform's list and skills in agent.disabled_toolsets, which wins.
func TestToolsetsMissingFromRecordedLists(t *testing.T) {
	for _, tc := range []struct {
		sample  string
		want    []string
		enabled int
	}{
		{"unset", []string{ToolsetKanban}, 17},
		{"cron-unset", []string{ToolsetKanban}, 17},
		{"explicit-neither", []string{ToolsetKanban, ToolsetSkills}, -1},
		{"explicit-kanban-only", []string{ToolsetSkills}, -1},
		{"explicit-skills-only", []string{ToolsetKanban}, -1},
		{"explicit-both", nil, -1},
		{"disabled-globally", []string{ToolsetSkills}, -1},
	} {
		t.Run(tc.sample, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join("testdata", "tools-list", tc.sample+".txt"))
			if err != nil {
				t.Fatal(err)
			}
			got, err := parseToolsets(string(b), RequiredToolsets)
			if err != nil || !slices.Equal(got.Missing, tc.want) {
				t.Errorf("missing = %v, err = %v; want %v", got.Missing, err, tc.want)
			}
			if slices.Contains(got.Enabled, ToolsetKanban) == slices.Contains(got.Missing, ToolsetKanban) ||
				slices.Contains(got.Enabled, ToolsetSkills) == slices.Contains(got.Missing, ToolsetSkills) {
				t.Errorf("enabled %v and missing %v do not split the required toolsets", got.Enabled, got.Missing)
			}
			if tc.enabled >= 0 && len(got.Enabled) != tc.enabled {
				t.Errorf("%d toolsets enabled (%v), want %d", len(got.Enabled), got.Enabled, tc.enabled)
			}
		})
	}
}

// An answer without the row of a required toolset is not "missing": Hermes answered an
// unknown platform with a message and exit 0, or the listing changed.
func TestToolsetsListWithoutTheRowsIsRefused(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "tools-list", "unknown-platform.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for name, out := range map[string]string{
		"unknown platform": string(b),
		"nothing":          "",
		"no skills row":    "Built-in toolsets (feishu):\n  ✓ enabled  kanban  📌 Kanban\n",
		"rows only of MCP": "Built-in toolsets (feishu):\nMCP servers:\n  ✓ enabled  kanban  x\n  ✓ enabled  skills  x\n",
	} {
		if got, err := parseToolsets(out, RequiredToolsets); err == nil {
			t.Errorf("%s: read as %+v, want a refusal", name, got)
		}
	}
}

// The client asks Hermes for the profile and platform given, and an answer it cannot
// read is an error, not a verdict. The fake answers only for that profile and platform;
// for any other it gives a recording in which both toolsets are enabled.
func TestToolsetsThroughHermes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer string
		want   ToolsetReading
		err    error
	}{
		{"kanban off", "  ✗ disabled  kanban  x\n  ✓ enabled  web  x\n  ✓ enabled  skills  x\n",
			ToolsetReading{Enabled: []string{"web", "skills"}, Missing: []string{ToolsetKanban}}, nil},
		{"unreadable", "✗ Unknown platform 'x'. Valid: feishu\n", ToolsetReading{}, ErrUnexpectedOutput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctl := t.TempDir()
			t.Setenv(kanbantest.ControlEnv, ctl)
			if err := os.WriteFile(filepath.Join(ctl, kanbantest.ToolsFileOf("ops", "slack")), []byte(tc.answer), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := fakeClient(t, kanbantest.ModeBoard).Toolsets(context.Background(), "ops", "slack")
			if !errors.Is(err, tc.err) || (tc.err == nil && err != nil) ||
				!slices.Equal(got.Enabled, tc.want.Enabled) || !slices.Equal(got.Missing, tc.want.Missing) {
				t.Errorf("reading = %+v, err = %v; want %+v, %v", got, err, tc.want, tc.err)
			}
		})
	}
}
