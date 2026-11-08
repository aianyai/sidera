package adapterproto

import "slices"

// MaxAllowedModels bounds the models of one allowlist, so that a status reply
// listing the allowlist of every preset of a runtime stays within one signed
// record.
const MaxAllowedModels = 16

// ModelChoiceCapability is the model_choice part of RuntimeCapabilities.
type ModelChoiceCapability struct {
	// Efforts are the efforts the runtime can run, lowest first: a subset of the
	// scale, at least one.
	Efforts []Effort `json:"efforts"`
}

// Validate checks the efforts.
func (m ModelChoiceCapability) Validate() error {
	var c checker
	c.field("efforts", validEffortList(m.Efforts))
	return c.err
}

// ModelAllowance is one model of a preset's allowlist and the efforts a requester
// may choose with it.
type ModelAllowance struct {
	Model string `json:"model"`
	// Efforts are the allowed efforts, lowest first, at least one.
	Efforts []Effort `json:"efforts"`
}

// Validate checks the model name and the efforts.
func (a ModelAllowance) Validate() error {
	var c checker
	c.field("model", ValidateModelName(a.Model))
	c.field("efforts", validEffortList(a.Efforts))
	return c.err
}

// validEffortList checks a list of efforts: at least one, each on the scale, none
// twice, in the order of the scale. The scale has five levels, so five is also
// the bound.
func validEffortList(efforts_ []Effort) error {
	var c checker
	c.ok(len(efforts_) >= 1 && len(efforts_) <= len(efforts), "%d entries, need 1..%d", len(efforts_), len(efforts))
	for i, e := range efforts_ {
		c.field(index(i), enumOK(e, efforts))
	}
	c.field("", unique(efforts_))
	c.beyond(slices.IsSortedFunc(efforts_, compareEffort), "not in the order of the scale")
	return c.err
}

// compareEffort orders efforts along the scale.
func compareEffort(a, b Effort) int { return slices.Index(efforts, a) - slices.Index(efforts, b) }

// checkChoice checks the model and the effort a requester chose; either may be
// absent.
func checkChoice(c *checker, model string, effort Effort) {
	if model != "" {
		c.field("model", ValidateModelName(model))
	}
	if effort != "" {
		c.field("effort", enumOK(effort, efforts))
	}
}

// checkPresetChoice checks what a preset reports of the model choice: its default
// model and effort and its allowlist, each absent when the preset says nothing.
// The allowlist is sorted by model with no model twice.
func checkPresetChoice(c *checker, model string, effort Effort, allowed []ModelAllowance) {
	checkChoice(c, model, effort)
	if allowed == nil {
		return
	}
	c.ok(len(allowed) >= 1 && len(allowed) <= MaxAllowedModels, "allowed_models: %d entries, need 1..%d", len(allowed), MaxAllowedModels)
	list(c, "allowed_models", allowed)
	models := make([]string, len(allowed))
	for i, a := range allowed {
		models[i] = a.Model
	}
	c.beyond(len(duplicates(models)) == 0, "allowed_models: two entries share a model")
	c.beyond(slices.IsSorted(models), "allowed_models: not sorted by model")
}
