package setup

import (
	"context"
	"errors"
	"strings"
)

// fakeClaude is Claude Code in memory, with the behaviour of the real one that setup
// depends on: an existing server is not added over, a plugin installs only from a
// marketplace that is there, installing re-enables, and what is not there is an error
// to remove. calls lists what setup asked, in order. fail maps a call name to the
// error it returns.
type fakeClaude struct {
	server       *MCPServer
	marketplaces []Marketplace
	plugin       *Plugin
	// version is what the plugin source holds, read when a plugin is installed or updated.
	version func() string
	calls   []string
	fail    map[string]error
}

func (f *fakeClaude) do(name string) error {
	f.calls = append(f.calls, name)
	return f.fail[name]
}

func (f *fakeClaude) MCPServer(context.Context) (*MCPServer, error) {
	if err := f.do("mcp get"); err != nil {
		return nil, err
	}
	if f.server == nil {
		return nil, nil
	}
	s := *f.server
	return &s, nil
}

func (f *fakeClaude) AddMCPServer(_ context.Context, command string, args []string) error {
	if err := f.do("mcp add"); err != nil {
		return err
	}
	if f.server != nil {
		return errors.New("MCP server sidera already exists in user config")
	}
	f.server = &MCPServer{Command: command, Args: strings.Join(args, " ")}
	return nil
}

func (f *fakeClaude) RemoveMCPServer(context.Context) error {
	if err := f.do("mcp remove"); err != nil {
		return err
	}
	if f.server == nil {
		return errors.New("No MCP server named sidera in user scope")
	}
	f.server = nil
	return nil
}

func (f *fakeClaude) Marketplaces(context.Context) ([]Marketplace, error) {
	if err := f.do("marketplace list"); err != nil {
		return nil, err
	}
	return append([]Marketplace(nil), f.marketplaces...), nil
}

func (f *fakeClaude) AddMarketplace(_ context.Context, dir string) error {
	if err := f.do("marketplace add"); err != nil {
		return err
	}
	f.marketplaces = append(f.marketplaces, Marketplace{Name: marketplaceName, Path: dir})
	return nil
}

func (f *fakeClaude) UpdateMarketplace(context.Context) error { return f.do("marketplace update") }

func (f *fakeClaude) RemoveMarketplace(context.Context) error {
	if err := f.do("marketplace remove"); err != nil {
		return err
	}
	f.marketplaces = nil
	return nil
}

func (f *fakeClaude) Plugins(context.Context) ([]Plugin, error) {
	if err := f.do("plugin list"); err != nil {
		return nil, err
	}
	if f.plugin == nil {
		return nil, nil
	}
	return []Plugin{*f.plugin}, nil
}

func (f *fakeClaude) InstallPlugin(context.Context) error {
	if err := f.do("plugin install"); err != nil {
		return err
	}
	if len(f.marketplaces) == 0 {
		return errors.New(`Plugin "sidera" not found in marketplace "sidera"`)
	}
	f.plugin = &Plugin{ID: pluginID, Version: f.version(), Enabled: true}
	return nil
}

func (f *fakeClaude) UpdatePlugin(context.Context) error {
	if err := f.do("plugin update"); err != nil {
		return err
	}
	f.plugin.Version = f.version()
	return nil
}

func (f *fakeClaude) UninstallPlugin(context.Context) error {
	if err := f.do("plugin uninstall"); err != nil {
		return err
	}
	f.plugin = nil
	return nil
}
