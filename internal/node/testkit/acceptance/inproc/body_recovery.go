package inproc

import (
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/disk"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
)

func retainLostSourceBody(e scenario.Env) error {
	for _, tx := range []string{"feedback-clear", "tick"} {
		e.(*harness.H).Faults().Fail(fsxtest.Match{Tx: tx, Role: "out", Step: fsx.StepCreate}, fsxtest.DiskFull)
	}
	return nil
}

func configureBodyRecoveryScenario(s *harness.WalkScenario) {
	if s.Name() != "recovery-body-feedback" && s.Name() != "recovery-body-archive" {
		return
	}
	s.Options.ArchiveSweepEvery = time.Hour
	var damagedBody string
	check := s.Check
	s.Check = func(h *harness.H) error {
		if damagedBody != "" {
			scan, err := h.Records()
			if err != nil {
				return err
			}
			alerts := 0
			for _, alert := range scan.Alerts {
				if alert.Kind == "corrupt_body" && alert.Subject == damagedBody {
					alerts++
				}
			}
			if alerts != 1 {
				return fmt.Errorf("damaged source body has %d corresponding owner alerts", alerts)
			}
		}
		return check(h)
	}
	s.Damage = func(h *harness.H, trigger crashwalk.Trigger) error {
		if trigger != crashwalk.CorruptOffline("source-body", "flip-body") {
			return fmt.Errorf("unsupported body damage: %+v", trigger)
		}
		result, err := disk.Damage(h.Disk(), trigger)
		if err != nil {
			return err
		}
		damagedBody = result.Body
		return nil
	}
}
