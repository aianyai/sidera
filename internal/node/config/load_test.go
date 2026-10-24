package config_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/node/config"
)

// An invalid file refuses startup and the error names the file. One row per
// kind of file and kind of failure; the content of a row replaces the file of
// the valid tree, and a row with an empty content removes it. Nothing is
// written when a file is refused: not even the repair of the adapter
// directory, because the tree is not understood.
func TestInvalidFileRefusesStartupAndNamesTheFile(t *testing.T) {
	// Replacements of a part of a golden file keep the rows short and the
	// failure they cause visible next to the text it breaks.
	edit := func(doc, old, new string) string {
		t.Helper()
		if !strings.Contains(doc, old) {
			t.Fatalf("test row edits %q, which the golden file does not hold", old)
		}
		return strings.Replace(doc, old, new, 1)
	}
	revData, digest, _ := selfRevocation(t, 7)
	otherData, otherDigest, _ := selfRevocation(t, 9)
	_ = otherData

	type row struct {
		name    string
		path    string // the file that is replaced and that the error must name
		content string // "" removes the file
		kind    config.FileKind
		reason  config.FileReason
		field   string // a part of the JSON path the error must hold, if any
	}
	rows := []row{
		// node.json
		{"node missing", config.NodePath, "", config.KindNode, config.FileMissing, ""},
		{"node empty", config.NodePath, " ", config.KindNode, config.FileMalformed, ""},
		{"node not json", config.NodePath, "{", config.KindNode, config.FileMalformed, ""},
		{"node data after the document", config.NodePath, goldenNode + "{}", config.KindNode, config.FileMalformed, ""},
		{"node unknown field", config.NodePath, edit(goldenNode, `"version": 3,`, `"version": 3, "extra": 1,`), config.KindNode, config.FileMalformed, "extra"},
		{"node unknown setting", config.NodePath, edit(goldenNode, `"log_total_bytes"`, `"log_bytes"`), config.KindNode, config.FileMalformed, ""},
		{"node key in another case", config.NodePath, edit(goldenNode, `"version": 3,`, `"Version": 3,`), config.KindNode, config.FileMalformed, ""},
		{"node repeated key", config.NodePath, edit(goldenNode, `"version": 3,`, `"version": 3, "version": 4,`), config.KindNode, config.FileMalformed, "version"},
		{"node null value", config.NodePath, edit(goldenNode, `"version": 3,`, `"version": null,`), config.KindNode, config.FileMalformed, "version"},
		{"node missing setting", config.NodePath, edit(goldenNode, `    "node_queue_limit": 10000,`+"\n", ""), config.KindNode, config.FileMalformed, "node_queue_limit"},
		{"node wrong type", config.NodePath, edit(goldenNode, `"reply_permission": true`, `"reply_permission": "yes"`), config.KindNode, config.FileMalformed, "reply_permission"},
		{"node unknown format", config.NodePath, edit(goldenNode, `"format": 1`, `"format": 2`), config.KindNode, config.FileInvalid, "format"},
		{"node version zero", config.NodePath, edit(goldenNode, `"version": 3,`, `"version": 0,`), config.KindNode, config.FileInvalid, "version"},
		{"node queue limit zero", config.NodePath, edit(goldenNode, `"node_queue_limit": 10000`, `"node_queue_limit": 0`), config.KindNode, config.FileInvalid, "node_queue_limit"},
		{"node prompt turns zero", config.NodePath, edit(goldenNode, `"node_prompt_turns": 8`, `"node_prompt_turns": 0`), config.KindNode, config.FileInvalid, "node_prompt_turns"},
		{"node backlog limit zero", config.NodePath, edit(goldenNode, `"backlog_limit": 200`, `"backlog_limit": 0`), config.KindNode, config.FileInvalid, "backlog_limit"},
		{"node queue time beyond the cap", config.NodePath, edit(goldenNode, `"max_queue_s": 1800`, `"max_queue_s": 99999`), config.KindNode, config.FileInvalid, "max_queue_s"},
		{"node reserve below what the queue can grow", config.NodePath, edit(goldenNode, `"disk_reserve_bytes": 268435456`, `"disk_reserve_bytes": 1048576`), config.KindNode, config.FileInvalid, "disk_reserve_bytes"},
		{"node retention zero", config.NodePath, edit(goldenNode, `"archive_retention_days": 30`, `"archive_retention_days": 0`), config.KindNode, config.FileInvalid, "archive_retention_days"},
		{"node operation id of the wrong length", config.NodePath, edit(goldenNode, opIDText, "0011"), config.KindNode, config.FileInvalid, "last_operation.id"},
		{"node operation person unknown", config.NodePath, edit(goldenNode, `"person": "local"`, `"person": "someone"`), config.KindNode, config.FileInvalid, "last_operation.person"},
		{"node operation result not an object", config.NodePath, edit(goldenNode, "\"result\": {\n      \"version\": 3\n    }", `"result": [1]`), config.KindNode, config.FileInvalid, "last_operation.result"},
		{"node operation result null", config.NodePath, edit(goldenNode, "\"result\": {\n      \"version\": 3\n    }", `"result": null`), config.KindNode, config.FileInvalid, "last_operation.result"},

		// policy.json
		{"policy missing", config.PolicyPath, "", config.KindPolicy, config.FileMissing, ""},
		{"policy not json", config.PolicyPath, "[", config.KindPolicy, config.FileMalformed, ""},
		{"policy rules missing", config.PolicyPath, `{"format": 1, "version": 1}`, config.KindPolicy, config.FileMalformed, "rules"},
		{"policy unknown rule field", config.PolicyPath, edit(goldenPolicy, `"action": "see"`, `"action": "see", "why": "x"`), config.KindPolicy, config.FileMalformed, "why"},
		{"policy unknown effect", config.PolicyPath, edit(goldenPolicy, `"effect": "deny"`, `"effect": "forbid"`), config.KindPolicy, config.FileInvalid, "rules[1]"},
		{"policy unknown subject kind", config.PolicyPath, edit(goldenPolicy, `"kind": "member"`, `"kind": "everyone"`), config.KindPolicy, config.FileInvalid, "rules[0]"},
		{"policy unknown action", config.PolicyPath, edit(goldenPolicy, `"action": "see"`, `"action": "look"`), config.KindPolicy, config.FileInvalid, "rules[0]"},
		{"policy node that is not an id", config.PolicyPath, edit(goldenPolicy, `"node": "`+node2Text+`"`, `"node": "node1abc"`), config.KindPolicy, config.FileInvalid, "rules[1]"},
		{"policy rule authz refuses", config.PolicyPath, edit(goldenPolicy, `"action": "see"`, `"action": "relay.reserve"`), config.KindPolicy, config.FileInvalid, "rules[0]"},
		{"policy time that is not a time", config.PolicyPath, edit(goldenPolicy, `"2027-01-01T00:00:00Z"`, `"next year"`), config.KindPolicy, config.FileInvalid, "rules[1]"},
		{"policy version zero", config.PolicyPath, edit(goldenPolicy, `"version": 2`, `"version": 0`), config.KindPolicy, config.FileInvalid, "version"},

		// presets.json
		{"presets missing", config.PresetsPath, "", config.KindPresets, config.FileMissing, ""},
		{"presets not json", config.PresetsPath, "nope", config.KindPresets, config.FileMalformed, ""},
		{"preset without the local source statement", config.PresetsPath, edit(goldenPresets, `      "local_sources": "allow",`+"\n", ""), config.KindPresets, config.FileMalformed, "presets.default.local_sources"},
		{"preset with another statement", config.PresetsPath, edit(goldenPresets, `"local_sources": "allow"`, `"local_sources": "maybe"`), config.KindPresets, config.FileInvalid, "presets.default.local_sources"},
		{"preset with a boolean statement", config.PresetsPath, edit(goldenPresets, `"local_sources": "allow"`, `"local_sources": true`), config.KindPresets, config.FileMalformed, "presets.default.local_sources"},
		{"preset with a null statement", config.PresetsPath, edit(goldenPresets, `"local_sources": "allow"`, `"local_sources": null`), config.KindPresets, config.FileMalformed, "presets.default.local_sources"},
		{"preset name with a space", config.PresetsPath, edit(goldenPresets, `"sandboxed"`, `"not valid"`), config.KindPresets, config.FileInvalid, "not valid"},
		{"preset config for an adapter id that is not one", config.PresetsPath, edit(goldenPresets, `"example": {`, `"Example": {`), config.KindPresets, config.FileInvalid, "Example"},
		{"preset config with a bad key", config.PresetsPath, edit(goldenPresets, `"model"`, `"Model"`), config.KindPresets, config.FileInvalid, "Model"},
		{"preset config value null", config.PresetsPath, edit(goldenPresets, `"small"`, `null`), config.KindPresets, config.FileInvalid, "model"},
		{"preset empty description", config.PresetsPath, edit(goldenPresets, `"Created by sidera init."`, `""`), config.KindPresets, config.FileMalformed, "description"},
		{"presets version zero", config.PresetsPath, edit(goldenPresets, `"version": 2`, `"version": 0`), config.KindPresets, config.FileInvalid, "version"},

		// instances.json
		{"instances missing", config.InstancesPath, "", config.KindInstances, config.FileMissing, ""},
		{"instances not json", config.InstancesPath, `{"format":`, config.KindInstances, config.FileMalformed, ""},
		{"instance name that is not a name", config.InstancesPath, edit(goldenInstances, `"example": {`, `"Example": {`), config.KindInstances, config.FileInvalid, "Example"},
		{"instance adapter that is not an id", config.InstancesPath, edit(goldenInstances, `"adapter": "example"`, `"adapter": "Bad Id"`), config.KindInstances, config.FileInvalid, "adapter"},
		{"instance enabled missing", config.InstancesPath, edit(goldenInstances, `      "enabled": true,`+"\n", ""), config.KindInstances, config.FileMalformed, "enabled"},
		{"instance config key that is not a key", config.InstancesPath, edit(goldenInstances, `"workspace"`, `"Work Space"`), config.KindInstances, config.FileInvalid, "Work Space"},
		{"instance endpoint that is not an id", config.InstancesPath, edit(goldenInstances, `"`+ep3Text+`"`, `"ep1abc"`), config.KindInstances, config.FileInvalid, "ep1abc"},
		{"instance endpoint setting that sets nothing", config.InstancesPath, edit(goldenInstances, "{\n          \"backlog_limit\": 50\n        }", "{}"), config.KindInstances, config.FileInvalid, ep1Text},
		{"instance endpoint explicit zero", config.InstancesPath, edit(goldenInstances, `"backlog_limit": 50`, `"backlog_limit": 0`), config.KindInstances, config.FileMalformed, "backlog_limit"},
		{"instance endpoint queue time beyond the cap", config.InstancesPath, edit(goldenInstances, `"max_queue_s": 3600`, `"max_queue_s": 90000`), config.KindInstances, config.FileInvalid, "max_queue_s"},
		{"instance endpoint negative backlog", config.InstancesPath, edit(goldenInstances, `"backlog_limit": 400`, `"backlog_limit": -1`), config.KindInstances, config.FileInvalid, "backlog_limit"},

		// endpoint-ids
		{"endpoint ids not json", config.EndpointIDsPath("example"), "{", config.KindEndpointIDs, config.FileMalformed, ""},
		{"endpoint ids runtime that is not an id", config.EndpointIDsPath("example"), edit(goldenEndpointIDs, `"runtime": "`+ep1Text+`"`, `"runtime": "ep1x"`), config.KindEndpointIDs, config.FileInvalid, "runtime"},
		{"endpoint ids direct that is not an id", config.EndpointIDsPath("example"), edit(goldenEndpointIDs, `"`+ep2Text+`"`, `"ep1x"`), config.KindEndpointIDs, config.FileInvalid, "notes"},
		{"endpoint ids the same id twice", config.EndpointIDsPath("example"), edit(goldenEndpointIDs, `"notes": "`+ep2Text+`"`, `"notes": "`+ep1Text+`"`), config.KindEndpointIDs, config.FileInvalid, "notes"},
		{"endpoint ids empty key", config.EndpointIDsPath("example"), edit(goldenEndpointIDs, `"notes"`, `""`), config.KindEndpointIDs, config.FileInvalid, ""},
		{"endpoint ids key beyond the length bound", config.EndpointIDsPath("example"), edit(goldenEndpointIDs, `"notes"`, `"`+strings.Repeat("k", 129)+`"`), config.KindEndpointIDs, config.FileInvalid, ""},
		{"endpoint ids unknown field", config.EndpointIDsPath("example"), edit(goldenEndpointIDs, `"format": 1,`, `"format": 1, "extra": 1,`), config.KindEndpointIDs, config.FileMalformed, "extra"},
		{"endpoint ids file of an instance with no valid name", config.EndpointIDsPath("Bad Name"), goldenEndpointIDs, config.KindEndpointIDs, config.FileMisnamed, ""},
		{"endpoint ids id of another instance", config.EndpointIDsPath("other"), `{"format": 1, "direct": {"y": "` + ep1Text + `"}}`, config.KindEndpointIDs, config.FileInvalid, ""},

		// revocation-backup.json
		{"backup not json", config.BackupPath, "x", config.KindBackup, config.FileMalformed, ""},
		{"backup time that is not a time", config.BackupPath, strings.Replace(edit(goldenBackup, `2026-10-24T06:00:00Z`, `yesterday`), "%s", digest.String(), 1), config.KindBackup, config.FileInvalid, "confirmed_at"},
		{"backup digest of the wrong length", config.BackupPath, strings.Replace(goldenBackup, "%s", "abcd", 1), config.KindBackup, config.FileInvalid, "digest"},
		{"backup digest that is not hex", config.BackupPath, strings.Replace(goldenBackup, "%s", strings.Repeat("zz", 32), 1), config.KindBackup, config.FileInvalid, "digest"},

		// revocation records
		{"revocation file with another name", config.RevocationPath(otherDigest), string(revData), config.KindRevocation, config.FileMisnamed, ""},
		{"revocation not a record", config.RevocationPath(digest), "not a record", config.KindRevocation, config.FileMalformed, ""},

		// adapters
		{"adapter directory that is not an id", config.AdapterDir("Bad Id") + "/current", goldenCurrent, config.KindDir, config.FileMisnamed, ""},
		{"adapter current not json", config.AdapterCurrentPath("example"), "1.0.0", config.KindAdapterCurrent, config.FileMalformed, ""},
		{"adapter current with a version that is not semver", config.AdapterCurrentPath("example"), edit(goldenCurrent, `1.0.0`, `one`), config.KindAdapterCurrent, config.FileInvalid, "version"},
		{"adapter current names a version that is not there", config.AdapterCurrentPath("example"), edit(goldenCurrent, `1.0.0`, `1.1.0`), config.KindAdapterManifest, config.FileMissing, ""},
		{"adapter manifest not valid", config.AdapterManifestPath("example", "1.0.0"), `{"manifest_version": 1}`, config.KindAdapterManifest, config.FileInvalid, ""},
		{"adapter manifest of another adapter", config.AdapterManifestPath("example", "1.0.0"), manifestOf("another", "1.0.0"), config.KindAdapterManifest, config.FileMisnamed, ""},
		{"adapter manifest of another version", config.AdapterManifestPath("example", "1.0.0"), manifestOf("example", "1.0.1"), config.KindAdapterManifest, config.FileMisnamed, ""},
	}

	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			rg := newRig(t)
			tree := validTree(t)
			if r.content == "" {
				delete(tree, r.path)
			} else {
				tree[r.path] = r.content
			}
			putTree(t, rg.mem, tree)
			// A stray staging directory shows that nothing is repaired when the
			// tree is refused.
			mkdirAll(t, rg.mem, config.AdapterStageDir("example", "feed"))
			before := snapshot(t, rg.mem)

			_, err := config.Load(rg.w)
			if err == nil {
				t.Fatalf("Load accepted %s", r.path)
			}
			fe := fileError(t, err)
			wantPath := r.path
			if r.kind == config.KindAdapterManifest && r.reason == config.FileMissing {
				wantPath = config.AdapterManifestPath("example", "1.1.0")
			}
			if r.kind == config.KindDir && r.reason == config.FileMisnamed {
				wantPath = config.AdapterDir("Bad Id")
			}
			if fe.Path != wantPath || fe.Kind != r.kind || fe.Reason != r.reason {
				t.Errorf("error is {path %q kind %v reason %v}, want {path %q kind %v reason %v}: %v", fe.Path, fe.Kind, fe.Reason, wantPath, r.kind, r.reason, err)
			}
			if !strings.Contains(err.Error(), wantPath) {
				t.Errorf("the message does not name the file %q: %v", wantPath, err)
			}
			if r.field != "" && !strings.Contains(fe.Field+" "+err.Error(), r.field) {
				t.Errorf("the error does not point at %q: field %q, message %v", r.field, fe.Field, err)
			}
			if after := snapshot(t, rg.mem); !reflect.DeepEqual(before, after) {
				t.Errorf("refusing the tree wrote something: before %v after %v", keysOf(before), keysOf(after))
			}
		})
	}
}

