package fakeadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/aianyai/sidera/sdk/go/adapterkit"
	"github.com/aianyai/sidera/sdk/go/adaptersetup"
	"github.com/aianyai/sidera/sdk/go/manifest"
)

// SetupVersion is the version the fake setup records its items with.
const SetupVersion = "fake-1"

// Kinds of the items of the fake setup. A client file lives in the directory the
// instance configuration names and needs consent; an own file lives in the state
// directory and does not.
const (
	KindClientFile = "client_file"
	KindOwnFile    = "own_file"
)

// SetupField declares the key "setup" of the instance configuration for a manifest
// whose values the daemon checks. No field type holds an object, so a script that
// goes through the daemon is written as the JSON text of a SetupConfig in this
// string; a script that does not (one handed to the setup directly) may be the
// object itself.
var SetupField = manifest.Field{Key: "setup", Type: manifest.FieldString, Title: "Setup script"}

// SetupConfig is what the fake setup reads from the instance configuration, under
// the key "setup": a script for the setup of a test, in the form of files to keep.
type SetupConfig struct {
	// ClientDir is where the client files are; required by an item that needs
	// consent.
	ClientDir string `json:"client_dir,omitempty"`
	// Items are the files setup wants, in order.
	Items []SetupItem `json:"items,omitempty"`
	// Fail lists ids whose apply, undo or refresh fails.
	Fail []string `json:"fail,omitempty"`
	// Notes are what a plan and a check tell the person besides their items and drift.
	Notes []string `json:"notes,omitempty"`
}

// SetupItem is one file the fake setup keeps.
type SetupItem struct {
	ID string `json:"id"`
	// Consent puts the file in the client directory and makes the CLI ask; without it
	// the file is in the state directory.
	Consent bool `json:"consent,omitempty"`
	// Content is what the file holds; a refresh rewrites a recorded file whose
	// content differs.
	Content string `json:"content"`
	// Requires lists ids of earlier items that this one cannot be done without (the
	// Requires member of the contract's items).
	Requires []string `json:"requires,omitempty"`
}

// SetupCommand is the `setup` command of the fake adapter program.
func SetupCommand() adapterkit.Command {
	return adaptersetup.Command("sidera-fake-adapter", SetupHandler{})
}

// SetupHandler is a setup whose work is files, scripted by the instance
// configuration. It implements the whole contract with the shared helpers the real
// adapters use, so a test of the core CLI against it is a test of the contract.
type SetupHandler struct{}

type fakeSetup struct {
	req   adaptersetup.Request
	cfg   SetupConfig
	items map[string]SetupItem
}

func load(req adaptersetup.Request) (*fakeSetup, error) {
	f := &fakeSetup{req: req, items: map[string]SetupItem{}}
	if raw, ok := req.Config["setup"]; ok {
		var text string
		if json.Unmarshal(raw, &text) == nil {
			raw = json.RawMessage(text)
		}
		if err := json.Unmarshal(raw, &f.cfg); err != nil {
			return nil, fmt.Errorf("the setup script of the instance configuration: %w", err)
		}
	}
	for _, it := range f.cfg.Items {
		f.items[it.ID] = it
	}
	return f, nil
}

func (f *fakeSetup) path(id string, consent bool) string {
	if consent {
		return filepath.Join(f.cfg.ClientDir, id)
	}
	return filepath.Join(f.req.StateDir, "setup", "files", id)
}

func kindOf(consent bool) string {
	if consent {
		return KindClientFile
	}
	return KindOwnFile
}

func (f *fakeSetup) item(it SetupItem, action adaptersetup.Action) adaptersetup.Item {
	summary := "write " + it.ID
	if len(f.req.Options) > 0 {
		summary += " (options: " + strings.Join(f.req.Options, " ") + ")"
	}
	return adaptersetup.Item{ID: it.ID, Kind: kindOf(it.Consent), Target: f.path(it.ID, it.Consent), Action: action, Summary: summary, Consent: it.Consent, Requires: it.Requires}
}

func (f *fakeSetup) fails(id string) bool { return slices.Contains(f.cfg.Fail, id) }

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

