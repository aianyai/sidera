package adapterproto_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
)

func TestInitializeParamsRules(t *testing.T) {
	method(t, func() adapterproto.InitializeParams {
		return adapterproto.InitializeParams{
			Protocol: adapterproto.Range{Min: 1, Max: 1},
			Daemon:   adapterproto.DaemonInfo{Version: "0.1.0"},
			Instance: adapterproto.InstanceInfo{ID: "work", LockPath: "/state/lock"},
		}
	}, []row[adapterproto.InitializeParams]{
		{"protocol range is validated", func(p *adapterproto.InitializeParams) { p.Protocol = adapterproto.Range{Min: 3, Max: 2} }, "protocol", true},
		{"protocol range of zero", func(p *adapterproto.InitializeParams) { p.Protocol = adapterproto.Range{} }, "protocol.min", false},
		{"daemon version required", func(p *adapterproto.InitializeParams) { p.Daemon.Version = "" }, "daemon.version", false},
		{"daemon version at the limit", func(p *adapterproto.InitializeParams) { p.Daemon.Version = repeat("v", 64) }, "", false},
		{"daemon version over the limit", func(p *adapterproto.InitializeParams) { p.Daemon.Version = repeat("v", 65) }, "daemon.version", false},
		{"instance id required", func(p *adapterproto.InitializeParams) { p.Instance.ID = "" }, "instance.id", false},
		{"instance id over the limit", func(p *adapterproto.InitializeParams) { p.Instance.ID = repeat("i", 129) }, "instance.id", false},
		{"lock path required", func(p *adapterproto.InitializeParams) { p.Instance.LockPath = "" }, "instance.lock_path", false},
		{"lock path at the limit", func(p *adapterproto.InitializeParams) { p.Instance.LockPath = repeat("p", 4096) }, "", false},
		{"lock path over the limit", func(p *adapterproto.InitializeParams) { p.Instance.LockPath = repeat("p", 4097) }, "instance.lock_path", false},
		{"lengths count characters, not bytes", func(p *adapterproto.InitializeParams) { p.Instance.ID = repeat("é", 128) }, "", false},
	})
}

func TestInitializeResultRules(t *testing.T) {
	method(t, func() adapterproto.InitializeResult {
		return adapterproto.InitializeResult{ProtocolVersion: 1, Adapter: adapterproto.AdapterInfo{ID: "example", Version: "0.3.1"},
			Capabilities: adapterproto.Capabilities{Direct: &adapterproto.DirectCapabilities{MaxEndpoints: 5}}}
	}, []row[adapterproto.InitializeResult]{
		{"protocol version zero", func(r *adapterproto.InitializeResult) { r.ProtocolVersion = 0 }, "protocol_version", false},
		{"protocol version over the bound", func(r *adapterproto.InitializeResult) { r.ProtocolVersion = 65536 }, "protocol_version", false},
		{"adapter id uppercase", func(r *adapterproto.InitializeResult) { r.Adapter.ID = "Example" }, "adapter.id", false},
		{"adapter id of 32 characters", func(r *adapterproto.InitializeResult) { r.Adapter.ID = "a" + repeat("b", 31) }, "", false},
		{"adapter id of 33 characters", func(r *adapterproto.InitializeResult) { r.Adapter.ID = "a" + repeat("b", 32) }, "adapter.id", false},
		{"adapter id starting with a dash", func(r *adapterproto.InitializeResult) { r.Adapter.ID = "-a" }, "adapter.id", false},
		{"adapter id with an underscore", func(r *adapterproto.InitializeResult) { r.Adapter.ID = "a_b" }, "adapter.id", false},
		{"adapter version not SemVer", func(r *adapterproto.InitializeResult) { r.Adapter.Version = "1.0" }, "adapter.version", false},
		{"adapter version with pre-release and build", func(r *adapterproto.InitializeResult) { r.Adapter.Version = "1.0.0-rc.1+b5" }, "", false},
		{"capabilities are validated", func(r *adapterproto.InitializeResult) { r.Capabilities.Direct.MaxEndpoints = 0 }, "capabilities.direct.max_endpoints", false},
		{"no capabilities at all is a valid value", func(r *adapterproto.InitializeResult) { r.Capabilities = adapterproto.Capabilities{} }, "", false},
	})
}

