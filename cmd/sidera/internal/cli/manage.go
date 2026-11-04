package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

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
	exportOut     *string
	// wait is how long `trust request` waits, on the client's side only, for the
	// pairing to complete after the node has answered; zero does not wait.
	wait time.Duration
	// expectVersion is the version `daemon restart` requires of the program the
	// daemon starts again; empty requires none.
	expectVersion string
}

func (c *command) manage(ctx context.Context, args []string) error {
	plan, err := c.managementPlan(args)
	if err != nil {
		return err
	}
	home, err := c.dataDir()
	if err != nil {
		return err
	}
	client, err := manageapi.NewClient(manageapi.ClientConfig{Home: home})
	if err != nil {
		return err
	}
	defer client.Close()
	if plan.method == "identity.export_revocation" {
		return c.exportRevocation(ctx, client, plan.exportOut)
	}
	if plan.method == "daemon.restart" {
		return c.restartCommand(ctx, client, c.random, restartRequest{ExpectVersion: plan.expectVersion, Wait: plan.wait, Home: home})
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
		req.OperationID, err = admin.NewOperationID(c.random)
		if err != nil {
			return err
		}
	}
	// The version, identifier and params are chosen once. A lost answer permits
	// one resend of this request, never a fresh read or a newly chosen identifier.
	raw, err := callResending(ctx, client, plan.method, req)
	if err != nil {
		return err
	}
	if plan.method == "status" {
		if raw, err = statusWithHome(raw, home); err != nil {
			return err
		}
	}
	switch {
	case plan.method == "trust.request":
		return c.awaitPairing(ctx, readTrustView(client), raw, plan.wait)
	case plan.method == "trust.list" && !c.json:
		return c.trustList(raw)
	}
	if err := c.managementResult(raw); err != nil {
		return err
	}
	if !c.json {
		switch plan.method {
		case "adapters.enable":
			_, err := fmt.Fprintln(c.Stdout, "enabled, probing in progress; see sidera adapter list")
			return err
		case "network.listen.set":
			_, err := fmt.Fprintln(c.Stdout, "restart the daemon for the new listen addresses to take effect")
			return err
		}
	}
	return nil
}

