package clientsetup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
)

// fakeCodex behaves as the installed Codex does for the commands setup uses, as far as
// they were observed: a marketplace is a directory whose manifest is read when it is
// listed, a plugin is copied from it when it is added, and the server Codex offers is a
// person's own entry or, failing that, the installed and enabled plugin's.
type fakeCodex struct {
	marketplaces map[string]string // name to root
	installed    bool
	enabled      bool
	version      string
	copied       bundle // what `plugin add` copied
	user         *server
	// userStays makes a server of the person's survive `mcp remove`, as one that comes
	// from a project's or a managed configuration does.
	userStays bool
	// noServer makes a plugin install offer no server, as a Codex that dropped the layout.
	noServer bool
	// failing makes the named call fail: "add-marketplace", "add-plugin", "remove-plugin", ...
	failing map[string]bool
	// down makes every call fail, as an uninstalled Codex.
	down  bool
	calls []string
}

func newFake() *fakeCodex {
	return &fakeCodex{marketplaces: map[string]string{}, failing: map[string]bool{}}
}

var errDown = errors.New("codex: executable file not found in $PATH")

func (f *fakeCodex) call(name string) error {
	f.calls = append(f.calls, name)
	if f.down {
		return errDown
	}
	if f.failing[name] {
		return errors.New("scripted failure of " + name)
	}
	return nil
}

func (f *fakeCodex) called(name string) int {
	n := 0
	for _, c := range f.calls {
		if c == name {
			n++
		}
	}
	return n
}

// unreadable is what Codex does when configured marketplaces have no manifest: every
// listing of marketplaces and plugins fails, with a line for each marketplace it cannot
// read and the directory it names for that one alone. The error is made from the text
// Codex writes, by the parser the command line uses.
func (f *fakeCodex) unreadable() error {
	var names []string
	for name, root := range f.marketplaces {
		if _, err := os.Stat(filepath.Join(root, marketplaceFile)); err != nil {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	slices.Sort(names)
	stderr := "Error: failed to load marketplace(s):\n"
	for _, name := range names {
		stderr += "- `" + name + "` at " + f.marketplaces[name] + ": " + unreadableText + "\n"
	}
	return parseUnreadable(stderr, "codex plugin list failed (exit 1): "+stderr)
}

func (f *fakeCodex) Plugin(context.Context) (plugin, error) {
	if err := f.call("plugin-list"); err != nil {
		return plugin{}, err
	}
	if err := f.unreadable(); err != nil {
		return plugin{}, err
	}
	return plugin{Installed: f.installed, Enabled: f.installed && f.enabled, Version: f.version}, nil
}

func (f *fakeCodex) Marketplace(_ context.Context, name string) (marketplace, error) {
	if err := f.call("marketplace-list"); err != nil {
		return marketplace{}, err
	}
	if err := f.unreadable(); err != nil {
		return marketplace{}, err
	}
	root, ok := f.marketplaces[name]
	return marketplace{Registered: ok, Root: root}, nil
}

func (f *fakeCodex) AddMarketplace(_ context.Context, dir string) error {
	if err := f.call("add-marketplace"); err != nil {
		return err
	}
	f.marketplaces[marketplaceName] = dir
	return nil
}

func (f *fakeCodex) RemoveMarketplace(_ context.Context, name string) error {
	if err := f.call("remove-marketplace"); err != nil {
		return err
	}
	delete(f.marketplaces, name)
	return nil
}

func (f *fakeCodex) AddPlugin(context.Context, string) error {
	if err := f.call("add-plugin"); err != nil {
		return err
	}
	root, ok := f.marketplaces[marketplaceName]
	if !ok {
		return errors.New("marketplace not configured")
	}
	b, _, err := readBundle(filepath.Join(root, filepath.FromSlash(pluginRoot)))
	if err != nil {
		return err
	}
	f.copied = bundle{}
	for p, data := range b {
		f.copied[pluginRoot+"/"+p] = data
	}
	var m struct{ Version string }
	if err := json.Unmarshal(f.copied[pluginManifest], &m); err != nil {
		return err
	}
	f.installed, f.enabled, f.version = true, true, m.Version
	return nil
}

func (f *fakeCodex) RemovePlugin(context.Context, string) error {
	if err := f.call("remove-plugin"); err != nil {
		return err
	}
	f.installed, f.copied = false, nil
	return nil
}

// pluginServer is the server in the installed plugin's .mcp.json.
func (f *fakeCodex) pluginServer() server {
	var m struct {
		MCPServers map[string]struct {
			Command string
			Args    []string
			Approve string `json:"default_tools_approval_mode"`
		} `json:"mcpServers"`
	}
	if f.noServer || !f.installed || !f.enabled || json.Unmarshal(f.copied[pluginServers], &m) != nil {
		return server{}
	}
	s := m.MCPServers[serverName]
	return server{Found: true, Enabled: true, Command: s.Command, Args: s.Args, Approval: s.Approve}
}

func (f *fakeCodex) Server(context.Context, string) (server, error) {
	if err := f.call("server"); err != nil {
		return server{}, err
	}
	if f.user != nil {
		return *f.user, nil
	}
	return f.pluginServer(), nil
}

func (f *fakeCodex) RemoveServer(context.Context, string) (string, error) {
	if err := f.call("remove-server"); err != nil {
		return "", err
	}
	if f.user == nil {
		return "No MCP server named 'sidera' found.", nil
	}
	if !f.userStays {
		f.user = nil
	}
	return "Removed global MCP server 'sidera'.", nil
}

var _ client = (*fakeCodex)(nil)
