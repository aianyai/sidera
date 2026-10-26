package engine

import (
	"encoding/json"
	"fmt"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/node/config"
)

// PresetChange is what the owner's change of the presets did, for the result to
// say.
type PresetChange struct {
	Name string
	// Version is the version of the presets after the change: one for the whole
	// collection, which any preset's change raises.
	Version admin.Version
}

func presetResult(encode func(PresetChange) ([]byte, error), name string, x *opRun) config.ResultFunc[config.Presets] {
	return func(after config.Presets) (json.RawMessage, error) {
		b, err := encode(PresetChange{Name: name, Version: after.Version})
		if err != nil {
			return nil, fmt.Errorf("the result of %s: %w", x.name, err)
		}
		return b, nil
	}
}

// AddPresetOp is the operation that adds a preset from its document. The document
// must make the statement about local sources, which has no default, and a name
// that is taken is refused: a preset is changed by removing it and adding it again.
// Adding a preset loosens what the node allows, so the administrative rules judge it
// as loosening. The version is that of the whole collection, checked inside the
// change, the operation and its result kept in presets.json in the same replacement.
// encode makes the result; it runs in the loop and must not wait.
func AddPresetOp(name string, preset []byte, encode func(PresetChange) ([]byte, error)) Operation {
	return &addPresetOp{name: name, preset: preset, encode: encode}
}

type addPresetOp struct {
	name   string
	preset []byte
	encode func(PresetChange) ([]byte, error)
}

func (o *addPresetOp) Admin() admin.Operation {
	return admin.Operation{Kind: admin.OpChangePreset, PresetChange: admin.PresetChangeLoosening}
}
func (o *addPresetOp) stops() bool { return false }
func (o *addPresetOp) run(e *Engine, x *opRun) ([]byte, error) {
	after, err := e.d.Config.AddPreset(x.configRequest(), o.name, o.preset, presetResult(o.encode, o.name, x))
	if err != nil {
		return nil, err
	}
	return after.LastOperation.Result, nil
}

// RemovePresetOp is the operation that removes a preset. Removing narrows what the
// node allows, so the administrative rules judge it as narrowing. The version and
// the keeping of the result are those of AddPresetOp.
func RemovePresetOp(name string, encode func(PresetChange) ([]byte, error)) Operation {
	return &removePresetOp{name: name, encode: encode}
}

type removePresetOp struct {
	name   string
	encode func(PresetChange) ([]byte, error)
}

func (o *removePresetOp) Admin() admin.Operation {
	return admin.Operation{Kind: admin.OpChangePreset, PresetChange: admin.PresetChangeNarrowing}
}
func (o *removePresetOp) stops() bool { return false }
func (o *removePresetOp) run(e *Engine, x *opRun) ([]byte, error) {
	after, err := e.d.Config.RemovePreset(x.configRequest(), o.name, presetResult(o.encode, o.name, x))
	if err != nil {
		return nil, err
	}
	return after.LastOperation.Result, nil
}
