// Package clientsetup is the setup of the Codex adapter: it brings Codex's own
// configuration in line with an instance, through `sidera adapter setup`. It installs
// one plugin, which carries the `sidera` MCP server (approved in advance, started with
// the Sidera program of the instance) and the Skill that tells an agent what Sidera is,
// and it offers to remove a person's own [mcp_servers.sidera], which would hide the
// plugin's server. The contract it speaks is sdk/go/adaptersetup; the reasons for what
// it does are in the Codex adapter reference.
package clientsetup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/codexcfg"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/adaptersetup"
	"github.com/aianyai/sidera/sdk/go/guidance"
)

// Ids and kinds of the items. The three first are what the record holds; mcpServerID
// is only ever offered, for something setup did not make.
const (
	sourceID    = "plugin_source"
	marketplID  = "marketplace"
	pluginItem  = "plugin"
	mcpServerID = "mcp_server"
	programID   = "program"
	clientID    = "codex"
)

// Handler is the Codex adapter's setup. The zero value is not usable; use New.
type Handler struct {
	// Version is the adapter's version. It is the plugin's version and goes into the
	// record.
	Version string
	// Skill is the text of the plugin's SKILL.md.
	Skill []byte
	// Environ is the environment of the setup program. It names the Codex home
	// (CODEX_HOME), and Codex is run in the environment of the home an item belongs to.
	Environ []string
	// NewClient makes the Codex command line named by program (the instance's
	// codex_program), run in the directory dir with the environment env; tests replace it.
	NewClient func(program, dir string, env []string) client
}

// New returns the setup of the adapter of the given version, for a setup program with the
// environment environ.
func New(version string, environ []string) Handler {
	return Handler{Version: version, Skill: guidance.Skill(), Environ: environ,
		NewClient: func(program, dir string, env []string) client { return cli{program: program, dir: dir, env: env} }}
}

// env is what one request works from.
type env struct {
	h     Handler
	req   adaptersetup.Request
	c     client
	codex string
	dir   string // the marketplace directory, in the instance's state directory
	// home is the Codex home the request acts on; current is the one the environment of
	// this run names. A plan and an apply act on the current one; the other operations on
	// the one the items were recorded in.
	home, current codexHome
	def           serverDef
	want          bundle
}

// env gathers what a request works from. A request that acts on what was recorded
// (recorded true) works in the Codex home of the record, whatever the environment names.
func (h Handler) env(req adaptersetup.Request, needProgram, recorded bool) (*env, error) {
	inst, err := codexcfg.ParseInstance(adapterproto.ConfigValues(req.Config))
	if err != nil {
		return nil, fmt.Errorf("the instance configuration: %w", err)
	}
	current := currentHome(h.Environ)
	home := current
	if recorded {
		rec, err := adaptersetup.ReadRecord(req.StateDir)
		if err != nil {
			return nil, err
		}
		home = actingHome(rec, current)
	}
	e := &env{h: h, req: req, codex: inst.CodexProgram, home: home, current: current,
		c:   h.NewClient(inst.CodexProgram, req.StateDir, home.environ(h.Environ)),
		dir: filepath.Join(req.StateDir, "setup", "codex", "marketplace")}
	if !needProgram {
		return e, nil
	}
	// The instance's own program wins, as it does for the sessions the adapter starts;
	// without one the plugin names the program that ran setup. It is resolved the way
	// those sessions resolve it, so that the plugin starts the program they start.
	program := req.SideraProgram
	if _, set := req.Config[codexcfg.KeySideraProgram]; set && inst.SideraProgram != "" {
		program = inst.SideraProgram
	}
	if program == "" {
		return nil, errors.New("no sidera program: the request names none and the instance sets no sidera_program")
	}
	// A program that is not found stays as it was written; check says that it is missing.
	program, _ = codexcfg.ResolveProgram(program, exec.LookPath)
	args := []string{"mcp"}
	if !req.HomeIsDefault {
		args = []string{"--home", req.Home, "mcp"}
	}
	e.def = serverDef{Command: program, Args: args}
	e.want = render(h.Version, e.def, h.Skill)
	return e, nil
}

// recordedHome is the Codex home the items of the record were made in, if it names one.
func recordedHome(rec *adaptersetup.Record) (codexHome, bool) {
	if rec.Installed() {
		for _, id := range []string{marketplID, pluginItem} {
			if r, ok := rec.Find(id); ok {
				if home, ok := homeOf(r.Target); ok {
					return home, true
				}
			}
		}
	}
	return codexHome{}, false
}

// actingHome is the Codex home the items of the record were made in, else the fallback.
func actingHome(rec *adaptersetup.Record, fallback codexHome) codexHome {
	if home, ok := recordedHome(rec); ok {
		return home
	}
	return fallback
}

