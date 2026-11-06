package setup

import "context"

// The names setup gives what it installs in Claude Code. The server's name decides
// the tool names a session sees (`mcp__sidera__send`) and so the permission rule; the
// plugin and its marketplace share the name, which makes the plugin `sidera@sidera`.
const (
	serverName      = "sidera"
	marketplaceName = "sidera"
	pluginName      = "sidera"
	pluginID        = pluginName + "@" + marketplaceName
	permissionRule  = "mcp__sidera"
)

// MCPServer is a registration of the `sidera` MCP server in the user's configuration of
// Claude Code, as `claude mcp get` prints it. Args is one string, the arguments joined
// by spaces, because that is all the command says; two registrations are compared by
// Command and Args as printed.
type MCPServer struct {
	Command string
	Args    string
}

// Marketplace is a configured plugin marketplace. Path is its directory, empty for a
// marketplace that is not a local directory.
type Marketplace struct {
	Name string
	Path string
}

// Plugin is an installed plugin of the user's scope.
type Plugin struct {
	ID string
	// Version is the version of the installed copy.
	Version string
	// SourceVersion is the version the plugin's source directory holds now, as Claude
	// Code reads it; empty when Claude Code does not say. An installed copy that is
	// behind it is stale whatever setup recorded.
	SourceVersion string
	Enabled       bool
	// Errors are the problems Claude Code lists for the plugin, such as a marketplace
	// whose source directory is gone, which makes the plugin vanish from new sessions
	// without a word.
	Errors []string
}

// Client is Claude Code, as far as setup uses it. A method that changes something
// returns an error that says why Claude Code refused. The calls always concern the
// user's scope and the names in this package.
type Client interface {
	// MCPServer returns the registration of the sidera server, nil when there is none.
	// A server of that name in another scope than the user's is an error: setup
	// registers in the user's scope and must not leave one that shadows it, or hide
	// behind one. Only what Claude Code resolves outside any project is seen: a
	// local-scope registration made inside a project wins in that project, and is not
	// seen from here.
	MCPServer(ctx context.Context) (*MCPServer, error)
	AddMCPServer(ctx context.Context, command string, args []string) error
	RemoveMCPServer(ctx context.Context) error

	Marketplaces(ctx context.Context) ([]Marketplace, error)
	AddMarketplace(ctx context.Context, dir string) error
	UpdateMarketplace(ctx context.Context) error
	RemoveMarketplace(ctx context.Context) error

	// Plugins returns the plugins of the user's scope.
	Plugins(ctx context.Context) ([]Plugin, error)
	InstallPlugin(ctx context.Context) error
	UpdatePlugin(ctx context.Context) error
	UninstallPlugin(ctx context.Context) error
}
