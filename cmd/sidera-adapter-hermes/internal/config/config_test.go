package config

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func values(t *testing.T, kv map[string]any) adapterproto.ConfigValues {
	t.Helper()
	v := adapterproto.ConfigValues{}
	for k, x := range kv {
		raw, err := json.Marshal(x)
		if err != nil {
			t.Fatal(err)
		}
		v[k] = raw
	}
	return v
}

func valid() map[string]any {
	return map[string]any{
		KeyHermesHome:   "/home/o/.hermes",
		KeyBoard:        "default",
		KeyHomeProfile:  "default",
		KeyHomePlatform: "feishu",
		KeyHomeChatID:   "oc_1",
		KeyAliases:      []string{"Node-B/Codex=nodeb/codex;modes=new_session,message;presets=work,review"},
	}
}

func TestDecodeAppliesDefaultsAndLowercasesAliases(t *testing.T) {
	c, err := Decode(values(t, valid()))
	if err != nil {
		t.Fatal(err)
	}
	if c.NoReplyTimeout != 24*time.Hour || c.ClaimTTL != 7*24*time.Hour || c.MaxEndpoints != 64 || c.HermesPath != "hermes" {
		t.Errorf("defaults not applied: %+v", c)
	}
	a, ok := c.Aliases.Lookup("NODE-b/codex")
	if !ok || a.Name != "node-b/codex" || !a.Allows(ModeMessage) || !a.AllowsPreset("review") || a.AllowsPreset("other") {
		t.Errorf("alias lookup: %+v %v", a, ok)
	}
}

func TestDecodeRefuses(t *testing.T) {
	cases := []struct {
		name   string
		change func(m map[string]any)
		want   string
	}{
		{"unknown key", func(m map[string]any) { m["aliasess"] = "x" }, "aliasess"},
		{"no alias", func(m map[string]any) { m[KeyAliases] = []string{} }, "at least one alias"},
		{"relative hermes home", func(m map[string]any) { m[KeyHermesHome] = "hermes" }, KeyHermesHome},
		{"bad board", func(m map[string]any) { m[KeyBoard] = "--all" }, KeyBoard},
		{"no home chat", func(m map[string]any) { delete(m, KeyHomeChatID) }, "chat id"},
		{"bad duration", func(m map[string]any) { m[KeyNoReplyTimeout] = "a day" }, KeyNoReplyTimeout},
		{"claim shorter than wait", func(m map[string]any) { m[KeyClaimTTL] = "1h" }, KeyClaimTTL},
		{"endpoint cap too small", func(m map[string]any) { m[KeyMaxEndpoints] = 3 }, KeyMaxEndpoints},
		{"wrong type", func(m map[string]any) { m[KeyBoard] = 5 }, KeyBoard},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := valid()
			tc.change(m)
			_, err := Decode(values(t, m))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want an error naming %q, got %v", tc.want, err)
			}
		})
	}
}

func TestParseAliasesRefuses(t *testing.T) {
	cases := map[string][]string{
		"no modes":                    {"a=n/e"},
		"unknown mode":                {"a=n/e;modes=reply"},
		"new_session without preset":  {"a=n/e;modes=new_session"},
		"presets without new_session": {"a=n/e;modes=message;presets=work"},
		"bad preset name":             {"a=n/e;modes=new_session;presets=Not Ok"},
		"unknown key":                 {"a=n/e;modes=message;mode=message"},
		"no address":                  {"a;modes=message"},
		"space in address":            {"a=n /e;modes=message"},
		"duplicate by case":           {"a=n/e;modes=message", "A=n/f;modes=message"},
		"bad name":                    {"-a=n/e;modes=message"},
	}
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseAliases(entries); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}