// scopeNote says that a run acted on a Codex home other than the one its environment
// names, empty when they are the same.
func scopeNote(acting, current codexHome) string {
	if acting == current {
		return ""
	}
	return "this acted on the Codex home setup recorded, " + acting.text() + ", not on the one this environment names, " + current.text()
}

// noteScope adds the scope note to the message of every outcome.
func (e *env) noteScope(outcomes []adaptersetup.Outcome) {
	note := scopeNote(e.home, e.current)
	for i := range outcomes {
		switch {
		case note == "":
		case outcomes[i].Message == "":
			outcomes[i].Message = note
		default:
			outcomes[i].Message += "; " + note
		}
	}
}

// Notes implements adaptersetup.Noter: a check says when the environment names another
// Codex home than the one setup recorded its items in.
func (h Handler) Notes(_ context.Context, req adaptersetup.Request) ([]string, error) {
	if req.Op != adaptersetup.OpCheck {
		return nil, nil
	}
	rec, err := adaptersetup.ReadRecord(req.StateDir)
	if err != nil || !rec.Installed() {
		return nil, err
	}
	current := currentHome(h.Environ)
	if note := scopeNote(actingHome(rec, current), current); note != "" {
		return []string{note}, nil
	}
	return nil, nil
}

func (e *env) marketplaceTarget() string { return e.home.target(marketplaceName) }
func (e *env) pluginTarget() string      { return e.home.target(pluginID) }

func mpDigest(root string) string {
	return adaptersetup.Digest([]byte("marketplace\x00" + marketplaceName + "\x00" + root))
}

// resolve follows the links of the part of a path that exists and keeps the rest as it
// is: Codex names a directory by its real path (on macOS "/var" is "/private/var"), and
// when the directory is gone the real path of its parents is all there is to compare.
func resolve(p string) string {
	p = filepath.Clean(p)
	for rest := ""; ; {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return filepath.Join(p, rest)
		}
		rest, p = filepath.Join(filepath.Base(p), rest), parent
	}
}