func TestCapabilitiesRules(t *testing.T) {
	method(t, func() adapterproto.Capabilities {
		return adapterproto.Capabilities{
			Runtime: &adapterproto.RuntimeCapabilities{Interrupt: true, AckOnBehalf: true, ServerRequests: true, Frontends: []adapterproto.Frontend{"cli", "mcp"}},
			Direct:  &adapterproto.DirectCapabilities{MaxEndpoints: 50},
		}
	}, []row[adapterproto.Capabilities]{
		{"runtime needs a front end", func(c *adapterproto.Capabilities) { c.Runtime.Frontends = nil }, "frontends", false},
		{"runtime with an empty front-end list", func(c *adapterproto.Capabilities) { c.Runtime.Frontends = []adapterproto.Frontend{} }, "frontends", false},
		{"one front end is enough", func(c *adapterproto.Capabilities) { c.Runtime.Frontends = []adapterproto.Frontend{"mcp"} }, "", false},
		{"front end unknown", func(c *adapterproto.Capabilities) { c.Runtime.Frontends = []adapterproto.Frontend{"web"} }, "frontends[0]", false},
		{"front end direct is not an agent tool", func(c *adapterproto.Capabilities) { c.Runtime.Frontends = []adapterproto.Frontend{"direct"} }, "frontends[0]", false},
		{"front end repeated", func(c *adapterproto.Capabilities) { c.Runtime.Frontends = []adapterproto.Frontend{"cli", "cli"} }, "frontends", false},
		{"flags false are valid", func(c *adapterproto.Capabilities) {
			c.Runtime.Interrupt, c.Runtime.AckOnBehalf, c.Runtime.ServerRequests = false, false, false
		}, "", false},
		{"direct limit 1", func(c *adapterproto.Capabilities) { c.Direct.MaxEndpoints = 1 }, "", false},
		{"direct limit 1000", func(c *adapterproto.Capabilities) { c.Direct.MaxEndpoints = 1000 }, "", false},
		{"direct limit 0", func(c *adapterproto.Capabilities) { c.Direct.MaxEndpoints = 0 }, "direct.max_endpoints", false},
		{"direct limit 1001", func(c *adapterproto.Capabilities) { c.Direct.MaxEndpoints = 1001 }, "direct.max_endpoints", false},
		{"runtime only", func(c *adapterproto.Capabilities) { c.Direct = nil }, "", false},
		{"direct only", func(c *adapterproto.Capabilities) { c.Runtime = nil }, "", false},
	})
}

func TestProcessIdentityRules(t *testing.T) {
	method(t, func() adapterproto.ProcessIdentity { return *proc() }, []row[adapterproto.ProcessIdentity]{
		{"pid required", func(p *adapterproto.ProcessIdentity) { p.PID = 0 }, "pid", false},
		{"pid negative", func(p *adapterproto.ProcessIdentity) { p.PID = -4 }, "pid", false},
		{"pid at the 32-bit bound", func(p *adapterproto.ProcessIdentity) { p.PID = 4294967295 }, "", false},
		{"pid over the 32-bit bound", func(p *adapterproto.ProcessIdentity) { p.PID = 4294967296 }, "pid", false},
		{"start key required", func(p *adapterproto.ProcessIdentity) { p.StartKey = "" }, "start_key", false},
		{"start key at the limit", func(p *adapterproto.ProcessIdentity) { p.StartKey = repeat("k", 64) }, "", false},
		{"start key over the limit", func(p *adapterproto.ProcessIdentity) { p.StartKey = repeat("k", 65) }, "start_key", false},
		{"start time is optional", func(p *adapterproto.ProcessIdentity) { p.StartedAtMS = 0 }, "", false},
		{"start time negative", func(p *adapterproto.ProcessIdentity) { p.StartedAtMS = -1 }, "started_at_ms", false},
		{"start time over the exact-integer bound", func(p *adapterproto.ProcessIdentity) { p.StartedAtMS = 1 << 53 }, "started_at_ms", false},
		{"container is optional", func(p *adapterproto.ProcessIdentity) { p.Container = nil }, "", false},
		{"container kind unknown", func(p *adapterproto.ProcessIdentity) { p.Container.Kind = "docker" }, "container.kind", false},
		{"container kind missing", func(p *adapterproto.ProcessIdentity) { p.Container.Kind = "" }, "container.kind", false},
		{"container with no isolation has no name", func(p *adapterproto.ProcessIdentity) { p.Container = &adapterproto.Container{Kind: "none"} }, "", false},
		{"container with no isolation and a name", func(p *adapterproto.ProcessIdentity) { p.Container.Kind = "none" }, "container", false},
		{"container with isolation needs a name", func(p *adapterproto.ProcessIdentity) { p.Container.Name = "" }, "container.name", false},
		{"container name at the limit", func(p *adapterproto.ProcessIdentity) { p.Container.Name = repeat("n", 256) }, "", false},
		{"container name over the limit", func(p *adapterproto.ProcessIdentity) { p.Container.Name = repeat("n", 257) }, "container.name", false},
	})
	for _, k := range []adapterproto.ContainerKind{"systemd_unit", "process_group", "launchd_job", "windows_job", "scheduled_task"} {
		p := *proc()
		p.Container = &adapterproto.Container{Kind: k, Name: "n"}
		if err := p.Validate(); err != nil {
			t.Errorf("container kind %s: %v", k, err)
		}
	}
}

