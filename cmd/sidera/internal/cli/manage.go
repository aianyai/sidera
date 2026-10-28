package cli

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/manageapi"
)

type managementPlan struct {
	method        string
	params        map[string]any
	change        bool
	versionMethod string
	expected      *admin.Version
}

func (c *command) manage(ctx context.Context, args []string) error {
	plan, err := c.managementPlan(args)
	if err != nil {
		return err
	}
	client, err := manageapi.NewClient(manageapi.ClientConfig{Home: c.home})
	if err != nil {
		return err
	}
	defer client.Close()
	if plan.method == "identity.export_revocation" {
		return c.exportRevocation(ctx, client)
	}
	req := manageapi.Request{Params: plan.params, ExpectedVersion: plan.expected}
	if plan.versionMethod != "" && req.ExpectedVersion == nil {
		raw, err := client.Call(ctx, plan.versionMethod, manageapi.Request{})
		if err != nil {
			return err
		}
		version, err := managementVersion(raw)
		if err != nil {
			return err
		}
		req.ExpectedVersion = &version
	}
	if plan.change {
		req.OperationID, err = admin.NewOperationID(rand.Reader)
		if err != nil {
			return err
		}
	}
	// The version, identifier and params are chosen once. A lost answer permits
	// one resend of this request, never a fresh read or a newly chosen identifier.
	raw, err := client.Call(ctx, plan.method, req)
	if err != nil && !req.OperationID.IsZero() && apiwire.FromError(err).Category == apiwire.CategoryOutcomeUnknown {
		raw, err = client.Call(ctx, plan.method, req)
	}
	if err != nil {
		return err
	}
	if err := c.managementResult(raw); err != nil {
		return err
	}
	if !c.json && plan.method == "adapters.enable" {
		_, err := fmt.Fprintln(c.Stdout, "enabled, probing in progress; see sidera adapter list")
		return err
	}
	return nil
}

