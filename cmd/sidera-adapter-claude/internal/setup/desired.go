package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/aianyai/sidera/sdk/go/adaptersetup"
)

// The ids of the items, in the order a plan lists them and does them. Each item has
// a place in Claude Code: the plugin's source (the adapter's own file), the marketplace
// that points at it, the plugin installed from the marketplace, the registration of the
// MCP server, and the rule that lets a session call its tools.
const (
	idSource      = "plugin_source"
	idMarketplace = "plugin_marketplace"
	idPlugin      = "plugin"
	idMCP         = "mcp_server"
	idPermission  = "permission"
)

var planOrder = []string{idSource, idMarketplace, idPlugin, idMCP, idPermission}

// removeOrder is the order setup is undone in: the plugin before the marketplace it
// came from, the marketplace before the source it points at.
var removeOrder = []string{idPlugin, idMarketplace, idSource, idMCP, idPermission}

// desired is what this version of the adapter wants for one request: the files, the
// command Claude Code is to start for the server, and the digest of each item.
type desired struct {
	req adaptersetup.Request
	// config is the Claude Code configuration the request acts on.
	config   configuration
	version  string
	srcDir   string
	files    []sourceFile
	command  string
	args     []string
	settings string
	digest   map[string]string
	// programErr says why the sidera program cannot be found. The desire is still made,
	// with the configured value as the command, so that a check can say so; a plan
	// refuses, and a refresh does not register a program that cannot be found.
	programErr error
}

// places are the paths of a request that need no knowledge of the program: enough to
// undo what was done.
func (h *Handler) places(req adaptersetup.Request, cfg configuration) *desired {
	return &desired{req: req, config: cfg, srcDir: sourceDir(req.StateDir), settings: cfg.settings()}
}

func (h *Handler) desire(req adaptersetup.Request, cfg configuration) (*desired, error) {
	files, err := sourceFiles(h.o.Version)
	if err != nil {
		return nil, err
	}
	command, programErr := h.program(req)
	args := []string{"mcp"}
	if !req.HomeIsDefault {
		args = []string{"--home", req.Home, "mcp"}
	}
	d := h.places(req, cfg)
	d.version, d.files, d.command, d.args, d.programErr = h.o.Version, files, command, args, programErr
	d.digest = map[string]string{
		idSource:      digestOfSource(files),
		idMarketplace: adaptersetup.Digest([]byte(d.srcDir)),
		idPlugin:      adaptersetup.Digest([]byte(h.o.Version)),
		idMCP:         digestOfServer(MCPServer{Command: command, Args: strings.Join(args, " ")}),
		idPermission:  adaptersetup.Digest([]byte(permissionRule)),
	}
	return d, nil
}

func digestOfServer(s MCPServer) string {
	return adaptersetup.Digest([]byte(s.Command + "\n" + s.Args))
}

// program is the sidera program the server is registered with: the instance's
// sidera_path when the owner set one (so that a session an adapter starts and one a
// person opens run the same program), else the program that ran setup. A sidera_path
// that cannot be found is returned as configured, with the reason.
func (h *Handler) program(req adaptersetup.Request) (string, error) {
	var configured string
	if raw, ok := req.Config["sidera_path"]; ok {
		if err := json.Unmarshal(raw, &configured); err != nil {
			return "", fmt.Errorf("sidera_path of the instance is not a string: %w", err)
		}
	}
	if configured != "" {
		path, err := h.o.LookPath(configured)
		if err != nil {
			return configured, fmt.Errorf("sidera_path %q of the instance: %w", configured, err)
		}
		path, err = filepath.Abs(path)
		if err != nil {
			return configured, fmt.Errorf("sidera_path %q of the instance: %w", configured, err)
		}
		return path, nil
	}
	if req.SideraProgram == "" {
		return "", errors.New("the request names no sidera program")
	}
	return req.SideraProgram, nil
}

func (d *desired) kind(id string) string {
	return map[string]string{idSource: "plugin_source", idMarketplace: "marketplace", idPlugin: "plugin", idMCP: "mcp_server", idPermission: "permission_rule"}[id]
}

func (d *desired) target(id string) string {
	switch id {
	case idSource:
		return d.srcDir
	case idMarketplace:
		return d.config.target(marketplaceName)
	case idPlugin:
		return d.config.target(pluginID)
	case idMCP:
		return d.config.target(serverName + " (user scope)")
	default:
		return d.config.target(d.settings)
	}
}

// seen is what looking at one item in Claude Code (or in the adapter's own files)
// found. digest is the Digest of what is there in the form the item's wanted digest
// has; err says it could not be looked at, and the item is then neither planned nor
// reconciled, only reported.
type seen struct {
	digest string
	exists bool
	err    error
	// unseen says the item was not looked at because Claude Code cannot be run: it is
	// neither there nor gone, and it is reported once, as the client.
	unseen bool
}

