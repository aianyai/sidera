package manageapi

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"sort"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/engine"
)

// requireText refuses a member of the params that is empty: a name, an adapter, a
// path is never the empty string, and a request that carries one meant something
// else.
func requireText(value, path string) error {
	if value == "" {
		return apiwire.Malformed("the value is required and is not empty", path)
	}
	return nil
}

// refuseNull refuses a member that is carried as it was written and was written as
// null: null is no value, in this interface as in every file `config` reads.
func refuseNull(raw json.RawMessage, path string) error {
	if string(bytes.TrimSpace(raw)) == "null" {
		return apiwire.Malformed("null is no value", path)
	}
	return nil
}

// adapters.install: install the package in a directory of this machine, or upgrade
// the adapter to its version. The owner and the daemon are the same user on the
// same machine, so the package is named by an absolute path and the daemon copies it
// itself; nothing is uploaded.
var adaptersInstall = &method{
	name: "adapters.install", op: admin.Operation{Kind: admin.OpInstallAdapter},
	plan: func(_ *Server, raw json.RawMessage) (*plan, error) {
		var p struct {
			Source string `json:"source"`
		}
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		if err := requireText(p.Source, "params.source"); err != nil {
			return nil, err
		}
		if !filepath.IsAbs(p.Source) {
			return nil, apiwire.Malformed("the package is named by an absolute path", "params.source")
		}
		op := engine.InstallAdapterOp(p.Source, func(a engine.AdapterInstalled) ([]byte, error) {
			return json.Marshal(struct {
				Adapter struct {
					ID      string `json:"id"`
					Version string `json:"version"`
				} `json:"adapter"`
			}{Adapter: struct {
				ID      string `json:"id"`
				Version string `json:"version"`
			}{a.ID, a.Version}})
		})
		return &plan{canonical: canonical(p), operation: op}, nil
	},
}

// adapters.list: the installed adapters, the instances and the probe of each enabled
// one, with the version a change of the instances names.
var adaptersList = &method{
	name: "adapters.list", op: admin.Operation{Kind: admin.OpRead},
	plan: func(_ *Server, raw json.RawMessage) (*plan, error) {
		var p struct{}
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		return &plan{canonical: canonical(p), read: func(ctx context.Context, svc engine.ManageService) (any, error) {
			l, err := svc.AdapterList(ctx)
			if err != nil {
				return nil, err
			}
			return adapterListJSON(l), nil
		}}, nil
	},
}

type adapterListResult struct {
	Version   uint64          `json:"version"`
	Adapters  []installedJSON `json:"adapters"`
	Instances []instanceJSON  `json:"instances"`
}

type installedJSON struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

type instanceJSON struct {
	Instance string     `json:"instance"`
	Adapter  string     `json:"adapter"`
	Enabled  bool       `json:"enabled"`
	Probe    *probeJSON `json:"probe,omitempty"`
}

type probeJSON struct {
	State     string        `json:"state"`
	At        string        `json:"at,omitempty"`
	Frontends []reachedJSON `json:"frontends"`
}

type reachedJSON struct {
	Preset   string `json:"preset"`
	Frontend string `json:"frontend"`
}

func adapterListJSON(l engine.AdapterList) adapterListResult {
	out := adapterListResult{Version: uint64(l.Version), Adapters: []installedJSON{}, Instances: []instanceJSON{}}
	for _, a := range l.Adapters {
		out.Adapters = append(out.Adapters, installedJSON{ID: a.ID, Version: a.Version})
	}
	for _, in := range l.Instances {
		v := instanceJSON{Instance: in.Name, Adapter: in.Adapter, Enabled: in.Enabled}
		if in.Enabled {
			p := &probeJSON{State: in.Probe.State.String(), Frontends: []reachedJSON{}}
			if !in.Probe.At.IsZero() {
				p.At = in.Probe.At.UTC().Format(time.RFC3339)
			}
			for _, r := range in.Probe.Reached {
				p.Frontends = append(p.Frontends, reachedJSON{Preset: r.Preset, Frontend: string(r.Frontend)})
			}
			v.Probe = p
		}
		out.Instances = append(out.Instances, v)
	}
	return out
}

func instanceResult(c engine.InstanceChange) ([]byte, error) {
	return json.Marshal(struct {
		Instance string `json:"instance"`
		Enabled  bool   `json:"enabled"`
		Version  uint64 `json:"version"`
	}{c.Instance, c.Enabled, uint64(c.Version)})
}

