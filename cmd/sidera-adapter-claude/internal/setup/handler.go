package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/claudeargs"
	"github.com/aianyai/sidera/sdk/go/adaptersetup"
)

// Options are what a Handler needs from the world.
type Options struct {
	// Version is the adapter's version: the plugin's version, and what the record keeps.
	Version string
	// Environ is the environment of the setup program. It names Claude Code's
	// configuration directory (CLAUDE_CONFIG_DIR), and Claude Code is run in it, without
	// what would make it behave as the session that started setup.
	Environ []string
	// Home is the user's home directory, where Claude Code's default configuration is.
	Home string
	// NewClient returns Claude Code for a request, to be run with the environment env;
	// the default runs the claude command the instance names (claude_path), else the one
	// on PATH.
	NewClient func(req adaptersetup.Request, env []string) (Client, error)
	// LookPath finds an executable by name; the default is exec.LookPath.
	LookPath func(file string) (string, error)
}

// Handler is the setup of the Claude adapter, an adaptersetup.Handler.
type Handler struct{ o Options }

// New returns the handler for the options.
func New(o Options) *Handler {
	if o.LookPath == nil {
		o.LookPath = lookPath
	}
	h := &Handler{o: o}
	if h.o.NewClient == nil {
		h.o.NewClient = h.defaultClient
	}
	return h
}

func (h *Handler) defaultClient(req adaptersetup.Request, env []string) (Client, error) {
	name := "claude"
	if raw, ok := req.Config["claude_path"]; ok {
		var configured string
		if err := json.Unmarshal(raw, &configured); err != nil {
			return nil, fmt.Errorf("claude_path of the instance is not a string: %w", err)
		}
		if configured != "" {
			name = configured
		}
	}
	path, err := h.o.LookPath(name)
	if err != nil {
		return nil, fmt.Errorf("Claude Code cannot be run: %w", err)
	}
	if path, err = filepath.Abs(path); err != nil {
		return nil, err
	}
	cli, err := NewCLI(path, req.StateDir, env)
	if err != nil {
		return nil, err
	}
	return cli, nil
}

// current is the configuration the environment of this run names.
func (h *Handler) current() configuration {
	return currentConfiguration(h.o.Environ, h.o.Home)
}

// client returns Claude Code for the configuration. nil, with the reason, when it cannot
// be run.
func (h *Handler) client(req adaptersetup.Request, cfg configuration) (Client, error) {
	client, err := h.o.NewClient(req, cfg.environ(claudeargs.SanitizeEnv(h.o.Environ)))
	if err != nil {
		return nil, err
	}
	return client, nil
}

// world is everything one request needs: what is wanted, Claude Code, what the
// record holds and what was seen.
type world struct {
	d      *desired
	client Client
	rec    *adaptersetup.Record
	obs    observation
	// current is the configuration this run's environment names; d.config is the one
	// the request acts on.
	current configuration
}

// look gathers a world. A plan and an apply act on the configuration the environment
// names, because that is what the person is setting up now; a check and a refresh (the
// recorded argument) act on the one the items were recorded in, whatever the
// environment says.
func (h *Handler) look(ctx context.Context, req adaptersetup.Request, recorded bool) (*world, error) {
	rec, err := adaptersetup.ReadRecord(req.StateDir)
	if err != nil {
		return nil, err
	}
	cfg := h.current()
	if recorded {
		cfg = actingConfiguration(rec, cfg)
	} else if err := refuseAnotherConfiguration(req, rec, cfg); err != nil {
		return nil, err
	}
	d, err := h.desire(req, cfg)
	if err != nil {
		return nil, err
	}
	client, clientErr := h.client(req, cfg)
	return &world{d: d, client: client, rec: rec, current: h.current(), obs: d.observe(ctx, client, clientErr)}, nil
}

// refuseAnotherConfiguration refuses a setup in a configuration other than the one its
// items were made in. Every configuration's marketplace points at the same source
// directory in the instance's state directory, so removing the setup of either would
// delete the source the other's plugin is loaded from, and the record keeps one
// configuration only. The way out is the removal, which works in the recorded
// configuration whatever the environment names.
func refuseAnotherConfiguration(req adaptersetup.Request, rec *adaptersetup.Record, cfg configuration) error {
	recorded := actingConfiguration(rec, cfg)
	if !rec.Installed() || recorded == cfg {
		return nil
	}
	return fmt.Errorf("setup already made this instance's items in the Claude Code configuration %s, and this environment names %s; both would share the plugin's source in the instance's state directory, so removing either would break the other. Run `sidera adapter setup %s --remove` first: it works in the recorded configuration whatever the environment names", recorded.text(), cfg.text(), req.Instance)
}

// actingConfiguration is the configuration the items of the record were made in, else
// the fallback.
func actingConfiguration(rec *adaptersetup.Record, fallback configuration) configuration {
	if rec.Installed() {
		for _, id := range planOrder {
			if id == idSource {
				continue // a path in the state directory, not in a configuration
			}
			if r, ok := rec.Find(id); ok {
				if cfg, ok := configurationOf(r.Target); ok {
					return cfg
				}
			}
		}
	}
	return fallback
}