// plan lists what setup would do: nothing for a recorded file that is as wanted.
func (f *fakeSetup) plan() ([]adaptersetup.Item, error) {
	rec, err := adaptersetup.ReadRecord(f.req.StateDir)
	if err != nil {
		return nil, err
	}
	var items []adaptersetup.Item
	for _, it := range f.cfg.Items {
		path := f.path(it.ID, it.Consent)
		digest, exists, err := digestOfFile(path)
		if err != nil {
			return nil, err
		}
		isRecorded := false
		if rec != nil {
			_, isRecorded = rec.Find(it.ID)
		}
		want := adaptersetup.Digest([]byte(it.Content))
		switch {
		case !exists:
			items = append(items, f.item(it, adaptersetup.ActionCreate))
		case digest != want:
			items = append(items, f.item(it, adaptersetup.ActionUpdate))
		case !isRecorded:
			items = append(items, f.item(it, adaptersetup.ActionAdopt))
		}
	}
	return items, nil
}

func (f *fakeSetup) write(it SetupItem, rec *adaptersetup.Record) error {
	if err := adaptersetup.WritePrivateFile(f.path(it.ID, it.Consent), []byte(it.Content)); err != nil {
		return err
	}
	rec.Set(adaptersetup.RecordItem{ID: it.ID, Kind: kindOf(it.Consent), Target: f.path(it.ID, it.Consent), Digest: adaptersetup.Digest([]byte(it.Content)), Version: SetupVersion})
	return nil
}

func (f *fakeSetup) record() (*adaptersetup.Record, error) {
	rec, err := adaptersetup.ReadRecord(f.req.StateDir)
	if err != nil {
		return nil, err
	}
	if rec == nil {
		rec = &adaptersetup.Record{}
	}
	rec.AdapterVersion, rec.SideraProgram = SetupVersion, f.req.SideraProgram
	return rec, nil
}

func outcome(it adaptersetup.Item, status adaptersetup.OutcomeStatus, message string) adaptersetup.Outcome {
	return adaptersetup.Outcome{ID: it.ID, Kind: it.Kind, Target: it.Target, Status: status, Message: message}
}

// Plan implements adaptersetup.Handler.
func (SetupHandler) Plan(_ context.Context, req adaptersetup.Request) ([]adaptersetup.Item, error) {
	f, err := load(req)
	if err != nil {
		return nil, err
	}
	return f.plan()
}

// Apply implements adaptersetup.Handler.
func (SetupHandler) Apply(_ context.Context, req adaptersetup.Request) ([]adaptersetup.Outcome, error) {
	f, err := load(req)
	if err != nil {
		return nil, err
	}
	planned, err := f.plan()
	if err != nil {
		return nil, err
	}
	rec, err := f.record()
	if err != nil {
		return nil, err
	}
	var outcomes []adaptersetup.Outcome
	for _, id := range req.Accepted {
		i := slices.IndexFunc(planned, func(it adaptersetup.Item) bool { return it.ID == id })
		if i < 0 {
			outcomes = append(outcomes, adaptersetup.Outcome{ID: id, Status: adaptersetup.OutcomeSkipped, Message: "not in the plan"})
			continue
		}
		it := planned[i]
		if f.fails(id) {
			outcomes = append(outcomes, outcome(it, adaptersetup.OutcomeFailed, "scripted to fail"))
			continue
		}
		if err := f.write(f.items[id], rec); err != nil {
			outcomes = append(outcomes, outcome(it, adaptersetup.OutcomeFailed, err.Error()))
			continue
		}
		outcomes = append(outcomes, outcome(it, adaptersetup.OutcomeDone, ""))
	}
	if err := adaptersetup.WriteRecord(req.StateDir, *rec); err != nil {
		return nil, err
	}
	return outcomes, nil
}

// Remove implements adaptersetup.Handler.
func (SetupHandler) Remove(_ context.Context, req adaptersetup.Request) ([]adaptersetup.Item, error) {
	rec, err := adaptersetup.ReadRecord(req.StateDir)
	if err != nil || rec == nil {
		return nil, err
	}
	f, err := load(req)
	if err != nil {
		return nil, err
	}
	var items []adaptersetup.Item
	for _, r := range rec.Items {
		items = append(items, adaptersetup.Item{ID: r.ID, Kind: r.Kind, Target: r.Target, Action: adaptersetup.ActionRemove,
			Summary: "delete " + r.ID, Consent: r.Kind == KindClientFile, Requires: f.items[r.ID].Requires})
	}
	return items, nil
}

