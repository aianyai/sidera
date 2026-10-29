package inproc

import (
	"fmt"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/disk"
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
)

type damageState struct {
	applied bool
	result  disk.DamageResult
}

func recoveryBindings(state *damageState) recipes.RecoveryOps {
	return recipes.RecoveryOps{
		CheckClosedRecords: func(e scenario.Env) error {
			h := e.(*harness.H)
			scan, err := h.Records()
			if err != nil {
				return err
			}
			enabled := func(instance string) bool { return h.Config().Instances().Items[instance].Enabled }
			return disk.CheckClosed(h.Disk(), scan, h.Clock().Now(), enabled)
		},
		InspectDamage: func(e scenario.Env) (recipes.DamageFacts, error) {
			facts := recipes.DamageFacts{Applied: state.applied, Endpoint: state.result.Endpoint, Body: state.result.Body}
			if !state.applied {
				return facts, nil
			}
			scan, err := e.(*harness.H).Records()
			if err != nil {
				return facts, err
			}
			return disk.InspectDamage(scan, state.result), nil
		},
	}
}

func configureRecoveryScenario(s *harness.WalkScenario, kind recipes.DamageKind, state *damageState) {
	if kind == "" {
		return
	}
	s.Damage = func(h *harness.H, tr crashwalk.Trigger) error {
		if tr.Pattern != string(kind) {
			return fmt.Errorf("undeclared damage kind %q, want %q", tr.Pattern, kind)
		}
		result, err := disk.Damage(h.Disk(), tr)
		if err != nil {
			return err
		}
		state.result, state.applied = result, true
		return nil
	}
}

func configureRecoveryStartup(s *harness.StartupWalkScenario, kind recipes.DamageKind) {
	if kind == "" {
		return
	}
	s.Damage = func(d fsx.FS, tr crashwalk.Trigger) error {
		if tr.Pattern != string(kind) {
			return fmt.Errorf("undeclared damage kind %q, want %q", tr.Pattern, kind)
		}
		_, err := disk.Damage(d, tr)
		return err
	}
	s.Check = disk.CheckLayout
}