// The directories of config/ must all be there: a missing one is an owner
// deleting what init made, and the node names it instead of starting without.
func TestMissingDirectoryRefusesStartupAndNamesIt(t *testing.T) {
	for _, dir := range config.LayoutDirs() {
		t.Run(dir, func(t *testing.T) {
			rg := newRig(t)
			tree := validTree(t)
			putTree(t, rg.mem, tree)
			removeTree(t, rg.mem, dir)
			_, err := config.Load(rg.w)
			if err == nil {
				t.Fatal("Load accepted a tree without " + dir)
			}
			fe := fileError(t, err)
			if fe.Path != dir || fe.Kind != config.KindDir || fe.Reason != config.FileMissing {
				t.Errorf("error is %+v, want a missing directory %s", fe, dir)
			}
		})
	}
}

// When several files are invalid the first in a fixed order is named, so the
// owner fixes them one by one in a predictable order.
func TestFirstInvalidFileInFixedOrderIsNamed(t *testing.T) {
	rg := newRig(t)
	tree := validTree(t)
	tree[config.InstancesPath] = "{"
	tree[config.PresetsPath] = "{"
	tree[config.BackupPath] = "{"
	putTree(t, rg.mem, tree)
	_, err := config.Load(rg.w)
	if fe := fileError(t, err); fe.Path != config.PresetsPath {
		t.Errorf("named %q, want %q: node, policy, presets, instances, endpoint ids, backup, revocations, adapters", fe.Path, config.PresetsPath)
	}
}