func TestToolAccessRules(t *testing.T) {
	method(t, toolAccess, []row[adapterproto.ToolAccess]{
		{"env may be empty but present", func(a *adapterproto.ToolAccess) { a.Env = map[string]string{} }, "", false},
		{"env missing", func(a *adapterproto.ToolAccess) { a.Env = nil }, "env", false},
		{"name with a dash", func(a *adapterproto.ToolAccess) { a.Env = map[string]string{"A-B": "x"} }, "env", false},
		{"name starting with a digit", func(a *adapterproto.ToolAccess) { a.Env = map[string]string{"1A": "x"} }, "env", false},
		{"name starting with an underscore", func(a *adapterproto.ToolAccess) { a.Env = map[string]string{"_A": "x"} }, "", false},
		{"name over the limit", func(a *adapterproto.ToolAccess) { a.Env = map[string]string{repeat("A", 129): "x"} }, "env", false},
		{"empty value", func(a *adapterproto.ToolAccess) { a.Env = map[string]string{"A": ""} }, "", false},
		{"value at the limit", func(a *adapterproto.ToolAccess) { a.Env = map[string]string{"A": repeat("v", 4096)} }, "", false},
		{"value over the limit", func(a *adapterproto.ToolAccess) { a.Env = map[string]string{"A": repeat("v", 4097)} }, "env", false},
		{"64 variables", func(a *adapterproto.ToolAccess) {
			a.Env = map[string]string{}
			for i := 0; i < 64; i++ {
				a.Env["V"+string(rune('A'+i%26))+string(rune('a'+i/26))] = "x"
			}
		}, "", false},
		{"65 variables", func(a *adapterproto.ToolAccess) {
			a.Env = map[string]string{}
			for i := 0; i < 65; i++ {
				a.Env["V"+string(rune('A'+i%26))+string(rune('a'+i/26))] = "x"
			}
		}, "env", false},
	})
}

func TestConfigValuesRules(t *testing.T) {
	good := func() adapterproto.ConfigValues {
		return adapterproto.ConfigValues{"home": raw(`"/home/dev"`), "retries": raw(`3`), "verbose": raw(`true`), "extra": raw(`["a","b"]`), "none": raw(`[]`)}
	}
	method(t, good, []row[adapterproto.ConfigValues]{
		{"missing map", func(v *adapterproto.ConfigValues) { *v = nil }, "required map", false},
		{"key uppercase", func(v *adapterproto.ConfigValues) { (*v)["Home"] = raw(`"x"`) }, "Home", false},
		{"key starting with a digit", func(v *adapterproto.ConfigValues) { (*v)["1a"] = raw(`"x"`) }, "1a", false},
		{"key of 64 characters", func(v *adapterproto.ConfigValues) { (*v)["a"+repeat("b", 63)] = raw(`"x"`) }, "", false},
		{"key of 65 characters", func(v *adapterproto.ConfigValues) { (*v)["a"+repeat("b", 64)] = raw(`"x"`) }, "a", false},
		{"null value", func(v *adapterproto.ConfigValues) { (*v)["k"] = raw(`null`) }, "k", false},
		{"missing value", func(v *adapterproto.ConfigValues) { (*v)["k"] = nil }, "k", false},
		{"object value", func(v *adapterproto.ConfigValues) { (*v)["k"] = raw(`{}`) }, "k", false},
		{"float value", func(v *adapterproto.ConfigValues) { (*v)["k"] = raw(`1.5`) }, "k", false},
		{"negative integer", func(v *adapterproto.ConfigValues) { (*v)["k"] = raw(`-7`) }, "", false},
		{"integer at the exact bound", func(v *adapterproto.ConfigValues) { (*v)["k"] = raw(`9007199254740991`) }, "", false},
		{"integer over the exact bound", func(v *adapterproto.ConfigValues) { (*v)["k"] = raw(`9007199254740992`) }, "k", false},
		{"string at the limit", func(v *adapterproto.ConfigValues) { (*v)["k"] = raw(`"` + repeat("s", 4096) + `"`) }, "", false},
		{"string over the limit", func(v *adapterproto.ConfigValues) { (*v)["k"] = raw(`"` + repeat("s", 4097) + `"`) }, "k", false},
		{"list holding a number", func(v *adapterproto.ConfigValues) { (*v)["k"] = raw(`["a",1]`) }, "k[1]", false},
		{"list holding null", func(v *adapterproto.ConfigValues) { (*v)["k"] = raw(`["a",null]`) }, "k[1]", false},
		{"list of 256", func(v *adapterproto.ConfigValues) { (*v)["k"] = raw("[" + repeat(`"a",`, 255) + `"a"]`) }, "", false},
		{"list of 257", func(v *adapterproto.ConfigValues) { (*v)["k"] = raw("[" + repeat(`"a",`, 256) + `"a"]`) }, "k", false},
		{"list item over the limit", func(v *adapterproto.ConfigValues) { (*v)["k"] = raw(`["` + repeat("s", 4097) + `"]`) }, "k[0]", false},
	})
}