func managementVersion(raw []byte) (admin.Version, error) {
	var out struct {
		Result struct {
			Version *admin.Version `json:"version"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Result.Version == nil {
		return 0, apiwire.OutcomeUnknown("")
	}
	return *out.Result.Version, nil
}

func (c *command) managementPlan(args []string) (managementPlan, error) {
	plan := managementPlan{params: map[string]any{}}
	group := args[0]
	if group == "status" {
		plan.method = "status"
		return plan, nil
	}
	if len(args) < 2 {
		return plan, usage(group + " needs a subcommand; use --help")
	}
	action := args[1]
	f := c.arguments.flags
	o := &c.arguments.management
	var versioned bool
	switch group + " " + action {
	case "daemon stop":
		plan.method = "daemon.stop"
		plan.change = true
	case "alerts list":
		plan.method = "alerts.list"
	case "alerts ack":
		plan.method = "alerts.ack"
		plan.change = true
	case "preset list":
		plan.method = "presets.list"
	case "preset add":
		plan.method = "presets.add"
		plan.change = true
		plan.versionMethod = "presets.list"
		versioned = true
	case "preset remove":
		plan.method = "presets.remove"
		plan.change = true
		plan.versionMethod = "presets.list"
		versioned = true
	case "adapter list":
		plan.method = "adapters.list"
	case "adapter install":
		plan.method = "adapters.install"
		plan.change = true
	case "adapter enable":
		plan.method = "adapters.enable"
		plan.change = true
		plan.versionMethod = "adapters.list"
		versioned = true
	case "adapter disable":
		plan.method = "adapters.disable"
		plan.change = true
		plan.versionMethod = "adapters.list"
		versioned = true
	case "endpoint list":
		plan.method = "endpoints.list"
	case "endpoint close":
		plan.method = "endpoints.close"
		plan.change = true
	case "endpoint set":
		plan.method = "endpoints.set"
		plan.change = true
		plan.versionMethod = "adapters.list"
		versioned = true
	case "node show":
		plan.method = "node.show"
	case "node set":
		plan.method = "node.set"
		plan.change = true
		plan.versionMethod = "node.show"
		versioned = true
	case "identity export-revocation":
		plan.method = "identity.export_revocation"
	case "identity confirm-backup":
		plan.method = "identity.confirm_backup"
		plan.change = true
	case "identity retire":
		plan.method = "identity.retire"
		plan.change = true
	case "clock clear-rollback":
		plan.method = "clock.clear_rollback"
		plan.change = true
	default:
		return plan, usage("unknown command: " + group + " " + action)
	}
	p, err := parse(f, args[2:])
	if err != nil {
		return plan, err
	}
	if versioned && present(f, "expected-version") {
		v := admin.Version(o.version)
		plan.expected = &v
	}
	want := 0
	switch plan.method {
	case "alerts.ack", "presets.add", "presets.remove", "adapters.install", "adapters.enable", "adapters.disable", "endpoints.close", "identity.confirm_backup":
		want = 1
	case "node.set":
		if len(p) < 1 {
			return plan, usage("node set needs field=json assignments")
		}
		want = len(p)
	case "endpoints.set":
		if len(p) < 2 {
			return plan, usage("endpoint set needs an address and field=json assignments")
		}
		want = len(p)
	}
	if len(p) != want {
		return plan, usage(group + " " + action + " has the wrong number of arguments; use --help")
	}
	switch plan.method {
	case "alerts.list":
		addPresent(f, plan.params, "all", "include_acknowledged", o.all)
	case "alerts.ack":
		plan.params["alert"] = p[0]
	case "presets.add":
		if !present(f, "file") {
			return plan, usage("preset add needs --file")
		}
		data, err := c.readDocument(o.file)
		if err != nil {
			return plan, err
		}
		plan.params["name"], plan.params["preset"] = p[0], data
	case "presets.remove":
		plan.params["name"] = p[0]
	case "adapters.install":
		source, err := filepath.Abs(p[0])
		if err != nil {
			return plan, err
		}
		plan.params["source"] = source
	case "adapters.enable":
		plan.params["adapter"] = p[0]
		addPresent(f, plan.params, "instance", "instance", o.instance)
		if present(f, "config") {
			data, err := c.readDocument(o.file)
			if err != nil {
				return plan, err
			}
			plan.params["config"] = data
		}
	case "adapters.disable":
		plan.params["instance"] = p[0]
	case "endpoints.list":
		addPresent(f, plan.params, "instance", "instance", o.instance)
	case "endpoints.close":
		plan.params["endpoint"] = p[0]
	case "endpoints.set":
		patch, err := assignments(p[1:])
		if err != nil {
			return plan, err
		}
		plan.params["endpoint"], plan.params["settings"] = p[0], patch
	case "node.set":
		patch, err := assignments(p)
		if err != nil {
			return plan, err
		}
		plan.params["settings"] = patch
	case "identity.confirm_backup":
		plan.params["digest"] = p[0]
	case "identity.retire":
		if !o.yes || !present(f, "node") {
			return plan, usage("identity retire needs --node <id> --yes")
		}
		plan.params["node"] = o.node
	}
	return plan, nil
}

func (c *command) readDocument(path string) (json.RawMessage, error) {
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(c.Stdin)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, apiwire.Malformed("cannot read the JSON file", "file")
	}
	return json.RawMessage(data), nil
}

// Assignment values remain JSON when they are JSON, including their original
// number spelling. Other values are strings. The settings owner knows the keys.
func assignments(args []string) (map[string]any, error) {
	out := make(map[string]any, len(args))
	for _, arg := range args {
		name, value, ok := strings.Cut(arg, "=")
		if !ok || name == "" {
			return nil, usage("settings are field=json assignments")
		}
		if _, exists := out[name]; exists {
			return nil, usage("a setting may be assigned only once")
		}
		if json.Valid([]byte(value)) {
			out[name] = json.RawMessage(value)
		} else {
			out[name] = value
		}
	}
	return out, nil
}

type managementFlags struct {
	file, instance, node string
	all, yes             bool
	version              uint64
}

func (o *managementFlags) add(f *flag.FlagSet, name string) {
	switch name {
	case "alerts list":
		f.BoolVar(&o.all, "all", false, "include acknowledged alerts")
	case "preset add":
		f.StringVar(&o.file, "file", "", "preset JSON file, or - for stdin")
	case "adapter enable":
		f.StringVar(&o.instance, "instance", "", "instance name")
		f.StringVar(&o.file, "config", "", "instance JSON file, or - for stdin")
	case "endpoint list":
		f.StringVar(&o.instance, "instance", "", "filter by instance")
	case "identity retire":
		f.StringVar(&o.node, "node", "", "node to retire")
		f.BoolVar(&o.yes, "yes", false, "confirm permanent retirement")
	}
	switch name {
	case "preset add", "preset remove", "adapter enable", "adapter disable", "endpoint set", "node set":
		f.Uint64Var(&o.version, "expected-version", 0, "version the change is based on")
	}
}
