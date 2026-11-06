package guidance

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// The facts are written once and composed into every text. A text that stopped
// carrying one would tell a session less than the others do, with nothing to
// show it. The Skill carries Full whole, which TestSkillIsOneFileWithFrontMatter
// proves, so it needs no row here.
func TestEveryTextCarriesEveryFact(t *testing.T) {
	texts := map[string]string{"Short": Short, "Full": Full}
	for name, text := range texts {
		for fact, sentence := range map[string]string{"channel": factChannel, "wake": factWake, "ack": factAck, "use": factUse} {
			if !strings.Contains(text, sentence) {
				t.Errorf("%s does not carry the %s fact %q", name, fact, sentence)
			}
		}
	}
}

// The instructions of a server are cut by the client that reads them. Claude Code
// cuts at 4096 characters. Codex lists only the first line of the instructions of
// a server whose tools it defers, keeps its first 250 characters and replaces the
// last three with "...", so the facts that say what the channel is, when to use it
// and what to do on a wake-up have to end within the first 247.
func TestShortFitsWhatClientsKeep(t *testing.T) {
	if strings.Contains(Short, "\n") {
		t.Error("Short has more than one line")
	}
	if n := utf8.RuneCountInString(Short); n > 4096 {
		t.Errorf("Short is %d characters, over the 4096 Claude Code keeps", n)
	}
	lead := factChannel + " " + factUse + " " + factWake
	if !strings.HasPrefix(Short, lead) {
		t.Errorf("Short does not start with the channel, use and wake facts:\n%s", Short)
	}
	if n, kept := utf8.RuneCountInString(lead), 250-len("..."); n > kept {
		t.Errorf("the leading facts are %d characters, over the %d Codex keeps of a deferred server", n, kept)
	}
}

// A client composes the name of a tool its own way, so no text spells one.
func TestNoTextSpellsAToolName(t *testing.T) {
	for name, text := range map[string]string{"Short": Short, "Full": Full, "Skill": string(Skill())} {
		if strings.Contains(text, "mcp__") {
			t.Errorf("%s spells a full tool name", name)
		}
	}
}

// A client loads a Skill by the front matter of one file.
func TestSkillIsOneFileWithFrontMatter(t *testing.T) {
	skill := string(Skill())
	head, body, ok := strings.Cut(strings.TrimPrefix(skill, "---\n"), "\n---\n")
	if !strings.HasPrefix(skill, "---\n") || !ok {
		t.Fatalf("no front matter:\n%.120s", skill)
	}
	lines := strings.Split(head, "\n")
	if len(lines) != 2 || lines[0] != "name: "+SkillName || !strings.HasPrefix(lines[1], "description: ") {
		t.Errorf("front matter = %q, want a name and a one-line description", head)
	}
	if description := strings.TrimPrefix(lines[1], "description: "); strings.Contains(description, ": ") {
		t.Errorf("the description %q has a colon and a space, which YAML reads as a mapping", description)
	}
	if !strings.Contains(body, Full) {
		t.Error("the body does not carry Full")
	}
}