// callResending sends req, and when it carries an operation identifier and its
// outcome is unknown sends the same request once more. The daemon answers a resend
// of a request that took effect with the stored result.
func callResending(ctx context.Context, client caller, method string, req manageapi.Request) ([]byte, error) {
	raw, err := client.Call(ctx, method, req)
	if err != nil && !req.OperationID.IsZero() && apiwire.FromError(err).Category == apiwire.CategoryOutcomeUnknown {
		raw, err = client.Call(ctx, method, req)
	}
	return raw, err
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
	action, rest := args[1], args[2:]
	if group == "network" && (action == "address" || action == "listen") {
		if len(args) < 3 {
			return plan, usage("network " + action + " needs a subcommand; use --help")
		}
		action, rest = action+" "+args[2], args[3:]
	}
	f := c.arguments.flags
	o := &c.arguments.management
	var versioned bool
	switch group + " " + action {
	case "daemon stop":
		plan.method = "daemon.stop"
		plan.change = true
	case "daemon restart":
		plan.method = "daemon.restart"
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
	case "adapter config":
		plan.method = "adapters.config"
		plan.change = true
		plan.versionMethod = "adapters.list"
		versioned = true
	case "adapter remove":
		plan.method = "adapters.remove"
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
	case "network show":
		plan.method = "network.show"
	case "network address set":
		plan.method = "network.address.set"
		plan.change = true
		plan.versionMethod = "network.show"
		versioned = true
	case "network address remove":
		plan.method = "network.address.remove"
		plan.change = true
		plan.versionMethod = "network.show"
		versioned = true
	case "network listen set":
		plan.method = "network.listen.set"
		plan.change = true
		plan.versionMethod = "network.show"
		versioned = true
	case "trust expect":
		plan.method = "trust.expect"
		plan.change = true
	case "trust forget":
		plan.method = "trust.forget"
		plan.change = true
	case "trust reject":
		plan.method = "trust.reject"
		plan.change = true
	case "trust request":
		plan.method = "trust.request"
		plan.change = true
	case "trust list":
		plan.method = "trust.list"
	case "trust remove":
		plan.method = "trust.remove"
		plan.change = true
	case "trust clear":
		plan.method = "trust.clear"
		plan.change = true
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
	p, err := parse(f, rest)
	if err != nil {
		return plan, err
	}
	if present(f, "out") {
		plan.exportOut = &o.output
	}
	if versioned && present(f, "expected-version") {
		v := admin.Version(o.version)
		plan.expected = &v
	}
	want := 0
	switch plan.method {
	case "daemon.restart":
		if o.wait < 0 {
			return plan, usage("--wait cannot be negative")
		}
		plan.wait, plan.expectVersion = o.wait, o.expectVersion
	case "trust.request":
		if len(p) < 1 {
			return plan, usage("trust request needs a node and optionally multiaddrs to dial")
		}
		if o.wait < 0 {
			return plan, usage("--wait cannot be negative")
		}
		want = len(p)
		plan.wait = o.wait
	case "trust.expect", "trust.forget", "trust.reject", "trust.remove", "trust.clear":
		want = 1
	case "alerts.ack", "presets.add", "presets.remove", "adapters.install", "adapters.enable", "adapters.config", "adapters.disable", "adapters.remove", "endpoints.close", "identity.confirm_backup":
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
	case "network.address.set":
		if len(p) < 2 {
			return plan, usage("network address set needs a node and at least one multiaddr")
		}
		want = len(p)
	case "network.address.remove":
		want = 1
	case "network.listen.set":
		if len(p) < 1 {
			return plan, usage("network listen set needs at least one multiaddr")
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
	case "adapters.config":
		if !present(f, "config") {
			return plan, usage("adapter config needs --config")
		}
		data, err := c.readDocument(o.file)
		if err != nil {
			return plan, err
		}
		plan.params["instance"], plan.params["config"] = p[0], data
	case "adapters.disable", "adapters.remove":
		plan.params["instance"] = p[0]
	case "endpoints.list":
		addPresent(f, plan.params, "instance", "instance", o.instance)
	case "endpoints.close":
		plan.params["endpoint"] = p[0]
		if o.allowAttached {
			plan.params["allow_attached"] = true
		}
	case "endpoints.set":
		patch, err := assignments(p[1:])
		if err != nil {
			return plan, err
		}
		plan.params["endpoint"], plan.params["settings"] = p[0], patch
	case "network.address.set":
		plan.params["node"], plan.params["addresses"] = p[0], p[1:]
	case "network.address.remove":
		plan.params["node"] = p[0]
	case "trust.reject":
		plan.params["pairing_id"] = p[0]
	case "trust.expect", "trust.forget", "trust.remove", "trust.clear":
		plan.params["node"] = p[0]
	case "trust.request":
		plan.params["node"] = p[0]
		if len(p) > 1 {
			plan.params["addresses"] = p[1:]
		}
	case "network.listen.set":
		plan.params["addresses"] = p
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
	file, instance, node, output string
	expectVersion                string
	all, yes, allowAttached      bool
	version                      uint64
	wait                         time.Duration
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
	case "adapter config":
		f.StringVar(&o.file, "config", "", "instance JSON file, or - for stdin")
	case "endpoint list":
		f.StringVar(&o.instance, "instance", "", "filter by instance")
	case "endpoint close":
		f.BoolVar(&o.allowAttached, "allow-attached", false, "close a session a person opened and is using")
	case "identity export-revocation":
		f.StringVar(&o.output, "out", "", "write the verified raw record to a new private file")
	case "trust request":
		f.DurationVar(&o.wait, "wait", defaultPairingWait, "how long to wait for the pairing to complete; 0 returns at once")
	case "daemon restart":
		f.DurationVar(&o.wait, "wait", defaultRestartWait, "how long to wait for the new daemon to run; 0 returns once the restart is accepted")
		f.StringVar(&o.expectVersion, "expect-version", "", "refuse to restart unless the program that would be started reports this version")
	case "identity retire":
		f.StringVar(&o.node, "node", "", "node to retire")
		f.BoolVar(&o.yes, "yes", false, "confirm permanent retirement")
	}
	switch name {
	case "preset add", "preset remove", "adapter enable", "adapter config", "adapter disable", "adapter remove", "endpoint set", "node set",
		"network address set", "network address remove", "network listen set":
		f.Uint64Var(&o.version, "expected-version", 0, "version the change is based on")
	}
}