// scopeNote says that a run acted on a configuration other than the one its environment
// names, empty when they are the same.
func (w *world) scopeNote() string {
	return differentConfiguration(w.d.config, w.current)
}

func differentConfiguration(acting, current configuration) string {
	if acting == current {
		return ""
	}
	return "this acted on the Claude Code configuration setup recorded, " + acting.text() + ", not on the one this environment names, " + current.text()
}

// Notes implements adaptersetup.Noter: a check says when the environment names another
// configuration than the one setup recorded its items in.
func (h *Handler) Notes(_ context.Context, req adaptersetup.Request) ([]string, error) {
	if req.Op != adaptersetup.OpCheck {
		return nil, nil
	}
	rec, err := adaptersetup.ReadRecord(req.StateDir)
	if err != nil || !rec.Installed() {
		return nil, err
	}
	if note := differentConfiguration(actingConfiguration(rec, h.current()), h.current()); note != "" {
		return []string{note}, nil
	}
	return nil, nil
}

// pluginBehind says that the installed copy of the plugin is older than the source
// directory Claude Code reads it from, which Claude Code reports directly.
func (w *world) pluginBehind() bool {
	p := w.obs.plugin
	return p != nil && p.SourceVersion != "" && p.SourceVersion != p.Version
}

func (w *world) recorded(id string) (adaptersetup.RecordItem, bool) {
	if !w.rec.Installed() {
		return adaptersetup.RecordItem{}, false
	}
	return w.rec.Find(id)
}

// Plan implements adaptersetup.Handler.
func (h *Handler) Plan(ctx context.Context, req adaptersetup.Request) ([]adaptersetup.Item, error) {
	w, err := h.look(ctx, req, false)
	if err != nil {
		return nil, err
	}
	return w.plan()
}

// plan lists what is to be done, in the order of planOrder. It refuses when an item
// cannot be looked at: a plan that left it out would say that nothing was wrong.
func (w *world) plan() ([]adaptersetup.Item, error) {
	if err := errors.Join(w.obs.errs(), w.d.programErr); err != nil {
		return nil, err
	}
	var items []adaptersetup.Item
	for _, id := range planOrder {
		action, ok := w.action(id)
		if !ok {
			continue
		}
		it := adaptersetup.Item{ID: id, Kind: w.d.kind(id), Target: w.d.target(id), Action: action, Summary: w.d.summary(id, action), Consent: id != idSource}
		switch id {
		case idMarketplace:
			it.Requires = listed(items, idSource)
		case idPlugin:
			it.Requires = listed(items, idMarketplace)
		}
		items = append(items, it)
	}
	return items, nil
}

func listed(items []adaptersetup.Item, id string) []string {
	if slices.ContainsFunc(items, func(it adaptersetup.Item) bool { return it.ID == id }) {
		return []string{id}
	}
	return nil
}

// action is what an item needs: made when it is not there, replaced when it is not what
// is wanted, taken into the record when it is what is wanted and not recorded as
// such. The permission rule is the exception to the last: a rule that was already
// there is the person's, and is not recorded, so removing the setup never removes it.
func (w *world) action(id string) (adaptersetup.Action, bool) {
	s := w.obs.items[id]
	want := w.d.digest[id]
	switch {
	case !s.exists:
		return adaptersetup.ActionCreate, true
	case s.digest != want:
		return adaptersetup.ActionUpdate, true
	case id == idPlugin && w.pluginBehind():
		return adaptersetup.ActionUpdate, true
	case id == idPermission:
		return "", false
	}
	if r, recorded := w.recorded(id); !recorded || r.Digest != s.digest {
		return adaptersetup.ActionAdopt, true
	}
	return "", false
}

func (d *desired) summary(id string, action adaptersetup.Action) string {
	command := d.command
	if len(d.args) > 0 {
		command += " " + strings.Join(d.args, " ")
	}
	switch id {
	case idSource:
		return "write the Sidera plugin (its marketplace, manifest and Skill) in the adapter's state directory"
	case idMarketplace:
		return "add the marketplace " + marketplaceName + ", which holds the Sidera plugin, to Claude Code"
	case idPlugin:
		if action == adaptersetup.ActionUpdate {
			return "update the plugin " + pluginID + " in Claude Code to " + d.version
		}
		return "install the plugin " + pluginID + ", which gives Claude Code the Sidera Skill, for the user"
	case idMCP:
		if action == adaptersetup.ActionUpdate {
			return "replace the registration of the " + serverName + " MCP server in Claude Code with " + command
		}
		return "register the " + serverName + " MCP server with Claude Code for the user: " + command
	default:
		return "allow the tools of the " + serverName + " MCP server without asking: add " + permissionRule + " to permissions.allow in " + d.settings
	}
}
