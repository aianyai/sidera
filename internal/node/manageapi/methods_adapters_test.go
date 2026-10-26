package manageapi

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// The methods for adapters and presets: what is installed and enabled, what the
// presets are, and what each change asks of the engine. Their results are words of
// the interface; the tests of the daemon read them through the whole node.

// The adapters as the owner lists them, written out by hand: the installed
// packages, the instances with the state of their first probe, and the version a
// change of the instances names. A disabled instance has no probe to show, and an
// instance whose probe has not reported has no time to show.
func TestAdaptersListAnswersWhatTheEngineKnowsInTheWordsOfTheInterface(t *testing.T) {
	r := newRig(t)
	r.svc.adapters = engine.AdapterList{
		Version: 4,
		Adapters: []engine.InstalledAdapter{
			{ID: "chat", Version: "1.0.0"},
			{ID: "relay", Version: "2.1.0"},
		},
		Instances: []engine.InstanceView{
			{Name: "old", Adapter: "relay"},
			{Name: "relay", Adapter: "relay", Enabled: true, Probe: engine.ProbeView{
				State: engine.ProbePassed, At: t0,
				Reached: []engine.ProbeReached{{Preset: "default", Frontend: adapterproto.FrontendCLI}},
			}},
			{Name: "waiting", Adapter: "chat", Enabled: true, Probe: engine.ProbeView{State: engine.ProbePending}},
		},
	}
	status, out := r.post("adapters.list", `{}`)
	want := `{"result":{"version":4,` +
		`"adapters":[{"id":"chat","version":"1.0.0"},{"id":"relay","version":"2.1.0"}],` +
		`"instances":[{"instance":"old","adapter":"relay","enabled":false},` +
		`{"instance":"relay","adapter":"relay","enabled":true,"probe":{"state":"passed","at":"2026-10-24T09:00:00Z","frontends":[{"preset":"default","frontend":"cli"}]}},` +
		`{"instance":"waiting","adapter":"chat","enabled":true,"probe":{"state":"pending","frontends":[]}}]}}`
	if status != 200 || strings.TrimSpace(string(out)) != want {
		t.Errorf("%d\n%s\nwant\n%s", status, out, want)
	}
	r.svc.adapters = engine.AdapterList{Version: 1}
	status, out = r.post("adapters.list", `{}`)
	if status != 200 || strings.TrimSpace(string(out)) != `{"result":{"version":1,"adapters":[],"instances":[]}}` {
		t.Errorf("nothing installed: %d %s", status, out)
	}
	for _, state := range []engine.ProbeState{engine.ProbePending, engine.ProbePassed, engine.ProbeFailed} {
		r.svc.adapters = engine.AdapterList{Version: 1, Instances: []engine.InstanceView{{Name: "x", Adapter: "x", Enabled: true, Probe: engine.ProbeView{State: state, At: t0.Add(time.Second)}}}}
		_, out = r.post("adapters.list", `{}`)
		if !strings.Contains(string(out), `"state":"`+state.String()+`"`) {
			t.Errorf("the state %v is not shown as %q: %s", state, state, out)
		}
	}
}

// The presets as the owner lists them: the name, the statement about local sources
// and what the preset says besides, in the form the file keeps, with the version a
// change of the collection names.
func TestPresetsListAnswersTheCollectionWithItsVersion(t *testing.T) {
	r := newRig(t)
	r.svc.presets = config.Presets{Version: 2, Items: map[string]config.Preset{
		"tight":   {LocalSources: authz.PresetLocalDeny, Description: "closed"},
		"default": {LocalSources: authz.PresetLocalAllow},
	}}
	status, out := r.post("presets.list", `{}`)
	want := `{"result":{"version":2,"presets":[` +
		`{"name":"default","local_sources":"allow"},` +
		`{"name":"tight","local_sources":"deny","description":"closed"}]}}`
	if status != 200 || strings.TrimSpace(string(out)) != want {
		t.Errorf("%d\n%s\nwant\n%s", status, out, want)
	}
}

