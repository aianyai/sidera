package setup

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/config"
)

func TestPlan(t *testing.T) {
	home := filepath.Join(t.TempDir(), "h")
	skillDir := func(profile string) string { return filepath.Join(home, profile, "skills", "sidera") }
	placed := func(profile string) string {
		return "place /s/SKILL.md as " + filepath.Join(skillDir(profile), "SKILL.md")
	}
	cases := []struct {
		name      string
		profiles  []string
		platforms []string
		want      []string
	}{
		{
			name:     "one platform: that platform and cron, then the Skill",
			profiles: []string{"default"}, platforms: []string{"feishu"},
			want: []string{
				"hermes -p default tools enable kanban --platform cron",
				"hermes -p default tools enable kanban --platform feishu",
				placed("default"),
			},
		},
		{
			name:     "cron named by the owner is not repeated",
			profiles: []string{"default"}, platforms: []string{"cron", "telegram", "telegram"},
			want: []string{
				"hermes -p default tools enable kanban --platform cron",
				"hermes -p default tools enable kanban --platform telegram",
				placed("default"),
			},
		},
		{
			name:     "every profile gets its own tools and its own copy of the Skill",
			profiles: []string{"default", "feishu"}, platforms: []string{"feishu"},
			want: []string{
				"hermes -p default tools enable kanban --platform cron",
				"hermes -p default tools enable kanban --platform feishu",
				placed("default"),
				"hermes -p feishu tools enable kanban --platform cron",
				"hermes -p feishu tools enable kanban --platform feishu",
				placed("feishu"),
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			for _, s := range Plan(c.profiles, c.platforms, "/s/SKILL.md", skillDir) {
				got = append(got, s.Command("hermes"))
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("steps:\n got %q\nwant %q", got, c.want)
			}
		})
	}
}

func TestSkillListsEveryAliasWithItsModesPresetsAndTheBoard(t *testing.T) {
	aliases, err := config.ParseAliases([]string{
		"Node-B/Codex=nodeb/codex;modes=new_session,message;presets=work,review",
		"ops=nodec/ops;modes=message",
	})
	if err != nil {
		t.Fatal(err)
	}
	skill, err := Skill("e2e-board", aliases)
	if err != nil {
		t.Fatal(err)
	}
	text := string(skill)
	for _, want := range []string{
		"- `node-b/codex`: `new_session`, `message`; presets: `work`, `review`\n",
		"- `ops`: `message`\n",
		"name: sidera\n",
		"`board: \"e2e-board\"`",
		"- `board`: `e2e-board`.\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("skill lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "nodeb/codex") || strings.Contains(text, "nodec/ops") {
		t.Error("the Skill shows an address: a card names an alias, never an address")
	}
}

// The daemon refuses to start an adapter whose state directory holds an entry that
// others can reach, so what setup writes there must be owner-only, also in a directory
// an earlier version made wider.
func TestWriteSkillLeavesTheStateDirectoryOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("access on Windows is an access list inherited from the private state directory, not mode bits")
	}
	for _, wide := range []bool{false, true} {
		stateDir := filepath.Join(t.TempDir(), "state")
		if err := os.Mkdir(stateDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if wide {
			if err := os.Mkdir(filepath.Join(stateDir, "skill"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(filepath.Join(stateDir, "skill"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := writeSkill(stateDir, []byte("skill")); err != nil {
			t.Fatal(err)
		}
		err := filepath.WalkDir(stateDir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			if info.Mode().Perm()&0o077 != 0 {
				t.Errorf("wide=%v: %s has mode %v, want no access for group and others", wide, p, info.Mode().Perm())
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// A profile whose gateway has the Kanban notifier off is named in the plan, with the
// command that turns it on, and one that has it on is not.
func TestPlanWarnsOfAProfileWithTheNotifierOff(t *testing.T) {
	for _, tc := range []struct {
		name string
		off  []string
		want []string
	}{
		{"on everywhere", nil, nil},
		{"the default off", []string{"default"}, []string{
			"the Kanban notifier is off in profile default (kanban.notify_in_gateway is false)",
			"hermes -p default config set kanban.notify_in_gateway true",
		}},
		{"another profile off", []string{"feishu"}, []string{
			"off in profile feishu (kanban.notify_in_gateway is false): a gateway started from it",
			"hermes -p feishu config set kanban.notify_in_gateway true",
		}},
		{"two off", []string{"feishu", "default"}, []string{
			"hermes -p feishu config set kanban.notify_in_gateway true",
			"hermes -p default config set kanban.notify_in_gateway true",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			printPlan(&out, config.Config{HermesPath: "hermes", HermesHome: "/h", Board: "lane"}, "/s/SKILL.md", nil, tc.off)
			text := out.String()
			for _, want := range tc.want {
				if !strings.Contains(text, want) {
					t.Errorf("the plan lacks %q:\n%s", want, text)
				}
			}
			if len(tc.want) == 0 && strings.Contains(text, "notifier") {
				t.Errorf("the plan mentions the notifier although it is on:\n%s", text)
			}
		})
	}
}
