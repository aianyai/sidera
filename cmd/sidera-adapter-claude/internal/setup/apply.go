package setup

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/aianyai/sidera/sdk/go/adaptersetup"
)

// Apply implements adaptersetup.Handler. It does the accepted items of a plan made now,
// in the plan's order, and keeps the record up to date after each, so that a crash
// leaves a record of what was done. An accepted id the plan no longer lists is
// skipped.
func (h *Handler) Apply(ctx context.Context, req adaptersetup.Request) ([]adaptersetup.Outcome, error) {
	w, err := h.look(ctx, req, false)
	if err != nil {
		return nil, err
	}
	planned, err := w.plan()
	if err != nil {
		return nil, err
	}
	var outcomes []adaptersetup.Outcome
	for _, it := range planned {
		if !contains(req.Accepted, it.ID) {
			continue
		}
		if err := w.execute(ctx, it.ID, it.Action); err != nil {
			outcomes = append(outcomes, outcomeOf(it.ID, w.d, adaptersetup.OutcomeFailed, err.Error()))
			continue
		}
		if err := w.remember(h, it.ID); err != nil {
			return nil, err
		}
		outcomes = append(outcomes, outcomeOf(it.ID, w.d, adaptersetup.OutcomeDone, ""))
	}
	for _, id := range req.Accepted {
		if !hasItem(planned, id) {
			outcomes = append(outcomes, adaptersetup.Outcome{ID: id, Status: adaptersetup.OutcomeSkipped, Message: "setup has nothing to do for it now"})
		}
	}
	return outcomes, nil
}

func hasItem(items []adaptersetup.Item, id string) bool {
	for _, it := range items {
		if it.ID == id {
			return true
		}
	}
	return false
}

func contains(ids []string, id string) bool {
	for _, s := range ids {
		if s == id {
			return true
		}
	}
	return false
}

func outcomeOf(id string, d *desired, status adaptersetup.OutcomeStatus, message string) adaptersetup.Outcome {
	return adaptersetup.Outcome{ID: id, Kind: d.kind(id), Target: d.target(id), Status: status, Message: message}
}

// execute makes the item what is wanted. Adopting changes nothing in Claude Code.
func (w *world) execute(ctx context.Context, id string, action adaptersetup.Action) error {
	if action == adaptersetup.ActionAdopt {
		return nil
	}
	if id == idSource {
		return writeSource(w.d.srcDir, w.d.files)
	}
	if id == idPermission {
		return w.allowTools()
	}
	if w.client == nil {
		return w.obs.client
	}
	update := action == adaptersetup.ActionUpdate
	switch id {
	case idMarketplace:
		return w.client.AddMarketplace(ctx, w.d.srcDir)
	case idPlugin:
		if !update {
			return w.client.InstallPlugin(ctx)
		}
		if err := w.client.UpdateMarketplace(ctx); err != nil {
			return err
		}
		return w.client.UpdatePlugin(ctx)
	case idMCP:
		if update {
			if err := w.client.RemoveMCPServer(ctx); err != nil {
				return err
			}
		}
		if err := w.client.AddMCPServer(ctx, w.d.command, w.d.args); err != nil {
			if update {
				return fmt.Errorf("the earlier registration was removed and the new one could not be added; run setup again: %w", err)
			}
			return err
		}
		return nil
	}
	return fmt.Errorf("setup has no way to do %q", id)
}

// allowTools adds the rule to permissions.allow unless it is there.
func (w *world) allowTools() error {
	_, err := adaptersetup.EditJSONFile(w.d.settings, func(root *adaptersetup.Object) (bool, error) {
		return root.AddString(permissionPath, permissionRule)
	})
	if err != nil {
		return settingsError(w.d.settings, err)
	}
	return nil
}

// remember records the item as it is now wanted, and writes the record.
func (w *world) remember(h *Handler, id string) error {
	if w.rec == nil {
		w.rec = &adaptersetup.Record{}
	}
	w.rec.AdapterVersion = h.o.Version
	// The program the registration names (the instance's sidera_path wins over the
	// program that ran setup); one that cannot be found is not what any item names.
	if w.d.programErr == nil {
		w.rec.SideraProgram = w.d.command
	}
	w.rec.Set(adaptersetup.RecordItem{ID: id, Kind: w.d.kind(id), Target: w.d.target(id), Digest: w.d.digest[id], Version: h.o.Version})
	return adaptersetup.WriteRecord(w.d.req.StateDir, *w.rec)
}

// Refresh implements adaptersetup.Handler. It rewrites the recorded items that are
// intact and out of date: the plugin's source, the installed plugin when its version
// is behind, the registration when the program moved. It makes nothing that is gone.
func (h *Handler) Refresh(ctx context.Context, req adaptersetup.Request) (adaptersetup.Refresh, error) {
	// Without a record of something setup did there is nothing to refresh, and Claude
	// Code is not asked.
	if rec, err := adaptersetup.ReadRecord(req.StateDir); err != nil || !rec.Installed() {
		return adaptersetup.Refresh{}, err
	}
	w, err := h.look(ctx, req, true)
	if err != nil {
		return adaptersetup.Refresh{}, err
	}
	rc, err := w.reconcile()
	if err != nil {
		return adaptersetup.Refresh{}, err
	}
	out := adaptersetup.Refresh{Recorded: true, Drift: append(rc.Drift, w.problems()...)}
	sourceFailed := false
	for _, id := range planOrder {
		if !isStale(rc, id) {
			continue
		}
		var err error
		switch {
		case id == idPlugin && sourceFailed:
			err = errors.New("the plugin's source could not be rewritten, so the plugin is not updated from it")
		default:
			err = w.execute(ctx, id, adaptersetup.ActionUpdate)
		}
		if err != nil {
			sourceFailed = sourceFailed || id == idSource
			out.Outcomes = append(out.Outcomes, outcomeOf(id, w.d, adaptersetup.OutcomeFailed, err.Error()))
			continue
		}
		if err := w.remember(h, id); err != nil {
			return adaptersetup.Refresh{}, err
		}
		out.Outcomes = append(out.Outcomes, outcomeOf(id, w.d, adaptersetup.OutcomeDone, w.scopeNote()))
	}
	return out, nil
}

