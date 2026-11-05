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
// be in, and which of the required toolsets are not enabled. unset is the platform with
// no platform_toolsets entry: the defaults, which leave kanban off. disabled-globally
// has both in the platform's list and skills in agent.disabled_toolsets, which wins.
func TestToolsetsMissingFromRecordedLists(t *testing.T) {
	for _, tc := range []struct {
		sample string
		want   []string
	}{
		{"unset", []string{ToolsetKanban}},
		{"cron-unset", []string{ToolsetKanban}},
		{"explicit-neither", []string{ToolsetKanban, ToolsetSkills}},
		{"explicit-kanban-only", []string{ToolsetSkills}},
		{"explicit-skills-only", []string{ToolsetKanban}},
		{"explicit-both", nil},
		{"disabled-globally", []string{ToolsetSkills}},
	} {
		t.Run(tc.sample, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join("testdata", "tools-list", tc.sample+".txt"))
			if err != nil {
				t.Fatal(err)
			}
			got, err := parseMissingToolsets(string(b), RequiredToolsets)
			if err != nil || !slices.Equal(got, tc.want) {
				t.Errorf("missing = %v, err = %v; want %v", got, err, tc.want)
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
		if got, err := parseMissingToolsets(out, RequiredToolsets); err == nil {
			t.Errorf("%s: read as missing %v, want a refusal", name, got)
		}
	}
}

// The client asks Hermes for the profile and platform given, and an answer it cannot
// read is an error, not a verdict.
func TestToolsetsMissingThroughHermes(t *testing.T) {
	for _, tc := range []struct {
		name string
		file string // answer of the fake; empty leaves the recording
		want []string
		err  error
	}{
		{"recorded", "", nil, nil},
		{"kanban off", "  ✗ disabled  kanban  x\n  ✓ enabled  skills  x\n", []string{ToolsetKanban}, nil},
		{"unreadable", "✗ Unknown platform 'x'. Valid: feishu\n", nil, ErrUnexpectedOutput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctl := t.TempDir()
			t.Setenv(kanbantest.ControlEnv, ctl)
			if tc.file != "" {
				if err := os.WriteFile(filepath.Join(ctl, kanbantest.ToolsFile), []byte(tc.file), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := fakeClient(t, kanbantest.ModeBoard).ToolsetsMissing(context.Background(), "feishu", "feishu")
			if !errors.Is(err, tc.err) || (tc.err == nil && err != nil) || !slices.Equal(got, tc.want) {
				t.Errorf("missing = %v, err = %v; want %v, %v", got, err, tc.want, tc.err)
			}
		})
	}
}
