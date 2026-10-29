package recipes

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

type ManagementRequest struct {
	ID       admin.OperationID
	Expected admin.Version
}
type ManagementOutcome struct {
	Value          string
	Version        admin.Version
	At             time.Time
	Replayed, Stop bool
	Code           string
}
type ManagementVersions struct{ Node, Presets, Instances admin.Version }
type NodeFacts struct {
	QueueLimit                            int
	Version                               admin.Version
	Now, HighWater                        time.Time
	ClockPaused, BackupConfirmed, Retired bool
}
type EndpointFacts struct {
	Closed, TokenCleared bool
	BacklogLimit         int
}
type InstanceFacts struct {
	Enabled       bool
	OpenEndpoints int
}

// Operations return the first actual result after checking its persistent replay.
// Inspection returns only facts needed by this domain's requested effects.
type ManagementOps struct {
	Versions         func(scenario.Env) (ManagementVersions, error)
	NodeSet          func(scenario.Env, ManagementRequest, string) (ManagementOutcome, error)
	PresetAdd        func(scenario.Env, ManagementRequest, string, string) (ManagementOutcome, error)
	PresetRemove     func(scenario.Env, ManagementRequest, string) (ManagementOutcome, error)
	EndpointSet      func(scenario.Env, ManagementRequest, string, string) (ManagementOutcome, error)
	CloseEndpoint    func(scenario.Env, ManagementRequest, string) (ManagementOutcome, error)
	EnableAdapter    func(scenario.Env, ManagementRequest, string, string) (ManagementOutcome, error)
	DisableAdapter   func(scenario.Env, ManagementRequest, string) (ManagementOutcome, error)
	RaiseAlert       func(scenario.Env, string, string, string) (string, error)
	AcknowledgeAlert func(scenario.Env, ManagementRequest, string) (ManagementOutcome, error)
	ConfirmBackup    func(scenario.Env, ManagementRequest) (ManagementOutcome, error)
	ClearClock       func(scenario.Env, ManagementRequest) (ManagementOutcome, error)
	InspectNode      func(scenario.Env) (NodeFacts, error)
	InspectPreset    func(scenario.Env, string) (bool, error)
	InspectEndpoint  func(scenario.Env, string) (EndpointFacts, error)
	InspectInstance  func(scenario.Env, string) (InstanceFacts, error)
	InspectAlert     func(scenario.Env, string) (bool, error)
	DirectOn         func(scenario.Env, string, string) (string, error)
}

var ErrManagementEffect = errors.New("acceptance: management result and effect disagree")

type managementRun struct {
	active     func(scenario.Env) error
	checks     map[string]func(scenario.Env) error
	recovering bool
}

// step keeps the current request across a crash, without keeping a process handle.
// Completed checks are replaced by the latest request on the same domain fact.
func (r *managementRun) step(domain, name string, id byte, version func(ManagementVersions) admin.Version, ops ManagementOps, run func(scenario.Env, ManagementRequest) (ManagementOutcome, error), check func(scenario.Env, ManagementOutcome) error) scenario.Step {
	return scenario.Step{Name: name, Do: func(e scenario.Env) error {
		req := ManagementRequest{ID: admin.OperationID{id}}
		if version != nil {
			versions, err := ops.Versions(e)
			if err != nil {
				return err
			}
			req.Expected = version(versions)
		}
		r.active = func(e scenario.Env) error {
			out, err := run(e, req)
			if err != nil {
				return err
			}
			if out.Code != "" || (version != nil && out.Version != req.Expected+1) {
				return fmt.Errorf("%w: %s returned %+v", ErrManagementEffect, name, out)
			}
			if r.checks == nil {
				r.checks = make(map[string]func(scenario.Env) error)
			}
			r.checks[domain] = func(e scenario.Env) error { return check(e, out) }
			return check(e, out)
		}
		if err := r.active(e); err != nil {
			return err
		}
		r.active = nil
		return nil
	}}
}
func (r *managementRun) drain(e scenario.Env) error {
	if r.active != nil {
		r.recovering = true
		if err := r.active(e); err != nil {
			return err
		}
		r.active = nil
	}
	return nil
}
func (r *managementRun) check(e scenario.Env) error {
	var domains []string
	for domain := range r.checks {
		domains = append(domains, domain)
	}
	sort.Strings(domains)
	for _, domain := range domains {
		if err := r.checks[domain](e); err != nil {
			return err
		}
	}
	return nil
}