func TestStartParamsRules(t *testing.T) {
	session := func() adapterproto.SessionAttach {
		return adapterproto.SessionAttach{Endpoint: ep1, Preset: "default", NativeRef: "sess_1", Process: proc(), TurnActive: true, ToolAccess: toolAccess(),
			Checkpoint: raw(`{"phase":"ready"}`)}
	}
	method(t, func() adapterproto.StartParams {
		return adapterproto.StartParams{Node: "node-a", RuntimeEndpoint: epRT, Config: adapterproto.ConfigValues{},
			Presets:         []adapterproto.PresetConfig{{Name: "default", Config: adapterproto.ConfigValues{"model": raw(`"standard"`)}}},
			Sessions:        []adapterproto.SessionAttach{session()},
			DirectEndpoints: []adapterproto.DirectAttach{{Endpoint: ep2, Key: "chat:alice"}}}
	}, []row[adapterproto.StartParams]{
		{"node required", func(p *adapterproto.StartParams) { p.Node = "" }, "node", false},
		{"node over the limit", func(p *adapterproto.StartParams) { p.Node = repeat("n", 129) }, "node", false},
		{"runtime endpoint is optional", func(p *adapterproto.StartParams) { p.RuntimeEndpoint = "" }, "", false},
		{"runtime endpoint over the limit", func(p *adapterproto.StartParams) { p.RuntimeEndpoint = adapterproto.EndpointRef(repeat("e", 129)) }, "runtime_endpoint", false},
		{"config required", func(p *adapterproto.StartParams) { p.Config = nil }, "config", false},
		{"config values validated", func(p *adapterproto.StartParams) { p.Config["k"] = raw(`null`) }, "config.k", false},
		{"presets required", func(p *adapterproto.StartParams) { p.Presets = nil }, "presets", false},
		{"no presets", func(p *adapterproto.StartParams) { p.Presets = []adapterproto.PresetConfig{} }, "", false},
		{"preset name uppercase", func(p *adapterproto.StartParams) { p.Presets[0].Name = "Default" }, "presets[0].name", false},
		{"preset name with a dot, dash and underscore", func(p *adapterproto.StartParams) { p.Presets[0].Name = "a.b-c_d" }, "", false},
		{"preset name of 64", func(p *adapterproto.StartParams) { p.Presets[0].Name = repeat("p", 64) }, "", false},
		{"preset name of 65", func(p *adapterproto.StartParams) { p.Presets[0].Name = repeat("p", 65) }, "presets[0].name", false},
		{"preset config required", func(p *adapterproto.StartParams) { p.Presets[0].Config = nil }, "presets[0].config", false},
		{"sessions required", func(p *adapterproto.StartParams) { p.Sessions = nil }, "sessions", false},
		{"session endpoint required", func(p *adapterproto.StartParams) { p.Sessions[0].Endpoint = "" }, "sessions[0].endpoint", false},
		{"session preset invalid", func(p *adapterproto.StartParams) { p.Sessions[0].Preset = "Bad Preset" }, "sessions[0].preset", false},
		{"session native ref required", func(p *adapterproto.StartParams) { p.Sessions[0].NativeRef = "" }, "sessions[0].native_ref", false},
		{"session native ref of 256", func(p *adapterproto.StartParams) { p.Sessions[0].NativeRef = adapterproto.NativeRef(repeat("r", 256)) }, "", false},
		{"session native ref of 257", func(p *adapterproto.StartParams) { p.Sessions[0].NativeRef = adapterproto.NativeRef(repeat("r", 257)) }, "sessions[0].native_ref", false},
		{"session process optional", func(p *adapterproto.StartParams) { p.Sessions[0].Process = nil }, "", false},
		{"session process validated", func(p *adapterproto.StartParams) { p.Sessions[0].Process.PID = 0 }, "sessions[0].process.pid", false},
		{"session tool access validated", func(p *adapterproto.StartParams) { p.Sessions[0].ToolAccess.Env = nil }, "sessions[0].tool_access", false},
		{"session checkpoint optional", func(p *adapterproto.StartParams) { p.Sessions[0].Checkpoint = nil }, "", false},
		{"session checkpoint is an object", func(p *adapterproto.StartParams) { p.Sessions[0].Checkpoint = raw(`[]`) }, "sessions[0].checkpoint", false},
		{"session checkpoint null", func(p *adapterproto.StartParams) { p.Sessions[0].Checkpoint = raw(`null`) }, "sessions[0].checkpoint", false},
		{"session checkpoint at the size limit", func(p *adapterproto.StartParams) {
			p.Sessions[0].Checkpoint = raw(`{"b":"` + repeat("a", 16384-8) + `"}`)
		}, "", false},
		{"session checkpoint over the size limit", func(p *adapterproto.StartParams) {
			p.Sessions[0].Checkpoint = raw(`{"b":"` + repeat("a", 16384-7) + `"}`)
		}, "sessions[0].checkpoint", true},
		{"session checkpoint size counts the compact form", func(p *adapterproto.StartParams) {
			p.Sessions[0].Checkpoint = raw(`{ "b" : "` + repeat("a", 16384-8) + `" }`)
		}, "", false},
		{"direct endpoints required", func(p *adapterproto.StartParams) { p.DirectEndpoints = nil }, "direct_endpoints", false},
		{"direct endpoint key required", func(p *adapterproto.StartParams) { p.DirectEndpoints[0].Key = "" }, "direct_endpoints[0].key", false},
		{"direct endpoint ref required", func(p *adapterproto.StartParams) { p.DirectEndpoints[0].Endpoint = "" }, "direct_endpoints[0].endpoint", false},
	})
}

