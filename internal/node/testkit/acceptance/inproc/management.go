package inproc

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strconv"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/engine"
	nodekeys "github.com/aianyai/sidera/internal/node/keys"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
	"github.com/aianyai/sidera/internal/revocation"
)

func newManagementOps() recipes.ManagementOps {
	return recipes.ManagementOps{
		Versions: func(e scenario.Env) (recipes.ManagementVersions, error) {
			n, err := readManagementFile(e, config.NodePath, config.DecodeNode)
			if err != nil {
				return recipes.ManagementVersions{}, err
			}
			p, err := readManagementFile(e, config.PresetsPath, config.DecodePresets)
			if err != nil {
				return recipes.ManagementVersions{}, err
			}
			i, err := readManagementFile(e, config.InstancesPath, config.DecodeInstances)
			return recipes.ManagementVersions{Node: n.Version, Presets: p.Version, Instances: i.Version}, err
		},
		NodeSet: func(e scenario.Env, q recipes.ManagementRequest, patch string) (recipes.ManagementOutcome, error) {
			node := e.(*harness.H).Engine()
			return runManagement(e, node, q, "node.set", patch, engine.SetNodeSettingsOp([]byte(patch), func(v config.Node) ([]byte, error) {
				return json.Marshal(recipes.ManagementOutcome{Value: strconv.Itoa(v.Settings.NodeQueueLimit), Version: v.Version})
			}))
		},
		PresetAdd: func(e scenario.Env, q recipes.ManagementRequest, name, document string) (recipes.ManagementOutcome, error) {
			node := e.(*harness.H).Engine()
			return runManagement(e, node, q, "presets.add", name+" "+document, engine.AddPresetOp(name, []byte(document), encodeManagementPreset))
		},
		PresetRemove: func(e scenario.Env, q recipes.ManagementRequest, name string) (recipes.ManagementOutcome, error) {
			node := e.(*harness.H).Engine()
			return runManagement(e, node, q, "presets.remove", name, engine.RemovePresetOp(name, encodeManagementPreset))
		},
		EndpointSet: func(e scenario.Env, q recipes.ManagementRequest, address, patch string) (recipes.ManagementOutcome, error) {
			id, err := managementEndpoint(address)
			if err != nil {
				return recipes.ManagementOutcome{}, err
			}
			node := e.(*harness.H).Engine()
			return runManagement(e, node, q, "endpoints.set", address+" "+patch, engine.SetEndpointSettingsOp(id, []byte(patch), func(v engine.EndpointChange) ([]byte, error) {
				return json.Marshal(recipes.ManagementOutcome{Value: strconv.Itoa(v.Settings.BacklogLimit), Version: v.Version})
			}))
		},
		CloseEndpoint: func(e scenario.Env, q recipes.ManagementRequest, address string) (recipes.ManagementOutcome, error) {
			id, err := managementEndpoint(address)
			if err != nil {
				return recipes.ManagementOutcome{}, err
			}
			node := e.(*harness.H).Engine()
			return runManagement(e, node, q, "endpoints.close", address, engine.CloseEndpointOp(id, func(v engine.CloseOutcome) ([]byte, error) {
				return json.Marshal(recipes.ManagementOutcome{Value: v.String()})
			}))
		},
		EnableAdapter: func(e scenario.Env, q recipes.ManagementRequest, adapter, instance string) (recipes.ManagementOutcome, error) {
			node := e.(*harness.H).Engine()
			return runManagement(e, node, q, "adapters.enable", adapter+" "+instance, engine.EnableAdapterOp(adapter, instance, nil, encodeManagementInstance))
		},
		DisableAdapter: func(e scenario.Env, q recipes.ManagementRequest, instance string) (recipes.ManagementOutcome, error) {
			node := e.(*harness.H).Engine()
			return runManagement(e, node, q, "adapters.disable", instance, engine.DisableAdapterOp(instance, encodeManagementInstance))
		},
		RaiseAlert: func(e scenario.Env, kind, subject, detail string) (string, error) {
			id, err := e.(*harness.H).Engine().RaiseAlert(e.Ctx(), kind, subject, detail)
			return id.String(), err
		},
		AcknowledgeAlert: func(e scenario.Env, q recipes.ManagementRequest, name string) (recipes.ManagementOutcome, error) {
			id, err := store.ParseAlertID(name)
			if err != nil {
				return recipes.ManagementOutcome{}, err
			}
			node := e.(*harness.H).Engine()
			return runManagement(e, node, q, "alerts.ack", name, engine.AcknowledgeAlertOp(id, func(v store.AlertID) ([]byte, error) {
				return json.Marshal(recipes.ManagementOutcome{Value: v.String()})
			}))
		},
		ConfirmBackup: func(e scenario.Env, q recipes.ManagementRequest) (recipes.ManagementOutcome, error) {
			node := e.(*harness.H).Engine()
			exported, err := node.ExportRevocation(e.Ctx())
			if err != nil {
				return recipes.ManagementOutcome{}, err
			}
			return runManagement(e, node, q, "identity.confirm-backup", exported.Digest.String(), engine.ConfirmRevocationBackupOp(exported.Digest, func(v engine.BackupConfirmed) ([]byte, error) {
				return json.Marshal(recipes.ManagementOutcome{Value: "confirmed", At: v.At})
			}))
		},
		ClearClock: func(e scenario.Env, q recipes.ManagementRequest) (recipes.ManagementOutcome, error) {
			node := e.(*harness.H).Engine()
			return runManagement(e, node, q, "clock.clear-rollback", "", engine.ClearClockRollbackOp(func(v engine.ClockClear) ([]byte, error) {
				return json.Marshal(recipes.ManagementOutcome{Value: strconv.FormatBool(v.Cleared), At: v.HighWater})
			}))
		},
		InspectNode: inspectManagedNode,
		InspectPreset: func(e scenario.Env, name string) (bool, error) {
			p, err := readManagementFile(e, config.PresetsPath, config.DecodePresets)
			if err != nil {
				return false, err
			}
			_, exists := p.Items[name]
			return exists, nil
		},
		InspectEndpoint: func(e scenario.Env, address string) (recipes.EndpointFacts, error) {
			id, err := managementEndpoint(address)
			if err != nil {
				return recipes.EndpointFacts{}, err
			}
			scan, err := e.(*harness.H).Records()
			if err != nil {
				return recipes.EndpointFacts{}, err
			}
			instances, err := readManagementFile(e, config.InstancesPath, config.DecodeInstances)
			if err != nil {
				return recipes.EndpointFacts{}, err
			}
			for _, ep := range scan.Endpoints {
				if ep.ID == id {
					return recipes.EndpointFacts{Closed: ep.Status == store.EndpointClosed, TokenCleared: len(ep.Token) == 0, BacklogLimit: instances.Items[ep.Instance].Endpoints[id].BacklogLimit}, nil
				}
			}
			return recipes.EndpointFacts{}, fmt.Errorf("acceptance: managed endpoint %s is absent", id)
		},
		InspectInstance: func(e scenario.Env, name string) (recipes.InstanceFacts, error) {
			instances, err := readManagementFile(e, config.InstancesPath, config.DecodeInstances)
			if err != nil {
				return recipes.InstanceFacts{}, err
			}
			in, exists := instances.Items[name]
			if !exists {
				return recipes.InstanceFacts{}, fmt.Errorf("acceptance: managed instance %s is absent", name)
			}
			scan, err := e.(*harness.H).Records()
			if err != nil {
				return recipes.InstanceFacts{}, err
			}
			out := recipes.InstanceFacts{Enabled: in.Enabled}
			for _, ep := range scan.Endpoints {
				if ep.Instance == name && ep.Status != store.EndpointClosed {
					out.OpenEndpoints++
				}
				if ep.Instance == name && ep.Status == store.EndpointClosed && len(ep.Token) != 0 {
					return recipes.InstanceFacts{}, fmt.Errorf("%w: disabled endpoint keeps its token", recipes.ErrManagementEffect)
				}
			}
			return out, nil
		},
		InspectAlert: func(e scenario.Env, name string) (bool, error) {
			scan, err := e.(*harness.H).Records()
			if err != nil {
				return false, err
			}
			for _, a := range scan.Alerts {
				if a.ID.String() == name {
					return a.Acked != nil, nil
				}
			}
			return false, fmt.Errorf("acceptance: managed alert %s absent", name)
		},
		DirectOn: func(e scenario.Env, instance, name string) (string, error) {
			return e.(*harness.H).DirectOn(instance, name)
		},
	}
}

