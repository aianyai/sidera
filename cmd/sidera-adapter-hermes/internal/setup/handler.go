// Package setup is the client setup of the Hermes adapter, in the contract every adapter's
// setup speaks (sdk/go/adaptersetup): the Sidera Skill placed in the skills directory of
// each Hermes profile whose agents delegate, and the advice on what else Hermes needs.
package setup

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/config"
	"github.com/aianyai/sidera/internal/release"
	"github.com/aianyai/sidera/sdk/go/adaptersetup"
)

// Kinds of the items and of the findings.
const (
	kindSkill    = "skill"
	kindToolsets = "toolsets"
	kindNotifier = "notifier"
)

// Handler is the setup of the Hermes adapter. NewHermes makes the Hermes an instance
// configuration names; a test gives one that answers from a script.
type Handler struct {
	NewHermes func(config.Config) Hermes
}

// NewHandler is the Handler of the program.
func NewHandler() Handler { return Handler{NewHermes: NewHermes} }

// itemID is the id of the item that places the Skill in a profile.
func itemID(profile string) string { return kindSkill + ":" + profile }

// profileOf is the profile an item id names; ok is false for an id that is not one.
func profileOf(id string) (string, bool) { return strings.CutPrefix(id, kindSkill+":") }

// session is one request of one instance: the decoded configuration, the profiles and
// platforms the options and the record name, and what has been learnt from Hermes
// meanwhile.
type session struct {
	req      adaptersetup.Request
	cfg      config.Config
	hermes   Hermes
	profiles []string
	// platforms are the platforms setup was run with, those of this request and of the
	// earlier runs; remembered in a file of the adapter's own.
	platforms []string
	skill     []byte
	configs   map[string]string
}

// options are the words after `--` on the command line of `sidera adapter setup`.
type options struct{ profiles, platforms []string }