// adapters.enable: enable an instance of an installed adapter, making it when it is
// new. The instance is named after the adapter when the request leaves it out, and
// the request is then the same as one that writes that name, which is why the
// fingerprint is made of the name the daemon resolved. The configuration values are
// `config`'s to read; this layer carries them.
var adaptersEnable = &method{
	name: "adapters.enable", op: admin.Operation{Kind: admin.OpEnableAdapter},
	plan: func(_ *Server, raw json.RawMessage) (*plan, error) {
		var p struct {
			Adapter  string          `json:"adapter"`
			Instance *string         `json:"instance,omitempty"`
			Config   json.RawMessage `json:"config,omitempty"`
		}
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		if err := requireText(p.Adapter, "params.adapter"); err != nil {
			return nil, err
		}
		instance := p.Adapter
		if p.Instance != nil {
			if err := requireText(*p.Instance, "params.instance"); err != nil {
				return nil, err
			}
			instance = *p.Instance
		}
		resolved := struct {
			Adapter  string          `json:"adapter"`
			Instance string          `json:"instance"`
			Config   json.RawMessage `json:"config,omitempty"`
		}{Adapter: p.Adapter, Instance: instance}
		if err := refuseNull(p.Config, "params.config"); err != nil {
			return nil, err
		}
		if len(p.Config) != 0 {
			resolved.Config = canonicalSettings(p.Config)
		}
		op := engine.EnableAdapterOp(p.Adapter, instance, resolved.Config, instanceResult)
		return &plan{canonical: canonical(resolved), operation: op}, nil
	},
}

// adapters.disable: disable an instance. It stays, with its settings and endpoint
// ids, so that enabling it again finds the same endpoints.
var adaptersDisable = &method{
	name: "adapters.disable", op: admin.Operation{Kind: admin.OpDisableAdapter},
	plan: func(_ *Server, raw json.RawMessage) (*plan, error) {
		var p struct {
			Instance string `json:"instance"`
		}
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		if err := requireText(p.Instance, "params.instance"); err != nil {
			return nil, err
		}
		return &plan{canonical: canonical(p), operation: engine.DisableAdapterOp(p.Instance, instanceResult)}, nil
	},
}

func presetResult(c engine.PresetChange) ([]byte, error) {
	return json.Marshal(struct {
		Name    string `json:"name"`
		Version uint64 `json:"version"`
	}{c.Name, uint64(c.Version)})
}

// presets.list: the presets, each with its name and the members of its document, and
// the version a change of the collection names.
var presetsList = &method{
	name: "presets.list", op: admin.Operation{Kind: admin.OpRead},
	plan: func(_ *Server, raw json.RawMessage) (*plan, error) {
		var p struct{}
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		return &plan{canonical: canonical(p), read: func(ctx context.Context, svc engine.ManageService) (any, error) {
			ps, err := svc.Presets(ctx)
			if err != nil {
				return nil, err
			}
			names := make([]string, 0, len(ps.Items))
			for name := range ps.Items {
				names = append(names, name)
			}
			sort.Strings(names)
			out := struct {
				Version uint64       `json:"version"`
				Presets []presetItem `json:"presets"`
			}{Version: uint64(ps.Version), Presets: []presetItem{}}
			for _, name := range names {
				wire, err := ps.Items[name].WireJSON()
				if err != nil {
					return nil, err
				}
				out.Presets = append(out.Presets, presetItem{name: name, wire: wire})
			}
			return out, nil
		}}, nil
	},
}

// presetItem is one preset as the list shows it: its name, then the members of its
// document, which is the form `config` keeps and `presets.add` takes.
type presetItem struct {
	name string
	wire []byte
}

func (p presetItem) MarshalJSON() ([]byte, error) {
	name, err := json.Marshal(p.name)
	if err != nil {
		return nil, err
	}
	// wire is a compact object with at least one member: the members follow the name.
	out := append([]byte(`{"name":`), name...)
	out = append(out, ',')
	return append(out, p.wire[1:]...), nil
}

// presets.add: add a preset from its document, which the daemon reads with the
// strict decoder of `config` and which must make the statement about local sources.
// Adding loosens what the node allows.
var presetsAdd = &method{
	name: "presets.add", op: admin.Operation{Kind: admin.OpChangePreset, PresetChange: admin.PresetChangeLoosening},
	plan: func(_ *Server, raw json.RawMessage) (*plan, error) {
		var p struct {
			Name   string          `json:"name"`
			Preset json.RawMessage `json:"preset"`
		}
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		if err := requireText(p.Name, "params.name"); err != nil {
			return nil, err
		}
		if len(p.Preset) == 0 {
			return nil, apiwire.Malformed("a preset is added from its document", "params.preset")
		}
		if err := refuseNull(p.Preset, "params.preset"); err != nil {
			return nil, err
		}
		p.Preset = canonicalSettings(p.Preset)
		return &plan{canonical: canonical(p), operation: engine.AddPresetOp(p.Name, p.Preset, presetResult)}, nil
	},
}

// presets.remove: remove a preset. Removing narrows what the node allows.
var presetsRemove = &method{
	name: "presets.remove", op: admin.Operation{Kind: admin.OpChangePreset, PresetChange: admin.PresetChangeNarrowing},
	plan: func(_ *Server, raw json.RawMessage) (*plan, error) {
		var p struct {
			Name string `json:"name"`
		}
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		if err := requireText(p.Name, "params.name"); err != nil {
			return nil, err
		}
		return &plan{canonical: canonical(p), operation: engine.RemovePresetOp(p.Name, presetResult)}, nil
	},
}
