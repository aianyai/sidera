package inproc

import (
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
)

func retainLostSourceBody(e scenario.Env) error {
	for _, tx := range []string{"feedback-clear", "tick"} {
		e.(*harness.H).Faults().Fail(fsxtest.Match{Tx: tx, Role: "out", Step: fsx.StepCreate}, fsxtest.DiskFull)
	}
	return nil
}