func TestStartResultRules(t *testing.T) {
	method(t, func() adapterproto.StartResult {
		return adapterproto.StartResult{Sessions: []adapterproto.SessionState{{Endpoint: ep1, State: "attached", TurnActive: true, Process: proc()}}}
	}, []row[adapterproto.StartResult]{
		{"sessions required", func(r *adapterproto.StartResult) { r.Sessions = nil }, "sessions", false},
		{"no sessions", func(r *adapterproto.StartResult) { r.Sessions = []adapterproto.SessionState{} }, "", false},
		{"state unreachable", func(r *adapterproto.StartResult) { r.Sessions[0].State = "unreachable" }, "", false},
		{"state lost", func(r *adapterproto.StartResult) { r.Sessions[0].State = "lost" }, "", false},
		{"state unknown", func(r *adapterproto.StartResult) { r.Sessions[0].State = "gone" }, "sessions[0].state", false},
		{"state missing", func(r *adapterproto.StartResult) { r.Sessions[0].State = "" }, "sessions[0].state", false},
		{"endpoint required", func(r *adapterproto.StartResult) { r.Sessions[0].Endpoint = "" }, "sessions[0].endpoint", false},
		{"process optional", func(r *adapterproto.StartResult) { r.Sessions[0].Process = nil }, "", false},
		{"process validated", func(r *adapterproto.StartResult) { r.Sessions[0].Process.StartKey = "" }, "sessions[0].process.start_key", false},
	})
}