func parseOptions(words []string) (options, error) {
	var o options
	fs := flag.NewFlagSet("options", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Var((*list)(&o.profiles), "profile", "")
	fs.Var((*list)(&o.platforms), "platform", "")
	if err := fs.Parse(words); err != nil {
		return o, err
	}
	if fs.NArg() != 0 {
		return o, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	for _, p := range o.profiles {
		if err := config.ValidateProfileName(p); err != nil {
			return o, fmt.Errorf("--profile: %w", err)
		}
	}
	return o, nil
}

type list []string

func (l *list) String() string     { return strings.Join(*l, ",") }
func (l *list) Set(v string) error { *l = append(*l, v); return nil }

const optionsHelp = "the options of this adapter's setup are --profile <name> (a Hermes profile whose agents delegate; repeatable; default: home_profile) and --platform <name> (a platform whose agents delegate; repeatable; default: home_platform; setup remembers the platforms, and so does --check)"

// open decodes the configuration and the options of a request. The profiles are those the
// options name, with those of the record: a refresh gets no options from `sidera update`
// and must still look after what was set up. With neither, the home profile. The platforms
// are those the options name, with those setup was run with before (platforms.go), and with
// neither the home platform.
func (h Handler) open(req adaptersetup.Request, rec *adaptersetup.Record) (*session, error) {
	o, err := parseOptions(req.Options)
	if errors.Is(err, flag.ErrHelp) {
		return nil, errors.New(optionsHelp)
	}
	if err != nil {
		return nil, fmt.Errorf("%w; %s", err, optionsHelp)
	}
	cfg, err := config.Decode(req.Config)
	if err != nil {
		return nil, fmt.Errorf("the instance configuration: %w", err)
	}
	s := &session{req: req, cfg: cfg, hermes: h.NewHermes(cfg), configs: map[string]string{}}
	profiles := o.profiles
	if rec != nil {
		for _, it := range rec.Items {
			if p, ok := profileOf(it.ID); ok {
				profiles = append(profiles, p)
			}
		}
	}
	if len(profiles) == 0 {
		profiles = []string{cfg.Home.Profile}
	}
	s.profiles = slices.Compact(slices.Sorted(slices.Values(profiles)))
	platforms := append(o.platforms, readPlatforms(req.StateDir)...)
	if len(platforms) == 0 {
		platforms = []string{cfg.Home.Platform}
	}
	s.platforms = slices.Compact(slices.Sorted(slices.Values(platforms)))
	if s.skill, err = Skill(cfg.Board, cfg.Aliases); err != nil {
		return nil, err
	}
	return s, nil
}

// configFile is the config.yaml of a profile, asked of Hermes once.
func (s *session) configFile(ctx context.Context, profile string) (string, error) {
	if f, ok := s.configs[profile]; ok {
		return f, nil
	}
	f, err := s.hermes.ConfigFile(ctx, profile)
	if err != nil {
		return "", err
	}
	s.configs[profile] = f
	return f, nil
}

// skillFile is where a profile keeps the Skill. Hermes finds a skill in any directory under
// the profile's skills directory that holds a SKILL.md, and lists one it did not install as
// local; `hermes skills install` takes no local path, so the file is placed there.
func (s *session) skillFile(ctx context.Context, profile string) (string, error) {
	f, err := s.configFile(ctx, profile)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(f), "skills", SkillName, "SKILL.md"), nil
}

func (s *session) wantedDigest() string { return adaptersetup.Digest(s.skill) }

func digestOfFile(path string) (digest string, exists bool, err error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return adaptersetup.Digest(data), true, nil
}

// restart is what a profile's agents need before they can use the Skill: Hermes reads its
// Skills and toolsets when a gateway starts, and setup restarts nothing, since a restart
// interrupts the conversations the gateway is serving.
func restart(profile string) string {
	return "restart the gateway that serves profile " + profile + " for the Skill to take effect (by default one host gateway serves every profile; a profile with its own gateway, gateway.standalone, is restarted on its own)"
}

// noCopy ends the summary of an item that replaces a file nobody but the person wrote:
// the old content is gone afterwards.
const noCopy = " (no copy is kept; --yes accepts this)"

// Plan implements adaptersetup.Handler.
func (h Handler) Plan(ctx context.Context, req adaptersetup.Request) ([]adaptersetup.Item, error) {
	rec, err := adaptersetup.ReadRecord(req.StateDir)
	if err != nil {
		return nil, err
	}
	s, err := h.open(req, rec)
	if err != nil {
		return nil, err
	}
	return s.plan(ctx, rec)
}

func (s *session) plan(ctx context.Context, rec *adaptersetup.Record) ([]adaptersetup.Item, error) {
	var items []adaptersetup.Item
	for _, profile := range s.profiles {
		target, err := s.skillFile(ctx, profile)
		if err != nil {
			return nil, err
		}
		digest, exists, err := digestOfFile(target)
		if err != nil {
			return nil, err
		}
		var recorded adaptersetup.RecordItem
		var isRecorded bool
		if rec != nil {
			recorded, isRecorded = rec.Find(itemID(profile))
		}
		it := adaptersetup.Item{ID: itemID(profile), Kind: kindSkill, Target: target, Consent: true}
		forProfile := "the agents of profile " + profile
		switch {
		case !exists:
			it.Action, it.Summary = adaptersetup.ActionCreate, "give "+forProfile+" the Sidera Skill, which explains how to delegate and answer"
		case digest == s.wantedDigest() && isRecorded && recorded.Digest == digest:
			continue
		case digest == s.wantedDigest():
			it.Action, it.Summary = adaptersetup.ActionAdopt, "keep the Sidera Skill that "+forProfile+" already have, as it is, in the setup record"
		case isRecorded && recorded.Digest == digest:
			it.Action, it.Summary = adaptersetup.ActionUpdate, "bring the Sidera Skill of "+forProfile+" up to date"
		case isRecorded:
			it.Action, it.Summary = adaptersetup.ActionUpdate, "replace the Sidera Skill of "+forProfile+", which was changed since setup wrote it, discarding that change"+noCopy
		default:
			it.Action, it.Summary = adaptersetup.ActionUpdate, "replace the file where "+forProfile+" keep the Sidera Skill, which setup did not write, discarding its content"+noCopy
		}
		items = append(items, it)
	}
	return items, nil
}

// finding is something Hermes lacks for the Skill to be of use.
type finding struct {
	id, kind, target, text string
}

// findings reads what Hermes lacks for the given profiles and platforms: the toolsets an
// agent delegates with, and the Kanban notifier. It changes nothing. A read that fails is
// a finding of its own: the person learns that the check could not be made.
func (s *session) findings(ctx context.Context, profiles, platforms []string) []finding {
	var out []finding
	for _, profile := range profiles {
		file, err := s.configFile(ctx, profile)
		if err != nil {
			out = append(out, finding{id: kindToolsets + ":" + profile, kind: kindToolsets, target: profile,
				text: fmt.Sprintf("could not find the configuration of profile %s to read its toolsets: %v", profile, err)})
			continue
		}
		for _, platform := range platforms {
			reading, err := s.hermes.Toolsets(ctx, profile, platform)
			id := kindToolsets + ":" + profile + ":" + platform
			switch {
			case err != nil:
				out = append(out, finding{id: id, kind: kindToolsets, target: file,
					text: fmt.Sprintf("could not read which toolsets profile %s has on platform %s: %v", profile, platform, err)})
			case len(reading.Missing) > 0:
				out = append(out, finding{id: id, kind: kindToolsets, target: file,
					text: ToolsetGap{Profile: profile, Platform: platform, ConfigFile: file, Enabled: reading.Enabled, Missing: reading.Missing}.Advice()})
			}
		}
	}
	off, err := s.hermes.NotifierDisabled(ctx, profiles)
	if err != nil {
		out = append(out, finding{id: kindNotifier, kind: kindNotifier, target: s.cfg.HermesHome,
			text: fmt.Sprintf("could not read whether the Kanban notifier is on in every profile: %v", err)})
	}
	for _, profile := range off {
		out = append(out, finding{id: kindNotifier + ":" + profile, kind: kindNotifier, target: s.cfg.HermesHome, text: notifierAdvice(profile)})
	}
	return out
}

func outcome(it adaptersetup.Item, status adaptersetup.OutcomeStatus, message string) adaptersetup.Outcome {
	return adaptersetup.Outcome{ID: it.ID, Kind: it.Kind, Target: it.Target, Status: status, Message: message}
}

// Apply implements adaptersetup.Handler. The plan is made again and the ids are matched
// against it: an item that no longer applies is skipped.
func (h Handler) Apply(ctx context.Context, req adaptersetup.Request) ([]adaptersetup.Outcome, error) {
	rec, err := adaptersetup.ReadRecord(req.StateDir)
	if err != nil {
		return nil, err
	}
	s, err := h.open(req, rec)
	if err != nil {
		return nil, err
	}
	planned, err := s.plan(ctx, rec)
	if err != nil {
		return nil, err
	}
	if rec == nil {
		rec = &adaptersetup.Record{}
	}
	kept := false
	var outcomes []adaptersetup.Outcome
	for _, id := range req.Accepted {
		i := slices.IndexFunc(planned, func(it adaptersetup.Item) bool { return it.ID == id })
		if i < 0 {
			outcomes = append(outcomes, adaptersetup.Outcome{ID: id, Status: adaptersetup.OutcomeSkipped, Message: "no longer in the plan"})
			continue
		}
		it := planned[i]
		profile, _ := profileOf(id)
		message := ""
		if it.Action != adaptersetup.ActionAdopt {
			if err := writeSkill(it.Target, s.skill); err != nil {
				outcomes = append(outcomes, outcome(it, adaptersetup.OutcomeFailed, err.Error()))
				continue
			}
			message = restart(profile)
		}
		s.keep(rec, it)
		kept = true
		outcomes = append(outcomes, outcome(it, adaptersetup.OutcomeDone, message))
	}
	// A record is written only when an item was put in it: one that holds none would be
	// a setup that exists and does nothing.
	if kept {
		// No item names the sidera program: the Skill never mentions it. The record keeps
		// the one setup was run with, which is what the field means when nothing in the
		// items depends on it.
		rec.AdapterVersion, rec.SideraProgram = release.Version(), req.SideraProgram
		if err := adaptersetup.WriteRecord(req.StateDir, *rec); err != nil {
			return nil, err
		}
		if err := writePlatforms(req.StateDir, s.platforms); err != nil {
			return nil, err
		}
	}
	return outcomes, nil
}

// keep records the Skill of an item as it stands now.
func (s *session) keep(rec *adaptersetup.Record, it adaptersetup.Item) {
	rec.Set(adaptersetup.RecordItem{ID: it.ID, Kind: it.Kind, Target: it.Target, Digest: s.wantedDigest(), Version: release.Version()})
}

// writeSkill writes the Skill with the modes Hermes gives its own files and directories.
// A SKILL.md that is a link into the owner's dotfiles stays a link and the file it leads to
// is written.
func writeSkill(path string, data []byte) error {
	return adaptersetup.WriteFile(path, data, 0o644, 0o755)
}

// Remove implements adaptersetup.Handler: every recorded Skill.
func (Handler) Remove(_ context.Context, req adaptersetup.Request) ([]adaptersetup.Item, error) {
	rec, err := adaptersetup.ReadRecord(req.StateDir)
	if err != nil || !rec.Installed() {
		return nil, err
	}
	var items []adaptersetup.Item
	for _, r := range rec.Items {
		profile, _ := profileOf(r.ID)
		summary := "delete the Sidera Skill of profile " + profile
		// A failed read is left to the undo, which reports it.
		if digest, exists, err := digestOfFile(r.Target); err == nil && exists && digest != r.Digest {
			summary = "stop recording the Sidera Skill of profile " + profile + ": it was changed since setup wrote it, so the file stays"
		}
		items = append(items, adaptersetup.Item{ID: r.ID, Kind: r.Kind, Target: r.Target, Action: adaptersetup.ActionRemove,
			Summary: summary, Consent: true})
	}
	return items, nil
}

// Undo implements adaptersetup.Handler. A file is deleted only while it is what setup
// wrote. One someone changed is left, and it leaves the record too: it is no longer
// setup's, and a record that kept it would keep the instance from being removed.
func (Handler) Undo(_ context.Context, req adaptersetup.Request) ([]adaptersetup.Outcome, error) {
	rec, err := adaptersetup.ReadRecord(req.StateDir)
	if err != nil {
		return nil, err
	}
	var outcomes []adaptersetup.Outcome
	for _, id := range req.Accepted {
		var r adaptersetup.RecordItem
		var ok bool
		if rec != nil {
			r, ok = rec.Find(id)
		}
		if !ok {
			outcomes = append(outcomes, adaptersetup.Outcome{ID: id, Status: adaptersetup.OutcomeSkipped, Message: "not recorded"})
			continue
		}
		it := adaptersetup.Item{ID: r.ID, Kind: r.Kind, Target: r.Target}
		digest, exists, err := digestOfFile(r.Target)
		switch {
		case err != nil:
			outcomes = append(outcomes, outcome(it, adaptersetup.OutcomeFailed, err.Error()))
			continue
		case exists && digest != r.Digest:
			rec.Drop(id)
			outcomes = append(outcomes, outcome(it, adaptersetup.OutcomeSkipped, "changed since setup wrote it, left in place and no longer recorded"))
			continue
		case exists:
			if err := os.Remove(r.Target); err != nil {
				outcomes = append(outcomes, outcome(it, adaptersetup.OutcomeFailed, err.Error()))
				continue
			}
			// The directory is the one setup made for the file; it goes when it is empty.
			os.Remove(filepath.Dir(r.Target))
		}
		rec.Drop(id)
		outcomes = append(outcomes, outcome(it, adaptersetup.OutcomeDone, ""))
	}
	if rec != nil {
		// The record is written as it stands; Handle removes one that holds no item (and
		// the declines with it). The platforms file is the adapter's own and goes with the
		// last Skill.
		if err := adaptersetup.WriteRecord(req.StateDir, *rec); err != nil {
			return nil, err
		}
		if !rec.Installed() {
			if err := removePlatforms(req.StateDir); err != nil {
				return nil, err
			}
		}
	}
	return outcomes, nil
}

// reconcile compares the Skill of each profile with the record. A recorded item is
// looked for where it was placed; one with no record has no place yet and is named by
// its profile.
func (s *session) reconcile(rec *adaptersetup.Record) (adaptersetup.Reconciliation, error) {
	wanted := make([]adaptersetup.Wanted, len(s.profiles))
	for i, profile := range s.profiles {
		target := "profile " + profile
		if rec != nil {
			if r, ok := rec.Find(itemID(profile)); ok {
				target = r.Target
			}
		}
		wanted[i] = adaptersetup.Wanted{ID: itemID(profile), Kind: kindSkill, Target: target, Digest: s.wantedDigest()}
	}
	return adaptersetup.Reconcile(rec, wanted, func(r adaptersetup.RecordItem) (string, bool, error) { return digestOfFile(r.Target) })
}

// Check implements adaptersetup.Handler. With a record that holds a Skill it also reads what
// Hermes lacks, as drift of the problem broken: the Skill is there and cannot work, and a
// check is where a person looks for what silently stops delegation. It reads the profiles
// and platforms setup was run with, and the home conversation's agent, which the gate
// watches; cron only when setup was run with it. With no record it says only what setup
// would have to make, without starting Hermes: the check before `adapter remove` runs for
// every instance.
func (h Handler) Check(ctx context.Context, req adaptersetup.Request) (adaptersetup.Check, error) {
	rec, err := adaptersetup.ReadRecord(req.StateDir)
	if err != nil {
		return adaptersetup.Check{}, err
	}
	s, err := h.open(req, rec)
	if err != nil {
		return adaptersetup.Check{}, err
	}
	r, err := s.reconcile(rec)
	if err != nil {
		return adaptersetup.Check{}, err
	}
	drift := r.CheckDrift()
	if rec.Installed() {
		profiles := withValue(s.profiles, s.cfg.Home.Profile)
		platforms := withValue(s.platforms, s.cfg.Home.Platform)
		for _, f := range s.findings(ctx, profiles, platforms) {
			drift = append(drift, adaptersetup.Drift{ID: f.id, Kind: f.kind, Target: f.target, Problem: adaptersetup.ProblemBroken, Summary: f.text})
		}
	}
	return adaptersetup.Check{Recorded: rec.Installed(), Drift: drift}, nil
}

// Notes implements adaptersetup.Noter: what Hermes lacks for the profiles and platforms a
// plan is about, to be changed by hand, and for cron, which an agent may delegate from as
// well as from a chat platform. A check says the same as drift and carries no note.
func (h Handler) Notes(ctx context.Context, req adaptersetup.Request) ([]string, error) {
	if req.Op != adaptersetup.OpPlan {
		return nil, nil
	}
	rec, err := adaptersetup.ReadRecord(req.StateDir)
	if err != nil {
		return nil, err
	}
	s, err := h.open(req, rec)
	if err != nil {
		return nil, err
	}
	var notes []string
	for _, f := range s.findings(ctx, s.profiles, withValue(s.platforms, CronPlatform)) {
		notes = append(notes, f.text)
	}
	return notes, nil
}

// Refresh implements adaptersetup.Handler: it rewrites the recorded Skills that are out
// of date (the alias table or the board changed, or a release changed the text) and
// asks Hermes nothing.
func (h Handler) Refresh(_ context.Context, req adaptersetup.Request) (adaptersetup.Refresh, error) {
	rec, err := adaptersetup.ReadRecord(req.StateDir)
	if err != nil || !rec.Installed() {
		return adaptersetup.Refresh{}, err
	}
	s, err := h.open(req, rec)
	if err != nil {
		return adaptersetup.Refresh{}, err
	}
	r, err := s.reconcile(rec)
	if err != nil {
		return adaptersetup.Refresh{}, err
	}
	out := adaptersetup.Refresh{Recorded: true, Drift: r.Drift}
	for _, w := range r.Stale {
		it := adaptersetup.Item{ID: w.ID, Kind: w.Kind, Target: w.Target, Action: adaptersetup.ActionUpdate}
		if err := writeSkill(w.Target, s.skill); err != nil {
			out.Outcomes = append(out.Outcomes, outcome(it, adaptersetup.OutcomeFailed, err.Error()))
			continue
		}
		s.keep(rec, it)
		profile, _ := profileOf(w.ID)
		out.Outcomes = append(out.Outcomes, outcome(it, adaptersetup.OutcomeDone, restart(profile)))
	}
	if len(out.Outcomes) > 0 {
		rec.AdapterVersion, rec.SideraProgram = release.Version(), req.SideraProgram
		if err := adaptersetup.WriteRecord(req.StateDir, *rec); err != nil {
			return adaptersetup.Refresh{}, err
		}
	}
	return out, nil
}
