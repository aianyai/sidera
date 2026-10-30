package acceptance

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/config"
	nodekeys "github.com/aianyai/sidera/internal/node/keys"
	"github.com/aianyai/sidera/internal/node/manageapi"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
	"github.com/aianyai/sidera/internal/revocation"
)

func newManagementOps() recipes.ManagementOps {
	return recipes.ManagementOps{
		Versions: managementVersions,
		NodeSet: func(e scenario.Env, q recipes.ManagementRequest, patch string) (recipes.ManagementOutcome, error) {
			return managementChange(e, q, "node.set", map[string]any{"settings": json.RawMessage(patch)})
		},
		PresetAdd: func(e scenario.Env, q recipes.ManagementRequest, name, document string) (recipes.ManagementOutcome, error) {
			return managementChange(e, q, "presets.add", map[string]any{"name": name, "preset": json.RawMessage(document)})
		},
		PresetRemove: func(e scenario.Env, q recipes.ManagementRequest, name string) (recipes.ManagementOutcome, error) {
			return managementChange(e, q, "presets.remove", map[string]any{"name": name})
		},
		EndpointSet: func(e scenario.Env, q recipes.ManagementRequest, address, patch string) (recipes.ManagementOutcome, error) {
			return managementChange(e, q, "endpoints.set", map[string]any{"endpoint": address, "settings": json.RawMessage(patch)})
		},
		CloseEndpoint: func(e scenario.Env, q recipes.ManagementRequest, address string) (recipes.ManagementOutcome, error) {
			return managementChange(e, q, "endpoints.close", map[string]any{"endpoint": address})
		},
		EnableAdapter: func(e scenario.Env, q recipes.ManagementRequest, adapter, instance string) (recipes.ManagementOutcome, error) {
			return managementChange(e, q, "adapters.enable", map[string]any{"adapter": adapter, "instance": instance})
		},
		DisableAdapter: func(e scenario.Env, q recipes.ManagementRequest, instance string) (recipes.ManagementOutcome, error) {
			return managementChange(e, q, "adapters.disable", map[string]any{"instance": instance})
		},
		CreateVersionAlert: newVersionAlertOp(),
		AcknowledgeAlert: func(e scenario.Env, q recipes.ManagementRequest, name string) (recipes.ManagementOutcome, error) {
			return managementChange(e, q, "alerts.ack", map[string]any{"alert": name})
		},
		ConfirmBackup: func(e scenario.Env, q recipes.ManagementRequest) (recipes.ManagementOutcome, error) {
			var exported manageapi.RevocationExportResult
			if err := managementRead(e, "identity.export_revocation", nil, &exported); err != nil {
				return recipes.ManagementOutcome{}, err
			}
			return managementChange(e, q, "identity.confirm_backup", map[string]any{"digest": exported.Digest})
		},
		ClearClock: func(e scenario.Env, q recipes.ManagementRequest) (recipes.ManagementOutcome, error) {
			return managementChange(e, q, "clock.clear_rollback", nil)
		},
		InspectNode: inspectManagedNode,
		InspectPreset: func(e scenario.Env, name string) (bool, error) {
			var listed struct {
				Presets []struct {
					Name string `json:"name"`
				} `json:"presets"`
			}
			if err := managementRead(e, "presets.list", nil, &listed); err != nil {
				return false, err
			}
			for _, p := range listed.Presets {
				if p.Name == name {
					return true, nil
				}
			}
			return false, nil
		},
		InspectEndpoint: inspectManagedEndpoint,
		InspectInstance: inspectManagedInstance,
		InspectAlert: func(e scenario.Env, name string) (bool, error) {
			alerts, err := managementAlerts(e)
			if err != nil {
				return false, err
			}
			for _, a := range alerts {
				if a.ID == name {
					return a.Acknowledged, nil
				}
			}
			return false, fmt.Errorf("acceptance: managed alert %s absent", name)
		},
		DirectOn: func(e scenario.Env, instance, name string) (string, error) {
			return envFrom(e).directOn(instance, name)
		},
	}
}