func TestProbeParamsRules(t *testing.T) {
	probe := func() adapterproto.ToolProbeRequest {
		return adapterproto.ToolProbeRequest{Preset: "default", Frontend: "cli", Token: token1, Env: map[string]string{"SIDERA_PROBE_TOKEN": token1}}
	}
	method(t, func() adapterproto.ProbeParams {
		return adapterproto.ProbeParams{ToolProbes: []adapterproto.ToolProbeRequest{probe()}}
	},
		[]row[adapterproto.ProbeParams]{
			{"tool probes required", func(p *adapterproto.ProbeParams) { p.ToolProbes = nil }, "tool_probes", false},
			{"no tool probes", func(p *adapterproto.ProbeParams) { p.ToolProbes = []adapterproto.ToolProbeRequest{} }, "", false},
			{"256 tool probes", func(p *adapterproto.ProbeParams) { p.ToolProbes = repeatProbe(probe(), 256) }, "", false},
			{"257 tool probes", func(p *adapterproto.ProbeParams) { p.ToolProbes = repeatProbe(probe(), 257) }, "tool_probes", false},
			{"preset invalid", func(p *adapterproto.ProbeParams) { p.ToolProbes[0].Preset = "" }, "tool_probes[0].preset", false},
			{"front end mcp", func(p *adapterproto.ProbeParams) { p.ToolProbes[0].Frontend = "mcp" }, "", false},
			{"front end direct", func(p *adapterproto.ProbeParams) { p.ToolProbes[0].Frontend = "direct" }, "tool_probes[0].frontend", false},
			{"token too short", func(p *adapterproto.ProbeParams) { p.ToolProbes[0].Token = "abc" }, "tool_probes[0].token", false},
			{"token with padding", func(p *adapterproto.ProbeParams) { p.ToolProbes[0].Token = adapterproto.Token(token1[:42] + "=") }, "tool_probes[0].token", false},
			{"token with a last character that carries stray bits", func(p *adapterproto.ProbeParams) { p.ToolProbes[0].Token = adapterproto.Token(token1[:42] + "B") }, "tool_probes[0].token", false},
			{"token with a character outside base64url", func(p *adapterproto.ProbeParams) {
				p.ToolProbes[0].Token = adapterproto.Token(token1[:10] + "+" + token1[11:])
			}, "tool_probes[0].token", false},
			{"token of 44 characters", func(p *adapterproto.ProbeParams) { p.ToolProbes[0].Token = adapterproto.Token(token1 + "A") }, "tool_probes[0].token", false},
			{"env required", func(p *adapterproto.ProbeParams) { p.ToolProbes[0].Env = nil }, "tool_probes[0].env", false},
			{"env name invalid", func(p *adapterproto.ProbeParams) { p.ToolProbes[0].Env = map[string]string{"a b": "x"} }, "tool_probes[0].env", false},
		})
}

func repeatProbe(p adapterproto.ToolProbeRequest, n int) []adapterproto.ToolProbeRequest {
	out := make([]adapterproto.ToolProbeRequest, n)
	for i := range out {
		out[i] = p
	}
	return out
}

