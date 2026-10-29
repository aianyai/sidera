package inproc

import (
	"bytes"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/daemon"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
	"github.com/aianyai/sidera/internal/node/txn"
)

func recoveryBindings() recipes.RecoveryOps {
	return recipes.RecoveryOps{CheckClosedRecords: func(e scenario.Env) error { return checkRecoveryClosed(e.(*harness.H)) }}
}

func configureRecoveryScenario(s *harness.WalkScenario, extraRecovery bool) {
	if extraRecovery {
		domainCheck := s.Check
		s.Check = func(h *harness.H) error {
			if domainCheck != nil {
				if err := domainCheck(h); err != nil {
					return err
				}
			}
			return checkRecoveryClosed(h)
		}
	}
	if !strings.HasPrefix(s.Name(), "recovery-") {
		return
	}
	if s.Name() == "recovery-create" || s.Name() == "recovery-creating" {
		s.Options.Adapter = &harness.AdapterOptions{Runtime: true}
	}
	var original store.EndpointRecord
	var damaged bool
	s.Damage = func(h *harness.H, tr crashwalk.Trigger) error {
		sc, err := readStoppedRecoveryRecords(h.Disk())
		if err != nil {
			return err
		}
		var file string
		switch tr.Pattern {
		case "source":
			if len(sc.Out) != 1 {
				return fmt.Errorf("damage requires one actual source, got %d", len(sc.Out))
			}
			file = path.Join(store.OutDir, sc.Out[0].Name())
		case "target":
			if len(sc.In) != 1 {
				return fmt.Errorf("damage requires one actual target, got %d", len(sc.In))
			}
			file = path.Join(store.InDir, sc.In[0].Name())
		case "creating-control":
			if len(sc.Ctl) != 1 || sc.Ctl[0].Intent == nil {
				return errors.New("damage requires a real creation intent")
			}
			for _, ep := range sc.Endpoints {
				if ep.Status == store.EndpointCreating && ep.CreatedBy+".rec" == sc.Ctl[0].Name() {
					original = ep
				}
			}
			if original.ID.IsZero() {
				return errors.New("clean shutdown did not preserve a creating endpoint")
			}
			file = path.Join(store.CtlDir, sc.Ctl[0].Name())
		default:
			return fmt.Errorf("unknown recovery damage pattern %q", tr.Pattern)
		}
		data, err := h.Disk().ReadFile(file)
		if err != nil {
			return err
		}
		switch tr.Damage {
		case "preserve-header":
			newline := bytes.IndexByte(data, '\n')
			if newline < 0 {
				return errors.New("actual source has no header")
			}
			data = append(append([]byte(nil), data[:newline+1]...), []byte("damaged payload")...)
		case "unreadable":
			data = []byte("unreadable actual record")
		default:
			return fmt.Errorf("unknown recovery damage %q", tr.Damage)
		}
		damaged = true
		return fsx.Replace(h.Disk(), fsx.Tag{Tx: "offline", Role: "record"}, file, data, fsx.ModePrivate)
	}
	previous := s.Check
	s.Check = func(h *harness.H) error {
		if previous != nil {
			if err := previous(h); err != nil {
				return err
			}
		}
		if !damaged {
			return nil
		}
		sc, err := h.Records()
		if err != nil {
			return err
		}
		if len(sc.Alerts) == 0 {
			return errors.New("quarantined record has no owner alert")
		}
		if original.ID.IsZero() {
			return nil
		}
		if err := checkClosedSession(h, original.ID.String()); err != nil {
			return err
		}
		alert := false
		for _, a := range sc.Alerts {
			if a.Kind == "control_unknown" {
				alert = true
			}
		}
		if !alert {
			return errors.New("creating quarantine did not report control uncertainty")
		}
		return nil
	}
}