// The params of these methods are read strictly: a package named by a path that is
// not absolute, a name that is absent, a member the method does not take are
// refused as invalid arguments that name the place, and nothing reaches the engine.
func TestTheParamsOfTheMethodsForAdaptersAndPresetsAreReadStrictly(t *testing.T) {
	r := newRig(t)
	id := opID(1)
	for _, tc := range []struct {
		name, method, body, path string
	}{
		{"no package", "adapters.install", `{"operation_id":"` + id + `","params":{}}`, "params.source"},
		{"a relative package", "adapters.install", `{"operation_id":"` + id + `","params":{"source":"adapters/relay"}}`, "params.source"},
		{"a package of two members", "adapters.install", `{"operation_id":"` + id + `","params":{"source":"` + abs(t) + `","also":1}}`, "params.also"},
		{"no adapter to enable", "adapters.enable", `{"operation_id":"` + id + `","expected_version":1,"params":{}}`, "params.adapter"},
		{"an instance that is empty", "adapters.enable", `{"operation_id":"` + id + `","expected_version":1,"params":{"adapter":"relay","instance":""}}`, "params.instance"},
		{"a configuration that is null", "adapters.enable", `{"operation_id":"` + id + `","expected_version":1,"params":{"adapter":"relay","config":null}}`, "params.config"},
		{"no instance to disable", "adapters.disable", `{"operation_id":"` + id + `","expected_version":1,"params":{}}`, "params.instance"},
		{"a listing that takes a member", "adapters.list", `{"params":{"all":true}}`, "params.all"},
		{"a preset without a name", "presets.add", `{"operation_id":"` + id + `","expected_version":1,"params":{"preset":{"local_sources":"allow"}}}`, "params.name"},
		{"a preset that is absent", "presets.add", `{"operation_id":"` + id + `","expected_version":1,"params":{"name":"x"}}`, "params.preset"},
		{"a removal without a name", "presets.remove", `{"operation_id":"` + id + `","expected_version":1,"params":{}}`, "params.name"},
		{"a listing that takes a member of its own", "presets.list", `{"params":{"all":true}}`, "params.all"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, out := r.post(tc.method, tc.body)
			e := r.wantError(400, "invalid_argument", "malformed_request", status, out)
			if e.Error.Details["path"] != tc.path {
				t.Errorf("the fault is placed at %v, want %q", e.Error.Details["path"], tc.path)
			}
			if n := len(r.svc.calls()); n != 0 {
				t.Errorf("a refused request reached the engine %d times", n)
			}
		})
	}
	// An installation names no setting, so it names no version, whatever its value.
	status, out := r.post("adapters.install", `{"operation_id":"`+id+`","expected_version":1,"params":{"source":"`+abs(t)+`"}}`)
	r.wantError(400, "invalid_argument", "unexpected_version", status, out)
	// An enabling that names no version is refused by the administrative rules.
	status, out = r.post("adapters.enable", `{"operation_id":"`+id+`","params":{"adapter":"relay"}}`)
	r.wantError(400, "invalid_argument", "missing_expected_version", status, out)
}

// abs is a directory that is an absolute path on this platform.
func abs(t testing.TB) string {
	t.Helper()
	return strings.ReplaceAll(filepath.ToSlash(t.TempDir()), `"`, "")
}