func isStale(rc adaptersetup.Reconciliation, id string) bool {
	for _, w := range rc.Stale {
		if w.ID == id {
			return true
		}
	}
	return false
}

// reconcile compares the record with what is wanted and seen. An item that could not
// be looked at is left out (it is reported by problems), and so is a permission rule
// that was there before setup and so is not setup's. What a person declined is not
// drift (Reconcile leaves it out). A registration is never rewritten with a program
// that cannot be found, and a plugin whose installed copy is behind its source is
// stale whatever the record says.
func (w *world) reconcile() (adaptersetup.Reconciliation, error) {
	var wanted []adaptersetup.Wanted
	for _, id := range planOrder {
		s := w.obs.items[id]
		if s.err != nil || s.unseen {
			continue
		}
		if _, recorded := w.recorded(id); id == idPermission && s.exists && !recorded {
			continue
		}
		wanted = append(wanted, w.d.wanted(id))
	}
	rc, err := adaptersetup.Reconcile(w.rec, wanted, func(r adaptersetup.RecordItem) (string, bool, error) {
		s := w.obs.items[r.ID]
		return s.digest, s.exists, nil
	})
	if err != nil {
		return rc, err
	}
	if w.d.programErr != nil {
		rc.Stale = slices.DeleteFunc(rc.Stale, func(it adaptersetup.Wanted) bool { return it.ID == idMCP })
	}
	if _, recorded := w.recorded(idPlugin); recorded && w.pluginBehind() && !isStale(rc, idPlugin) &&
		!slices.ContainsFunc(rc.Drift, func(d adaptersetup.Drift) bool { return d.ID == idPlugin }) {
		for _, it := range wanted {
			if it.ID == idPlugin {
				rc.Stale = append(rc.Stale, it)
			}
		}
	}
	return rc, nil
}

func (d *desired) wanted(id string) adaptersetup.Wanted {
	return adaptersetup.Wanted{ID: id, Kind: d.kind(id), Target: d.target(id), Digest: d.digest[id]}
}

// Check implements adaptersetup.Handler.
func (h *Handler) Check(ctx context.Context, req adaptersetup.Request) (adaptersetup.Check, error) {
	rec, err := adaptersetup.ReadRecord(req.StateDir)
	if err != nil {
		return adaptersetup.Check{}, err
	}
	if !rec.Installed() {
		// Setup did nothing for this instance, so there is nothing to look at: Claude
		// Code is not run, and the answer is that everything is still to set up (but
		// what a person declined).
		d, err := h.desire(req, h.current())
		if err != nil {
			return adaptersetup.Check{}, err
		}
		var wanted []adaptersetup.Wanted
		for _, id := range planOrder {
			wanted = append(wanted, d.wanted(id))
		}
		rc, err := adaptersetup.Reconcile(rec, wanted, nil)
		return adaptersetup.Check{Drift: rc.CheckDrift()}, err
	}
	w, err := h.look(ctx, req, true)
	if err != nil {
		return adaptersetup.Check{}, err
	}
	rc, err := w.reconcile()
	if err != nil {
		return adaptersetup.Check{}, err
	}
	return adaptersetup.Check{Recorded: w.rec.Installed(), Drift: append(rc.CheckDrift(), w.problems()...)}, nil
}

// problems are the ways setup can be broken that the record cannot show: Claude Code
// cannot be run, an item cannot be looked at, the plugin is disabled or lost its
// source, the program is gone.
func (w *world) problems() []adaptersetup.Drift {
	var out []adaptersetup.Drift
	broken := func(id, kind, target, summary string) {
		out = append(out, adaptersetup.Drift{ID: id, Kind: kind, Target: target, Problem: adaptersetup.ProblemBroken, Summary: summary})
	}
	if w.obs.client != nil {
		broken("claude", "client", "claude", w.obs.client.Error())
	}
	for _, id := range planOrder {
		if err := w.obs.items[id].err; err != nil {
			broken(id, w.d.kind(id), w.d.target(id), err.Error())
		}
	}
	if w.d.programErr != nil {
		broken("program", "program", w.d.command, w.d.programErr.Error())
	}
	if !w.rec.Installed() {
		return out
	}
	if p := w.obs.plugin; p != nil {
		if !p.Enabled {
			broken(idPlugin, w.d.kind(idPlugin), pluginID, "the plugin is disabled in Claude Code, so sessions are not offered the Sidera Skill; enable it with `claude plugin enable "+pluginID+"`")
		}
		if len(p.Errors) > 0 {
			broken(idPlugin, w.d.kind(idPlugin), pluginID, fmt.Sprintf("Claude Code reports the plugin as failing to load (%v); if its source directory is gone, run setup to write it again", p.Errors))
		}
	}
	if problem := w.d.programProblem(); problem != "" && w.d.programErr == nil {
		broken("program", "program", w.d.command, problem)
	}
	return out
}
