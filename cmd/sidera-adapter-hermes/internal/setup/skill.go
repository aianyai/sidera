package setup

import (
	"bytes"
	_ "embed"
	"maps"
	"slices"
	"strings"
	"text/template"

	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/config"
)

// SkillName is the name of the generated Skill: the directory Hermes keeps it in and
// the `name` of its front matter.
const SkillName = "sidera"

// skillDescription is what Hermes shows the agent to decide when to load the Skill.
// Hermes cuts the description to 60 characters in the list the agent chooses from, so
// the part that says when to use the Skill comes first and is short.
const skillDescription = "Delegate tasks to agents on other machines over Sidera."

//go:embed skill.tmpl
var skillBody string

var skillTemplate = template.Must(template.New("skill").Parse(skillBody))

type skillAlias struct {
	Name    string
	Modes   string
	Presets string
}

// Skill renders the SKILL.md for an alias table and the board the adapter serves. The
// aliases are listed by name with their modes and presets and never with their
// addresses: a card names an alias, and an address in a prompt would only invite the
// agent to use it. The board is named because the kanban tools act on the board Hermes
// has selected unless a call names one, and the adapter reads only its own.
func Skill(board string, aliases config.Aliases) ([]byte, error) {
	var list []skillAlias
	for _, name := range slices.Sorted(maps.Keys(aliases)) {
		a := aliases[name]
		list = append(list, skillAlias{
			Name:    name,
			Modes:   code(a.Modes),
			Presets: code(a.Presets),
		})
	}
	var out bytes.Buffer
	out.WriteString("---\nname: " + SkillName + "\ndescription: " + skillDescription + "\n---\n\n")
	if err := skillTemplate.Execute(&out, struct {
		Board   string
		Aliases []skillAlias
	}{board, list}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// code writes a list as comma-separated inline code, or nothing for an empty list.
func code(items []string) string {
	if len(items) == 0 {
		return ""
	}
	return "`" + strings.Join(items, "`, `") + "`"
}
