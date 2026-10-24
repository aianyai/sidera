package config_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/fsx"
)

const (
	initNode = `{
  "format": 1,
  "version": 1,
  "settings": {
    "reply_permission": true,
    "node_queue_limit": 10000,
    "disk_reserve_bytes": 268435456,
    "node_prompt_turns": 8,
    "archive_retention_days": 30,
    "log_total_bytes": 157286400,
    "endpoint_defaults": {
      "max_queue_s": 1800,
      "backlog_limit": 200
    }
  }
}
`
	initPresets = `{
  "format": 1,
  "version": 1,
  "presets": {
    "default": {
      "local_sources": "allow",
      "description": "Created by sidera init."
    }
  }
}
`
	initInstances = `{
  "format": 1,
  "version": 1,
  "instances": {}
}
`
)

// runInit is the init transaction as the initialisation entry composes it: the
// configuration side, and the rest of init, which is a single write here.
func runInit(rg *rig) error {
	plan, err := config.PlanInit(rg.w.Reader())
	if err != nil {
		return err
	}
	tx := rg.w.Begin(testInit)
	defer tx.Close()
	if err := plan.MakeDirs(tx); err != nil {
		return err
	}
	if err := plan.WriteFiles(tx); err != nil {
		return err
	}
	if err := tx.Replace("marker", "init-marker", []byte("init"), fsx.ModePrivate); err != nil {
		return err
	}
	return tx.Commit()
}

// On an empty data directory init makes the directories and the four files
// every node starts with, at version 1, with no operation recorded: the
// settings are the decided defaults, the policy is the default policy, and
// there is one preset, `default`, which local sources may use.
func TestInitWritesTheDefaults(t *testing.T) {
	rg := newRig(t)
	if err := runInit(rg); err != nil {
		t.Fatalf("init: %v", err)
	}
	got := snapshot(t, rg.mem)
	for p, want := range map[string]string{
		config.NodePath:      initNode,
		config.PresetsPath:   initPresets,
		config.InstancesPath: initInstances,
	} {
		if got[p] != want {
			t.Errorf("%s =\n%s\nwant\n%s", p, got[p], want)
		}
	}
	for _, d := range config.LayoutDirs() {
		if _, ok := got[d+"/"]; !ok {
			t.Errorf("directory %s was not made", d)
		}
	}

	c := rg.load()
	pol := c.Policy()
	if pol.Version != 1 || pol.LastOperation != nil || !reflect.DeepEqual(pol.Policy.Rules(), authz.DefaultPolicy().Rules()) {
		t.Errorf("policy = version %d with %d rules, want version 1 with the default policy", pol.Version, len(pol.Policy.Rules()))
	}
	presets := c.Presets()
	if len(presets.Items) != 1 || presets.Items["default"].LocalSources != authz.PresetLocalAllow {
		t.Errorf("presets = %+v, want only default, which allows local sources", presets)
	}
	if inst := c.Instances(); inst.Version != 1 || len(inst.Items) != 0 {
		t.Errorf("instances = %+v", inst)
	}
	if n := c.Node(); n.Version != 1 || n.LastOperation != nil {
		t.Errorf("node = %+v", n)
	}
}

