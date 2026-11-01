package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/claudeargs"
	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/runtime"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/manifest"
)

// The manifest built into the executable is the one the daemon installs the
// package by, and the adapter declares what the manifest says it provides.
func TestManifestAndDeclarationAgree(t *testing.T) {
	m, err := loadManifest()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config(runtime.New(runtime.Options{}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := (adapterproto.InitializeResult{ProtocolVersion: adapterproto.ProtocolMin, Adapter: cfg.Adapter, Capabilities: cfg.Capabilities}).Validate(); err != nil {
		t.Fatalf("the declaration is not a valid initialize result: %v", err)
	}
	if cfg.Adapter.ID != m.ID || cfg.Adapter.Version != m.Version {
		t.Fatalf("identity %+v, manifest %s %s", cfg.Adapter, m.ID, m.Version)
	}
	var declared []string
	for _, f := range cfg.Capabilities.Runtime.Frontends {
		declared = append(declared, string(f))
	}
	for _, b := range m.Budgets {
		if !slices.Contains(declared, string(b.Frontend)) {
			t.Errorf("a budget for the undeclared front end %s", b.Frontend)
		}
	}
	for _, l := range m.RecvWaitLimits {
		if !slices.Contains(declared, string(l.Frontend)) {
			t.Errorf("a receive wait for the undeclared front end %s", l.Frontend)
		}
	}
	// The keys the adapter reads are the keys the manifest lets the owner write.
	var presetKeys []string
	for _, f := range m.Config.Preset {
		presetKeys = append(presetKeys, f.Key)
	}
	slices.Sort(presetKeys)
	if want := []string{"allowed_tools", "append_system_prompt", "background_wait_ms", "effort", "model", "permission_mode", "project_dir"}; !slices.Equal(presetKeys, want) {
		t.Fatalf("preset keys %v, the adapter reads %v", presetKeys, want)
	}
	for _, f := range m.Config.Preset {
		switch f.Key {
		case "permission_mode":
			if !slices.Equal(f.Options, claudeargs.PermissionModes) {
				t.Errorf("permission modes: manifest %v, adapter %v", f.Options, claudeargs.PermissionModes)
			}
		case "effort":
			if !slices.Equal(f.Options, claudeargs.Efforts) {
				t.Errorf("efforts: manifest %v, adapter %v", f.Options, claudeargs.Efforts)
			}
		}
	}
	var instanceKeys []string
	for _, f := range m.Config.Instance {
		instanceKeys = append(instanceKeys, f.Key)
	}
	if !slices.Equal(instanceKeys, []string{"claude_path", "sidera_path"}) {
		t.Fatalf("instance keys %v", instanceKeys)
	}
}

// The package written for a platform the manifest lists is one the daemon reads, with
// that platform alone and the binary at the entry; a platform it does not list gets
// no package.
func TestPackageIsWrittenOnlyForAListedPlatform(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "built")
	if err := os.WriteFile(bin, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range [][2]string{{"darwin", "arm64"}, {"darwin", "amd64"}, {"linux", "amd64"}, {"windows", "amd64"}} {
		t.Run(p[0]+"/"+p[1], func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "pkg")
			err := writePackage(out, bin, p[0], p[1])
			if p[0] != "darwin" {
				if err == nil {
					t.Fatal("a package was written for a platform the manifest does not list")
				}
				if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
					t.Errorf("something was written: %v", statErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Join(out, manifest.FileName))
			if err != nil {
				t.Fatal(err)
			}
			m, err := manifest.Parse(raw)
			if err != nil {
				t.Fatalf("the manifest is refused: %v", err)
			}
			if m.ID != "claude" || len(m.Platforms) != 1 || m.Platforms[0] != (manifest.Platform{OS: p[0], Arch: p[1]}) {
				t.Errorf("manifest %+v", m)
			}
			if got, err := os.ReadFile(filepath.Join(out, "bin", "sidera-adapter-claude")); err != nil || string(got) != "binary" {
				t.Errorf("binary %q, %v", got, err)
			}
		})
	}
}