// Undo implements adaptersetup.Handler. A file that is no longer what setup wrote is
// left alone.
func (SetupHandler) Undo(_ context.Context, req adaptersetup.Request) ([]adaptersetup.Outcome, error) {
	f, err := load(req)
	if err != nil {
		return nil, err
	}
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
		case f.fails(id):
			outcomes = append(outcomes, outcome(it, adaptersetup.OutcomeFailed, "scripted to fail"))
			continue
		case err != nil:
			outcomes = append(outcomes, outcome(it, adaptersetup.OutcomeFailed, err.Error()))
			continue
		case exists && digest != r.Digest:
			outcomes = append(outcomes, outcome(it, adaptersetup.OutcomeSkipped, "changed since setup, left alone"))
			continue
		case exists:
			if err := os.Remove(r.Target); err != nil {
				outcomes = append(outcomes, outcome(it, adaptersetup.OutcomeFailed, err.Error()))
				continue
			}
		}
		rec.Drop(id)
		outcomes = append(outcomes, outcome(it, adaptersetup.OutcomeDone, ""))
	}
	if rec != nil {
		if len(rec.Items) == 0 {
			err = adaptersetup.RemoveRecord(req.StateDir)
		} else {
			err = adaptersetup.WriteRecord(req.StateDir, *rec)
		}
		if err != nil {
			return nil, err
		}
	}
	return outcomes, nil
}

// reconcile compares the files the configuration wants with the record.
func (f *fakeSetup) reconcile(rec *adaptersetup.Record) (adaptersetup.Reconciliation, error) {
	wanted := make([]adaptersetup.Wanted, len(f.cfg.Items))
	for i, it := range f.cfg.Items {
		wanted[i] = adaptersetup.Wanted{ID: it.ID, Kind: kindOf(it.Consent), Target: f.path(it.ID, it.Consent), Digest: adaptersetup.Digest([]byte(it.Content))}
	}
	return adaptersetup.Reconcile(rec, wanted, func(r adaptersetup.RecordItem) (string, bool, error) { return digestOfFile(r.Target) })
}

// Notes implements adaptersetup.Noter with the scripted notes.
func (SetupHandler) Notes(_ context.Context, req adaptersetup.Request) ([]string, error) {
	f, err := load(req)
	if err != nil {
		return nil, err
	}
	return f.cfg.Notes, nil
}

// Check implements adaptersetup.Handler. With no record it still says what setup
// would have to make.
func (SetupHandler) Check(_ context.Context, req adaptersetup.Request) (adaptersetup.Check, error) {
	f, err := load(req)
	if err != nil {
		return adaptersetup.Check{}, err
	}
	rec, err := adaptersetup.ReadRecord(req.StateDir)
	if err != nil {
		return adaptersetup.Check{}, err
	}
	r, err := f.reconcile(rec)
	if err != nil {
		return adaptersetup.Check{}, err
	}
	return adaptersetup.Check{Recorded: rec.Installed(), Drift: r.CheckDrift()}, nil
}

// Refresh implements adaptersetup.Handler.
func (SetupHandler) Refresh(_ context.Context, req adaptersetup.Request) (adaptersetup.Refresh, error) {
	f, err := load(req)
	if err != nil {
		return adaptersetup.Refresh{}, err
	}
	rec, err := adaptersetup.ReadRecord(req.StateDir)
	if err != nil || !rec.Installed() {
		return adaptersetup.Refresh{}, err
	}
	r, err := f.reconcile(rec)
	if err != nil {
		return adaptersetup.Refresh{}, err
	}
	out := adaptersetup.Refresh{Recorded: true, Drift: r.Drift}
	for _, w := range r.Stale {
		it := f.items[w.ID]
		i := f.item(it, adaptersetup.ActionUpdate)
		if f.fails(it.ID) {
			out.Outcomes = append(out.Outcomes, outcome(i, adaptersetup.OutcomeFailed, "scripted to fail"))
			continue
		}
		if err := f.write(it, rec); err != nil {
			out.Outcomes = append(out.Outcomes, outcome(i, adaptersetup.OutcomeFailed, err.Error()))
			continue
		}
		out.Outcomes = append(out.Outcomes, outcome(i, adaptersetup.OutcomeDone, ""))
	}
	if len(out.Outcomes) > 0 {
		rec.AdapterVersion, rec.SideraProgram = SetupVersion, req.SideraProgram
		if err := adaptersetup.WriteRecord(req.StateDir, *rec); err != nil {
			return adaptersetup.Refresh{}, err
		}
	}
	return out, nil
}
