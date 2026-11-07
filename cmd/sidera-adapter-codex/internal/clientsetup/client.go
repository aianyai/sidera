package clientsetup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/codexcfg"
	"github.com/aianyai/sidera/sdk/go/adaptersetup"
)

// Names of what the plugin is called in Codex. The plugin and the marketplace that
// carries it share a name, so the plugin's id is "sidera@sidera".
const (
	marketplaceName = "sidera"
	pluginName      = "sidera"
	pluginID        = pluginName + "@" + marketplaceName
	// serverName is the name of the MCP server in Codex: the plugin's server and a
	// person's own [mcp_servers.sidera] share it, and the person's wins.
	serverName = "sidera"
)

// commandTimeout bounds one run of the Codex command line. `plugin add` copies the
// plugin into Codex's cache, which is the longest thing asked of it.
const commandTimeout = 60 * time.Second

// errMarketplaceUnreadable is what Codex's listings say when a configured
// marketplace's source directory is gone or has no manifest. The listing of every
// marketplace fails then, and an installed plugin keeps running from Codex's cache.
var errMarketplaceUnreadable = errors.New("a configured marketplace has no readable manifest")

// plugin is the plugin as `codex plugin list --json` reports it.
type plugin struct {
	Installed bool
	Enabled   bool
	Version   string
}

// marketplace is a marketplace configured in Codex.
type marketplace struct {
	// Registered says whether a marketplace of that name is configured.
	Registered bool
	// Root is the directory it was added from.
	Root string
}

// server is an MCP server as Codex offers it to a session: the plugin's, or a
// person's own of the same name, which hides it.
type server struct {
	Found    bool
	Enabled  bool
	Command  string
	Args     []string
	Approval string
}

// client is the Codex command line, as far as setup uses it. Every method that
// cannot get an answer returns an error that carries what Codex wrote on standard
// error.
type client interface {
	// Plugin reports the sidera plugin. When Codex cannot list, the error is an
	// *unreadableError.
	Plugin(ctx context.Context) (plugin, error)
	// Marketplace reports the marketplace of that name. When Codex cannot list, the
	// error is an *unreadableError, which names the marketplaces Codex cannot read.
	Marketplace(ctx context.Context, name string) (marketplace, error)
	AddMarketplace(ctx context.Context, dir string) error
	RemoveMarketplace(ctx context.Context, name string) error
	AddPlugin(ctx context.Context, id string) error
	RemovePlugin(ctx context.Context, id string) error
	// Server reports the MCP server of that name.
	Server(ctx context.Context, name string) (server, error)
	RemoveServer(ctx context.Context, name string) (string, error)
}

// unreadableMarketplace is a marketplace Codex names as one it cannot read, with the
// directory it names for it.
type unreadableMarketplace struct {
	Name, Root string
}

// unreadableError is errMarketplaceUnreadable with the marketplaces Codex names. Codex
// names each unreadable marketplace on a line of its own, so the error says which
// marketplace is unreadable, not only that one is: the one that is ours is a source
// gone, and any other is somebody else's to repair.
type unreadableError struct {
	Marketplaces []unreadableMarketplace
	msg          string
}

func (e *unreadableError) Error() string { return e.msg }
func (e *unreadableError) Unwrap() error { return errMarketplaceUnreadable }

// root returns the directory Codex names for the marketplace of that name.
func (e *unreadableError) root(name string) (string, bool) {
	for _, m := range e.Marketplaces {
		if m.Name == name {
			return m.Root, true
		}
	}
	return "", false
}

// namesOtherThan lists the unreadable marketplaces that are not the one of that name. An
// error that names none (a Codex that words it differently) lists one that is not named,
// so that it is never taken to be ours.
func (e *unreadableError) namesOtherThan(name string) []string {
	var out []string
	for _, m := range e.Marketplaces {
		if m.Name != name {
			out = append(out, m.Name)
		}
	}
	if len(e.Marketplaces) == 0 {
		out = append(out, "(one that Codex does not name)")
	}
	return out
}

const unreadableText = "marketplace root does not contain a supported manifest"