func encodeManagementPreset(v engine.PresetChange) ([]byte, error) {
	return json.Marshal(recipes.ManagementOutcome{Value: v.Name, Version: v.Version})
}
func encodeManagementInstance(v engine.InstanceChange) ([]byte, error) {
	return json.Marshal(recipes.ManagementOutcome{Value: strconv.FormatBool(v.Enabled), Version: v.Version})
}

// A callback resolves one process once. A selected crash may end it between the
// operation and its replay; using that local handle then returns an ordinary error.
func runManagement(e scenario.Env, node *engine.Engine, q recipes.ManagementRequest, name, payload string, operation engine.Operation) (recipes.ManagementOutcome, error) {
	fingerprint, err := json.Marshal(struct {
		Name, Payload string
		Expected      admin.Version
	}{name, payload, q.Expected})
	if err != nil {
		return recipes.ManagementOutcome{}, err
	}
	cmd := engine.ManagementCommand{Key: admin.KeyFor(admin.LocalOwner(), q.ID), Fingerprint: sha256.Sum256(fingerprint), Expected: q.Expected, Name: name, Operation: operation}
	result, err := node.Execute(e.Ctx(), cmd)
	var conflict *config.ChangeError
	if errors.As(err, &conflict) && conflict.Reason == config.ChangeVersionConflict {
		if _, found, readErr := node.OperationResult(e.Ctx(), cmd.Key); readErr != nil || found {
			return recipes.ManagementOutcome{}, fmt.Errorf("%w: rejected version stored a result", recipes.ErrManagementEffect)
		}
		return recipes.ManagementOutcome{Code: "version_conflict"}, nil
	}
	if err != nil {
		return recipes.ManagementOutcome{}, err
	}
	stored, found, err := node.OperationResult(e.Ctx(), cmd.Key)
	if err != nil {
		return recipes.ManagementOutcome{}, err
	}
	if !found || !bytes.Equal(stored, result.Result) {
		return recipes.ManagementOutcome{}, fmt.Errorf("%w: %s has no matching persistent result", recipes.ErrManagementEffect, name)
	}
	again, err := node.Execute(e.Ctx(), cmd)
	if err != nil {
		return recipes.ManagementOutcome{}, err
	}
	if !again.Replayed || again.Stop != result.Stop || !bytes.Equal(again.Result, result.Result) {
		return recipes.ManagementOutcome{}, fmt.Errorf("%w: %s replay changed the result or stop request", recipes.ErrManagementEffect, name)
	}
	var out recipes.ManagementOutcome
	if err := json.Unmarshal(result.Result, &out); err != nil {
		return recipes.ManagementOutcome{}, err
	}
	out.Replayed, out.Stop = result.Replayed, result.Stop
	return out, nil
}