// What the engine is asked for each change: the name of the method, the version the
// request names, and the operation of the catalogue that the method is authorized
// as, with the class of a change of the presets. An enabling that leaves out the
// name of the instance names the adapter, and is the same request as one that
// writes the name down; a configuration or a preset has one fingerprint however it
// is spaced and ordered.
func TestTheCommandsOfAdaptersAndPresets(t *testing.T) {
	r := newRig(t)
	send := func(method, body string) engine.ManagementCommand {
		t.Helper()
		before := len(r.svc.calls())
		if status, out := r.post(method, body); status != 200 {
			t.Fatalf("%s answered %d %s", body, status, out)
		}
		calls := r.svc.calls()
		if len(calls) != before+1 {
			t.Fatalf("%d commands, want %d", len(calls), before+1)
		}
		return calls[before]
	}
	dir := abs(t)
	install := send("adapters.install", `{"operation_id":"`+opID(1)+`","params":{"source":"`+dir+`"}}`)
	if install.Name != "adapters.install" || install.Expected != 0 || install.Operation.Admin().Kind != admin.OpInstallAdapter {
		t.Errorf("command %+v", install)
	}
	omitted := send("adapters.enable", `{"operation_id":"`+opID(2)+`","expected_version":3,"params":{"adapter":"relay"}}`)
	named := send("adapters.enable", `{"operation_id":"`+opID(3)+`","expected_version":3,"params":{"instance":"relay","adapter":"relay"}}`)
	if omitted.Name != "adapters.enable" || omitted.Expected != 3 || omitted.Operation.Admin().Kind != admin.OpEnableAdapter {
		t.Errorf("command %+v", omitted)
	}
	if omitted.Fingerprint != named.Fingerprint {
		t.Error("an enabling that leaves out the instance and one that names it after the adapter are two requests")
	}
	other := send("adapters.enable", `{"operation_id":"`+opID(4)+`","expected_version":3,"params":{"adapter":"relay","instance":"second"}}`)
	if other.Fingerprint == named.Fingerprint {
		t.Error("an enabling of another instance shares a fingerprint")
	}
	cfg1 := send("adapters.enable", `{"operation_id":"`+opID(5)+`","expected_version":3,"params":{"adapter":"relay","instance":"third","config":{"b":1,"a":"x"}}}`)
	cfg2 := send("adapters.enable", `{"operation_id":"`+opID(6)+`","expected_version":3,"params":{"config":{ "a" : "x" , "b" : 1 },"instance":"third","adapter":"relay"}}`)
	if cfg1.Fingerprint != cfg2.Fingerprint {
		t.Error("the same configuration written in another order has another fingerprint")
	}
	disable := send("adapters.disable", `{"operation_id":"`+opID(7)+`","expected_version":5,"params":{"instance":"relay"}}`)
	if disable.Name != "adapters.disable" || disable.Expected != 5 || disable.Operation.Admin().Kind != admin.OpDisableAdapter {
		t.Errorf("command %+v", disable)
	}
	add := send("presets.add", `{"operation_id":"`+opID(8)+`","expected_version":2,"params":{"name":"tight","preset":{"local_sources":"deny"}}}`)
	if add.Name != "presets.add" || add.Expected != 2 ||
		add.Operation.Admin() != (admin.Operation{Kind: admin.OpChangePreset, PresetChange: admin.PresetChangeLoosening}) {
		t.Errorf("command %+v: adding a preset loosens", add)
	}
	remove := send("presets.remove", `{"operation_id":"`+opID(9)+`","expected_version":2,"params":{"name":"tight"}}`)
	if remove.Name != "presets.remove" || remove.Expected != 2 ||
		remove.Operation.Admin() != (admin.Operation{Kind: admin.OpChangePreset, PresetChange: admin.PresetChangeNarrowing}) {
		t.Errorf("command %+v: removing a preset narrows", remove)
	}
	// The class a request is authorized as is the class of the operation it is
	// handed to the engine as: the catalogue judges the first and the engine's
	// operation says the second.
	for method, class := range map[string]admin.PresetChangeClass{
		"presets.add": admin.PresetChangeLoosening, "presets.remove": admin.PresetChangeNarrowing,
	} {
		if got := r.srv.methods[method].op.PresetChange; got != class {
			t.Errorf("%s is authorized as class %v, want %v", method, got, class)
		}
	}
}