// unreadableLine is one line of the error: "- `foo` at /some/dir: marketplace root ...".
var unreadableLine = regexp.MustCompile("(?m)^- `([^`]+)` at (.+): " + unreadableText + `\r?$`)

// parseUnreadable reads what Codex wrote on standard error when it cannot list
// marketplaces because some have no manifest; msg is the error's text.
func parseUnreadable(stderr, msg string) *unreadableError {
	e := &unreadableError{msg: msg}
	for _, m := range unreadableLine.FindAllStringSubmatch(stderr, -1) {
		e.Marketplaces = append(e.Marketplaces, unreadableMarketplace{Name: m[1], Root: strings.TrimSpace(m[2])})
	}
	return e
}

// cli runs the Codex command line. program is the instance's codex_program, a name or a
// path; dir is the working directory of every run. Each command gets no standard input,
// a time limit, no shell and a process group that ends with it (adaptersetup.ClientCommand);
// what it wrote on standard error is part of the error of a failure.
type cli struct {
	program string
	// dir is a directory with no project configuration of its own: Codex reads the
	// .codex/config.toml of a trusted project it is started in, which would change what
	// setup sees from one run to the next. Setup uses the state directory of the
	// instance, which holds none.
	dir string
	// env is the environment of every run; it decides which Codex home Codex works in.
	env []string
	// timeout bounds one run; zero means commandTimeout.
	timeout time.Duration
}

func (c cli) run(ctx context.Context, args ...string) (string, error) {
	program, err := codexcfg.ResolveProgram(c.program, exec.LookPath)
	if err != nil {
		return "", fmt.Errorf("Codex is not installed: %w", err)
	}
	timeout := c.timeout
	if timeout == 0 {
		timeout = commandTimeout
	}
	cmd := adaptersetup.ClientCommand{Program: program, Args: args, Dir: c.dir, Env: c.env, Timeout: timeout}
	out, err := cmd.Run(ctx)
	switch {
	case adaptersetup.NotInstalled(err):
		return "", fmt.Errorf("Codex is not installed: %w", err)
	case err != nil && ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded):
		return "", &noAnswerError{command: strings.Join(args, " "), err: err}
	case err != nil:
		return "", err
	case out.ExitCode != 0:
		failure := cmd.Failure(out)
		if strings.Contains(string(out.Stderr), unreadableText) {
			return "", parseUnreadable(string(out.Stderr), failure.Error())
		}
		return "", failure
	}
	return string(out.Stdout), nil
}

// noAnswerError is a run of Codex that ran out of its time limit. A listing waits for
// Codex's remote plugin catalogue, which can take longer than the limit when nothing of
// it is cached, and the next run finds it cached; the message says so, since the person
// reads it after an update that looks as if it broke Codex.
type noAnswerError struct {
	command string
	err     error
}

func (e *noAnswerError) Error() string {
	return fmt.Sprintf("Codex did not answer `codex %s` in time, likely while it fetched its remote plugin catalogue; "+
		"running `sidera adapter setup codex --refresh` again usually succeeds (%v)", e.command, e.err)
}
func (e *noAnswerError) Unwrap() error { return e.err }

// runRead runs a command that only reads Codex's state. One that ran out of time is run
// once more: Codex is usually quicker the second time, and asking again changes nothing.
// A command that changes Codex's configuration is run with run, never again, since the
// first run may have taken effect.
func (c cli) runRead(ctx context.Context, args ...string) (string, error) {
	out, err := c.run(ctx, args...)
	var noAnswer *noAnswerError
	if errors.As(err, &noAnswer) {
		return c.run(ctx, args...)
	}
	return out, err
}

type pluginListJSON struct {
	Installed []struct {
		Name            string `json:"name"`
		MarketplaceName string `json:"marketplaceName"`
		Version         string `json:"version"`
		Installed       bool   `json:"installed"`
		Enabled         bool   `json:"enabled"`
	} `json:"installed"`
}

