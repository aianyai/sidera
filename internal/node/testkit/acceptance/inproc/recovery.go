package inproc

import (
	"errors"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/disk"
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
	"strings"
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
		result, err := disk.Damage(h.Disk(), tr)
		if err != nil {
			return err
		}
		if result.Endpoint != "" {
			id, err := ident.ParseEndpointID(result.Endpoint)
			if err != nil {
				return err
			}
			original.ID = id
		}
		damaged = true
		return nil
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
	s.Damage = func(d fsx.FS, tr crashwalk.Trigger) error { _, err := disk.Damage(d, tr); return err }
	s.Check = disk.CheckLayout
}

func checkRecoveryClosed(h *harness.H) error {
	sc, err := h.Records()
	if err != nil {
		return err
	}
	enabled := func(instance string) bool { return h.Config().Instances().Items[instance].Enabled }
	return disk.CheckClosed(h.Disk(), sc, h.Clock().Now(), enabled)
}
