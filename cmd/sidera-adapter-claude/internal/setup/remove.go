package setup

import (
	"context"
	"errors"
	"slices"

	"github.com/aianyai/sidera/sdk/go/adaptersetup"
)

// Remove implements adaptersetup.Handler. It lists the recorded items in the order they
// are undone; the Skill's source is deleted only after the marketplace that points at it
// is removed, and that only after the plugin installed from it.
func (h *Handler) Remove(_ context.Context, req adaptersetup.Request) ([]adaptersetup.Item, error) {
	rec, err := adaptersetup.ReadRecord(req.StateDir)
	if err != nil || !rec.Installed() {
		return nil, err
	}
	var items []adaptersetup.Item
	for _, id := range removeOrder {
		r, ok := rec.Find(id)
		if !ok {
			continue
		}
		it := adaptersetup.Item{ID: id, Kind: r.Kind, Target: r.Target, Action: adaptersetup.ActionRemove, Summary: removeSummary(id, r.Target), Consent: id != idSource}
		switch id {
		case idMarketplace:
			it.Requires = listed(items, idPlugin)
		case idSource:
			it.Requires = append(listed(items, idMarketplace), listed(items, idPlugin)...)
		}
		items = append(items, it)
	}
	return items, nil
}

func removeSummary(id, target string) string {
	switch id {
	case idSource:
		return "delete the Sidera plugin's source in the adapter's state directory"
	case idMarketplace:
		return "remove the marketplace " + marketplaceName + " from Claude Code"
	case idPlugin:
		return "uninstall the plugin " + pluginID + " from Claude Code"
	case idMCP:
		return "remove the registration of the " + serverName + " MCP server from Claude Code"
	default:
		return "remove " + permissionRule + " from permissions.allow in " + target
	}
}

// Undo implements adaptersetup.Handler. An item that is already gone is done. One that
// is no longer what setup made (the registration was changed, the marketplace now
// points elsewhere) is left alone and forgotten: it is not setup's any more.
func (h *Handler) Undo(ctx context.Context, req adaptersetup.Request) ([]adaptersetup.Outcome, error) {
	rec, err := adaptersetup.ReadRecord(req.StateDir)
	if err != nil {
		return nil, err
	}
	current := h.current()
	cfg := actingConfiguration(rec, current)
	d := h.places(req, cfg)
	client, clientErr := h.client(req, cfg)
	scope := differentConfiguration(cfg, current)
	var outcomes []adaptersetup.Outcome
	for _, id := range removeOrder {
		if !contains(req.Accepted, id) {
			continue
		}
		r, ok := recordedItem(rec, id)
		if !ok {
			outcomes = append(outcomes, adaptersetup.Outcome{ID: id, Status: adaptersetup.OutcomeSkipped, Message: "setup did not record it"})
			continue
		}
		status, message := undo(ctx, d, client, clientErr, r)
		if scope != "" && message != "" {
			message += "; " + scope
		} else if scope != "" {
			message = scope
		}
		outcomes = append(outcomes, adaptersetup.Outcome{ID: id, Kind: r.Kind, Target: r.Target, Status: status, Message: message})
		if status != adaptersetup.OutcomeFailed {
			rec.Drop(id)
		}
	}
	for _, id := range req.Accepted {
		if !contains(removeOrder, id) {
			outcomes = append(outcomes, adaptersetup.Outcome{ID: id, Status: adaptersetup.OutcomeSkipped, Message: "not an item of setup"})
		}
	}
	if rec == nil {
		return outcomes, nil
	}
	// Handle removes a record that holds no item after an undo.
	return outcomes, adaptersetup.WriteRecord(req.StateDir, *rec)
}

func recordedItem(rec *adaptersetup.Record, id string) (adaptersetup.RecordItem, bool) {
	if rec == nil {
		return adaptersetup.RecordItem{}, false
	}
	return rec.Find(id)
}

// undo undoes one recorded item and says what became of it.
func undo(ctx context.Context, d *desired, client Client, clientErr error, r adaptersetup.RecordItem) (adaptersetup.OutcomeStatus, string) {
	failed := func(err error) (adaptersetup.OutcomeStatus, string) { return adaptersetup.OutcomeFailed, err.Error() }
	gone := func() (adaptersetup.OutcomeStatus, string) {
		return adaptersetup.OutcomeDone, "it was already gone"
	}
	left := func(why string) (adaptersetup.OutcomeStatus, string) {
		return adaptersetup.OutcomeSkipped, why + "; left as it is and no longer kept by setup"
	}
	switch r.ID {
	case idSource:
		if err := removeSource(d.srcDir); err != nil {
			return failed(err)
		}
		return adaptersetup.OutcomeDone, ""
	case idPermission:
		_, err := adaptersetup.EditJSONFile(d.settings, func(root *adaptersetup.Object) (bool, error) {
			return root.RemoveString(permissionPath, permissionRule)
		})
		if err != nil {
			return failed(settingsError(d.settings, err))
		}
		return adaptersetup.OutcomeDone, ""
	}
	if client == nil {
		return failed(clientErr)
	}
	switch r.ID {
	case idPlugin:
		plugins, err := client.Plugins(ctx)
		if err != nil {
			return failed(err)
		}
		if !slices.ContainsFunc(plugins, func(p Plugin) bool { return p.ID == pluginID }) {
			return gone()
		}
		// The plugin is setup's only while its marketplace is: a marketplace named
		// sidera that points elsewhere is another installation's, and so is the plugin
		// installed from it.
		list, err := client.Marketplaces(ctx)
		if err != nil {
			return failed(err)
		}
		for _, m := range list {
			if m.Name == marketplaceName && !samePath(m.Path, d.srcDir) {
				return left("the marketplace " + marketplaceName + " now points at " + m.Path + ", so the plugin is not the one setup installed")
			}
		}
		if err := client.UninstallPlugin(ctx); err != nil {
			return failed(err)
		}
		return adaptersetup.OutcomeDone, ""
	case idMarketplace:
		list, err := client.Marketplaces(ctx)
		if err != nil {
			return failed(err)
		}
		for _, m := range list {
			if m.Name != marketplaceName {
				continue
			}
			if !samePath(m.Path, d.srcDir) {
				return left("the marketplace now points at " + m.Path)
			}
			if err := client.RemoveMarketplace(ctx); err != nil {
				return failed(err)
			}
			return adaptersetup.OutcomeDone, ""
		}
		return gone()
	case idMCP:
		server, err := client.MCPServer(ctx)
		switch {
		case err != nil:
			return failed(err)
		case server == nil:
			return gone()
		case digestOfServer(*server) != r.Digest:
			return left("the registration was changed since setup made it")
		}
		if err := client.RemoveMCPServer(ctx); err != nil {
			return failed(err)
		}
		return adaptersetup.OutcomeDone, ""
	}
	return failed(errors.New("setup has no way to undo it"))
}
