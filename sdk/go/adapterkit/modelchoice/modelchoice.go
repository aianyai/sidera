// Package modelchoice is the allowlist of a preset and the rules that decide which
// model and effort a requester may choose for a new session, so that every adapter
// that declares model_choice applies them the same way. The daemon does not read an
// adapter's preset keys, so only the adapter can apply them.
package modelchoice

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// Key is the preset field that holds the allowlist. Every adapter that declares
// model_choice declares it the same way: type map, value string_list.
const Key = "allowed_models"

// MaxAllowedModels bounds the models of one allowlist, so that a status reply that
// lists every preset's allowlist stays within one signed record.
const MaxAllowedModels = adapterproto.MaxAllowedModels

// Allowed is a preset's allowlist: for each model, the efforts a requester may
// choose with it. The zero value is a preset with no allowlist, which allows no
// choice.
type Allowed struct {
	// efforts holds, for each model, its efforts in the order of the scale.
	efforts map[string][]adapterproto.Effort
}

// ParseAllowed reads the value of Key. supported is what the adapter declares in
// model_choice. It refuses an empty map, a model with no effort, a model name that
// breaks ValidateModelName, more than MaxAllowedModels models, an effort that is not
// one of the scale, an effort the adapter does not support, and an effort listed
// twice. Each refusal is a preset problem: an empty list is not read as "anything",
// because a runtime that adds a level later must not open it silently.
func ParseAllowed(raw json.RawMessage, supported []adapterproto.Effort) (Allowed, error) {
	var lists map[string][]string
	if err := json.Unmarshal(raw, &lists); err != nil {
		return Allowed{}, fmt.Errorf("%s must be a map from a model to a list of efforts: %w", Key, err)
	}
	if len(lists) == 0 {
		return Allowed{}, fmt.Errorf("%s lists no model", Key)
	}
	if len(lists) > MaxAllowedModels {
		return Allowed{}, fmt.Errorf("%s lists %d models, at most %d are allowed", Key, len(lists), MaxAllowedModels)
	}
	models := make([]string, 0, len(lists))
	for model := range lists {
		models = append(models, model)
	}
	slices.Sort(models) // the first problem reported is the same on every run

	parsed := make(map[string][]adapterproto.Effort, len(lists))
	for _, model := range models {
		if err := adapterproto.ValidateModelName(model); err != nil {
			return Allowed{}, fmt.Errorf("%s: model name %q: %w", Key, model, err)
		}
		if len(lists[model]) == 0 {
			return Allowed{}, fmt.Errorf("%s: model %q lists no effort", Key, model)
		}
		var efforts []adapterproto.Effort
		for _, name := range lists[model] {
			e := adapterproto.Effort(name)
			switch {
			case !e.Valid():
				return Allowed{}, fmt.Errorf("%s: model %q: effort %q is not one of %s", Key, model, name, scaleText())
			case !slices.Contains(supported, e):
				return Allowed{}, fmt.Errorf("%s: model %q: effort %q is not one this adapter supports", Key, model, name)
			case slices.Contains(efforts, e):
				return Allowed{}, fmt.Errorf("%s: model %q lists effort %q twice", Key, model, name)
			}
			efforts = append(efforts, e)
		}
		slices.SortFunc(efforts, compareEffort)
		parsed[model] = efforts
	}
	return Allowed{efforts: parsed}, nil
}

// Entries lists the allowlist for PresetStatus, sorted by model, the efforts of each
// in the order of the scale; nil for the zero value. The result belongs to the
// caller.
func (a Allowed) Entries() []adapterproto.ModelAllowance {
	if len(a.efforts) == 0 {
		return nil
	}
	out := make([]adapterproto.ModelAllowance, 0, len(a.efforts))
	for model, efforts := range a.efforts {
		out = append(out, adapterproto.ModelAllowance{Model: model, Efforts: slices.Clone(efforts)})
	}
	slices.SortFunc(out, func(x, y adapterproto.ModelAllowance) int { return strings.Compare(x.Model, y.Model) })
	return out
}

// Defaults are the preset's own model and effort; either may be empty.
type Defaults struct {
	Model  string
	Effort adapterproto.Effort
}