// init never overwrites a file that exists and is valid: whatever the owner put
// there, the way they laid it out, stays byte for byte, and the files that are
// missing are written next to it.
func TestInitNeverOverwritesExistingConfig(t *testing.T) {
	customNode := strings.Replace(goldenNode, `"reply_permission": true`, `"reply_permission": false`, 1)
	handPresets := `{"format":1,"version":7,"presets":{"mine":{"local_sources":"deny"}}}`
	handInstances := `{ "format": 1, "version": 9, "instances": { } }`
	handPolicy := `{"format":1,"version":3,"rules":[]}`
	existing := map[string]string{
		config.NodePath:      customNode,
		config.PolicyPath:    handPolicy,
		config.PresetsPath:   handPresets,
		config.InstancesPath: handInstances,
	}

	for path, content := range existing {
		t.Run("only "+path+" exists", func(t *testing.T) {
			rg := newRig(t)
			putTree(t, rg.mem, map[string]string{path: content})
			if err := runInit(rg); err != nil {
				t.Fatalf("init: %v", err)
			}
			snap := snapshot(t, rg.mem)
			if snap[path] != content {
				t.Errorf("%s was rewritten:\n%s\nwant\n%s", path, snap[path], content)
			}
			for _, other := range []string{config.NodePath, config.PolicyPath, config.PresetsPath, config.InstancesPath} {
				if _, ok := snap[other]; !ok {
					t.Errorf("%s was not written", other)
				}
			}
			rg.load()
		})
	}
	t.Run("all four exist", func(t *testing.T) {
		rg := newRig(t)
		putTree(t, rg.mem, existing)
		before := snapshot(t, rg.mem)
		if err := runInit(rg); err != nil {
			t.Fatalf("init: %v", err)
		}
		after := snapshot(t, rg.mem)
		delete(after, "init-marker")
		if !reflect.DeepEqual(before, after) {
			t.Errorf("init changed an existing configuration: %v", diffKeys(before, after))
		}
	})
	t.Run("files of the rest of config/ are not touched", func(t *testing.T) {
		rg := newRig(t)
		putTree(t, rg.mem, validTree(t))
		before := snapshot(t, rg.mem)
		if err := runInit(rg); err != nil {
			t.Fatalf("init: %v", err)
		}
		after := snapshot(t, rg.mem)
		delete(after, "init-marker")
		if !reflect.DeepEqual(before, after) {
			t.Errorf("init changed a configuration that was complete: %v", diffKeys(before, after))
		}
	})
	t.Run("running init twice", func(t *testing.T) {
		rg := newRig(t)
		if err := runInit(rg); err != nil {
			t.Fatal(err)
		}
		first := snapshot(t, rg.mem)
		if err := runInit(rg); err != nil {
			t.Fatalf("second init: %v", err)
		}
		if second := snapshot(t, rg.mem); !reflect.DeepEqual(first, second) {
			t.Errorf("the second init changed %v", diffKeys(first, second))
		}
	})
}

// A file that exists and is not valid is not replaced: init refuses and names
// it, so the owner decides what becomes of what they wrote. Nothing is written,
// not even the files that are missing.
func TestInitRefusesAnInvalidExistingFileNamingIt(t *testing.T) {
	rows := []struct {
		name string
		path string
		doc  string
	}{
		{"node not json", config.NodePath, "{"},
		{"node with an unknown setting", config.NodePath, strings.Replace(goldenNode, `"log_total_bytes"`, `"log_bytes"`, 1)},
		{"node with an invalid value", config.NodePath, strings.Replace(goldenNode, `"node_prompt_turns": 8`, `"node_prompt_turns": 0`, 1)},
		{"policy not json", config.PolicyPath, "["},
		{"policy with a rule authz refuses", config.PolicyPath, strings.Replace(goldenPolicy, `"action": "see"`, `"action": "relay.reserve"`, 1)},
		{"presets without the statement", config.PresetsPath, `{"format":1,"version":1,"presets":{"x":{}}}`},
		{"instances not json", config.InstancesPath, "nope"},
		{"an empty file", config.NodePath, ""},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			rg := newRig(t)
			putTree(t, rg.mem, map[string]string{r.path: r.doc})
			before := snapshot(t, rg.mem)
			err := runInit(rg)
			if err == nil {
				t.Fatal("init accepted an invalid file")
			}
			if fe := fileError(t, err); fe.Path != r.path {
				t.Errorf("init names %q, want %q: %v", fe.Path, r.path, err)
			}
			if !strings.Contains(err.Error(), r.path) {
				t.Errorf("the message does not name the file: %v", err)
			}
			if after := snapshot(t, rg.mem); !reflect.DeepEqual(before, after) {
				t.Errorf("init wrote before it refused: %v", diffKeys(before, after))
			}
		})
	}
}

// Directories that init made and the owner removed, or that a crash left
// uncreated, are made again, and the ones that exist are left alone.
func TestInitMakesTheMissingDirectoriesOnly(t *testing.T) {
	rg := newRig(t)
	mkdirAll(t, rg.mem, config.Dir)
	mkdirAll(t, rg.mem, config.AdaptersDir)
	if err := fsx.Replace(rg.mem, setupTag, config.AdaptersDir+"/keep", []byte("k"), fsx.ModePrivate); err != nil {
		t.Fatal(err)
	}
	if err := runInit(rg); err != nil {
		t.Fatalf("init: %v", err)
	}
	snap := snapshot(t, rg.mem)
	for _, d := range config.LayoutDirs() {
		if _, ok := snap[d+"/"]; !ok {
			t.Errorf("directory %s is missing", d)
		}
	}
	if snap[config.AdaptersDir+"/keep"] != "k" {
		t.Error("a file in an existing directory was disturbed")
	}
}