// trimVerbatim removes the "\\?\" prefix of a Windows path that names a drive: Codex is
// written in Rust, whose canonicalize returns paths in that form on Windows, so a
// directory it reports does not otherwise equal the same directory spelled the way Go
// spells it. A network share keeps its meaning: "\\?\UNC\server\share" is
// "\\server\share".
func trimVerbatim(p string) string {
	switch {
	case strings.HasPrefix(p, `\\?\UNC\`):
		return `\\` + p[len(`\\?\UNC\`):]
	case strings.HasPrefix(p, `\\?\`):
		return p[len(`\\?\`):]
	}
	return p
}

func sameDir(a, b string) bool {
	if runtime.GOOS == "windows" {
		a, b = trimVerbatim(a), trimVerbatim(b)
	}
	a, b = resolve(a), resolve(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// observed is what is there now: the source directory on disk and what Codex says. It
// is built even when parts of it cannot be read, so that a check can say what it can.
type observed struct {
	src       bundle
	srcExists bool
	// srcErr is set when the source directory exists but cannot be read.
	srcErr error
	// codexErr is set when Codex cannot be asked (not installed, it failed, it timed
	// out); nothing else of Codex's is known then.
	codexErr error
	// unreadable is set when Codex cannot list marketplaces because some have no
	// manifest. The plugin is then not asked about: whether it is installed, enabled
	// and at which version is not known (an installed plugin keeps running from
	// Codex's cache). The marketplace of ours is known only when the error names it.
	unreadable *unreadableError
	// blockers are the unreadable marketplaces that are not ours, which are somebody
	// else's to repair, and without which Codex lists nothing.
	blockers []string
	// sourceGone says that the error names our marketplace at our source directory.
	sourceGone bool
	// mpKnown says whether mp is what Codex has under our marketplace's name.
	mpKnown bool
	mp      marketplace
	pl      plugin
	srv     server
}

func (o observed) pluginKnown() bool { return o.codexErr == nil && o.unreadable == nil }

// observe never fails: what could not be read is in the fields that say so.
func (e *env) observe(ctx context.Context) observed {
	var o observed
	var err error
	if o.src, o.srcExists, o.srcErr = readBundle(e.dir); o.srcErr != nil {
		o.srcErr = fmt.Errorf("cannot read %s: %w", e.dir, o.srcErr)
	}
	o.mp, err = e.c.Marketplace(ctx, marketplaceName)
	var u *unreadableError
	switch {
	case errors.As(err, &u):
		o.unreadable = u
		o.blockers = u.namesOtherThan(marketplaceName)
		// Codex names where each marketplace comes from; the one that is ours is gone
		// only when it names ours, at our directory. An error that does not name it
		// says nothing of it: it is not taken to be ours.
		root, named := u.root(marketplaceName)
		o.mp, o.mpKnown = marketplace{Registered: named, Root: root}, named
		o.sourceGone = named && sameDir(root, e.dir)
	case err != nil:
		o.codexErr = err
		return o
	default:
		o.mpKnown = true
		if o.pl, err = e.c.Plugin(ctx); err != nil {
			o.codexErr = err
			return o
		}
	}
	if o.srv, err = e.c.Server(ctx, serverName); err != nil {
		o.codexErr = err
	}
	return o
}

func (e *env) wanted() []adaptersetup.Wanted {
	return []adaptersetup.Wanted{
		{ID: sourceID, Kind: kindSource, Target: e.dir, Digest: e.want.digest()},
		{ID: marketplID, Kind: kindMarketplace, Target: e.marketplaceTarget(), Digest: mpDigest(e.dir)},
		{ID: pluginItem, Kind: kindPlugin, Target: e.pluginTarget(), Digest: e.want.pluginDigest()},
	}
}

// probe reads, for a recorded item, the digest of what is there now: the source
// directory's content; the marketplace's root; the plugin as installed, which Codex
// reports by version only, so an installed plugin of the recorded version is as it was
// recorded. What cannot be read now is reported as it was recorded: that is not a
// difference, and broken says what could not be read.
func (e *env) probe(o observed) adaptersetup.Probe {
	return func(r adaptersetup.RecordItem) (string, bool, error) {
		switch r.ID {
		case sourceID:
			switch {
			case o.srcErr != nil:
				return r.Digest, true, nil
			case !o.srcExists:
				return "", false, nil
			}
			return o.src.digest(), true, nil
		case marketplID:
			switch {
			case o.codexErr != nil || !o.mpKnown:
				return r.Digest, true, nil
			case !o.mp.Registered:
				return "", false, nil
			case sameDir(o.mp.Root, e.dir):
				return mpDigest(e.dir), true, nil
			}
			return mpDigest(o.mp.Root), true, nil
		case pluginItem:
			switch {
			case !o.pluginKnown():
				return r.Digest, true, nil
			case !o.pl.Installed:
				return "", false, nil
			case o.pl.Version != r.Version:
				return adaptersetup.Digest([]byte("installed version " + o.pl.Version)), true, nil
			}
			return r.Digest, true, nil
		}
		return "", false, fmt.Errorf("the record holds %q, which this adapter does not know", r.ID)
	}
}

const (
	kindSource      = "plugin_source"
	kindMarketplace = "marketplace"
	kindPlugin      = "plugin"
	kindServer      = "mcp_server"
)

func recordedItem(rec *adaptersetup.Record, id string) (adaptersetup.RecordItem, bool) {
	if rec == nil {
		return adaptersetup.RecordItem{}, false
	}
	return rec.Find(id)
}

// Plan implements adaptersetup.Handler.
func (h Handler) Plan(ctx context.Context, req adaptersetup.Request) ([]adaptersetup.Item, error) {
	e, err := h.env(req, true, false)
	if err != nil {
		return nil, err
	}
	return e.plan(ctx)
}

func (e *env) plan(ctx context.Context) ([]adaptersetup.Item, error) {
	rec, err := adaptersetup.ReadRecord(e.req.StateDir)
	if err != nil {
		return nil, err
	}
	// The source directory is one for all homes: a marketplace in another home points at
	// it, and setup here would rewrite it under that marketplace.
	if recorded, ok := recordedHome(rec); ok && recorded != e.current {
		return nil, fmt.Errorf("setup of this instance was made in %s, and this environment names %s: both would use the same plugin source directory. Run `sidera adapter setup %s --remove` with %s first", recorded.text(), e.current.text(), e.req.Instance, recorded.setting())
	}
	o := e.observe(ctx)
	switch {
	case o.srcErr != nil:
		return nil, o.srcErr
	case o.codexErr != nil:
		return nil, o.codexErr
	case o.mp.Registered && !sameDir(o.mp.Root, e.dir):
		return nil, fmt.Errorf("Codex already has a marketplace named %q, added from %s and not from %s. Setup does not re-point it; remove it first if it is not yours (codex plugin marketplace remove %s), or set up the instance that owns it", marketplaceName, o.mp.Root, e.dir, marketplaceName)
	case len(o.blockers) > 0:
		return nil, fmt.Errorf("Codex cannot list plugins because %s: %w. Setup cannot tell whether the marketplace %s or the plugin exists; repair or remove the marketplace in Codex (codex plugin marketplace remove <name>) and run setup again", unreadableSentence(o.blockers), o.unreadable, marketplaceName)
	}
	var items []adaptersetup.Item
	add := func(it adaptersetup.Item) {
		var requires []string
		for _, id := range []string{sourceID, marketplID} {
			if slices.ContainsFunc(items, func(x adaptersetup.Item) bool { return x.ID == id }) && id != it.ID {
				requires = append(requires, id)
			}
		}
		if it.ID == marketplID || it.ID == pluginItem {
			it.Requires = requires
		}
		items = append(items, it)
	}

	// The source files are the adapter's own: no consent. Setup that is run on purpose
	// writes them again whatever they hold.
	if _, ok := recordedItem(rec, sourceID); !ok || !o.srcExists || o.src.digest() != e.want.digest() {
		action := adaptersetup.ActionCreate
		if o.srcExists {
			action = adaptersetup.ActionUpdate
		}
		add(adaptersetup.Item{ID: sourceID, Kind: kindSource, Target: e.dir, Action: action, Summary: "write the files of the Sidera plugin for Codex into " + e.dir})
	}
	_, mpRecorded := recordedItem(rec, marketplID)
	switch {
	case !o.mp.Registered:
		add(adaptersetup.Item{ID: marketplID, Kind: kindMarketplace, Target: e.marketplaceTarget(), Action: adaptersetup.ActionCreate, Consent: true,
			Summary: "add " + e.dir + " to Codex as the plugin marketplace " + marketplaceName})
	case !mpRecorded:
		add(adaptersetup.Item{ID: marketplID, Kind: kindMarketplace, Target: e.marketplaceTarget(), Action: adaptersetup.ActionAdopt, Consent: true,
			Summary: "take over the Codex plugin marketplace " + marketplaceName + ", which already points at " + e.dir})
	}
	rp, plRecorded := recordedItem(rec, pluginItem)
	installed := (o.pluginKnown() && o.pl.Installed) || (!o.pluginKnown() && plRecorded)
	pluginPlanned := false
	switch {
	case !installed:
		pluginPlanned = true
		add(adaptersetup.Item{ID: pluginItem, Kind: kindPlugin, Target: e.pluginTarget(), Action: adaptersetup.ActionCreate, Consent: true,
			Summary: "install the Sidera plugin in Codex: the sidera MCP server, approved in advance, and the Sidera Skill"})
	case !plRecorded:
		pluginPlanned = true
		add(adaptersetup.Item{ID: pluginItem, Kind: kindPlugin, Target: e.pluginTarget(), Action: adaptersetup.ActionAdopt, Consent: true,
			Summary: "take over the installed sidera plugin and install it again from the current files" + enablesNote(o)})
	case rp.Digest != e.want.pluginDigest() || o.pluginKnown() && o.pl.Version != rp.Version:
		pluginPlanned = true
		add(adaptersetup.Item{ID: pluginItem, Kind: kindPlugin, Target: e.pluginTarget(), Action: adaptersetup.ActionUpdate, Consent: true,
			Summary: "install the current Sidera plugin in Codex over the installed one" + enablesNote(o)})
	}
	// A person's own [mcp_servers.sidera] replaces the plugin's server of the same name.
	// When the plugin is about to be installed over an installed one, the server Codex
	// offers is probably the old plugin's, and the next check judges it.
	if o.srv.Found && !e.def.matches(o.srv) && !(pluginPlanned && installed) {
		add(adaptersetup.Item{ID: mcpServerID, Kind: kindServer, Target: serverName, Action: adaptersetup.ActionRemove, Consent: true,
			Summary: "remove your own MCP server sidera from Codex's config.toml (codex mcp remove sidera), which hides the plugin's server"})
	}
	return items, nil
}

// unreadableSentence words the marketplaces that Codex cannot read.
func unreadableSentence(names []string) string {
	if len(names) == 1 {
		return "the marketplace " + names[0] + " is unreadable"
	}
	return "the marketplaces " + strings.Join(names, ", ") + " are unreadable"
}

// enablesNote warns that installing the plugin enables it, which a person who disabled it
// did not ask for.
func enablesNote(o observed) string {
	switch {
	case !o.pluginKnown():
		return " (if it is disabled now, installing it enables it again)"
	case o.pl.Installed && !o.pl.Enabled:
		return " (it is disabled now; installing it enables it again)"
	}
	return ""
}

// record returns the record to add to, with the fields every write refreshes.
func (e *env) record() (*adaptersetup.Record, error) {
	rec, err := adaptersetup.ReadRecord(e.req.StateDir)
	if err != nil {
		return nil, err
	}
	if rec == nil {
		rec = &adaptersetup.Record{}
	}
	return rec, nil
}

func (e *env) stamp(rec *adaptersetup.Record) {
	rec.AdapterVersion, rec.SideraProgram = e.h.Version, e.def.Command
}

func outcome(id, kind, target string, status adaptersetup.OutcomeStatus, message string) adaptersetup.Outcome {
	return adaptersetup.Outcome{ID: id, Kind: kind, Target: target, Status: status, Message: message}
}

func failure(it adaptersetup.Item, err error) adaptersetup.Outcome {
	return outcome(it.ID, it.Kind, it.Target, adaptersetup.OutcomeFailed, err.Error())
}

// installPlugin installs the plugin from the marketplace and checks that Codex then
// offers a server: a Codex that dropped the legacy plugin format installs the plugin
// and offers nothing, which the person must hear of when it happens.
//
// installed says whether the plugin is installed whatever err is, so that the caller
// records it and it can be undone.
func (e *env) installPlugin(ctx context.Context) (installed bool, err error) {
	if err := e.c.AddPlugin(ctx, pluginID); err != nil {
		return false, err
	}
	s, err := e.c.Server(ctx, serverName)
	if err != nil {
		return true, fmt.Errorf("the plugin is installed, but Codex could not be asked for its servers: %w", err)
	}
	if !s.Found {
		return true, errors.New("the plugin is installed, but Codex does not offer its sidera MCP server: this Codex may no longer read the plugin layout Sidera writes (see the Codex adapter reference for the fallback)")
	}
	return true, nil
}

// removeHidingServer removes the person's own server and asks Codex again: `codex mcp
// remove` edits the user's config.toml only, so a server of the same name that comes from
// elsewhere (a project's or a managed configuration) stays, and removing the one that was
// offered did not unhide the plugin's.
func (e *env) removeHidingServer(ctx context.Context) (string, error) {
	message, err := e.c.RemoveServer(ctx, serverName)
	if err != nil {
		return "", err
	}
	s, err := e.c.Server(ctx, serverName)
	switch {
	case err != nil:
		return "", fmt.Errorf("the server was removed, but Codex could not be asked what it offers now: %w", err)
	case s.Found && !e.def.matches(s):
		return "", fmt.Errorf("Codex still offers an MCP server sidera that is not the plugin's after codex mcp remove (%s); it may come from a project's or a managed configuration, which setup does not edit", message)
	}
	return message, nil
}

// Apply implements adaptersetup.Handler.
func (h Handler) Apply(ctx context.Context, req adaptersetup.Request) ([]adaptersetup.Outcome, error) {
	e, err := h.env(req, true, false)
	if err != nil {
		return nil, err
	}
	planned, err := e.plan(ctx)
	if err != nil {
		return nil, err
	}
	rec, err := e.record()
	if err != nil {
		return nil, err
	}
	var outcomes []adaptersetup.Outcome
	for _, id := range req.Accepted {
		if !slices.ContainsFunc(planned, func(it adaptersetup.Item) bool { return it.ID == id }) {
			outcomes = append(outcomes, adaptersetup.Outcome{ID: id, Status: adaptersetup.OutcomeSkipped, Message: "not in the plan"})
		}
	}
	notDone := map[string]bool{}
	dirty := false
	for _, it := range planned {
		if !slices.Contains(req.Accepted, it.ID) {
			continue
		}
		if i := slices.IndexFunc(it.Requires, func(r string) bool { return notDone[r] }); i >= 0 {
			notDone[it.ID] = true
			outcomes = append(outcomes, outcome(it.ID, it.Kind, it.Target, adaptersetup.OutcomeSkipped, it.Requires[i]+" was not done"))
			continue
		}
		var err error
		message := ""
		switch it.ID {
		case sourceID:
			if err = writeBundle(e.dir, e.want); err == nil {
				rec.Set(adaptersetup.RecordItem{ID: sourceID, Kind: kindSource, Target: e.dir, Digest: e.want.digest(), Version: h.Version})
				dirty = true
			}
		case marketplID:
			if it.Action == adaptersetup.ActionCreate {
				err = e.c.AddMarketplace(ctx, e.dir)
			}
			if err == nil {
				rec.Set(adaptersetup.RecordItem{ID: marketplID, Kind: kindMarketplace, Target: e.marketplaceTarget(), Digest: mpDigest(e.dir), Version: h.Version})
				dirty = true
			}
		case pluginItem:
			var installed bool
			installed, err = e.installPlugin(ctx)
			if installed {
				rec.Set(adaptersetup.RecordItem{ID: pluginItem, Kind: kindPlugin, Target: e.pluginTarget(), Digest: e.want.pluginDigest(), Version: h.Version})
				dirty = true
			}
		case mcpServerID:
			message, err = e.removeHidingServer(ctx)
		}
		if err != nil {
			notDone[it.ID] = true
			outcomes = append(outcomes, failure(it, err))
			continue
		}
		outcomes = append(outcomes, outcome(it.ID, it.Kind, it.Target, adaptersetup.OutcomeDone, message))
	}
	if dirty {
		e.stamp(rec)
		if err := adaptersetup.WriteRecord(req.StateDir, *rec); err != nil {
			return nil, err
		}
	}
	return outcomes, nil
}

// Remove implements adaptersetup.Handler. The order is the order of undoing: the
// plugin, then the marketplace it came from, then the files the marketplace read.
func (h Handler) Remove(_ context.Context, req adaptersetup.Request) ([]adaptersetup.Item, error) {
	rec, err := adaptersetup.ReadRecord(req.StateDir)
	if err != nil || !rec.Installed() {
		return nil, err
	}
	e, err := h.env(req, false, true)
	if err != nil {
		return nil, err
	}
	var items []adaptersetup.Item
	add := func(it adaptersetup.Item) {
		if len(items) > 0 {
			it.Requires = []string{items[len(items)-1].ID}
		}
		items = append(items, it)
	}
	if r, ok := rec.Find(pluginItem); ok {
		add(adaptersetup.Item{ID: pluginItem, Kind: kindPlugin, Target: r.Target, Action: adaptersetup.ActionRemove, Consent: true, Summary: "uninstall the Sidera plugin from Codex"})
	}
	if r, ok := rec.Find(marketplID); ok {
		add(adaptersetup.Item{ID: marketplID, Kind: kindMarketplace, Target: r.Target, Action: adaptersetup.ActionRemove, Consent: true, Summary: "remove the Codex plugin marketplace " + marketplaceName})
	}
	if _, ok := rec.Find(sourceID); ok {
		add(adaptersetup.Item{ID: sourceID, Kind: kindSource, Target: e.dir, Action: adaptersetup.ActionRemove, Summary: "delete the files of the Sidera plugin in " + e.dir})
	}
	return items, nil
}

// Undo implements adaptersetup.Handler. It undoes the accepted items in the order Remove
// lists them, whatever the order of the ids, and leaves recorded what it did not undo.
func (h Handler) Undo(ctx context.Context, req adaptersetup.Request) ([]adaptersetup.Outcome, error) {
	e, err := h.env(req, false, true)
	if err != nil {
		return nil, err
	}
	rec, err := adaptersetup.ReadRecord(req.StateDir)
	if err != nil {
		return nil, err
	}
	var outcomes []adaptersetup.Outcome
	for _, id := range req.Accepted {
		if _, ok := recordedItem(rec, id); !ok {
			outcomes = append(outcomes, adaptersetup.Outcome{ID: id, Status: adaptersetup.OutcomeSkipped, Message: "not recorded"})
		}
	}
	notDone := false
	for _, id := range []string{pluginItem, marketplID, sourceID} {
		r, ok := recordedItem(rec, id)
		if !ok || !slices.Contains(req.Accepted, id) {
			continue
		}
		it := adaptersetup.Item{ID: r.ID, Kind: r.Kind, Target: r.Target}
		if notDone {
			outcomes = append(outcomes, outcome(it.ID, it.Kind, it.Target, adaptersetup.OutcomeSkipped, "an item it depends on was not undone"))
			continue
		}
		status, message, err := e.undo(ctx, r)
		switch {
		case err != nil:
			notDone = true
			outcomes = append(outcomes, failure(it, err))
			continue
		case status != adaptersetup.OutcomeDone:
			notDone = true
		default:
			rec.Drop(id)
		}
		outcomes = append(outcomes, outcome(it.ID, it.Kind, it.Target, status, message))
	}
	e.noteScope(outcomes)
	// A record left with no item is removed by the caller, declines included.
	if rec != nil {
		if err := adaptersetup.WriteRecord(req.StateDir, *rec); err != nil {
			return nil, err
		}
	}
	return outcomes, nil
}

func (e *env) undo(ctx context.Context, r adaptersetup.RecordItem) (adaptersetup.OutcomeStatus, string, error) {
	switch r.ID {
	case pluginItem:
		pl, err := e.c.Plugin(ctx)
		var u *unreadableError
		switch {
		case errors.As(err, &u):
			// The plugin cannot be listed (its marketplace's files are gone) but can be removed.
		case err != nil:
			return "", "", err
		case !pl.Installed:
			return adaptersetup.OutcomeDone, "already uninstalled", nil
		}
		return adaptersetup.OutcomeDone, "", e.c.RemovePlugin(ctx, pluginID)
	case marketplID:
		mp, err := e.c.Marketplace(ctx, marketplaceName)
		var u *unreadableError
		switch {
		case errors.As(err, &u):
			// The marketplace is removed only when Codex names it, at our directory.
			root, named := u.root(marketplaceName)
			switch {
			case !named:
				return adaptersetup.OutcomeSkipped, "Codex cannot list its marketplaces, so where " + marketplaceName + " points is not known; left alone (" + unreadableSentence(u.namesOtherThan(marketplaceName)) + ")", nil
			case !sameDir(root, e.dir):
				return adaptersetup.OutcomeSkipped, "the marketplace points at " + root + ", not at the files setup wrote; left alone", nil
			}
		case err != nil:
			return "", "", err
		case !mp.Registered:
			return adaptersetup.OutcomeDone, "already removed", nil
		case !sameDir(mp.Root, e.dir):
			return adaptersetup.OutcomeSkipped, "the marketplace points at " + mp.Root + ", not at the files setup wrote; left alone", nil
		}
		return adaptersetup.OutcomeDone, "", e.c.RemoveMarketplace(ctx, marketplaceName)
	default:
		if !sameDir(r.Target, e.dir) {
			return adaptersetup.OutcomeSkipped, "the record names " + r.Target + ", which is not where setup writes; left alone", nil
		}
		if err := os.RemoveAll(e.dir); err != nil {
			return "", "", err
		}
		// The directories made for it go too, when nothing else is in them.
		_ = os.Remove(filepath.Dir(e.dir))
		_ = os.Remove(filepath.Dir(filepath.Dir(e.dir)))
		return adaptersetup.OutcomeDone, "", nil
	}
}

func programExists(p string) bool {
	if filepath.IsAbs(p) {
		_, err := os.Stat(p)
		return err == nil
	}
	_, err := exec.LookPath(p)
	return err == nil
}

// broken lists what stops the plugin from working although every item is as recorded.
// stale says that the plugin is about to be installed again, in which case the server
// Codex offers is expected to differ from the wanted one. A server of the person's that
// hides the plugin's is left out when the person declined to have it removed.
func (e *env) broken(o observed, rec *adaptersetup.Record, stale bool) []adaptersetup.Drift {
	var out []adaptersetup.Drift
	add := func(id, kind, target, summary string) {
		out = append(out, adaptersetup.Drift{ID: id, Kind: kind, Target: target, Problem: adaptersetup.ProblemBroken, Summary: summary})
	}
	if o.srcErr != nil {
		add(sourceID, kindSource, e.dir, o.srcErr.Error())
	}
	switch {
	case o.codexErr != nil:
		add(clientID, "client", e.codex, "cannot ask Codex: "+o.codexErr.Error())
	case o.sourceGone:
		add(marketplID, kindMarketplace, marketplaceName, "Codex cannot read the plugin marketplace sidera, whose files are gone: "+o.unreadable.Error()+". An installed plugin keeps running from Codex's cache; run setup to write the files again")
	}
	if len(o.blockers) > 0 {
		add(clientID, "client", e.codex, "Codex cannot list plugins because "+unreadableSentence(o.blockers)+": "+o.unreadable.Error()+". Repair or remove it in Codex; setup cannot tell the state of the plugin until then")
	}
	if !programExists(e.def.Command) {
		add(programID, "program", e.def.Command, "the sidera program the plugin starts, "+e.def.Command+", does not exist")
	}
	if o.codexErr != nil {
		return out
	}
	if o.pluginKnown() && o.pl.Installed && !o.pl.Enabled {
		add(pluginItem, kindPlugin, pluginID, `the plugin is disabled, so Codex ignores it: enable it in Codex (set enabled = true under [plugins."sidera@sidera"] in its config.toml), or run `+"`sidera adapter setup "+e.req.Instance+" --remove`"+` to take it out. Setup never enables it`)
	}
	running := o.pluginKnown() && o.pl.Installed && o.pl.Enabled
	switch {
	case running && !o.srv.Found:
		add(pluginItem, kindPlugin, pluginID, "Codex does not offer the sidera MCP server although the plugin is installed: this Codex may no longer read the plugin layout Sidera writes (see the Codex adapter reference for the fallback)")
	case o.srv.Found && !e.def.matches(o.srv) && !stale && !rec.IsDeclined(mcpServerID):
		add(mcpServerID, kindServer, serverName, "an MCP server named sidera that is not the plugin's (another program or arguments, not approved in advance, or disabled) is configured in Codex and hides it; run setup, which offers to remove it")
	}
	return out
}

func isStale(r adaptersetup.Reconciliation, id string) bool {
	return slices.ContainsFunc(r.Stale, func(w adaptersetup.Wanted) bool { return w.ID == id })
}

// Check implements adaptersetup.Handler.
func (h Handler) Check(ctx context.Context, req adaptersetup.Request) (adaptersetup.Check, error) {
	e, err := h.env(req, true, true)
	if err != nil {
		return adaptersetup.Check{}, err
	}
	rec, err := adaptersetup.ReadRecord(req.StateDir)
	if err != nil {
		return adaptersetup.Check{}, err
	}
	if !rec.Installed() {
		// Nothing was done, so there is nothing to look at in Codex.
		r, err := adaptersetup.Reconcile(rec, e.wanted(), nil)
		return adaptersetup.Check{Drift: r.CheckDrift()}, err
	}
	o := e.observe(ctx)
	r, err := adaptersetup.Reconcile(rec, e.wanted(), e.probe(o))
	if err != nil {
		return adaptersetup.Check{}, err
	}
	return adaptersetup.Check{Recorded: true, Drift: append(r.CheckDrift(), e.broken(o, rec, isStale(r, pluginItem))...)}, nil
}

// Refresh implements adaptersetup.Handler. It rewrites the source files and installs
// the plugin again when the recorded ones are out of date; it asks nothing, creates
// nothing and does not touch the plugin of a person who disabled it. It acts only when
// Codex answers: the plugin's files and Codex's cache are one thing to keep in step.
func (h Handler) Refresh(ctx context.Context, req adaptersetup.Request) (adaptersetup.Refresh, error) {
	rec, err := adaptersetup.ReadRecord(req.StateDir)
	if err != nil || !rec.Installed() {
		return adaptersetup.Refresh{}, err
	}
	e, err := h.env(req, true, true)
	if err != nil {
		return adaptersetup.Refresh{}, err
	}
	o := e.observe(ctx)
	r, err := adaptersetup.Reconcile(rec, e.wanted(), e.probe(o))
	if err != nil {
		return adaptersetup.Refresh{}, err
	}
	out := adaptersetup.Refresh{Recorded: true}
	if o.codexErr != nil {
		out.Drift = append(r.Drift, e.broken(o, rec, false)...)
		return out, nil
	}
	// The plugin is installed from the source files through the marketplace; with either
	// one gone or changed there is nothing a refresh may install it from.
	sourceUsable := o.srcErr == nil
	for _, d := range r.Drift {
		if d.ID == sourceID || d.ID == marketplID {
			sourceUsable = false
		}
	}
	dirty := false
	if isStale(r, sourceID) && o.srcErr == nil {
		it := adaptersetup.Item{ID: sourceID, Kind: kindSource, Target: e.dir}
		if err := writeBundle(e.dir, e.want); err != nil {
			out.Outcomes = append(out.Outcomes, failure(it, err))
			sourceUsable = false
		} else {
			rec.Set(adaptersetup.RecordItem{ID: sourceID, Kind: kindSource, Target: e.dir, Digest: e.want.digest(), Version: h.Version})
			out.Outcomes = append(out.Outcomes, outcome(it.ID, it.Kind, it.Target, adaptersetup.OutcomeDone, ""))
			dirty = true
		}
	}
	pluginStale := isStale(r, pluginItem)
	if pluginStale {
		it := adaptersetup.Item{ID: pluginItem, Kind: kindPlugin, Target: e.pluginTarget()}
		switch {
		case !sourceUsable:
			out.Outcomes = append(out.Outcomes, outcome(it.ID, it.Kind, it.Target, adaptersetup.OutcomeSkipped, "the plugin's files or its marketplace are missing or changed, or the files could not be read or written; run setup"))
		case !o.pluginKnown():
			out.Outcomes = append(out.Outcomes, outcome(it.ID, it.Kind, it.Target, adaptersetup.OutcomeSkipped, "Codex cannot list plugins, so whether the plugin is disabled is not known; installing it again would enable it"))
		case !o.pl.Enabled:
			out.Outcomes = append(out.Outcomes, outcome(it.ID, it.Kind, it.Target, adaptersetup.OutcomeSkipped, "the plugin is disabled; installing it again would enable it"))
		default:
			installed, err := e.installPlugin(ctx)
			if installed {
				rec.Set(adaptersetup.RecordItem{ID: pluginItem, Kind: kindPlugin, Target: e.pluginTarget(), Digest: e.want.pluginDigest(), Version: h.Version})
				dirty = true
			}
			if err != nil {
				out.Outcomes = append(out.Outcomes, failure(it, err))
			} else {
				out.Outcomes = append(out.Outcomes, outcome(it.ID, it.Kind, it.Target, adaptersetup.OutcomeDone, ""))
			}
		}
	}
	if dirty {
		e.stamp(rec)
		if err := adaptersetup.WriteRecord(req.StateDir, *rec); err != nil {
			return adaptersetup.Refresh{}, err
		}
	}
	e.noteScope(out.Outcomes)
	out.Drift = append(r.Drift, e.broken(o, rec, pluginStale)...)
	return out, nil
}