func managementRecipes(b Bindings) []ScriptRecipe {
	return []ScriptRecipe{managementSettings(b), managementMaintenance(b), managementClose(b), managementInstance(b)}
}

func managementSettings(b Bindings) ScriptRecipe {
	ops := b.Management
	r := &managementRun{}
	steps := []scenario.Step{
		r.step("node", "set-node", 41, func(v ManagementVersions) admin.Version { return v.Node }, ops,
			func(e scenario.Env, q ManagementRequest) (ManagementOutcome, error) {
				return ops.NodeSet(e, q, `{"node_queue_limit":9000}`)
			},
			func(e scenario.Env, o ManagementOutcome) error {
				f, err := ops.InspectNode(e)
				if err != nil {
					return err
				}
				if o.Value != "9000" || f.QueueLimit != 9000 || f.Version != o.Version {
					return fmt.Errorf("%w: node result=%+v facts=%+v", ErrManagementEffect, o, f)
				}
				return nil
			}),
		{Name: "reject-stale-version", Do: func(e scenario.Env) error {
			before, err := ops.InspectNode(e)
			if err != nil {
				return err
			}
			out, err := ops.NodeSet(e, ManagementRequest{ID: admin.OperationID{42}, Expected: before.Version - 1}, `{"node_queue_limit":8000}`)
			if err != nil {
				return err
			}
			after, err := ops.InspectNode(e)
			if err != nil {
				return err
			}
			if out.Code != "version_conflict" || after.QueueLimit != before.QueueLimit || after.Version != before.Version {
				return fmt.Errorf("%w: stale setting returned %+v, before=%+v after=%+v", ErrManagementEffect, out, before, after)
			}
			return nil
		}},
		r.step("preset", "add-preset", 43, func(v ManagementVersions) admin.Version { return v.Presets }, ops,
			func(e scenario.Env, q ManagementRequest) (ManagementOutcome, error) {
				return ops.PresetAdd(e, q, "acceptance", `{"local_sources":"deny"}`)
			},
			func(e scenario.Env, o ManagementOutcome) error {
				exists, err := ops.InspectPreset(e, "acceptance")
				if err != nil {
					return err
				}
				if o.Value != "acceptance" || !exists {
					return fmt.Errorf("%w: added preset is absent", ErrManagementEffect)
				}
				return nil
			}),
		r.step("preset", "remove-preset", 44, func(v ManagementVersions) admin.Version { return v.Presets }, ops,
			func(e scenario.Env, q ManagementRequest) (ManagementOutcome, error) {
				return ops.PresetRemove(e, q, "acceptance")
			},
			func(e scenario.Env, o ManagementOutcome) error {
				exists, err := ops.InspectPreset(e, "acceptance")
				if err != nil {
					return err
				}
				if o.Value != "acceptance" || exists {
					return fmt.Errorf("%w: removed preset remains", ErrManagementEffect)
				}
				return nil
			}),
		r.step("endpoint", "set-endpoint", 45, func(v ManagementVersions) admin.Version { return v.Instances }, ops,
			func(e scenario.Env, q ManagementRequest) (ManagementOutcome, error) {
				return ops.EndpointSet(e, q, e.Addr("managed"), `{"backlog_limit":9}`)
			},
			func(e scenario.Env, o ManagementOutcome) error {
				f, err := ops.InspectEndpoint(e, e.Addr("managed"))
				if err != nil {
					return err
				}
				if o.Value != "9" || f.BacklogLimit != 9 {
					return fmt.Errorf("%w: endpoint settings result=%+v facts=%+v", ErrManagementEffect, o, f)
				}
				return nil
			}),
	}
	return ScriptRecipe{Script: scenario.Script{Name: "management-settings", Setup: scenario.Setup{Direct: []string{"managed"}}, Crash: scenario.CrashPlan{MaxPoints: 24}, Steps: steps}, Drain: r.drain, Check: r.check}
}