func TestProbeResultRules(t *testing.T) {
	method(t, func() adapterproto.ProbeResult {
		return adapterproto.ProbeResult{
			Runtime:      &adapterproto.RuntimeInfo{Name: "example-runtime", Version: "2.1.307"},
			Auth:         "valid",
			Dependencies: []adapterproto.Dependency{{Name: "git", State: "ok", Detail: "2.45"}},
			Presets:      []adapterproto.PresetStatus{{Name: "default", Usable: false, Problems: []string{"project_dir is missing"}}},
			ToolProbes:   []adapterproto.ToolProbeResult{{Preset: "default", Frontend: "cli", Outcome: "ran", Method: "sandboxed", Detail: "ok"}},
		}
	}, []row[adapterproto.ProbeResult]{
		{"runtime optional", func(r *adapterproto.ProbeResult) { r.Runtime = nil }, "", false},
		{"runtime name required", func(r *adapterproto.ProbeResult) { r.Runtime.Name = "" }, "runtime.name", false},
		{"runtime name over the limit", func(r *adapterproto.ProbeResult) { r.Runtime.Name = repeat("n", 65) }, "runtime.name", false},
		{"runtime version required", func(r *adapterproto.ProbeResult) { r.Runtime.Version = "" }, "runtime.version", false},
		{"runtime version over the limit", func(r *adapterproto.ProbeResult) { r.Runtime.Version = repeat("1", 65) }, "runtime.version", false},
		{"auth required", func(r *adapterproto.ProbeResult) { r.Auth = "" }, "auth", false},
		{"auth invalid", func(r *adapterproto.ProbeResult) { r.Auth = "invalid" }, "", false},
		{"auth unknown", func(r *adapterproto.ProbeResult) { r.Auth = "unknown" }, "", false},
		{"auth not a state", func(r *adapterproto.ProbeResult) { r.Auth = "maybe" }, "auth", false},
		{"dependencies required", func(r *adapterproto.ProbeResult) { r.Dependencies = nil }, "dependencies", false},
		{"dependency name required", func(r *adapterproto.ProbeResult) { r.Dependencies[0].Name = "" }, "dependencies[0].name", false},
		{"dependency name over the limit", func(r *adapterproto.ProbeResult) { r.Dependencies[0].Name = repeat("d", 129) }, "dependencies[0].name", false},
		{"dependency state missing", func(r *adapterproto.ProbeResult) { r.Dependencies[0].State = "missing" }, "", false},
		{"dependency state wrong_version", func(r *adapterproto.ProbeResult) { r.Dependencies[0].State = "wrong_version" }, "", false},
		{"dependency state unknown", func(r *adapterproto.ProbeResult) { r.Dependencies[0].State = "old" }, "dependencies[0].state", false},
		{"dependency detail optional", func(r *adapterproto.ProbeResult) { r.Dependencies[0].Detail = "" }, "", false},
		{"dependency detail over the limit", func(r *adapterproto.ProbeResult) { r.Dependencies[0].Detail = repeat("d", 1025) }, "dependencies[0].detail", false},
		{"presets required", func(r *adapterproto.ProbeResult) { r.Presets = nil }, "presets", false},
		{"preset name invalid", func(r *adapterproto.ProbeResult) { r.Presets[0].Name = "X" }, "presets[0].name", false},
		{"preset problems optional", func(r *adapterproto.ProbeResult) { r.Presets[0].Problems = nil }, "", false},
		{"preset with 32 problems", func(r *adapterproto.ProbeResult) { r.Presets[0].Problems = repeatStr("p", 32) }, "", false},
		{"preset with 33 problems", func(r *adapterproto.ProbeResult) { r.Presets[0].Problems = repeatStr("p", 33) }, "presets[0].problems", false},
		{"preset problem empty", func(r *adapterproto.ProbeResult) { r.Presets[0].Problems = []string{""} }, "presets[0].problems[0]", false},
		{"tool probes required", func(r *adapterproto.ProbeResult) { r.ToolProbes = nil }, "tool_probes", false},
		{"tool probe not run", func(r *adapterproto.ProbeResult) { r.ToolProbes[0].Outcome = "not_run"; r.ToolProbes[0].Method = "" }, "", false},
		{"tool probe failed", func(r *adapterproto.ProbeResult) { r.ToolProbes[0].Outcome = "failed" }, "", false},
		{"tool probe outcome unknown", func(r *adapterproto.ProbeResult) { r.ToolProbes[0].Outcome = "skipped" }, "tool_probes[0].outcome", false},
		{"tool probe method unsandboxed", func(r *adapterproto.ProbeResult) { r.ToolProbes[0].Method = "unsandboxed" }, "", false},
		{"tool probe method native turn", func(r *adapterproto.ProbeResult) { r.ToolProbes[0].Method = "native_turn" }, "", false},
		{"tool probe method unknown", func(r *adapterproto.ProbeResult) { r.ToolProbes[0].Method = "magic" }, "tool_probes[0].method", false},
		{"tool probe front end invalid", func(r *adapterproto.ProbeResult) { r.ToolProbes[0].Frontend = "direct" }, "tool_probes[0].frontend", false},
		{"256 tool probes", func(r *adapterproto.ProbeResult) { r.ToolProbes = repeatResult(r.ToolProbes[0], 256) }, "", false},
		{"257 tool probes", func(r *adapterproto.ProbeResult) { r.ToolProbes = repeatResult(r.ToolProbes[0], 257) }, "tool_probes", false},
	})
}

func repeatStr(s string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = s
	}
	return out
}

func repeatResult(p adapterproto.ToolProbeResult, n int) []adapterproto.ToolProbeResult {
	out := make([]adapterproto.ToolProbeResult, n)
	for i := range out {
		out[i] = p
	}
	return out
}

