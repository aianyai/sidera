package card

import (
	"errors"
	"strings"
	"testing"

	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/config"
)

func aliases(t *testing.T) config.Aliases {
	t.Helper()
	a, err := config.ParseAliases([]string{
		"codex=nodeb/codex;modes=new_session,message;presets=work",
		"fixed=nodeb/hermes;modes=message",
	})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestMatchesAssignee(t *testing.T) {
	for assignee, want := range map[string]bool{
		"sidera":            true,
		"Sidera":            true,
		"sidera:node-b":     true,
		"sidera:":           true,
		"sidera-inbox":      false,
		"sideraX":           false,
		"worker":            false,
		"":                  false,
		"x-sidera":          false,
		"sidera-inbox:more": false,
	} {
		if got := MatchesAssignee(assignee); got != want {
			t.Errorf("MatchesAssignee(%q) = %v, want %v", assignee, got, want)
		}
	}
}

func TestDecodeIsStrict(t *testing.T) {
	good := `{"sidera":1,"to":"codex","mode":"message","task":"x"}`
	if _, err := Decode(good); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"not json":       `hello`,
		"unknown member": `{"sidera":1,"to":"codex","mode":"message","task":"x","extra":1}`,
		"no version":     `{"to":"codex","mode":"message","task":"x"}`,
		"other version":  `{"sidera":2,"to":"codex","mode":"message","task":"x"}`,
		"two values":     good + good,
		"null":           `null`,
		"array":          `[1]`,
	} {
		_, err := Decode(body)
		var ie *InvalidError
		if !errors.As(err, &ie) {
			t.Errorf("%s: want an InvalidError, got %v", name, err)
		}
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name       string
		env        Envelope
		hasParents bool
		wantAlias  string // "" with wantErr "" means no alias (inherited)
		wantErr    string
	}{
		{"new session", Envelope{To: "CODEX", Mode: "new_session", Preset: "work", Task: "t"}, false, "codex", ""},
		{"message to a fixed address", Envelope{To: "fixed", Mode: "message", Task: "t"}, false, "fixed", ""},
		{"unknown alias", Envelope{To: "nobody", Mode: "message", Task: "t"}, false, "", "not an alias"},
		{"mode the alias does not allow", Envelope{To: "fixed", Mode: "new_session", Preset: "work", Task: "t"}, false, "", "does not allow mode"},
		{"preset the alias does not allow", Envelope{To: "codex", Mode: "new_session", Preset: "root", Task: "t"}, false, "", "does not allow the preset"},
		{"new session without preset", Envelope{To: "codex", Mode: "new_session", Task: "t"}, false, "", "does not allow the preset"},
		{"preset on a message", Envelope{To: "codex", Mode: "message", Preset: "work", Task: "t"}, false, "", "preset"},
		{"reply_to on a message", Envelope{To: "codex", Mode: "message", ReplyTo: "t_1", Task: "t"}, false, "", "reply_to"},
		{"unknown mode", Envelope{To: "codex", Mode: "broadcast", Task: "t"}, false, "", "mode"},
		{"empty task", Envelope{To: "codex", Mode: "message", Task: "  "}, false, "", "task"},
		{"message without to, no parents", Envelope{Mode: "message", Task: "t"}, false, "", "to"},
		{"message without to, follow-up", Envelope{Mode: "message", Task: "t"}, true, "", ""},
		{"new session without to, even with parents", Envelope{Mode: "new_session", Preset: "work", Task: "t"}, true, "", "to"},
		{"reply", Envelope{Mode: "reply", ReplyTo: "t_1", Task: "t"}, false, "", ""},
		{"reply without reply_to", Envelope{Mode: "reply", Task: "t"}, false, "", "reply_to"},
		{"reply with to", Envelope{To: "codex", Mode: "reply", ReplyTo: "t_1", Task: "t"}, false, "", "takes no"},
		{"reply with preset", Envelope{Mode: "reply", ReplyTo: "t_1", Preset: "work", Task: "t"}, false, "", "preset"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := tc.env.Validate(aliases(t), tc.hasParents)
			if tc.wantErr != "" {
				var ie *InvalidError
				if !errors.As(err, &ie) || !strings.Contains(ie.Reason, tc.wantErr) {
					t.Fatalf("want an InvalidError containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if r.Alias != tc.wantAlias {
				t.Errorf("alias = %q, want %q", r.Alias, tc.wantAlias)
			}
			if tc.wantAlias != "" && r.Address == "" {
				t.Error("alias without address")
			}
		})
	}
}

func TestCheckCreator(t *testing.T) {
	for _, tc := range []struct {
		allowed   []string
		createdBy string
		ok        bool
	}{
		{nil, "anyone", true},
		{[]string{"default", "feishu"}, "Feishu", true},
		{[]string{"default"}, "feishu", false},
		{[]string{"default"}, "", false},
	} {
		if err := CheckCreator(tc.allowed, tc.createdBy); (err == nil) != tc.ok {
			t.Errorf("CheckCreator(%v, %q) = %v", tc.allowed, tc.createdBy, err)
		}
	}
}
