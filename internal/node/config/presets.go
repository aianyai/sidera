package config

import (
	"encoding/json"
	"fmt"

	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// The two statements of the field local_sources. There is no third: the field
// is required, and a word that merely resembles a statement is not one.
const (
	localAllow = "allow"
	localDeny  = "deny"
)

// presetWire is one preset of presets.json, and the document presets.add takes.
type presetWire struct {
	LocalSources string                                `json:"local_sources"`
	Description  string                                `json:"description,omitempty"`
	Config       map[string]map[string]json.RawMessage `json:"config,omitempty"`
}

// presetsWire is presets.json.
type presetsWire struct {
	Format        int                   `json:"format"`
	Version       uint64                `json:"version"`
	Presets       map[string]presetWire `json:"presets"`
	LastOperation *operationWire        `json:"last_operation,omitempty"`
}

func (w presetWire) preset() (Preset, error) {
	var p Preset
	switch w.LocalSources {
	case localAllow:
		p.LocalSources = authz.PresetLocalAllow
	case localDeny:
		p.LocalSources = authz.PresetLocalDeny
	default:
		return Preset{}, invalidf("local_sources", "%q is neither %q nor %q", w.LocalSources, localAllow, localDeny)
	}
	p.Description = w.Description
	if w.Config != nil {
		p.Config = make(map[string]map[string]json.RawMessage, len(w.Config))
		for _, id := range sortedKeys(w.Config) {
			if err := adapterproto.ValidateAdapterID(id); err != nil {
				return Preset{}, invalid("config."+id, err)
			}
			values, err := checkValues(w.Config[id])
			if err != nil {
				return Preset{}, under("config."+id, err)
			}
			p.Config[id] = values
		}
	}
	return p, nil
}

func (p Preset) wire() (presetWire, error) {
	var w presetWire
	switch p.LocalSources {
	case authz.PresetLocalAllow:
		w.LocalSources = localAllow
	case authz.PresetLocalDeny:
		w.LocalSources = localDeny
	default:
		return w, fmt.Errorf("a preset makes no statement about local sources")
	}
	w.Description = p.Description
	if len(p.Config) != 0 {
		w.Config = p.Config
	}
	return w, nil
}

// DecodePreset decodes one preset document, as presets.add carries it. The
// statement about local sources is required and has no default: a document
// without it, or with a value that is not exactly "allow" or "deny", is
// refused, and the result never holds the zero statement. The error is a
// *FieldError.
func DecodePreset(data []byte) (Preset, error) {
	var w presetWire
	if err := decodeStrict(data, &w); err != nil {
		return Preset{}, err
	}
	return w.preset()
}

func decodePresets(data []byte) (Presets, error) {
	var w presetsWire
	if err := decodeStrict(data, &w); err != nil {
		return Presets{}, err
	}
	if err := checkHeader(w.Format, w.Version, true); err != nil {
		return Presets{}, err
	}
	out := Presets{Version: versionOf(w.Version), Items: make(map[string]Preset, len(w.Presets))}
	for _, name := range sortedKeys(w.Presets) {
		if err := adapterproto.ValidatePresetName(name); err != nil {
			return Presets{}, invalid("presets."+name, err)
		}
		p, err := w.Presets[name].preset()
		if err != nil {
			return Presets{}, under("presets."+name, err)
		}
		out.Items[name] = p
	}
	if w.LastOperation != nil {
		op, err := w.LastOperation.operation()
		if err != nil {
			return Presets{}, err
		}
		out.LastOperation = op
	}
	return out, nil
}

// DecodePresets decodes presets.json. The error is a *FileError that names the
// file.
func DecodePresets(data []byte) (Presets, error) {
	p, err := decodePresets(data)
	if err != nil {
		return Presets{}, fileErrorFor(PresetsPath, KindPresets, err)
	}
	return p, nil
}

// Encode returns the bytes of presets.json.
func (p Presets) Encode() ([]byte, error) {
	if err := checkEncodeVersion(uint64(p.Version)); err != nil {
		return nil, err
	}
	w := presetsWire{Format: FormatVersion, Version: uint64(p.Version), Presets: make(map[string]presetWire, len(p.Items))}
	for name, preset := range p.Items {
		pw, err := preset.wire()
		if err != nil {
			return nil, fmt.Errorf("preset %q: %w", name, err)
		}
		w.Presets[name] = pw
	}
	op, err := operationWireOf(p.LastOperation)
	if err != nil {
		return nil, err
	}
	w.LastOperation = op
	return encodeJSON(w)
}

func clonePreset(p Preset) Preset {
	if p.Config == nil {
		return p
	}
	cfg := make(map[string]map[string]json.RawMessage, len(p.Config))
	for id, values := range p.Config {
		cfg[id] = cloneRaw(values)
	}
	p.Config = cfg
	return p
}

func clonePresets(p Presets) Presets {
	items := make(map[string]Preset, len(p.Items))
	for name, preset := range p.Items {
		items[name] = clonePreset(preset)
	}
	return Presets{Version: p.Version, Items: items, LastOperation: cloneOperation(p.LastOperation)}
}