func TestAvailabilityRules(t *testing.T) {
	method(t, func() adapterproto.AvailabilityParams {
		return adapterproto.AvailabilityParams{Conditions: []adapterproto.Availability{
			{Condition: "runtime_auth", Endpoints: []adapterproto.EndpointRef{ep1}, SinceMS: 1792713540000, Detail: "token expired"}}}
	}, []row[adapterproto.AvailabilityParams]{
		{"conditions required", func(p *adapterproto.AvailabilityParams) { p.Conditions = nil }, "conditions", false},
		{"healthy is an empty list", func(p *adapterproto.AvailabilityParams) { p.Conditions = []adapterproto.Availability{} }, "", false},
		{"condition native_unavailable", func(p *adapterproto.AvailabilityParams) { p.Conditions[0].Condition = "native_unavailable" }, "", false},
		{"condition dependency_missing", func(p *adapterproto.AvailabilityParams) { p.Conditions[0].Condition = "dependency_missing" }, "", false},
		{"condition version_untested is the daemon's own judgment", func(p *adapterproto.AvailabilityParams) { p.Conditions[0].Condition = "version_untested" }, "conditions[0].condition", false},
		{"condition adapter_offline is the daemon's own", func(p *adapterproto.AvailabilityParams) { p.Conditions[0].Condition = "adapter_offline" }, "conditions[0].condition", false},
		{"condition missing", func(p *adapterproto.AvailabilityParams) { p.Conditions[0].Condition = "" }, "conditions[0].condition", false},
		{"endpoints optional", func(p *adapterproto.AvailabilityParams) { p.Conditions[0].Endpoints = nil }, "", false},
		{"endpoint invalid", func(p *adapterproto.AvailabilityParams) { p.Conditions[0].Endpoints = []adapterproto.EndpointRef{""} }, "conditions[0].endpoints[0]", false},
		{"since optional", func(p *adapterproto.AvailabilityParams) { p.Conditions[0].SinceMS = 0 }, "", false},
		{"since negative", func(p *adapterproto.AvailabilityParams) { p.Conditions[0].SinceMS = -1 }, "conditions[0].since_ms", false},
		{"detail optional", func(p *adapterproto.AvailabilityParams) { p.Conditions[0].Detail = "" }, "", false},
		{"detail over the limit", func(p *adapterproto.AvailabilityParams) { p.Conditions[0].Detail = repeat("d", 1025) }, "conditions[0].detail", false},
	})
}

// TestCancelRequestRules: the type is the peer layer's, so only that the method
// table and Decode apply its rule and report a failure like any other message.
func TestCancelRequestRules(t *testing.T) {
	for _, tc := range []struct {
		in     string
		ok     bool
		field  string
		reason string
	}{
		{`{"request_id":14}`, true, "", ""},
		{`{"request_id":"probe-2"}`, true, "", ""},
		{`{"request_id":9007199254740992}`, false, "", "request id"},
		{`{"request_id":""}`, false, "", "request id"},
		{`{"request_id":1.5}`, false, "", "request id"},
		{`{"request_id":null}`, false, "", "null"},
		{`{}`, false, "", "request_id"},
		{`{"request_id":1,"extra":2}`, false, "", "extra"},
	} {
		t.Run(tc.in, func(t *testing.T) {
			var p adapterproto.CancelRequestParams
			err := adapterproto.Decode([]byte(tc.in), &p)
			if (err == nil) != tc.ok {
				t.Fatalf("Decode = %v, want ok=%v", err, tc.ok)
			}
			if err == nil {
				return
			}
			var ve *adapterproto.ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("error is %T, want a *ValidationError", err)
			}
			if !strings.Contains(err.Error(), tc.reason) {
				t.Errorf("error %q does not mention %q", err, tc.reason)
			}
		})
	}
	var p adapterproto.CancelRequestParams
	err := adapterproto.Decode([]byte(`{"request_id":1}`), &p)
	if err != nil || p.RequestID != peer.NumberID(1) {
		t.Fatalf("decoded %+v, %v", p, err)
	}
	if _, err := adapterproto.Encode(&adapterproto.CancelRequestParams{}); err == nil {
		t.Error("an unset id was encoded")
	} else if ve := new(adapterproto.ValidationError); !errors.As(err, &ve) || ve.Field != "request_id" {
		t.Errorf("Encode error %v, want a ValidationError at request_id", err)
	}
	if adapterproto.MethodCancelRequest != peer.MethodCancelRequest {
		t.Errorf("method name %q differs from the peer's %q", adapterproto.MethodCancelRequest, peer.MethodCancelRequest)
	}
}
