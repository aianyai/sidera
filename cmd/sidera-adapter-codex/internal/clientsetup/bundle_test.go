package clientsetup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestRenderedPluginLayout(t *testing.T) {
	b := render("1.2.3", serverDef{Command: `C:\Users\me\sidera.exe`, Args: []string{"--home", `C:\data`, "mcp"}}, []byte("skill"))
	want(t, "files", sortedKeys(b), []string{marketplaceFile, pluginManifest, pluginServers, pluginSkill})

	var mp struct {
		Name    string
		Plugins []struct {
			Name     string
			Source   struct{ Source, Path string }
			Policy   struct{ Installation, Authentication string }
			Category string
		}
	}
	mustJSON(t, b[marketplaceFile], &mp)
	if mp.Name != "sidera" || len(mp.Plugins) != 1 || mp.Plugins[0].Source.Path != "./plugins/sidera" ||
		mp.Plugins[0].Policy.Installation == "" || mp.Plugins[0].Policy.Authentication == "" || mp.Plugins[0].Category == "" {
		t.Errorf("marketplace.json = %s", b[marketplaceFile])
	}
	var pm struct{ Name, Version string }
	mustJSON(t, b[pluginManifest], &pm)
	if pm.Name != "sidera" || pm.Version != "1.2.3" {
		t.Errorf("plugin.json = %s", b[pluginManifest])
	}
	// The program is data: a Windows path survives the encoder, and nothing is escaped
	// for HTML.
	var servers struct {
		MCPServers map[string]struct {
			Command string
			Args    []string
			Approve string `json:"default_tools_approval_mode"`
		} `json:"mcpServers"`
	}
	mustJSON(t, b[pluginServers], &servers)
	s := servers.MCPServers["sidera"]
	if s.Command != `C:\Users\me\sidera.exe` || !slices.Equal(s.Args, []string{"--home", `C:\data`, "mcp"}) || s.Approve != "approve" {
		t.Errorf(".mcp.json = %s", b[pluginServers])
	}
	if string(b[pluginSkill]) != "skill" {
		t.Errorf("SKILL.md = %q", b[pluginSkill])
	}
}

func sortedKeys(b bundle) []string {
	var keys []string
	for k := range b {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func mustJSON(t *testing.T, data []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("%s: %v", data, err)
	}
}

// The digest of the plugin is the digest of what Codex copies, and it moves with every
// byte of it and nothing else.
func TestDigests(t *testing.T) {
	def := serverDef{Command: "/bin/sidera", Args: []string{"mcp"}}
	base := render("1", def, []byte("skill"))
	cases := []struct {
		name         string
		other        bundle
		sameSource   bool
		samePlugin   bool
		wantDistinct bool
	}{
		{"the same", render("1", def, []byte("skill")), true, true, false},
		{"another version", render("2", def, []byte("skill")), false, false, true},
		{"another skill", render("1", def, []byte("skill 2")), false, false, true},
		{"another program", render("1", serverDef{Command: "/usr/bin/sidera", Args: []string{"mcp"}}, []byte("skill")), false, false, true},
		{"another home", render("1", serverDef{Command: "/bin/sidera", Args: []string{"--home", "/h", "mcp"}}, []byte("skill")), false, false, true},
	}
	for _, c := range cases {
		if got := c.other.digest() == base.digest(); got != c.sameSource {
			t.Errorf("%s: same source digest = %v", c.name, got)
		}
		if got := c.other.pluginDigest() == base.pluginDigest(); got != c.samePlugin {
			t.Errorf("%s: same plugin digest = %v", c.name, got)
		}
	}
	// The marketplace manifest is not part of the plugin Codex copies.
	changed := render("1", def, []byte("skill"))
	changed[marketplaceFile] = []byte("{}")
	if changed.pluginDigest() != base.pluginDigest() || changed.digest() == base.digest() {
		t.Error("the marketplace manifest must move the source digest only")
	}
	// A file moved to another path is another content, though the bytes are the same.
	a, b := bundle{"a": []byte("x")}, bundle{"b": []byte("x")}
	if a.digest() == b.digest() {
		t.Error("the path is not part of the digest")
	}
}

func TestWriteBundleMakesTheDirectoryHoldExactlyTheBundle(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "setup", "codex", "marketplace")
	b := render("1", serverDef{Command: "/bin/sidera", Args: []string{"mcp"}}, []byte("skill"))
	if err := writeBundle(dir, b); err != nil {
		t.Fatal(err)
	}
	got, exists, err := readBundle(dir)
	if err != nil || !exists || got.digest() != b.digest() {
		t.Fatalf("read back: exists %v err %v", exists, err)
	}
	// Something that is not ours is removed, and a changed file is written again.
	os.WriteFile(filepath.Join(dir, "stray"), []byte("x"), 0o600)
	os.WriteFile(filepath.Join(dir, filepath.FromSlash(pluginSkill)), []byte("edited"), 0o600)
	if got, _, _ := readBundle(dir); got.digest() == b.digest() {
		t.Fatal("the change is not seen")
	}
	if err := writeBundle(dir, b); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := readBundle(dir); got.digest() != b.digest() {
		t.Errorf("files = %v", sortedKeys(got))
	}
	if _, exists, _ := readBundle(filepath.Join(dir, "nowhere")); exists {
		t.Error("a missing directory exists")
	}
}

func TestOwnerOnlyAccess(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("modes have no effect on Windows")
	}
	dir := filepath.Join(t.TempDir(), "setup", "codex", "marketplace")
	if err := writeBundle(dir, render("1", serverDef{Command: "/bin/sidera"}, []byte("s"))); err != nil {
		t.Fatal(err)
	}
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		info, _ := d.Info()
		if info.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s has mode %v", path, info.Mode())
		}
		return nil
	})
}
