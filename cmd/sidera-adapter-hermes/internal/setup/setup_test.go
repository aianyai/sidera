package setup

import (
	"slices"
	"strings"
	"testing"

	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/config"
)

func TestPlan(t *testing.T) {
	skillDir := func(profile string) string { return "/h/" + profile + "/skills/sidera" }
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
				"place /s/SKILL.md as /h/default/skills/sidera/SKILL.md",
			},
		},
		{
			name:     "cron named by the owner is not repeated",
			profiles: []string{"default"}, platforms: []string{"cron", "telegram", "telegram"},
			want: []string{
				"hermes -p default tools enable kanban --platform cron",
				"hermes -p default tools enable kanban --platform telegram",
				"place /s/SKILL.md as /h/default/skills/sidera/SKILL.md",
			},
		},
		{
			name:     "every profile gets its own tools and its own copy of the Skill",
			profiles: []string{"default", "feishu"}, platforms: []string{"feishu"},
			want: []string{
				"hermes -p default tools enable kanban --platform cron",
				"hermes -p default tools enable kanban --platform feishu",
				"place /s/SKILL.md as /h/default/skills/sidera/SKILL.md",
				"hermes -p feishu tools enable kanban --platform cron",
				"hermes -p feishu tools enable kanban --platform feishu",
				"place /s/SKILL.md as /h/feishu/skills/sidera/SKILL.md",
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

func TestSkillListsEveryAliasWithItsModesAndPresets(t *testing.T) {
	aliases, err := config.ParseAliases([]string{
		"Node-B/Codex=nodeb/codex;modes=new_session,message;presets=work,review",
		"ops=nodec/ops;modes=message",
	})
	if err != nil {
		t.Fatal(err)
	}
	skill, err := Skill(aliases)
	if err != nil {
		t.Fatal(err)
	}
	text := string(skill)
	for _, want := range []string{
		"- `node-b/codex`: `new_session`, `message`; presets: `work`, `review`\n",
		"- `ops`: `message`\n",
		"name: sidera\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("skill lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "nodeb/codex") || strings.Contains(text, "nodec/ops") {
		t.Error("the Skill shows an address: a card names an alias, never an address")
	}
}