func managementRead(e scenario.Env, method string, params, out any) error {
	env := envFrom(e)
	if err := env.check(); err != nil {
		return err
	}
	raw, err := env.fixture.Manage(e.Ctx(), method, manageapi.Request{Params: params})
	if err != nil {
		return err
	}
	return resultBody(raw, out)
}

func managementVersions(e scenario.Env) (recipes.ManagementVersions, error) {
	var out recipes.ManagementVersions
	for _, item := range []struct {
		method  string
		version *admin.Version
	}{{"node.show", &out.Node}, {"presets.list", &out.Presets}, {"adapters.list", &out.Instances}} {
		var result struct {
			Version admin.Version `json:"version"`
		}
		if err := managementRead(e, item.method, nil, &result); err != nil {
			return recipes.ManagementVersions{}, err
		}
		*item.version = result.Version
	}
	return out, nil
}

// checkedManagement retains no process handle. A repeat after interruption sends
// the caller's unchanged request and checks the result through the public API.
func checkedManagement(e scenario.Env, method string, request manageapi.Request) (json.RawMessage, bool, string, error) {
	env := envFrom(e)
	if err := env.check(); err != nil {
		return nil, false, "", err
	}
	raw, err := env.fixture.Manage(e.Ctx(), method, request)
	if err != nil {
		var wire *apiwire.Error
		if !errors.As(err, &wire) || (wire.Code != "version_conflict" && wire.Code != "platform_unsupported") {
			return nil, false, "", err
		}
		_, readErr := env.fixture.Manage(e.Ctx(), "operation.result", manageapi.Request{Params: map[string]any{"operation_id": hex.EncodeToString(request.OperationID[:])}})
		var absent *apiwire.Error
		if !errors.As(readErr, &absent) || absent.Code != apiwire.CodeOperationUnknown {
			return nil, false, "", fmt.Errorf("acceptance: refused %s has no confirmed absent result: %v", method, readErr)
		}
		return nil, false, wire.Code, nil
	}
	var first struct {
		Replayed bool            `json:"replayed"`
		Result   json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &first); err != nil {
		return nil, false, "", err
	}
	var stored json.RawMessage
	if err := managementRead(e, "operation.result", map[string]any{"operation_id": hex.EncodeToString(request.OperationID[:])}, &stored); err != nil {
		return nil, false, "", err
	}
	if !bytes.Equal(stored, first.Result) {
		return nil, false, "", fmt.Errorf("acceptance: %s has no matching persistent result", method)
	}
	again, err := env.fixture.Manage(e.Ctx(), method, request)
	if err != nil {
		return nil, false, "", err
	}
	var replay struct {
		Replayed bool            `json:"replayed"`
		Result   json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(again, &replay); err != nil {
		return nil, false, "", err
	}
	if !replay.Replayed || !bytes.Equal(replay.Result, first.Result) {
		return nil, false, "", fmt.Errorf("acceptance: %s replay differs from its original result", method)
	}
	return first.Result, first.Replayed, "", nil
}

func managementChange(e scenario.Env, q recipes.ManagementRequest, method string, params any) (recipes.ManagementOutcome, error) {
	request := manageapi.Request{OperationID: q.ID, Params: params}
	switch method {
	case "node.set", "presets.add", "presets.remove", "endpoints.set", "adapters.enable", "adapters.disable":
		request.ExpectedVersion = &q.Expected
	}
	raw, replayed, code, err := checkedManagement(e, method, request)
	if err != nil {
		return recipes.ManagementOutcome{}, err
	}
	out := recipes.ManagementOutcome{Replayed: replayed, Code: code}
	if code != "" {
		return out, nil
	}
	var result struct {
		Version  admin.Version `json:"version"`
		Settings struct {
			NodeQueueLimit int `json:"node_queue_limit"`
			BacklogLimit   int `json:"backlog_limit"`
		} `json:"settings"`
		Name        string    `json:"name"`
		Outcome     string    `json:"outcome"`
		Enabled     bool      `json:"enabled"`
		Alert       string    `json:"alert"`
		ConfirmedAt time.Time `json:"confirmed_at"`
		Cleared     bool      `json:"cleared"`
		HighWater   time.Time `json:"high_water"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return recipes.ManagementOutcome{}, err
	}
	out.Version = result.Version
	switch method {
	case "node.set":
		out.Value = strconv.Itoa(result.Settings.NodeQueueLimit)
	case "endpoints.set":
		out.Value = strconv.Itoa(result.Settings.BacklogLimit)
	case "presets.add", "presets.remove":
		out.Value = result.Name
	case "endpoints.close":
		out.Value = result.Outcome
	case "adapters.enable", "adapters.disable":
		out.Value = strconv.FormatBool(result.Enabled)
	case "alerts.ack":
		out.Value = result.Alert
	case "identity.confirm_backup":
		out.Value = "confirmed"
		out.At = result.ConfirmedAt
	case "clock.clear_rollback":
		out.Value = strconv.FormatBool(result.Cleared)
		out.At = result.HighWater
	}
	return out, nil
}

func readManagementFile[T any](e scenario.Env, name string, decode func([]byte) (T, error)) (T, error) {
	var zero T
	disk, err := diskReader(e)
	if err != nil {
		return zero, err
	}
	raw, err := disk.ReadFile(name)
	if err != nil {
		return zero, err
	}
	return decode(raw)
}
func managedEndpointRecord(e scenario.Env, address string) (store.EndpointRecord, error) {
	addr, err := ident.ParseAddress(address)
	if err != nil {
		return store.EndpointRecord{}, err
	}
	disk, err := diskReader(e)
	if err != nil {
		return store.EndpointRecord{}, err
	}
	name := (store.EndpointRecord{ID: addr.Endpoint}).Name()
	raw, err := disk.ReadFile(path.Join(store.EndpointsDir, name))
	if err != nil {
		return store.EndpointRecord{}, err
	}
	rec, damage := store.DecodeRecord(store.EndpointsDir, name, raw)
	if damage != nil {
		return store.EndpointRecord{}, fmt.Errorf("acceptance: managed endpoint damaged: %v", damage)
	}
	return rec.(store.EndpointRecord), nil
}
func inspectManagedEndpoint(e scenario.Env, address string) (recipes.EndpointFacts, error) {
	ep, err := managedEndpointRecord(e, address)
	if err != nil {
		return recipes.EndpointFacts{}, err
	}
	instances, err := readManagementFile(e, config.InstancesPath, config.DecodeInstances)
	if err != nil {
		return recipes.EndpointFacts{}, err
	}
	return recipes.EndpointFacts{Closed: ep.Status == store.EndpointClosed, TokenCleared: len(ep.Token) == 0, BacklogLimit: instances.Items[ep.Instance].Endpoints[ep.ID].BacklogLimit}, nil
}
func inspectManagedInstance(e scenario.Env, name string) (recipes.InstanceFacts, error) {
	var listed struct {
		Instances []struct {
			Instance string `json:"instance"`
			Enabled  bool   `json:"enabled"`
		} `json:"instances"`
	}
	if err := managementRead(e, "adapters.list", nil, &listed); err != nil {
		return recipes.InstanceFacts{}, err
	}
	var out recipes.InstanceFacts
	found := false
	for _, in := range listed.Instances {
		if in.Instance == name {
			out.Enabled = in.Enabled
			found = true
		}
	}
	if !found {
		return out, fmt.Errorf("acceptance: managed instance %s absent", name)
	}
	var endpoints struct {
		Endpoints []struct {
			Address string `json:"address"`
		} `json:"endpoints"`
	}
	if err := managementRead(e, "endpoints.list", map[string]any{"instance": name}, &endpoints); err != nil {
		return out, err
	}
	for _, entry := range endpoints.Endpoints {
		ep, err := managedEndpointRecord(e, entry.Address)
		if err != nil {
			return out, err
		}
		if ep.Status != store.EndpointClosed {
			out.OpenEndpoints++
		} else if len(ep.Token) != 0 {
			return out, fmt.Errorf("%w: disabled endpoint keeps its token", recipes.ErrManagementEffect)
		}
	}
	return out, nil
}
func inspectManagedNode(e scenario.Env) (recipes.NodeFacts, error) {
	node, err := readManagementFile(e, config.NodePath, config.DecodeNode)
	if err != nil {
		return recipes.NodeFacts{}, err
	}
	disk, err := diskReader(e)
	if err != nil {
		return recipes.NodeFacts{}, err
	}
	clock, err := nodekeys.ReadClock(disk)
	if err != nil {
		return recipes.NodeFacts{}, err
	}
	out := recipes.NodeFacts{QueueLimit: node.Settings.NodeQueueLimit, Version: node.Version, Now: envFrom(e).fixture.Now(), HighWater: clock.HighWater, ClockPaused: clock.Paused()}
	backup, err := readManagementFile(e, config.BackupPath, config.DecodeBackup)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return recipes.NodeFacts{}, err
	}
	if err == nil {
		self, err := nodekeys.ReadSelfRevocation(disk)
		if err != nil {
			return recipes.NodeFacts{}, err
		}
		out.BackupConfirmed = backup.Confirms(self.Digest())
	}
	local, err := envFrom(e).localNode()
	if err != nil {
		return recipes.NodeFacts{}, err
	}
	entries, err := disk.ReadDir(config.RevocationsDir)
	if err != nil {
		return recipes.NodeFacts{}, err
	}
	for _, entry := range entries {
		raw, err := disk.ReadFile(path.Join(config.RevocationsDir, entry.Name))
		if err != nil {
			return recipes.NodeFacts{}, err
		}
		rev, _, err := config.DecodeRevocation(entry.Name, raw)
		if err != nil {
			return recipes.NodeFacts{}, err
		}
		out.Retired = out.Retired || (rev.Kind() == revocation.KindNodeSelf && rev.Node() == local)
	}
	return out, nil
}

type managementAlert struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	Subject      string `json:"subject"`
	Acknowledged bool   `json:"acknowledged"`
}

func managementAlerts(e scenario.Env) ([]managementAlert, error) {
	var listed struct {
		Alerts []managementAlert `json:"alerts"`
	}
	err := managementRead(e, "alerts.list", map[string]any{"include_acknowledged": true}, &listed)
	return listed.Alerts, err
}
func newVersionAlertOp() func(scenario.Env) (string, error) {
	var request *recipes.ManagementRequest
	return func(e scenario.Env) (string, error) {
		if err := envFrom(e).check(); err != nil {
			return "", err
		}
		if request == nil {
			versions, err := managementVersions(e)
			if err != nil {
				return "", err
			}
			request = &recipes.ManagementRequest{ID: admin.OperationID{54}, Expected: versions.Instances}
		}
		out, err := managementChange(e, *request, "adapters.enable", map[string]any{"adapter": versionWarningInstance, "instance": versionWarningInstance})
		if err != nil {
			return "", err
		}
		if out.Code != "" || out.Value != "true" || out.Version != request.Expected+1 {
			return "", fmt.Errorf("%w: warning adapter enable returned %+v", recipes.ErrManagementEffect, out)
		}
		if err := e.Quiesce(); err != nil {
			return "", err
		}
		alerts, err := managementAlerts(e)
		if err != nil {
			return "", err
		}
		facts := make([]recipes.VersionAlertFacts, 0, len(alerts))
		for _, a := range alerts {
			facts = append(facts, recipes.VersionAlertFacts{ID: a.ID, Kind: a.Kind, Subject: a.Subject})
		}
		return recipes.RequireVersionAlert(facts, versionWarningInstance)
	}
}
