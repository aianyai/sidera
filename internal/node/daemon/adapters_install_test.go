package daemon_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// relay is the adapter the owner installs in these tests: the fake adapter, under
// a name no node has installed before.
const relay = "relay"

func relayManifest() fakeadapter.ManifestOptions {
	o := fakeadapter.DefaultManifestOptions()
	o.ID = relay
	return o
}

// writePackage writes an adapter package of the manifest to a directory of the real
// disk, as an author's build leaves it, and returns the directory.
func writePackage(t testing.TB, o fakeadapter.ManifestOptions) string {
	t.Helper()
	dir := t.TempDir()
	raw, err := o.Encode()
	if err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(dir, filepath.FromSlash(o.Program))
	if err := os.MkdirAll(filepath.Dir(program), 0o755); err != nil {
		t.Fatal(err)
	}
	for p, c := range map[string][]byte{filepath.Join(dir, "sidera-adapter.json"): raw, program: []byte("#!fake\n")} {
		if err := os.WriteFile(p, c, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

type adaptersListed struct {
	Result struct {
		Version  uint64 `json:"version"`
		Adapters []struct {
			ID      string `json:"id"`
			Version string `json:"version"`
		} `json:"adapters"`
		Instances []struct {
			Instance string `json:"instance"`
			Adapter  string `json:"adapter"`
			Enabled  bool   `json:"enabled"`
			Probe    *struct {
				State     string `json:"state"`
				At        string `json:"at"`
				Frontends []struct {
					Preset   string `json:"preset"`
					Frontend string `json:"frontend"`
				} `json:"frontends"`
			} `json:"probe"`
		} `json:"instances"`
	} `json:"result"`
}

func listAdapters(t testing.TB, home string) adaptersListed {
	t.Helper()
	status, body := manage(t, home, "adapters.list", map[string]any{})
	if status != 200 {
		t.Fatalf("adapters.list answered %d: %s", status, body)
	}
	var l adaptersListed
	if err := json.Unmarshal(body, &l); err != nil {
		t.Fatalf("adapters.list: %v\n%s", err, body)
	}
	return l
}

func (l adaptersListed) instance(name string) (enabled bool, state string, found bool) {
	for _, in := range l.Result.Instances {
		if in.Instance == name {
			if in.Probe != nil {
				state = in.Probe.State
			}
			return in.Enabled, state, true
		}
	}
	return false, "", false
}

// The owner installs a package and enables an instance of it through the
// management interface, and the node does the rest: it starts the adapter through
// the host, the adapter says its tool probes ran, and the node calls the probe
// passed only when the tokens it issued have been presented to it. Disabling the
// instance stops the adapter and closes its endpoints. The presenting of the token
// over the Agent socket is the Agent interface's; the test presents it through the
// engine method that the interface calls.
func TestAnAdapterInstalledAndEnabledThroughTheManagementInterfaceIsStartedProbedAndStopped(t *testing.T) {
	noLeaks(t)
	c, clk := initialized(t)
	opts := relayManifest()
	a := newAdapters(t, clk, opts, nil)
	c.AdapterHost = a.host
	d := started(t, c)
	ctx := context.Background()

	// install: the answer is the adapter, the package is copied and no longer needed
	src := writePackage(t, opts)
	status, body := manage(t, c.Home, "adapters.install", map[string]any{"operation_id": opID(1), "params": map[string]any{"source": src}})
	installed := answerOf(t, status, body)
	if want := `{"adapter":{"id":"relay","version":"` + opts.Version + `"}}`; string(installed.Result) != want || installed.Replayed {
		t.Fatalf("the installation: %s (replayed %v), want %s", installed.Result, installed.Replayed, want)
	}
	if err := os.RemoveAll(src); err != nil {
		t.Fatal(err)
	}
	status, body = manage(t, c.Home, "adapters.install", map[string]any{"operation_id": opID(1), "params": map[string]any{"source": src}})
	if again := answerOf(t, status, body); !again.Replayed || string(again.Result) != string(installed.Result) {
		t.Errorf("the installation sent again, when the package is gone: %s", body)
	}
	listed := listAdapters(t, c.Home)
	if len(listed.Result.Adapters) != 1 || listed.Result.Adapters[0].ID != relay || listed.Result.Adapters[0].Version != opts.Version {
		t.Fatalf("the adapters listed: %+v", listed.Result.Adapters)
	}
	if _, _, found := listed.instance(relay); found {
		t.Fatal("an instance of relay exists before it was enabled")
	}
	if got := a.launcher.Launches(relay); len(got) != 0 {
		t.Fatalf("%d adapter processes were started by an installation", len(got))
	}

	// enable: the host starts the adapter, which is taken on and probed
	status, body = manage(t, c.Home, "adapters.enable", map[string]any{
		"operation_id": opID(2), "expected_version": listed.Result.Version, "params": map[string]any{"adapter": relay},
	})
	enabled := answerOf(t, status, body)
	if want := `{"instance":"relay","enabled":true,"version":` + itoa(listed.Result.Version+1) + `}`; string(enabled.Result) != want {
		t.Fatalf("enabling: %s, want %s", enabled.Result, want)
	}
	launch := a.launchesOf(t, relay, 1)[0]
	if err := waitFor(func() bool {
		v, err := d.Engine().Adapter(ctx, relay)
		return err == nil && v.Online && v.Probed
	}); err != nil {
		t.Fatalf("the adapter was not taken on and probed: %v", err)
	}
	if m := launch.Methods(); len(m) < 3 || m[0] != adapterproto.MethodInitialize || m[1] != adapterproto.MethodAdapterStart {
		t.Errorf("the daemon wrote %v to the adapter", m)
	}
	listed = listAdapters(t, c.Home)
	if enabled, state, found := listed.instance(relay); !found || !enabled || state != "pending" {
		t.Errorf("the instance after the adapter reported: enabled %v, probe %q, found %v; want it pending: ran is not enough", enabled, state, found)
	}

	// the tokens are presented to the node, as the tools the adapter ran would
	reqs, err := d.Engine().ProbeTokens(ctx, relay)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range reqs {
		if _, err := d.Engine().PresentProbeToken(ctx, q.Token, q.Frontend); err != nil {
			t.Fatalf("presenting the token of %s %s: %v", q.Preset, q.Frontend, err)
		}
	}
	listed = listAdapters(t, c.Home)
	enabledNow, state, _ := listed.instance(relay)
	if !enabledNow || state != "passed" {
		t.Fatalf("the instance after the tokens were presented: enabled %v, probe %q; want it passed", enabledNow, state)
	}
	if v, err := d.Engine().Adapter(ctx, relay); err != nil || !v.RuntimeDeclared {
		t.Errorf("the runtime endpoint is not declared after the probe passed: %+v, %v", v, err)
	}

	// disable: the adapter is stopped and the endpoints of the instance are closed
	status, body = manage(t, c.Home, "adapters.disable", map[string]any{
		"operation_id": opID(3), "expected_version": listed.Result.Version, "params": map[string]any{"instance": relay},
	})
	disabled := answerOf(t, status, body)
	if want := `{"instance":"relay","enabled":false,"version":` + itoa(listed.Result.Version+1) + `}`; string(disabled.Result) != want {
		t.Fatalf("disabling: %s, want %s", disabled.Result, want)
	}
	if err := waitFor(func() bool { return launch.Ended() }); err != nil {
		t.Fatalf("the adapter was not stopped: %v", err)
	}
	settled(t, d)
	if _, err := d.Engine().Adapter(ctx, relay); err == nil {
		t.Error("the engine knows a disabled instance")
	}
	eps, err := d.Engine().Endpoints(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, ep := range eps {
		if ep.Instance == relay && ep.Status != store.EndpointClosed {
			t.Errorf("the endpoint %s of the disabled instance is %v", ep.Address, ep.Status)
		}
	}
	if enabled, _, found := listAdapters(t, c.Home).instance(relay); !found || enabled {
		t.Errorf("the disabled instance is listed as found %v, enabled %v; want it kept and disabled", found, enabled)
	}
}

// launchesOf waits for the n-th adapter process of the instance to have been started
// and returns them.
func (a *adapters) launchesOf(t testing.TB, instance string, n int) []*harness.AdapterLaunch {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if l := a.launcher.Launches(instance); len(l) >= n {
			return l
		}
		if time.Now().After(deadline) {
			t.Fatalf("fewer than %d adapter processes were started for %s", n, instance)
		}
		time.Sleep(time.Millisecond)
	}
}

func itoa(n uint64) string { b, _ := json.Marshal(n); return string(b) }

func waitFor(cond func() bool) error {
	deadline := time.Now().Add(30 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			return context.DeadlineExceeded
		}
		time.Sleep(time.Millisecond)
	}
	return nil
}

// What the owner's request gets wrong is told in the words of the interface, and
// leaves the disk as it was: a directory that is not there, a package that is not
// one, and an adapter that is not installed.
func TestWhatTheOwnerGetsWrongInInstallingAndEnablingIsToldInTheWordsOfTheInterface(t *testing.T) {
	noLeaks(t)
	c, _ := initialized(t)
	started(t, c)
	before := snapshot(t, c.Home)
	missing := filepath.Join(t.TempDir(), "nothing")
	empty := t.TempDir()
	for _, tc := range []struct {
		name, method string
		params       map[string]any
		expected     uint64
		status       int
		code         string
	}{
		{"a directory that is not there", "adapters.install", map[string]any{"source": missing}, 0, 400, "source_unreadable"},
		{"a directory that holds no package", "adapters.install", map[string]any{"source": empty}, 0, 400, "config_invalid"},
		{"an adapter that is not installed", "adapters.enable", map[string]any{"adapter": "ghost"}, 1, 404, "adapter_unknown"},
		{"an instance that does not exist", "adapters.disable", map[string]any{"instance": "ghost"}, 1, 404, "instance_unknown"},
		{"a preset that does not exist", "presets.remove", map[string]any{"name": "ghost"}, 1, 404, "preset_unknown"},
		{"a preset that makes no statement about local sources", "presets.add", map[string]any{"name": "x", "preset": map[string]any{"description": "d"}}, 1, 400, "config_invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := map[string]any{"operation_id": opID(10), "params": tc.params}
			if tc.expected != 0 {
				req["expected_version"] = tc.expected
			}
			status, body := manage(t, c.Home, tc.method, req)
			if status != tc.status || !strings.Contains(string(body), `"code":"`+tc.code+`"`) {
				t.Errorf("answered %d %s, want %d with the code %s", status, body, tc.status, tc.code)
			}
		})
	}
	after := snapshot(t, c.Home)
	for p, v := range before {
		if strings.HasPrefix(p, "config/") && after[p] != v {
			t.Errorf("%s changed", p)
		}
	}
	for p := range after {
		if _, ok := before[p]; !ok && strings.HasPrefix(p, "config/") {
			t.Errorf("%s was written", p)
		}
	}
}

// Presets are added, listed and removed through the interface, each change naming
// the version of the whole collection; the statement about local sources is
// required, and a stale version is a conflict that names the current one.
func TestPresetsAreAddedListedAndRemovedThroughTheManagementInterface(t *testing.T) {
	noLeaks(t)
	c, _ := initialized(t)
	started(t, c)
	list := func() (version uint64, body string) {
		t.Helper()
		status, out := manage(t, c.Home, "presets.list", map[string]any{})
		if status != 200 {
			t.Fatalf("presets.list answered %d: %s", status, out)
		}
		var l struct {
			Result struct {
				Version uint64 `json:"version"`
			} `json:"result"`
		}
		if err := json.Unmarshal(out, &l); err != nil {
			t.Fatal(err)
		}
		return l.Result.Version, string(out)
	}
	version, body := list()
	if !strings.Contains(body, `{"name":"default","local_sources":"allow"}`) {
		t.Fatalf("a node that was just made lists %s, want the default preset that allows local sources", body)
	}
	status, out := manage(t, c.Home, "presets.add", map[string]any{
		"operation_id": opID(20), "expected_version": version,
		"params": map[string]any{"name": "tight", "preset": map[string]any{"local_sources": "deny", "description": "closed"}},
	})
	added := answerOf(t, status, out)
	if want := `{"name":"tight","version":` + itoa(version+1) + `}`; string(added.Result) != want {
		t.Errorf("adding: %s, want %s", added.Result, want)
	}
	_, body = list()
	if !strings.Contains(body, `{"name":"tight","local_sources":"deny","description":"closed"}`) {
		t.Errorf("the list after adding: %s", body)
	}
	status, out = manage(t, c.Home, "presets.add", map[string]any{
		"operation_id": opID(21), "expected_version": version,
		"params": map[string]any{"name": "other", "preset": map[string]any{"local_sources": "allow"}},
	})
	if status != 409 || !strings.Contains(string(out), `"version_conflict"`) || !strings.Contains(string(out), `"current_version":`+itoa(version+1)) {
		t.Errorf("a stale version: %d %s", status, out)
	}
	status, out = manage(t, c.Home, "presets.add", map[string]any{
		"operation_id": opID(22), "expected_version": version + 1,
		"params": map[string]any{"name": "tight", "preset": map[string]any{"local_sources": "allow"}},
	})
	if status != 409 || !strings.Contains(string(out), `"preset_exists"`) {
		t.Errorf("a name that is taken: %d %s", status, out)
	}
	status, out = manage(t, c.Home, "presets.remove", map[string]any{
		"operation_id": opID(23), "expected_version": version + 1, "params": map[string]any{"name": "tight"},
	})
	removed := answerOf(t, status, out)
	if want := `{"name":"tight","version":` + itoa(version+2) + `}`; string(removed.Result) != want {
		t.Errorf("removing: %s, want %s", removed.Result, want)
	}
	if _, body = list(); strings.Contains(body, "tight") {
		t.Errorf("the list after removing: %s", body)
	}
}