func readManagementFile[T any](e scenario.Env, name string, decode func([]byte) (T, error)) (T, error) {
	raw, err := e.(*harness.H).Disk().ReadFile(name)
	if err != nil {
		var zero T
		return zero, err
	}
	return decode(raw)
}
func managementEndpoint(address string) (ident.EndpointID, error) {
	a, err := ident.ParseAddress(address)
	return a.Endpoint, err
}

func inspectManagedNode(e scenario.Env) (recipes.NodeFacts, error) {
	h := e.(*harness.H)
	node, err := readManagementFile(e, config.NodePath, config.DecodeNode)
	if err != nil {
		return recipes.NodeFacts{}, err
	}
	clock, err := nodekeys.ReadClock(h.Disk())
	if err != nil {
		return recipes.NodeFacts{}, err
	}
	out := recipes.NodeFacts{QueueLimit: node.Settings.NodeQueueLimit, Version: node.Version, Now: h.Clock().Now(), HighWater: clock.HighWater, ClockPaused: clock.Paused()}
	backup, err := readManagementFile(e, config.BackupPath, config.DecodeBackup)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return recipes.NodeFacts{}, err
	}
	if err == nil {
		self, err := nodekeys.ReadSelfRevocation(h.Disk())
		if err != nil {
			return recipes.NodeFacts{}, err
		}
		out.BackupConfirmed = backup.Confirms(self.Digest())
	}
	entries, err := h.Disk().ReadDir(config.RevocationsDir)
	if err != nil {
		return recipes.NodeFacts{}, err
	}
	for _, entry := range entries {
		raw, err := h.Disk().ReadFile(path.Join(config.RevocationsDir, entry.Name))
		if err != nil {
			return recipes.NodeFacts{}, err
		}
		rev, _, err := config.DecodeRevocation(entry.Name, raw)
		if err != nil {
			return recipes.NodeFacts{}, err
		}
		out.Retired = out.Retired || (rev.Kind() == revocation.KindNodeSelf && rev.Node() == harness.NodeID())
	}
	return out, nil
}
