package clientsetup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/aianyai/sidera/sdk/go/adaptersetup"
)

// The files of the marketplace setup writes for Codex, relative to its root. The plugin
// is in the legacy layout (".codex-plugin", ".mcp.json"), the only one that can name an
// absolute program and state that its tools are approved in advance.
const (
	marketplaceFile = ".agents/plugins/marketplace.json"
	pluginRoot      = "plugins/sidera"
	pluginManifest  = pluginRoot + "/.codex-plugin/plugin.json"
	pluginServers   = pluginRoot + "/.mcp.json"
	pluginSkill     = pluginRoot + "/skills/sidera/SKILL.md"
)

// serverDef is how the plugin starts the MCP server.
type serverDef struct {
	Command string
	Args    []string
}

// matches says whether the server Codex offers is the plugin's: same program, same
// arguments and approved in advance. A server of a person's own with the same name
// that differs in any of them replaces the plugin's.
func (d serverDef) matches(s server) bool {
	return s.Found && s.Enabled && s.Command == d.Command && slices.Equal(s.Args, d.Args) && s.Approval == "approve"
}

// bundle is the content of the marketplace directory: relative slash-separated path to
// file content.
type bundle map[string][]byte

func encodeJSON(v any) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		panic(err) // the values are maps of strings
	}
	return b.Bytes()
}

// render builds the marketplace for the adapter version, the way the plugin starts
// the server and the Skill text.
func render(version string, def serverDef, skill []byte) bundle {
	args := def.Args
	if args == nil {
		args = []string{}
	}
	return bundle{
		marketplaceFile: encodeJSON(map[string]any{
			"name": marketplaceName,
			"plugins": []any{map[string]any{
				"name":     pluginName,
				"source":   map[string]any{"source": "local", "path": "./" + pluginRoot},
				"policy":   map[string]any{"installation": "AVAILABLE", "authentication": "ON_INSTALL"},
				"category": "Productivity",
			}},
		}),
		pluginManifest: encodeJSON(map[string]any{
			"name":        pluginName,
			"version":     version,
			"description": "Sidera, the message channel to the other agents and people of your cluster.",
		}),
		pluginServers: encodeJSON(map[string]any{
			"mcpServers": map[string]any{
				serverName: map[string]any{
					"command":                     def.Command,
					"args":                        args,
					"default_tools_approval_mode": "approve",
				},
			},
		}),
		pluginSkill: skill,
	}
}

// digest is the text a record keeps of the content: it depends on every path and every
// byte, and on nothing else (not the order, the modes or the times).
func (b bundle) digest() string { return b.digestUnder("") }

// pluginDigest is the digest of the plugin's own files, the ones Codex copies.
func (b bundle) pluginDigest() string { return b.digestUnder(pluginRoot + "/") }

func (b bundle) digestUnder(prefix string) string {
	var paths []string
	for p := range b {
		if strings.HasPrefix(p, prefix) {
			paths = append(paths, p)
		}
	}
	slices.Sort(paths)
	var all bytes.Buffer
	for _, p := range paths {
		fmt.Fprintf(&all, "%s\x00%d\x00", p, len(b[p]))
		all.Write(b[p])
	}
	return adaptersetup.Digest(all.Bytes())
}

// readBundle reads the directory as a bundle; exists is false when it is not there.
// Anything but a regular file counts by its name alone, so that it makes the digest differ
// instead of being followed.
func readBundle(dir string) (b bundle, exists bool, err error) {
	if _, err := os.Lstat(dir); os.IsNotExist(err) {
		return nil, false, nil
	}
	b = bundle{}
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil || rel == "." || d.IsDir() {
			return err
		}
		key := filepath.ToSlash(rel)
		if !d.Type().IsRegular() {
			b[key+"\x00not a regular file"] = nil
			return nil
		}
		data, err := os.ReadFile(path)
		b[key] = data
		return err
	})
	if err != nil {
		return nil, false, err
	}
	return b, true, nil
}

// writeBundle makes the directory hold exactly the bundle: each file is written for the
// owner alone, and a file or directory that is not part of it is deleted.
func writeBundle(dir string, b bundle) error {
	for p, data := range b {
		if err := adaptersetup.WritePrivateFile(filepath.Join(dir, filepath.FromSlash(p)), data); err != nil {
			return err
		}
	}
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil || rel == "." {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if _, wanted := b[filepath.ToSlash(rel)]; !wanted {
			return os.Remove(path)
		}
		return nil
	})
}
