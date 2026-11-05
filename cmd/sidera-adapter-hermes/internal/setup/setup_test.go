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

// The plan is the Skill file for each profile and nothing else: no Hermes command, since
// `hermes tools enable` rewrites a platform's whole toolset list.
func TestPlanPlacesTheSkillAndRunsNoHermesCommand(t *testing.T) {
	home := filepath.Join(t.TempDir(), "h")
	skillDir := func(profile string) string { return filepath.Join(home, profile, "skills", "sidera") }
	placed := func(profile string) string {
		return "place /s/SKILL.md as " + quote(filepath.Join(skillDir(profile), "SKILL.md"))
	}
	steps := Plan([]string{"default", "feishu"}, "/s/SKILL.md", skillDir)
	var got []string
	for _, s := range steps {
		if !s.IsCopy() {
			t.Errorf("step %q runs Hermes: %v", s.Why, s.Args)
		}
		got = append(got, s.Command("hermes"))
	}
	if want := []string{placed("default"), placed("feishu")}; !slices.Equal(got, want) {
		t.Errorf("steps:\n got %q\nwant %q", got, want)
	}
}

func TestPlatformsAlwaysIncludeCronOnce(t *testing.T) {
	for _, tc := range []struct{ asked, want []string }{
		{[]string{"feishu"}, []string{"cron", "feishu"}},
		{[]string{"telegram", "cron", "telegram"}, []string{"cron", "telegram"}},
	} {
		if got := Platforms(tc.asked); !slices.Equal(got, tc.want) {
			t.Errorf("Platforms(%v) = %v, want %v", tc.asked, got, tc.want)
		}
	}
}

// A platform whose agents lack a toolset is named in the plan with the entry to add by
// hand to the profile's configuration, and the plan says why it does not run
// `hermes tools enable`; platforms that have both toolsets are not mentioned.
func TestPlanSaysWhichToolsetsToEnableByHand(t *testing.T) {
	const file = "/home/profiles/feishu/config.yaml"
	for _, tc := range []struct {
		name string
		gaps []ToolsetGap
		want []string
	}{
		{"both present", nil, nil},
		{"kanban missing", []ToolsetGap{{Profile: "feishu", Platform: "feishu", ConfigFile: file, Missing: []string{"kanban"}}}, []string{
			"profile feishu on platform feishu has not enabled the toolset(s) kanban",
			"edit " + file + " by hand:",
			"add kanban to the list platform_toolsets.feishu",
			"add it as [hermes-feishu, kanban]",
		}},
		{"skills missing", []ToolsetGap{{Profile: "feishu", Platform: "api_server", ConfigFile: file, Missing: []string{"skills"}}}, []string{
			"toolset(s) skills",
			"add skills to the list platform_toolsets.api_server",
			"add it as [hermes-api-server, skills]",
		}},
		{"both missing, two platforms", []ToolsetGap{
			{Profile: "feishu", Platform: "cron", ConfigFile: file, Missing: []string{"kanban", "skills"}},
			{Profile: "feishu", Platform: "feishu", ConfigFile: file, Missing: []string{"kanban"}},
		}, []string{
			"platform cron has not enabled the toolset(s) kanban, skills",
			"add kanban, skills to the list platform_toolsets.cron",
			"platform feishu has not enabled the toolset(s) kanban:",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			printPlan(&out, config.Config{HermesPath: "hermes", HermesHome: "/h", Board: "lane"}, "/s/SKILL.md", nil, nil, tc.gaps)
			text := out.String()
			for _, want := range tc.want {
				if !strings.Contains(text, want) {
					t.Errorf("the plan lacks %q:\n%s", want, text)
				}
			}
			explained := strings.Contains(text, "setup does not run 'hermes tools enable'")
			if explained != (len(tc.gaps) > 0) {
				t.Errorf("the explanation of why 'tools enable' is not run is present: %v, with %d gaps:\n%s", explained, len(tc.gaps), text)
			}
			if len(tc.gaps) == 0 && strings.Contains(text, "has not enabled") {
				t.Errorf("the plan names a missing toolset although none is:\n%s", text)
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
			printPlan(&out, config.Config{HermesPath: "hermes", HermesHome: "/h", Board: "lane"}, "/s/SKILL.md", nil, tc.off, nil)
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

// A profile name becomes the value of -p, where Hermes would read one that begins with
// a dash as an option.
func TestParseRefusesAProfileNameHermesDoesNotTake(t *testing.T) {
	for _, tc := range []struct {
		profile string
		ok      bool
	}{
		{"work", true}, {"Work", true}, {"default", true}, {"-x", false}, {"--all", false}, {"a b", false}, {"", false},
	} {
		_, err := parse([]string{"--config", "c.json", "--profile=" + tc.profile})
		if (err == nil) != tc.ok {
			t.Errorf("--profile=%q: err = %v, want accepted: %v", tc.profile, err, tc.ok)
		}
	}
}