// A valid tree loads, and everything in it is available as the values the other
// packages read.
func TestValidTreeLoads(t *testing.T) {
	_, c := loaded(t)

	node := c.Node()
	if node.Version != 3 || !node.Settings.ReplyPermission || node.Settings.NodeQueueLimit != 10000 ||
		node.Settings.DiskReserveBytes != 268435456 || node.Settings.NodePromptTurns != 8 ||
		node.Settings.ArchiveRetentionDays != 30 || node.Settings.LogTotalBytes != 157286400 ||
		node.Settings.EndpointDefaults.MaxQueue.Seconds() != 1800 || node.Settings.EndpointDefaults.BacklogLimit != 200 {
		t.Errorf("node = %+v", node)
	}
	if node.LastOperation == nil || node.LastOperation.ID[0] != 0x00 || node.LastOperation.ID[15] != 0xff ||
		!node.LastOperation.Person.IsZero() || string(node.LastOperation.Result) != `{"version":3}` {
		t.Errorf("node last operation = %+v", node.LastOperation)
	}

	if got := len(c.Policy().Policy.Rules()); got != 5 || c.Policy().Version != 2 {
		t.Errorf("policy has %d rules at version %d, want 5 at version 2", got, c.Policy().Version)
	}

	presets := c.Presets()
	if presets.Version != 2 || len(presets.Items) != 2 ||
		presets.Items["default"].LocalSources != authz.PresetLocalAllow ||
		presets.Items["sandboxed"].LocalSources != authz.PresetLocalDeny {
		t.Errorf("presets = %+v", presets)
	}

	inst := c.Instances()
	ep1, ep3 := mustEndpoint(t, ep1Text), mustEndpoint(t, ep3Text)
	if inst.Version != 4 || inst.Items["example"].Adapter != "example" || !inst.Items["example"].Enabled ||
		inst.Items["example"].Endpoints[ep3].BacklogLimit != 400 || inst.Items["example"].Endpoints[ep1].BacklogLimit != 50 {
		t.Errorf("instances = %+v", inst)
	}

	ids := c.EndpointIDs("example")
	if ids.Runtime != ep1 || ids.Direct["notes"] != mustEndpoint(t, ep2Text) {
		t.Errorf("endpoint ids = %+v", ids)
	}
	if got := c.EndpointIDs("nobody"); !got.Runtime.IsZero() || len(got.Direct) != 0 {
		t.Errorf("endpoint ids of an unknown instance = %+v, want none", got)
	}

	if b, ok := c.Backup(); !ok || b.ConfirmedAt.Year() != 2026 {
		t.Errorf("backup = %+v, %v", b, ok)
	}
	_, digest, _ := selfRevocation(t, 7)
	revs := c.Revocations()
	if len(revs) != 1 || revs[0].Digest() != digest {
		t.Errorf("revocations = %+v, want the one of digest %s", revs, digest)
	}
	if !c.RevocationSet().NodeRetired(revs[0].Node()) {
		t.Error("the revocation set does not hold the retired node")
	}

	ads := c.Adapters()
	if len(ads) != 1 || ads[0].ID != "example" || ads[0].Version != "1.0.0" || ads[0].Manifest.ID != "example" {
		t.Errorf("adapters = %+v", ads)
	}
	if got := len(c.LastOperations()); got != 1 {
		t.Errorf("last operations = %d, want the one of node.json", got)
	}
}

// Files that are absent and optional are an empty configuration, not an error:
// no endpoint ids, no confirmation, no revocations, no adapters.
func TestOptionalFilesMayBeAbsent(t *testing.T) {
	rg := newRig(t)
	tree := validTree(t)
	for p := range tree {
		if strings.HasPrefix(p, config.EndpointIDsDir) || strings.HasPrefix(p, config.RevocationsDir) ||
			strings.HasPrefix(p, config.AdaptersDir) || p == config.BackupPath {
			delete(tree, p)
		}
	}
	putTree(t, rg.mem, tree)
	c := rg.load()
	if _, ok := c.Backup(); ok {
		t.Error("a confirmation appeared from nowhere")
	}
	if len(c.Revocations()) != 0 || len(c.Adapters()) != 0 {
		t.Error("revocations or adapters appeared from nowhere")
	}
}