func configureRecoveryStartup(s *harness.StartupWalkScenario) {
	if s.Name() != "recovery-layout" {
		return
	}
	s.Damage = func(d fsx.FS, tr crashwalk.Trigger) error {
		if tr != crashwalk.CorruptOffline("state/checkpoints", "remove-empty-directory") {
			return errors.New("unknown layout damage")
		}
		entries, err := d.ReadDir(tr.Pattern)
		if err != nil {
			return err
		}
		if len(entries) != 0 {
			return errors.New("layout damage requires an actual empty directory")
		}
		return fsx.RemoveAll(d, fsx.Tag{Tx: "offline", Role: "directory"}, tr.Pattern)
	}
	s.Check = func(r fsx.Reader) error {
		missing, err := daemon.LayoutWork(r)
		if err != nil {
			return err
		}
		if len(missing) != 0 {
			return fmt.Errorf("unrepaired layout %v", missing)
		}
		return nil
	}
}

func checkRecoveryClosed(h *harness.H) error {
	sc, err := h.Records()
	if err != nil {
		return err
	}
	enabled := func(instance string) bool { return h.Config().Instances().Items[instance].Enabled }
	if plan := engine.PlanRecovery(sc, h.Clock().Now(), enabled); !plan.Empty() {
		return fmt.Errorf("unsettled recovery work: %v", plan.Items())
	}
	if work, err := txn.CleanupWork(h.Disk()); err != nil || len(work) != 0 {
		return fmt.Errorf("unsettled transaction leftovers: %v: %v", work, err)
	}
	if work, err := config.AdapterGCWork(h.Disk()); err != nil || len(work) != 0 {
		return fmt.Errorf("unsettled adapter garbage: %v: %v", work, err)
	}
	if len(sc.Damaged) != 0 || len(sc.Unknown) != 0 {
		return fmt.Errorf("unsettled disk damage=%v unknown=%v", sc.Damaged, sc.Unknown)
	}
	for _, ep := range sc.Endpoints {
		if ep.Status == store.EndpointCreating {
			return fmt.Errorf("unsettled creating endpoint %s", ep.ID)
		}
	}
	for _, c := range sc.Ctl {
		if c.Record.State == delivery.StateQueued || c.Record.State == delivery.StateAccepted {
			return fmt.Errorf("unsettled control %s", c.Record.State)
		}
	}
	for _, in := range sc.In {
		if in.Record.State == delivery.StateQueued || in.Record.State == delivery.StateAccepted {
			return fmt.Errorf("unsettled target %+v", in)
		}
	}
	for _, out := range sc.Out {
		if len(out.Feedback) != 0 {
			return errors.New("unsettled source feedback")
		}
		{
			for _, recipient := range out.Outbound.Recipients {
				if recipient.Delivery.State == delivery.StateQueued || recipient.Delivery.State == delivery.StateAccepted {
					return fmt.Errorf("unsettled source %s", recipient.Delivery.State)
				}
			}
		}
	}
	return nil
}

// Only read the real records needed to select offline damage. H.Records requires
// a running process, while damage is deliberately restricted to a stopped disk.
func readStoppedRecoveryRecords(r fsx.Reader) (*store.Scan, error) {
	sc := &store.Scan{}
	for _, dir := range []string{store.OutDir, store.InDir, store.CtlDir, store.EndpointsDir} {
		entries, err := r.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir || fsx.IsLeftover(entry.Name) {
				continue
			}
			data, err := r.ReadFile(path.Join(dir, entry.Name))
			if err != nil {
				return nil, err
			}
			rec, damage := store.DecodeRecord(dir, entry.Name, data)
			if damage != nil {
				return nil, fmt.Errorf("preexisting damage: %+v", damage)
			}
			switch v := rec.(type) {
			case store.OutRecord:
				sc.Out = append(sc.Out, v)
			case store.InRecord:
				sc.In = append(sc.In, v)
			case store.CtlRecord:
				sc.Ctl = append(sc.Ctl, v)
			case store.EndpointRecord:
				sc.Endpoints = append(sc.Endpoints, v)
			}
		}
	}
	return sc, nil
}
