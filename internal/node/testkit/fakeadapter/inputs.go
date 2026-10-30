package fakeadapter

import (
	"context"
	"fmt"
	"slices"

	"github.com/aianyai/sidera/internal/node/testkit/fakeruntime"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// ExternalInputs are explicit conditions owned by a test run, independent of an
// adapter process's outcomes. Each value completely replaces the previous one;
// the zero value clears all conditions.
type ExternalInputs struct {
	CreateRefusal adapterproto.CreateFailure  `json:"create_refusal,omitempty"`
	PromptRefusal adapterproto.PromptRefusal  `json:"prompt_refusal,omitempty"`
	Availability  []adapterproto.Availability `json:"availability,omitempty"`
}

func (in ExternalInputs) validate() error {
	if err := fakeruntime.ValidateCreateRefusal(in.CreateRefusal); err != nil {
		return err
	}
	if in.PromptRefusal != "" && !in.PromptRefusal.Valid() {
		return fmt.Errorf("fakeadapter: invalid external prompt refusal %q", in.PromptRefusal)
	}
	return (adapterproto.AvailabilityParams{Conditions: append([]adapterproto.Availability{}, in.Availability...)}).Validate()
}
func (in ExternalInputs) clone() ExternalInputs {
	in.Availability = slices.Clone(in.Availability)
	for i := range in.Availability {
		in.Availability[i].Endpoints = slices.Clone(in.Availability[i].Endpoints)
	}
	return in
}

// InputReceiver installs a complete declaration. Implementations must not call
// back into the registry: installation runs inside its per-instance boundary.
type InputReceiver interface {
	ApplyInputs(context.Context, ExternalInputs) error
}

// InputRegistry owns desired inputs across adapter generations. Instance names
// are fixed at construction, including installed but initially disabled ones.
// Set, Attach and detach serialize per instance, including installation and its
// acknowledgement; different instances never share that boundary.
type InputRegistry struct{ instances map[string]*inputSlot }
type inputBinding struct{ receiver InputReceiver }
type inputSlot struct {
	gate    chan struct{}
	desired ExternalInputs
	binding *inputBinding
}

// NewInputRegistry validates and copies initial declarations before any adapter
// starts. Only explicitly listed instances may subsequently receive inputs.
func NewInputRegistry(instances []string, initial map[string]ExternalInputs) (*InputRegistry, error) {
	r := &InputRegistry{instances: make(map[string]*inputSlot, len(instances))}
	for _, name := range instances {
		if name == "" || r.instances[name] != nil {
			return nil, fmt.Errorf("fakeadapter: empty or duplicate input instance %q", name)
		}
		r.instances[name] = &inputSlot{gate: make(chan struct{}, 1)}
	}
	for name, in := range initial {
		slot, err := r.slot(name)
		if err != nil {
			return nil, err
		}
		if err := in.validate(); err != nil {
			return nil, err
		}
		slot.desired = in.clone()
	}
	return r, nil
}
func (r *InputRegistry) slot(instance string) (*inputSlot, error) {
	slot := r.instances[instance]
	if slot == nil {
		return nil, fmt.Errorf("fakeadapter: unknown input instance %q", instance)
	}
	return slot, nil
}
func (s *inputSlot) enter(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case s.gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			s.leave()
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *inputSlot) leave() { <-s.gate }

// Set retains the new desired value before installation. A failed installation
// returns its error but keeps that value for a retry or the next generation.
// Success confirms local installation, not daemon consumption of a notification.
func (r *InputRegistry) Set(ctx context.Context, instance string, in ExternalInputs) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := in.validate(); err != nil {
		return err
	}
	slot, err := r.slot(instance)
	if err != nil {
		return err
	}
	if err := slot.enter(ctx); err != nil {
		return err
	}
	defer slot.leave()
	slot.desired = in.clone()
	if slot.binding != nil {
		return slot.binding.receiver.ApplyInputs(ctx, slot.desired.clone())
	}
	return nil
}

// Attach installs the latest desired value before publishing the binding. Its
// detach only removes that binding, never a replacement or the desired value.
func (r *InputRegistry) Attach(ctx context.Context, instance string, receiver InputReceiver) (func(), error) {
	slot, err := r.slot(instance)
	if err != nil {
		return nil, err
	}
	if err := slot.enter(ctx); err != nil {
		return nil, err
	}
	defer slot.leave()
	if err := receiver.ApplyInputs(ctx, slot.desired.clone()); err != nil {
		return nil, err
	}
	binding := &inputBinding{receiver: receiver}
	slot.binding = binding
	return func() {
		_ = slot.enter(context.Background())
		defer slot.leave()
		if slot.binding == binding {
			slot.binding = nil
		}
	}, nil
}

// ApplyInputs installs inputs even before Serve. Once adapter.start has begun
// the legal notification phase, availability replacements are sent under the
// same lock as startup snapshots, so an older snapshot cannot follow a newer one.
func (k *Knobs) ApplyInputs(ctx context.Context, in ExternalInputs) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := in.validate(); err != nil {
		return err
	}
	k.inputsMu.Lock()
	defer k.inputsMu.Unlock()
	k.inputs = in.clone()
	if a := k.bound(); a != nil && k.inputsStarted == a {
		return a.notify(ctx, adapterproto.MethodAdapterAvailability, adapterproto.AvailabilityParams{Conditions: append([]adapterproto.Availability{}, in.Availability...)})
	}
	return nil
}
func (k *Knobs) startInputs(ctx context.Context, a *adapter) error {
	k.inputsMu.Lock()
	defer k.inputsMu.Unlock()
	k.inputsStarted = a
	if len(k.inputs.Availability) == 0 {
		return nil
	}
	return a.notify(ctx, adapterproto.MethodAdapterAvailability, adapterproto.AvailabilityParams{Conditions: k.inputs.clone().Availability})
}
func (k *Knobs) promptRefusal() adapterproto.PromptRefusal {
	k.inputsMu.Lock()
	defer k.inputsMu.Unlock()
	return k.inputs.PromptRefusal
}

func (k *Knobs) createRefusal() adapterproto.CreateFailure {
	k.inputsMu.Lock()
	defer k.inputsMu.Unlock()
	return k.inputs.CreateRefusal
}
