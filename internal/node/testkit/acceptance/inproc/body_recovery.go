package inproc

import (
	"fmt"
	"path"
	"time"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/store"
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
		scan, err := readStoppedRecoveryRecords(h.Disk())
		if err != nil {
			return err
		}
		if len(scan.Out) != 1 || scan.Out[0].Body == nil || !scan.Out[0].NeedsBody() {
			return fmt.Errorf("body damage requires one actual source retaining its body")
		}
		file := path.Join(store.BodiesDir, store.BodyFileName(scan.Out[0].Name()))
		data, err := h.Disk().ReadFile(file)
		if err != nil {
			return err
		}
		if len(data) == 0 || store.BodyRefOf(data) != *scan.Out[0].Body {
			return fmt.Errorf("body damage requires the actual intact source body")
		}
		data[0] ^= 1
		if err := fsx.Replace(h.Disk(), fsx.Tag{Tx: "offline", Role: "body"}, file, data, fsx.ModePrivate); err != nil {
			return err
		}
		damagedBody = file
		return nil
	}
}