// observation is the state of every item, looked at once for a request.
type observation struct {
	// client is why Claude Code cannot be run at all.
	client error
	items  map[string]seen
	// plugin is the installed plugin, nil when it is not.
	plugin *Plugin
}

// observe looks at every item. client is nil when Claude Code cannot be reached, with
// clientErr saying why; the items that live in Claude Code are then left unseen.
func (d *desired) observe(ctx context.Context, client Client, clientErr error) observation {
	obs := observation{client: clientErr, items: map[string]seen{}}

	digest, exists, err := readSource(d.srcDir, d.files)
	obs.items[idSource] = seen{digest: digest, exists: exists, err: err}
	obs.items[idPermission] = d.observePermission()
	if client == nil {
		for _, id := range []string{idMarketplace, idPlugin, idMCP} {
			obs.items[id] = seen{unseen: true}
		}
		return obs
	}

	marketplaces, err := client.Marketplaces(ctx)
	var notStarted *adaptersetup.NotStartedError
	if errors.As(err, &notStarted) {
		obs.client = fmt.Errorf("Claude Code cannot be run: %w", err)
		for _, id := range []string{idMarketplace, idPlugin, idMCP} {
			obs.items[id] = seen{unseen: true}
		}
		return obs
	}
	obs.items[idMarketplace] = d.observeMarketplace(marketplaces, err)

	plugins, err := client.Plugins(ctx)
	s := seen{err: err}
	for i := range plugins {
		if plugins[i].ID == pluginID {
			obs.plugin = &plugins[i]
			s = seen{digest: adaptersetup.Digest([]byte(plugins[i].Version)), exists: true}
		}
	}
	obs.items[idPlugin] = s

	server, err := client.MCPServer(ctx)
	s = seen{err: err}
	if server != nil {
		s = seen{digest: digestOfServer(*server), exists: true}
	}
	obs.items[idMCP] = s
	return obs
}

func (d *desired) observeMarketplace(list []Marketplace, err error) seen {
	if err != nil {
		return seen{err: err}
	}
	for _, m := range list {
		if m.Name != marketplaceName {
			continue
		}
		if !samePath(m.Path, d.srcDir) {
			return seen{err: fmt.Errorf("the marketplace %s of Claude Code points at %q, not at the plugin source %s; setup does not re-point a marketplace. If another instance of this adapter owns it, only that instance has the plugin; otherwise remove it with `claude plugin marketplace remove %s` and run setup again", marketplaceName, m.Path, d.srcDir, marketplaceName)}
		}
		return seen{digest: d.digest[idMarketplace], exists: true}
	}
	return seen{}
}

// samePath says whether two paths lead to the same place, links resolved where they exist.
func samePath(a, b string) bool {
	resolve := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			p = r
		}
		return filepath.Clean(p)
	}
	return a != "" && resolve(a) == resolve(b)
}

// observePermission looks for the rule in settings.json. The file is read as Claude
// Code reads it: a file that is not strictly valid JSON is ignored by Claude Code as
// a whole, so setup refuses to build on it.
func (d *desired) observePermission() seen {
	if d.settings == "" {
		return seen{err: errors.New("the configuration directory of Claude Code is not known (no CLAUDE_CONFIG_DIR and no home directory)")}
	}
	root, _, err := adaptersetup.ReadJSONFile(d.settings)
	if err != nil {
		return seen{err: settingsError(d.settings, err)}
	}
	has, err := root.HasString(permissionPath, permissionRule)
	if err != nil {
		return seen{err: settingsError(d.settings, err)}
	}
	if has {
		return seen{digest: d.digest[idPermission], exists: true}
	}
	return seen{}
}

var permissionPath = []string{"permissions", "allow"}

func settingsError(path string, err error) error {
	return fmt.Errorf("%s cannot be used: %w. Claude Code ignores a settings file it cannot read as a whole, so setup changes nothing in it; correct the file and run setup again", path, err)
}

// errs are the reasons items could not be looked at, for a plan, which cannot say
// what to do about them.
func (o observation) errs() error {
	var all []error
	if o.client != nil {
		all = append(all, o.client)
	}
	for _, id := range planOrder {
		if err := o.items[id].err; err != nil {
			all = append(all, err)
		}
	}
	return errors.Join(all...)
}

// programProblem says why the sidera program cannot be started, empty when it can or
// when it is a name left to the client's PATH.
func (d *desired) programProblem() string {
	if !filepath.IsAbs(d.command) {
		return ""
	}
	if _, err := os.Stat(d.command); err != nil {
		return fmt.Sprintf("the program %s that the sidera server is registered with does not exist; run setup again from the program that is installed", d.command)
	}
	return ""
}

func lookPath(file string) (string, error) { return exec.LookPath(file) }