func managementMaintenance(b Bindings) ScriptRecipe {
	ops := b.Management
	r := &managementRun{}
	var alert string
	steps := []scenario.Step{
		{Name: "raise-alert", Do: func(e scenario.Env) error {
			var err error
			alert, err = ops.RaiseAlert(e, "acceptance_check", "management", "A real management alert.")
			return err
		}},
		r.step("alert", "acknowledge-alert", 51, nil, ops, func(e scenario.Env, q ManagementRequest) (ManagementOutcome, error) {
			return ops.AcknowledgeAlert(e, q, alert)
		},
			func(e scenario.Env, o ManagementOutcome) error {
				acked, err := ops.InspectAlert(e, alert)
				if err != nil {
					return err
				}
				if o.Value != alert || !acked {
					return fmt.Errorf("%w: alert acknowledgement absent", ErrManagementEffect)
				}
				return nil
			}),
		r.step("backup", "confirm-backup", 52, nil, ops, ops.ConfirmBackup,
			func(e scenario.Env, o ManagementOutcome) error {
				f, err := ops.InspectNode(e)
				if err != nil {
					return err
				}
				if o.Value != "confirmed" || !f.BackupConfirmed {
					return fmt.Errorf("%w: backup confirmation absent", ErrManagementEffect)
				}
				return nil
			}),
		{Name: "roll-back-clock", Do: func(e scenario.Env) error {
			f, err := ops.InspectNode(e)
			if err != nil {
				return err
			}
			e.Set(f.Now.Add(-time.Hour))
			return nil
		}},
		r.step("clock", "clear-clock", 53, nil, ops, ops.ClearClock,
			func(e scenario.Env, o ManagementOutcome) error {
				f, err := ops.InspectNode(e)
				if err != nil {
					return err
				}
				if (o.Value != "true" && !(r.recovering && o.Value == "false")) || f.ClockPaused || !f.HighWater.Equal(o.At) {
					return fmt.Errorf("%w: clock result=%+v facts=%+v", ErrManagementEffect, o, f)
				}
				return nil
			}),
	}
	return ScriptRecipe{Script: scenario.Script{Name: "management-maintenance", Crash: scenario.CrashPlan{MaxPoints: 38}, Steps: steps}, Drain: r.drain, Check: r.check}
}

func managementClose(b Bindings) ScriptRecipe {
	ops := b.Management
	r := &managementRun{}
	facts := newDeliveryEvidence(b)
	var target string
	close := func(e scenario.Env, q ManagementRequest) (ManagementOutcome, error) {
		return ops.CloseEndpoint(e, q, target)
	}
	check := func(e scenario.Env, o ManagementOutcome) error {
		f, err := ops.InspectEndpoint(e, target)
		if err != nil {
			return err
		}
		if (o.Value != "closed" && o.Value != "already_closed") || !f.Closed || !f.TokenCleared {
			return fmt.Errorf("%w: closed endpoint result=%+v facts=%+v", ErrManagementEffect, o, f)
		}
		return nil
	}
	return ScriptRecipe{Script: scenario.Script{Name: "management-close", Setup: scenario.Setup{Direct: []string{"sender", "managed"}}, Crash: scenario.CrashPlan{MaxPoints: 83}, Steps: []scenario.Step{
		{Name: "queue-before-close", Do: func(e scenario.Env) error {
			target = e.Addr("managed")
			return sendBody(facts, "sender", "managed", "Close this endpoint with its body still queued.")(e)
		}},
		r.step("endpoint", "close-endpoint", 63, nil, ops, close, check),
	}}, Drain: func(e scenario.Env) error {
		if target == "" {
			target = e.Addr("managed")
		}
		if err := r.drain(e); err != nil {
			return err
		}
		out, err := close(e, ManagementRequest{ID: admin.OperationID{63}})
		if err != nil {
			return err
		}
		if err := check(e, out); err != nil {
			return err
		}
		return drainFeedback(e, facts, "sender")
	}, Check: func(e scenario.Env) error {
		if err := r.check(e); err != nil {
			return err
		}
		return facts.Check(e)
	}}
}