// ParseDefaults checks the model and the effort a preset sets for itself, and says
// every way they are unusable, as problems of the preset. The model must be a
// model name the protocol carries, because the probe reports it and a choice keeps
// it; the effort must be one of supported, the efforts the adapter declares in
// model_choice, so that a default the adapter could not run is the owner's to fix
// and not a surprise at the first turn. Empty values are not set and pass.
func ParseDefaults(model, effort string, supported []adapterproto.Effort) (Defaults, []string) {
	var problems []string
	if model != "" {
		if err := adapterproto.ValidateModelName(model); err != nil {
			problems = append(problems, fmt.Sprintf("model: %v", err))
		}
	}
	if effort != "" && !slices.Contains(supported, adapterproto.Effort(effort)) {
		problems = append(problems, fmt.Sprintf("effort: %q is not one of %v", effort, supported))
	}
	return Defaults{Model: model, Effort: adapterproto.Effort(effort)}, problems
}

// Choice is the model and effort a session runs with when a requester chose. A
// field that is empty is left to the runtime. It is also the form an adapter stores
// the choice in with its checkpoints: a member that is empty is left out.
type Choice struct {
	Model  string              `json:"model,omitempty"`
	Effort adapterproto.Effort `json:"effort,omitempty"`
}

// Validate refuses a choice that chooses nothing or holds a value that Resolve could
// not have returned. An adapter reads a stored choice back into the arguments of its
// runtime's process, so what it reads is checked as strictly as what it resolved.
func (c Choice) Validate() error {
	if !adapterproto.ChoosesModel(c.Model, c.Effort) {
		return errors.New("a choice that chooses nothing")
	}
	return adapterproto.ValidateModelChoice(c.Model, c.Effort)
}

// ErrNotAllowed: the preset does not allow what the requester chose.
var ErrNotAllowed = errors.New("modelchoice: the preset does not allow this model and effort")

// Resolve applies the fallback rules to what a session.create asked for. chosen is
// false when it asked for neither, and the session then follows the preset as
// before. An error wraps ErrNotAllowed and says which rule refused; nothing is ever
// downgraded or replaced.
//
// The allowlist checks only values Sidera knows: what the requester gave and what
// the preset states. A model or effort that neither names is left empty in the
// choice, for the runtime to decide as it does when the preset does not state it.
func Resolve(a Allowed, d Defaults, model string, effort adapterproto.Effort) (c Choice, chosen bool, err error) {
	if model == "" && effort == "" {
		return Choice{}, false, nil
	}
	if len(a.efforts) == 0 {
		return Choice{}, false, refuse("the preset has no allowlist")
	}

	switch {
	case effort == "": // only a model: the preset's effort goes with it
		allowedEfforts, listed := a.efforts[model]
		if !listed {
			return Choice{}, false, refuse("model %q is not in the allowlist", model)
		}
		if d.Effort != "" && !slices.Contains(allowedEfforts, d.Effort) {
			return Choice{}, false, refuse("the preset's effort %q is not among the efforts of model %q", d.Effort, model)
		}
		return Choice{Model: model, Effort: d.Effort}, true, nil

	case model == "": // only an effort: it is checked against the preset's model
		if allowedEfforts, listed := a.efforts[d.Model]; listed {
			if !slices.Contains(allowedEfforts, effort) {
				return Choice{}, false, refuse("effort %q is not among the efforts of the preset's model %q", effort, d.Model)
			}
			return Choice{Model: d.Model, Effort: effort}, true, nil
		}
		// Without a listed model nothing says which efforts the runtime's model has,
		// so only the effort the preset itself states is known to be acceptable.
		if d.Effort == "" {
			return Choice{}, false, refuse("effort %q cannot be checked: the preset names no effort and its model is not in the allowlist", effort)
		}
		if effort != d.Effort {
			return Choice{}, false, refuse("effort %q must equal the preset's effort %q because the preset's model is not in the allowlist", effort, d.Effort)
		}
		return Choice{Model: d.Model, Effort: effort}, true, nil

	default: // both
		allowedEfforts, listed := a.efforts[model]
		if !listed {
			return Choice{}, false, refuse("model %q is not in the allowlist", model)
		}
		if !slices.Contains(allowedEfforts, effort) {
			return Choice{}, false, refuse("effort %q is not among the efforts of model %q", effort, model)
		}
		return Choice{Model: model, Effort: effort}, true, nil
	}
}

func refuse(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrNotAllowed, fmt.Sprintf(format, args...))
}

// compareEffort orders efforts along the scale.
func compareEffort(a, b adapterproto.Effort) int {
	scale := adapterproto.Efforts()
	return slices.Index(scale, a) - slices.Index(scale, b)
}

func scaleText() string {
	names := make([]string, 0, 5)
	for _, e := range adapterproto.Efforts() {
		names = append(names, string(e))
	}
	return strings.Join(names, ", ")
}
