package adapter

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/config"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// The settings the manifest declares are the ones Decode reads, with the defaults
// it applies: a configuration made of the declared defaults and a value for each
// required setting decodes to the same configuration as the required values alone.
func TestDeclaredSettingsMatchDecode(t *testing.T) {
	m, err := Manifest("linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	required := map[string]any{
		config.KeyHermesHome:   t.TempDir(),
		config.KeyBoard:        "lane",
		config.KeyHomeProfile:  "default",
		config.KeyHomePlatform: "feishu",
		config.KeyHomeChatID:   "oc_1",
		config.KeyAliases:      []string{"codex=nodeb/codex;modes=message"},
	}
	only, withDefaults := adapterproto.ConfigValues{}, adapterproto.ConfigValues{}
	for k, v := range required {
		raw, _ := json.Marshal(v)
		only[k], withDefaults[k] = raw, raw
	}
	for _, f := range m.Config.Instance {
		if _, ok := required[f.Key]; f.Required != ok {
			t.Errorf("%s: required in the manifest is %v; the test supplies a value: %v", f.Key, f.Required, ok)
		}
		if f.Default != nil {
			withDefaults[f.Key] = f.Default
		}
	}
	want, err := config.Decode(only)
	if err != nil {
		t.Fatal(err)
	}
	got, err := config.Decode(withDefaults)
	if err != nil {
		t.Fatalf("the declared settings are not accepted: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("declared defaults decode to\n%+v\nwhile Decode's own defaults are\n%+v", got, want)
	}
}