func parsePlugin(out string) (plugin, error) {
	var l pluginListJSON
	if err := json.Unmarshal([]byte(out), &l); err != nil {
		return plugin{}, fmt.Errorf("the plugin list is not JSON: %w", err)
	}
	for _, p := range l.Installed {
		if p.Name == pluginName && p.MarketplaceName == marketplaceName {
			return plugin{Installed: p.Installed, Enabled: p.Enabled, Version: p.Version}, nil
		}
	}
	return plugin{}, nil
}

func (c cli) Plugin(ctx context.Context) (plugin, error) {
	out, err := c.runRead(ctx, "plugin", "list", "--json")
	if err != nil {
		return plugin{}, err
	}
	return parsePlugin(out)
}

type marketplaceListJSON struct {
	Marketplaces *[]struct {
		Name string `json:"name"`
		Root string `json:"root"`
	} `json:"marketplaces"`
}

// parseMarketplace finds the marketplace in `codex plugin marketplace list --json`. A
// reply that is not that list is an error, not "no marketplace": setup would otherwise
// plan to add one that exists.
func parseMarketplace(out, name string) (marketplace, error) {
	var l marketplaceListJSON
	if err := json.Unmarshal([]byte(out), &l); err != nil {
		return marketplace{}, fmt.Errorf("the marketplace list is not JSON: %w", err)
	}
	if l.Marketplaces == nil {
		return marketplace{}, errors.New("the marketplace list has no marketplaces member")
	}
	for _, m := range *l.Marketplaces {
		if m.Name == name {
			return marketplace{Registered: true, Root: m.Root}, nil
		}
	}
	return marketplace{}, nil
}

func (c cli) Marketplace(ctx context.Context, name string) (marketplace, error) {
	out, err := c.runRead(ctx, "plugin", "marketplace", "list", "--json")
	if err != nil {
		return marketplace{}, err
	}
	return parseMarketplace(out, name)
}

func (c cli) AddMarketplace(ctx context.Context, dir string) error {
	_, err := c.run(ctx, "plugin", "marketplace", "add", dir)
	return err
}

func (c cli) RemoveMarketplace(ctx context.Context, name string) error {
	_, err := c.run(ctx, "plugin", "marketplace", "remove", name)
	return err
}

func (c cli) AddPlugin(ctx context.Context, id string) error {
	_, err := c.run(ctx, "plugin", "add", id, "--json")
	return err
}

func (c cli) RemovePlugin(ctx context.Context, id string) error {
	_, err := c.run(ctx, "plugin", "remove", id)
	return err
}

type serverListJSON struct {
	Name      string `json:"name"`
	Enabled   bool   `json:"enabled"`
	Transport struct {
		Type    string   `json:"type"`
		Command string   `json:"command"`
		Args    []string `json:"args"`
	} `json:"transport"`
}

var approvalLine = regexp.MustCompile(`(?m)^\s*default_tools_approval_mode:\s*(\S+)\s*$`)

// parseServer finds the server in `codex mcp list --json`. The approval mode is not
// in the JSON; it is read from the text of `codex mcp get`, by the caller.
func parseServer(listJSON, name string) (server, error) {
	var l []serverListJSON
	if err := json.Unmarshal([]byte(listJSON), &l); err != nil {
		return server{}, fmt.Errorf("the MCP server list is not JSON: %w", err)
	}
	for _, s := range l {
		if s.Name == name {
			return server{Found: true, Enabled: s.Enabled, Command: s.Transport.Command, Args: s.Transport.Args}, nil
		}
	}
	return server{}, nil
}

func (c cli) Server(ctx context.Context, name string) (server, error) {
	out, err := c.runRead(ctx, "mcp", "list", "--json")
	if err != nil {
		return server{}, err
	}
	s, err := parseServer(out, name)
	if err != nil || !s.Found {
		return s, err
	}
	text, err := c.runRead(ctx, "mcp", "get", name)
	if err != nil {
		return server{}, err
	}
	if m := approvalLine.FindStringSubmatch(text); m != nil {
		s.Approval = m[1]
	}
	return s, nil
}

func (c cli) RemoveServer(ctx context.Context, name string) (string, error) {
	out, err := c.run(ctx, "mcp", "remove", name)
	return strings.TrimSpace(out), err
}