func managementInstance(b Bindings) ScriptRecipe {
	const instance = "management-target"
	ops := b.Management
	r := &managementRun{}
	facts := newDeliveryEvidence(b)
	var target string
	checkDisabled := func(e scenario.Env, o ManagementOutcome) error {
		f, err := ops.InspectInstance(e, instance)
		if err != nil {
			return err
		}
		if o.Value != "false" || f.Enabled {
			return fmt.Errorf("%w: instance disable result=%+v facts=%+v", ErrManagementEffect, o, f)
		}
		return nil
	}
	steps := []scenario.Step{
		{Name: "prepare-target-instance", Do: func(e scenario.Env) error {
			var err error
			target, err = ops.DirectOn(e, instance, "managed-target")
			if err != nil {
				return err
			}
			ctrl, err := e.Adapter(instance)
			if err != nil {
				return err
			}
			return ctrl.SetOutcome(e.Ctx(), fakeadapter.RequestCreate, fakeadapter.Outcome{Kind: fakeadapter.OutcomeHold})
		}},
		{Name: "queue-instance-records", Do: func(e scenario.Env) error {
			out, err := e.Agent("sender").Send(e.Ctx(), fakeagent.SendInput{To: []string{target}, Body: "Disable this instance with pending deliveries."})
			if err != nil {
				return err
			}
			facts.Accepted(out, "Disable this instance with pending deliveries.")
			control, err := e.Agent("sender").Control(e.Ctx(), fakeagent.ControlInput{To: b.Sessions.RuntimeAddr(e, instance), Signal: adapterproto.SignalSessionCreate, Preset: "default"})
			if err == nil {
				for _, to := range control.To {
					facts.RequireFeedback(control.MessageID, to, delivery.KindDeliveryUnknown)
				}
			}
			return err
		}},
		r.step("instance", "disable-instance", 71, func(v ManagementVersions) admin.Version { return v.Instances }, ops, func(e scenario.Env, q ManagementRequest) (ManagementOutcome, error) {
			return ops.DisableAdapter(e, q, instance)
		}, checkDisabled),
		r.step("instance", "enable-instance", 72, func(v ManagementVersions) admin.Version { return v.Instances }, ops, func(e scenario.Env, q ManagementRequest) (ManagementOutcome, error) {
			return ops.EnableAdapter(e, q, instance, instance)
		}, func(e scenario.Env, o ManagementOutcome) error {
			f, err := ops.InspectInstance(e, instance)
			if err != nil {
				return err
			}
			if o.Value != "true" || !f.Enabled {
				return fmt.Errorf("%w: enabled instance absent", ErrManagementEffect)
			}
			return nil
		}),
	}
	var drainRequest ManagementRequest
	return ScriptRecipe{Needs: Needs{RuntimeAdapter: true, ExtraInstances: []string{instance}}, RecoveryTriggers: []crashwalk.Trigger{crashwalk.CrashAt(fsx.Point{Key: fsx.Key{Tag: fsx.Tag{Tx: "instance-drain", Role: "endpoint"}, Step: fsx.StepCreate}, Hit: 1})}, Script: scenario.Script{Name: "management-instance", Setup: scenario.Setup{Direct: []string{"sender"}}, Crash: scenario.CrashPlan{MaxPoints: 195}, Steps: steps}, Drain: func(e scenario.Env) error {
		if err := r.drain(e); err != nil {
			return err
		}
		if drainRequest.ID.IsZero() {
			v, err := ops.Versions(e)
			if err != nil {
				return err
			}
			drainRequest = ManagementRequest{ID: admin.OperationID{73}, Expected: v.Instances}
		}
		out, err := ops.DisableAdapter(e, drainRequest, instance)
		if err != nil {
			return err
		}
		if err := checkDisabled(e, out); err != nil {
			return err
		}
		delete(r.checks, "instance")
		return drainFeedback(e, facts, "sender")
	}, Check: func(e scenario.Env) error {
		f, err := ops.InspectInstance(e, instance)
		if err != nil {
			return err
		}
		if f.Enabled || f.OpenEndpoints != 0 {
			return fmt.Errorf("%w: disabled instance retains open endpoints: %+v", ErrManagementEffect, f)
		}
		if err := facts.Check(e); err != nil {
			return err
		}
		return b.Recovery.CheckClosedRecords(e)
	}}
}
